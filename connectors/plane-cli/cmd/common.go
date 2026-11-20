package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	cli "github.com/urfave/cli/v3"

	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/output"
)

// AppContext is the resolved config, output settings, and a lazily built client.
type AppContext struct {
	Config       *config.Config
	OutputFormat string
	Columns      []string
	Verbose      bool
	cli          *client.Client
}

// Client builds the API client on first use.
func (a *AppContext) Client() (*client.Client, error) {
	if a.cli != nil {
		return a.cli, nil
	}
	if err := a.Config.Validate(); err != nil {
		return nil, err
	}
	a.cli = client.New(a.Config)
	a.cli.Verbose = a.Verbose
	return a.cli, nil
}

// Workspace returns the workspace slug or an error that says how to set it.
func (a *AppContext) Workspace() (string, error) {
	if ws := strings.TrimSpace(a.Config.Workspace); ws != "" {
		return ws, nil
	}
	if config.Trebi() {
		return "", fmt.Errorf("no workspace set: pass --workspace/-w")
	}
	return "", fmt.Errorf("no workspace set: pass --workspace/-w, set PLANE_WORKSPACE, or run `plane-cli config set workspace <slug>`")
}

// JSON reports whether JSON output is selected.
func (a *AppContext) JSON() bool { return a.OutputFormat == "json" }

const ctxKey = "app"

// FromCmd returns the AppContext attached to the root command.
func FromCmd(c *cli.Command) *AppContext {
	root := c.Root()
	if root.Metadata == nil {
		return nil
	}
	v, _ := root.Metadata[ctxKey].(*AppContext)
	return v
}

// AttachAppContext stores a in the command metadata.
func AttachAppContext(c *cli.Command, a *AppContext) {
	if c.Metadata == nil {
		c.Metadata = map[string]any{}
	}
	c.Metadata[ctxKey] = a
}

// Render writes an API response body in the selected format.
func (a *AppContext) Render(w io.Writer, body []byte) error {
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		_, err := w.Write(body)
		return err
	}
	switch a.OutputFormat {
	case "json":
		return output.WriteJSON(w, v)
	case "", "table":
	default:
		return fmt.Errorf("unsupported --output %q (use table or json)", a.OutputFormat)
	}
	switch t := v.(type) {
	case []any:
		return output.Rows(w, objects(t), a.Columns)
	case map[string]any:
		// Bulk result: {"results":[{index,result,id,...}],"succeeded","failed"}.
		if res, ok := t["results"].([]any); ok && t["succeeded"] != nil {
			cols := a.Columns
			if len(cols) == 0 {
				cols = []string{"index", "result", "id", "code", "detail"}
			}
			if err := output.Rows(w, objects(res), cols); err != nil {
				return err
			}
			fmt.Fprintf(w, "\nsucceeded: %s  failed: %s\n", output.Format(t["succeeded"]), output.Format(t["failed"]))
			return nil
		}
		if page := client.ParsePage(body); page.Paginated {
			if err := output.Rows(w, rawObjects(page.Items), a.Columns); err != nil {
				return err
			}
			pageFooter(page, len(page.Items))
			return nil
		}
		// A wrapper with one list, for example v1 search {"issues":[...]}.
		if len(t) == 1 {
			for _, x := range t {
				if list, ok := x.([]any); ok {
					return output.Rows(w, objects(list), a.Columns)
				}
			}
		}
		return output.Object(w, t, a.Columns)
	default:
		return output.WriteJSON(w, v)
	}
}

// RenderItems writes a list collected over many pages.
func (a *AppContext) RenderItems(w io.Writer, items []json.RawMessage) error {
	if a.JSON() {
		if items == nil {
			items = []json.RawMessage{}
		}
		return output.WriteJSON(w, items)
	}
	return output.Rows(w, rawObjects(items), a.Columns)
}

func pageFooter(p client.Page, n int) {
	var parts []string
	if p.TotalCount != nil {
		parts = append(parts, fmt.Sprintf("%d of %d", n, *p.TotalCount))
	}
	if p.HasMore {
		if p.NextOffset != nil {
			parts = append(parts, "next: --offset "+strconv.Itoa(*p.NextOffset))
		} else if p.NextCursor != "" {
			parts = append(parts, "next: --cursor "+p.NextCursor)
		}
		parts = append(parts, "or use --all")
	}
	if len(parts) > 0 {
		fmt.Fprintln(os.Stderr, "("+strings.Join(parts, "; ")+")")
	}
}

func objects(a []any) []map[string]any {
	out := make([]map[string]any, 0, len(a))
	for _, x := range a {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		} else {
			out = append(out, map[string]any{"value": x})
		}
	}
	return out
}

func rawObjects(items []json.RawMessage) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, raw := range items {
		var m map[string]any
		if json.Unmarshal(raw, &m) != nil {
			var x any
			_ = json.Unmarshal(raw, &x)
			m = map[string]any{"value": x}
		}
		out = append(out, m)
	}
	return out
}

// successf prints "✓ msg" unless JSON output is selected.
func successf(a *AppContext, w io.Writer, format string, args ...any) {
	if a.JSON() {
		return
	}
	fmt.Fprintf(w, "✓ "+format+"\n", args...)
}

// readValue reads "@file" from a file, "-" from stdin, or returns s as is.
func readValue(s string) ([]byte, error) {
	switch {
	case s == "-":
		return io.ReadAll(bufio.NewReader(os.Stdin))
	case strings.HasPrefix(s, "@"):
		return os.ReadFile(s[1:])
	}
	return []byte(s), nil
}

// parseJSON parses a JSON value from s (see readValue).
func parseJSON(s string) (any, error) {
	b, err := readValue(s)
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	return v, nil
}

// inferValue turns a --set value into a JSON value: numbers, booleans, null,
// and JSON objects/arrays are parsed; anything else stays a string.
func inferValue(s string) any {
	switch s {
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	if strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") {
		var v any
		if json.Unmarshal([]byte(s), &v) == nil {
			return v
		}
	}
	return s
}
