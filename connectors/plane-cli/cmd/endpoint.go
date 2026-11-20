package cmd

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	cli "github.com/urfave/cli/v3"

	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/markdown"
	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/output"
	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/spec"
)

// reserved holds flag names used by the root or by every endpoint command.
var reserved = map[string]bool{
	"api-key": true, "host": true, "workspace": true, "project": true, "output": true,
	"json": true, "columns": true, "verbose": true, "help": true, "version": true,
	"data": true, "set": true, "null": true, "filter": true, "all": true, "max": true,
	"dry-run": true, "workspace-scope": true,
}

// flagParam is a spec param with its CLI flag name.
type flagParam struct {
	spec.Param
	flag string
}

// markdownFlag returns the companion Markdown flag of an HTML body field
// (--description-md for description_html).
func (p flagParam) markdownFlag() (string, bool) {
	if p.In != "body" || !strings.HasSuffix(p.Name, "_html") {
		return "", false
	}
	return strings.TrimSuffix(p.flag, "-html") + "-md", true
}

// endpointCommand turns one spec command into a CLI leaf.
type endpointCommand struct {
	spec   spec.Command
	params []flagParam
}

func newEndpointCommand(sc spec.Command) *endpointCommand {
	e := &endpointCommand{spec: sc}
	seen := map[string]bool{}
	used := map[string]bool{}
	for _, v := range sc.Variants {
		for _, p := range v.Params {
			if p.In == "path" || seen[p.In+"/"+p.Name] {
				continue
			}
			seen[p.In+"/"+p.Name] = true
			name := strings.ReplaceAll(strings.ReplaceAll(p.Name, "__", "-"), "_", "-")
			if reserved[name] || used[name] {
				prefix := "body-"
				if p.In == "query" {
					prefix = "query-"
				}
				name = prefix + name
			}
			used[name] = true
			e.params = append(e.params, flagParam{Param: p, flag: name})
		}
	}
	return e
}

// dual reports whether the command has a workspace and a project variant.
func (e *endpointCommand) dual() bool { return len(e.spec.Variants) == 2 }

func (e *endpointCommand) primary() spec.Endpoint {
	for _, v := range e.spec.Variants {
		if v.ProjectScoped() {
			return v
		}
	}
	return e.spec.Variants[0]
}

func (e *endpointCommand) hasGET() bool {
	for _, v := range e.spec.Variants {
		if v.Method == http.MethodGet {
			return true
		}
	}
	return false
}

func (e *endpointCommand) hasBody() bool {
	for _, v := range e.spec.Variants {
		if v.Method != http.MethodGet {
			return true
		}
	}
	return false
}

// Command builds the urfave command.
func (e *endpointCommand) Command() *cli.Command {
	p := e.primary()
	name := e.spec.Path[len(e.spec.Path)-1]
	var desc strings.Builder
	desc.WriteString(p.Summary)
	desc.WriteString("\n\n")
	for _, v := range e.spec.Variants {
		scope := ""
		if e.dual() {
			scope = "  [workspace scope]"
			if v.ProjectScoped() {
				scope = "  [project scope]"
			}
		}
		fmt.Fprintf(&desc, "API (%s): %s %s%s\n", v.Version, v.Method, v.Path, scope)
	}
	for _, v := range e.spec.Variants {
		fmt.Fprintf(&desc, "Docs: %s\n", v.Doc)
	}
	if e.dual() {
		desc.WriteString("\nWith a project (--project, PLANE_PROJECT, or config) this calls the project endpoint.\nPass --workspace-scope to call the workspace endpoint instead.\n")
	}
	if e.hasBody() {
		desc.WriteString("\nBody: typed flags, plus --data '<json>' (or @file, - for stdin) as the base object,\n--set key=value for any field, and --null key to send null.\n")
	}
	return &cli.Command{
		Name:        name,
		Usage:       p.Title,
		Description: strings.TrimSpace(desc.String()),
		ArgsUsage:   e.argsUsage(p),
		Flags:       e.flags(),
		Action:      e.run,
	}
}

func (e *endpointCommand) argsUsage(ep spec.Endpoint) string {
	var parts []string
	for _, n := range positional(ep) {
		parts = append(parts, "<"+argName(n)+">")
	}
	return strings.Join(parts, " ")
}

// positional returns the path params that the user passes as arguments.
func positional(ep spec.Endpoint) []string {
	var out []string
	for _, n := range ep.PathParams() {
		switch n {
		case "slug", "workspace_slug", "project_id":
			continue
		}
		out = append(out, n)
	}
	return out
}

func argName(param string) string {
	switch param {
	case "pk", "resource_id":
		return "id"
	}
	return strings.ReplaceAll(strings.TrimSuffix(param, "_id"), "_", "-")
}

func (e *endpointCommand) flags() []cli.Flag {
	var flags []cli.Flag
	for _, p := range e.params {
		usage := p.Desc
		if len(p.Enum) > 0 {
			usage += " (one of: " + strings.Join(p.Enum, ", ") + ")"
		}
		if p.IsJSON() {
			usage += " [JSON, @file, or -]"
		}
		if p.Required && p.In == "body" {
			usage += " [required]"
		}
		// urfave reads `text` in a usage string as the value placeholder.
		usage = strings.ReplaceAll(usage, "`", "'")
		switch {
		case p.IsBool():
			flags = append(flags, &cli.BoolFlag{Name: p.flag, Usage: usage})
		case p.IsArray():
			flags = append(flags, &cli.StringSliceFlag{Name: p.flag, Usage: usage + " (repeatable or comma-separated)"})
		default:
			flags = append(flags, &cli.StringFlag{Name: p.flag, Usage: usage})
		}
		if md, ok := p.markdownFlag(); ok {
			flags = append(flags, &cli.StringFlag{Name: md, Usage: "Markdown (GitHub Flavored) for --" + p.flag + ", converted to HTML (or @file, - for stdin)"})
		}
	}
	if e.hasBody() {
		flags = append(flags,
			&cli.StringFlag{Name: "data", Aliases: []string{"d"}, Usage: "Request body as JSON (or @file, - for stdin); flags are merged over it"},
			&cli.StringSliceFlag{Name: "set", Usage: "Body field as key=value (repeatable; value types are inferred)"},
			&cli.StringSliceFlag{Name: "null", Usage: "Body field to send as null (repeatable)"},
		)
	}
	flags = append(flags, &cli.StringSliceFlag{Name: "filter", Aliases: []string{"q"}, Usage: "Extra query parameter as key=value (repeatable), for example state_group__in=started,completed"})
	if e.hasGET() {
		flags = append(flags,
			&cli.BoolFlag{Name: "all", Aliases: []string{"a"}, Usage: "Follow pagination and return every item"},
			&cli.IntFlag{Name: "max", Usage: "With --all, stop after this many items (0 = no cap)"},
		)
	}
	if e.guard() != nil {
		flags = append(flags, &cli.BoolFlag{Name: "force", Usage: "Skip the safety check (a closed state on a work item with open sub-items)"})
	}
	flags = append(flags, &cli.BoolFlag{Name: "dry-run", Usage: "Print the request instead of sending it"})
	return flags
}

func (e *endpointCommand) guard() guard { return guards[strings.Join(e.spec.Path, " ")] }

// check runs the guard of the command, unless --force is set.
func (e *endpointCommand) check(ctx context.Context, a *AppContext, c *cli.Command, cl *client.Client, body any) error {
	g := e.guard()
	if g == nil || c.Bool("force") {
		return nil
	}
	return g(ctx, a, cl, c.Args().Slice(), body)
}

// variant picks the endpoint to call.
func (e *endpointCommand) variant(a *AppContext, c *cli.Command) spec.Endpoint {
	if !e.dual() {
		return e.spec.Variants[0]
	}
	wantProject := a.Config.Project != "" && !c.Bool("workspace-scope")
	for _, v := range e.spec.Variants {
		if v.ProjectScoped() == wantProject {
			return v
		}
	}
	return e.spec.Variants[0]
}

func (e *endpointCommand) run(ctx context.Context, c *cli.Command) error {
	a := FromCmd(c)
	ep := e.variant(a, c)
	q, body, err := e.request(c, ep)
	if err != nil {
		return err
	}
	if c.Bool("dry-run") {
		path, err := e.dryPath(a, ep, c.Args().Slice())
		if err != nil {
			return err
		}
		if e.guard() != nil && !c.Bool("force") {
			if cl, err := a.Client(); err == nil {
				res := "check: OK"
				if err := e.check(ctx, a, c, cl, body); err != nil {
					res = "check: " + err.Error()
				}
				fmt.Fprintln(os.Stderr, res)
			}
		}
		return printDryRun(a, c, ep.Method, path, q, body)
	}
	cl, err := a.Client()
	if err != nil {
		return err
	}
	if err := e.check(ctx, a, c, cl, body); err != nil {
		return err
	}
	path, err := resolvePath(ctx, a, cl, ep, c.Args().Slice())
	if err != nil {
		return err
	}
	if err := resolveBodyKeys(ctx, a, cl, ep, body); err != nil {
		return err
	}
	w := c.Root().Writer
	if ep.Method == http.MethodGet && c.Bool("all") {
		items, err := cl.ListAll(ctx, path, q, ep.Version == "v2", int(c.Int("max")))
		if err != nil {
			return err
		}
		return a.RenderItems(w, items)
	}
	resp, err := cl.Do(ctx, client.Request{Method: ep.Method, Path: path, Query: q, Body: body})
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(resp.Body))) == 0 {
		successf(a, w, "%s %s (%d)", ep.Method, path, resp.Status)
		return nil
	}
	if u := resp.Header.Get("X-Plane-Upsert"); u != "" && !a.JSON() {
		fmt.Fprintf(os.Stderr, "upsert: %s\n", u)
	}
	return a.Render(w, resp.Body)
}

// request builds the query and body from flags.
func (e *endpointCommand) request(c *cli.Command, ep spec.Endpoint) (url.Values, any, error) {
	q := url.Values{}
	body := map[string]any{}
	if raw := c.String("data"); raw != "" {
		v, err := parseJSON(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("--data: %w", err)
		}
		m, ok := v.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("--data must be a JSON object")
		}
		body = m
	}
	for _, p := range e.params {
		if !c.IsSet(p.flag) {
			continue
		}
		// Unlisted query filters still go through; an unknown body field is an error.
		if _, ok := ep.Param(p.In, p.Name); !ok && p.In == "body" {
			return nil, nil, fmt.Errorf("--%s is not valid for %s %s", p.flag, ep.Method, ep.Path)
		}
		v, err := flagValue(c, p)
		if err != nil {
			return nil, nil, err
		}
		if p.In == "query" {
			q.Set(p.Name, queryString(v))
		} else {
			body[p.Name] = v
		}
	}
	for _, p := range e.params {
		md, ok := p.markdownFlag()
		if !ok || !c.IsSet(md) {
			continue
		}
		if c.IsSet(p.flag) {
			return nil, nil, fmt.Errorf("use --%s or --%s, not both", p.flag, md)
		}
		if _, ok := ep.Param(p.In, p.Name); !ok {
			return nil, nil, fmt.Errorf("--%s is not valid for %s %s", md, ep.Method, ep.Path)
		}
		src, err := readValue(c.String(md))
		if err != nil {
			return nil, nil, fmt.Errorf("--%s: %w", md, err)
		}
		html, err := markdown.New().HTML(src)
		if err != nil {
			return nil, nil, fmt.Errorf("--%s: %w", md, err)
		}
		body[p.Name] = html
	}
	for _, kv := range c.StringSlice("filter") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, nil, fmt.Errorf("invalid --filter %q (expected key=value)", kv)
		}
		q.Add(k, v)
	}
	for _, kv := range c.StringSlice("set") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, nil, fmt.Errorf("invalid --set %q (expected key=value)", kv)
		}
		body[k] = inferValue(v)
	}
	for _, k := range c.StringSlice("null") {
		body[k] = nil
	}
	if ep.Method == http.MethodGet {
		return q, nil, nil
	}
	var missing []string
	for _, p := range ep.Params {
		if p.In == "body" && p.Required {
			if _, ok := body[p.Name]; !ok {
				missing = append(missing, "--"+e.flagFor(p))
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, nil, fmt.Errorf("missing required body fields: %s (or pass them in --data)", strings.Join(missing, ", "))
	}
	if len(body) == 0 && ep.Method == http.MethodDelete {
		return q, nil, nil
	}
	return q, body, nil
}

func (e *endpointCommand) flagFor(p spec.Param) string {
	for _, fp := range e.params {
		if fp.In == p.In && fp.Name == p.Name {
			return fp.flag
		}
	}
	return p.Name
}

// flagValue converts a flag to the JSON value its param type needs.
func flagValue(c *cli.Command, p flagParam) (any, error) {
	switch {
	case p.IsBool():
		return c.Bool(p.flag), nil
	case p.IsArray():
		vals := c.StringSlice(p.flag)
		if len(vals) == 1 && strings.HasPrefix(strings.TrimSpace(vals[0]), "[") {
			return parseJSON(vals[0])
		}
		var out []any
		for _, v := range vals {
			for _, s := range strings.Split(v, ",") {
				if s = strings.TrimSpace(s); s == "" {
					continue
				}
				if strings.Contains(p.Type, "integer") {
					n, err := strconv.ParseInt(s, 10, 64)
					if err != nil {
						return nil, fmt.Errorf("--%s: %q is not an integer", p.flag, s)
					}
					out = append(out, n)
				} else {
					out = append(out, s)
				}
			}
		}
		if out == nil {
			out = []any{}
		}
		return out, nil
	case p.IsJSON():
		v, err := parseJSON(c.String(p.flag))
		if err != nil {
			return nil, fmt.Errorf("--%s: %w", p.flag, err)
		}
		return v, nil
	case p.Type == "integer":
		n, err := strconv.ParseInt(c.String(p.flag), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("--%s: %q is not an integer", p.flag, c.String(p.flag))
		}
		return n, nil
	case p.Type == "number":
		f, err := strconv.ParseFloat(c.String(p.flag), 64)
		if err != nil {
			return nil, fmt.Errorf("--%s: %q is not a number", p.flag, c.String(p.flag))
		}
		return f, nil
	}
	if v := c.String(p.flag); p.MaybeJSON() && (v == "-" || strings.HasPrefix(v, "@") || strings.HasPrefix(v, "{") || strings.HasPrefix(v, "[")) {
		j, err := parseJSON(v)
		if err != nil {
			return nil, fmt.Errorf("--%s: %w", p.flag, err)
		}
		return j, nil
	}
	return c.String(p.flag), nil
}

func queryString(v any) string {
	switch t := v.(type) {
	case []any:
		parts := make([]string, len(t))
		for i, x := range t {
			parts[i] = output.Format(x)
		}
		return strings.Join(parts, ",")
	}
	return output.Format(v)
}

// dryPath fills the path without network calls (no v1 id lookups).
func (e *endpointCommand) dryPath(a *AppContext, ep spec.Endpoint, args []string) (string, error) {
	return fillPath(a, ep, args, func(_, v string) (string, error) { return v, nil })
}

// resolvePath fills the path params. v1 paths need UUIDs, so project and work
// item keys are looked up first.
func resolvePath(ctx context.Context, a *AppContext, cl *client.Client, ep spec.Endpoint, args []string) (string, error) {
	return fillPath(a, ep, args, func(param, v string) (string, error) {
		if ep.Version != "v1" || client.IsUUID(v) {
			return v, nil
		}
		ws, err := a.Workspace()
		if err != nil {
			return "", err
		}
		if param == "project_id" {
			return cl.ProjectID(ctx, ws, v)
		}
		if _, _, ok := client.SplitIdentifier(v); ok {
			return cl.WorkItemID(ctx, ws, v)
		}
		return v, nil
	})
}

// workItemListFields are body fields that hold work item ids on work item endpoints.
var workItemListFields = map[string]bool{"add": true, "remove": true, "issues": true, "work_items": true, "work_item_ids": true}

// resolveBodyKeys replaces PROJ-123 keys in work item id lists with UUIDs,
// because these endpoints accept only UUIDs.
func resolveBodyKeys(ctx context.Context, a *AppContext, cl *client.Client, ep spec.Endpoint, body any) error {
	m, ok := body.(map[string]any)
	if !ok || !(strings.Contains(ep.Path, "/work-items/") || strings.Contains(ep.Path, "/issues/")) {
		return nil
	}
	for k, v := range m {
		list, ok := v.([]any)
		if !workItemListFields[k] || !ok {
			continue
		}
		for i, x := range list {
			key, ok := x.(string)
			if !ok {
				continue
			}
			if _, _, isKey := client.SplitIdentifier(key); !isKey {
				continue
			}
			ws, err := a.Workspace()
			if err != nil {
				return err
			}
			id, err := cl.WorkItemID(ctx, ws, key)
			if err != nil {
				return fmt.Errorf("resolve %s: %w", key, err)
			}
			list[i] = id
		}
	}
	return nil
}

func fillPath(a *AppContext, ep spec.Endpoint, args []string, resolve func(param, v string) (string, error)) (string, error) {
	pos := positional(ep)
	if len(args) != len(pos) {
		names := make([]string, len(pos))
		for i, n := range pos {
			names[i] = "<" + argName(n) + ">"
		}
		if len(pos) == 0 {
			return "", fmt.Errorf("this command takes no arguments, got %d", len(args))
		}
		return "", fmt.Errorf("expected arguments: %s (got %d)", strings.Join(names, " "), len(args))
	}
	values := map[string]string{}
	for i, n := range pos {
		values[n] = args[i]
	}
	path := ep.Path
	for _, n := range ep.PathParams() {
		var v string
		switch n {
		case "slug", "workspace_slug":
			ws, err := a.Workspace()
			if err != nil {
				return "", err
			}
			v = ws
		case "project_id":
			p, err := projectOf(a, args)
			if err != nil {
				return "", err
			}
			v = p
		default:
			v = values[n]
		}
		rv, err := resolve(n, v)
		if err != nil {
			return "", err
		}
		path = strings.Replace(path, "{"+n+"}", url.PathEscape(rv), 1)
	}
	return path, nil
}

// projectOf returns the project of a command: a work item key like ENG-42 in
// the args names it, else the configured project.
func projectOf(a *AppContext, args []string) (string, error) {
	for _, arg := range args {
		if proj, _, ok := client.SplitIdentifier(arg); ok {
			return proj, nil
		}
	}
	if a.Config.Project == "" {
		return "", fmt.Errorf("no project set: pass --project/-p (id or identifier like ENG), set PLANE_PROJECT, or run `plane-cli config set project <id>`")
	}
	return a.Config.Project, nil
}

func printDryRun(a *AppContext, c *cli.Command, method, path string, q url.Values, body any) error {
	u := a.Config.BaseURL() + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	w := c.Root().Writer
	fmt.Fprintf(w, "%s %s\n", method, u)
	if body != nil {
		return output.WriteJSON(w, body)
	}
	return nil
}
