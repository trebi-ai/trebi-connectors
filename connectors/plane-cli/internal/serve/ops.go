package serve

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"

	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/client"
)

// Ops runs the read-only operations of the manifest: search, get, and list.
// Each one reads a JSON object and writes JSON.
type Ops struct {
	Scope     Scope // empty: no limit
	Default   string
	Workspace func(slug string) Workspace
}

// OpNames lists the operations.
var OpNames = []string{"search", "get", "list"}

type opInput struct {
	Workspace  string `json:"workspace"`
	Query      string `json:"query"`
	Key        string `json:"key"`
	Project    string `json:"project"`
	StateGroup string `json:"state_group"`
	Label      string `json:"label"`
	Limit      int    `json:"limit"`
}

// Row is one work item in the result of search and list.
type Row struct {
	Key        string   `json:"key"`
	Name       string   `json:"name"`
	ID         string   `json:"id"`
	Workspace  string   `json:"workspace"`
	Project    string   `json:"project"`
	State      string   `json:"state,omitempty"`
	StateGroup string   `json:"state_group,omitempty"`
	Labels     []string `json:"labels,omitempty"`
	Priority   string   `json:"priority,omitempty"`
	UpdatedAt  string   `json:"updated_at,omitempty"`
	URL        string   `json:"url,omitempty"`
}

// Run runs the operation name with the JSON input in r.
func (o Ops) Run(ctx context.Context, name string, r io.Reader, w io.Writer) error {
	var in opInput
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &in); err != nil {
			return fmt.Errorf("read the input: %w", err)
		}
	}
	ws, err := o.workspace(in.Workspace)
	if err != nil {
		return err
	}
	acc := o.Workspace(ws)
	if !acc.HasKey {
		return errors.New("no API key for workspace " + ws)
	}
	m := mapper{ws: ws, web: acc.Web}
	var out any
	switch name {
	case "search":
		out, err = o.search(ctx, acc.Client, m, in)
	case "get":
		out, err = o.get(ctx, acc.Client, m, in)
	case "list":
		out, err = o.list(ctx, acc.Client, m, in)
	default:
		return fmt.Errorf("unknown operation %q (use %s)", name, strings.Join(OpNames, ", "))
	}
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// workspace picks the input value, else the first workspace of the scope,
// else the default. A workspace outside the scope is an error.
func (o Ops) workspace(in string) (string, error) {
	ws := strings.TrimSpace(in)
	if ws == "" && len(o.Scope) > 0 {
		ws = o.Scope.Workspaces()[0]
	}
	ws = cmp.Or(ws, o.Default)
	if ws == "" {
		return "", errors.New("no workspace: set workspace in the input")
	}
	if len(o.Scope) > 0 {
		if _, ok := o.Scope[ws]; !ok {
			return "", fmt.Errorf("workspace %s is not in %s", ws, InputProjects)
		}
	}
	return ws, nil
}

func (o Ops) allowed(ws, ident string) bool {
	return len(o.Scope) == 0 || o.Scope.Has(ws, ident)
}

func limit(n, def, most int) int {
	if n <= 0 {
		return def
	}
	return min(n, most)
}

func (o Ops) search(ctx context.Context, cl *client.Client, m mapper, in opInput) ([]Row, error) {
	if strings.TrimSpace(in.Query) == "" {
		return nil, errors.New("query is empty")
	}
	n := limit(in.Limit, 20, 100)
	q := url.Values{"search": {in.Query}, "limit": {fmt.Sprint(n)}, "workspace_search": {"true"}}
	if in.Project != "" {
		pid, err := cl.ProjectID(ctx, m.ws, strings.ToUpper(in.Project))
		if err != nil {
			return nil, err
		}
		q.Set("project_id", pid)
		q.Set("workspace_search", "false")
	}
	var res struct {
		Issues []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Project    string `json:"project__identifier"`
			SequenceID int    `json:"sequence_id"`
		} `json:"issues"`
	}
	if err := cl.GetJSON(ctx, "/api/v1/workspaces/"+url.PathEscape(m.ws)+"/work-items/search/", q, &res); err != nil {
		return nil, err
	}
	rows := []Row{}
	for _, it := range res.Issues {
		if !o.allowed(m.ws, it.Project) {
			continue
		}
		k := fmt.Sprintf("%s-%d", it.Project, it.SequenceID)
		rows = append(rows, Row{Key: k, Name: it.Name, ID: it.ID, Workspace: m.ws, Project: it.Project, URL: m.url(k)})
	}
	return rows[:min(len(rows), n)], nil
}

func (o Ops) get(ctx context.Context, cl *client.Client, m mapper, in opInput) (map[string]any, error) {
	k := strings.ToUpper(strings.TrimSpace(in.Key))
	ident, _, ok := client.SplitIdentifier(k)
	if !ok {
		return nil, fmt.Errorf("key %q: use the work item key, for example ENG-42", in.Key)
	}
	if !o.allowed(m.ws, ident) {
		return nil, fmt.Errorf("project %s is not in %s", ident, InputProjects)
	}
	var it map[string]any
	q := url.Values{"expand": {"state,labels,assignees"}}
	if err := cl.GetJSON(ctx, "/api/v2/workspaces/"+url.PathEscape(m.ws)+"/work-items/"+url.PathEscape(k)+"/", q, &it); err != nil {
		return nil, err
	}
	it["key"], it["workspace"], it["url"] = k, m.ws, m.url(k)
	return it, nil
}

func (o Ops) list(ctx context.Context, cl *client.Client, m mapper, in opInput) ([]Row, error) {
	ident := strings.ToUpper(strings.TrimSpace(in.Project))
	if ident == "" {
		return nil, errors.New("project is empty: use the project identifier, for example ENG")
	}
	if !o.allowed(m.ws, ident) {
		return nil, fmt.Errorf("project %s is not in %s", ident, InputProjects)
	}
	pid, err := cl.ProjectID(ctx, m.ws, ident)
	if err != nil {
		return nil, err
	}
	n := limit(in.Limit, 50, 200)
	q := url.Values{
		"project_id__in": {pid}, "order_by": {"-updated_at"}, "fields": {itemFields},
		"expand": {"state,labels"}, "per_page": {fmt.Sprint(min(n*2, 200))},
	}
	if in.StateGroup != "" {
		q.Set("state_group__in", in.StateGroup)
	}
	max := n
	if in.Label != "" {
		max = 1000 // the label filter is local
	}
	items, err := cl.ListAll(ctx, "/api/v2/workspaces/"+url.PathEscape(m.ws)+"/work-items/", q, true, max)
	if err != nil {
		return nil, err
	}
	p := project{ID: pid, Identifier: ident}
	rows := []Row{}
	for _, raw := range items {
		var it workItem
		if json.Unmarshal(raw, &it) != nil {
			continue
		}
		ls := it.labelNames()
		if in.Label != "" && !slices.ContainsFunc(ls, func(l string) bool { return strings.EqualFold(l, in.Label) }) {
			continue
		}
		k := key(p, it)
		rows = append(rows, Row{
			Key: k, Name: it.Name, ID: it.ID, Workspace: m.ws, Project: ident, State: it.stateName(),
			StateGroup: it.stateGroup(), Labels: ls, Priority: it.Priority, UpdatedAt: it.UpdatedAt, URL: m.url(k),
		})
		if len(rows) == n {
			break
		}
	}
	return rows, nil
}
