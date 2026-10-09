package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/mstodo-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/mstodo-cli/internal/config"
)

// settingsFromCtx returns what config.Resolve found in the before hook.
func settingsFromCtx(c *cli.Context) config.Settings {
	s, _ := c.App.Metadata["settings"].(config.Settings) //nolint:errcheck // the zero value has no login
	return s
}

// clientFromCtx builds a client on the saved login. In Trebi mode only
// serve writes auth.json, so a refreshed token stays in memory.
func clientFromCtx(c *cli.Context) (*client.Client, error) {
	s := settingsFromCtx(c)
	sess, err := client.LoadSession(s.AuthPath())
	if err != nil {
		return nil, err
	}
	if sess.AccessToken == "" {
		if s.Trebi {
			return nil, errors.New("not logged in: log in to Microsoft in Trebi")
		}
		return nil, errors.New("not logged in: run mstodo-cli auth login")
	}
	cl := newClient(c, sess)
	if !s.Trebi {
		cl.OnRefresh = func(sess client.Session) error { return sess.Save(s.AuthPath()) }
	}
	return cl, nil
}

// Metadata keys that point the client at a fake Graph, for tests.
const (
	MetaGraphURL  = "graph_url"
	MetaLoginBase = "login_base"
)

func newClient(c *cli.Context, sess client.Session) *client.Client {
	s := settingsFromCtx(c)
	cl := client.New(s.ClientID, s.Tenant, sess)
	if u, ok := c.App.Metadata[MetaGraphURL].(string); ok {
		cl.GraphURL = u
	}
	if u, ok := c.App.Metadata[MetaLoginBase].(string); ok {
		cl.LoginBase = u
	}
	return cl
}

// The flags that name a resource. Named flags work in any order, so an
// agent can put them after the command.
var (
	listFlag = &cli.StringFlag{Name: "list", Aliases: []string{"l"}, Usage: "list id or name", Required: true}
	taskFlag = &cli.StringFlag{Name: "task", Aliases: []string{"t"}, Usage: "task id", Required: true}
	idFlag   = &cli.StringFlag{Name: "id", Usage: "item id", Required: true}
	jsonFlag = &cli.BoolFlag{Name: "json", Aliases: []string{"j"}, Usage: "output as JSON"}
)

// leaf adds --json to a command, so the flag also works after the command.
func leaf(cmd *cli.Command) *cli.Command {
	cmd.Flags = append(cmd.Flags, jsonFlag)
	return cmd
}

// session is one command run: the client and the resolved list.
type session struct {
	c  *cli.Context
	cl *client.Client
}

func newSession(c *cli.Context) (*session, error) {
	if c.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q: use flags, see --help", c.Args().First())
	}
	cl, err := clientFromCtx(c)
	if err != nil {
		return nil, err
	}
	return &session{c: c, cl: cl}, nil
}

// list resolves --list.
func (s *session) list() (client.List, error) { return s.listNamed(s.c.String("list")) }

// listNamed finds a list by its id, or else by its name without regard to case.
func (s *session) listNamed(want string) (client.List, error) {
	lists, err := s.cl.Lists(s.c.Context)
	if err != nil {
		return client.List{}, err
	}
	var byName []client.List
	for _, l := range lists {
		if l.ID == want {
			return l, nil
		}
		if strings.EqualFold(l.DisplayName, want) {
			byName = append(byName, l)
		}
	}
	switch len(byName) {
	case 1:
		return byName[0], nil
	case 0:
		return client.List{}, fmt.Errorf("no list with the id or name %q", want)
	}
	return client.List{}, fmt.Errorf("%d lists have the name %q: use the id", len(byName), want)
}

// task resolves --list and --task.
func (s *session) task() (client.Ref, error) {
	l, err := s.list()
	if err != nil {
		return client.Ref{}, err
	}
	return client.Ref{List: l.ID, Task: s.c.String("task")}, nil
}

// render writes v as JSON with --json, else as a table.
func (s *session) render(v any, headers []string, rows [][]string) error {
	if s.c.Bool("json") {
		return printJSON(s.c.App.Writer, v)
	}
	return printTable(s.c.App.Writer, headers, rows)
}

// one writes v as JSON with --json, else runs table.
func (s *session) one(v any, table func() error) error {
	if s.c.Bool("json") {
		return printJSON(s.c.App.Writer, v)
	}
	return table()
}

// done writes v as JSON with --json, else one line of text.
func (s *session) done(v any, format string, args ...any) error {
	if s.c.Bool("json") {
		return printJSON(s.c.App.Writer, v)
	}
	_, err := fmt.Fprintf(s.c.App.Writer, format+"\n", args...)
	return err
}

// readData reads --data: a JSON object, or @path for a file.
func readData(v string) (map[string]any, error) {
	if v == "" {
		return nil, nil
	}
	raw := []byte(v)
	if path, ok := strings.CutPrefix(v, "@"); ok {
		var err error
		if raw, err = os.ReadFile(path); err != nil {
			return nil, err
		}
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("--data is not a JSON object: %w", err)
	}
	return m, nil
}

// printJSON writes v as indented JSON to w.
func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// printTable writes a simple text table to w.
func printTable(w io.Writer, headers []string, rows [][]string) error {
	var b strings.Builder
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	line := func(cells []string) {
		for i := range headers {
			if i > 0 {
				b.WriteString("  ")
			}
			cell := ""
			if i < len(cells) {
				cell = cells[i]
			}
			fmt.Fprintf(&b, "%-*s", widths[i], cell)
		}
		b.WriteString("\n")
	}
	line(headers)
	dashes := make([]string, len(widths))
	for i, wi := range widths {
		dashes[i] = strings.Repeat("-", wi)
	}
	line(dashes)
	for _, row := range rows {
		line(row)
	}
	_, err := io.WriteString(w, b.String())
	return err
}
