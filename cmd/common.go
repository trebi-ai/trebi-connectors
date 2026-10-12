package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	cli "github.com/urfave/cli/v3"

	"github.com/flarco/cli-tools/slack-cli/internal/client"
	"github.com/flarco/cli-tools/slack-cli/internal/config"
	"github.com/flarco/cli-tools/slack-cli/internal/output"
)

// AppContext is the resolved config, output settings, and a lazily built client.
type AppContext struct {
	Config        *config.Config
	WorkspaceFlag string
	OutputFormat  string
	Raw           bool
	NoResolve     bool
	Columns       []string
	Verbose       bool

	name string
	ws   config.Workspace
	cli  *client.Client
	dir  *client.Directory
}

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

// JSON reports whether JSON output is selected.
func (a *AppContext) JSON() bool { return a.OutputFormat == "json" }

// Workspace selects the workspace and returns its name and credentials.
func (a *AppContext) Workspace() (string, config.Workspace, error) {
	if a.name != "" {
		return a.name, a.ws, nil
	}
	name, w, err := a.Config.Select(a.WorkspaceFlag)
	if err != nil {
		return "", config.Workspace{}, err
	}
	a.name, a.ws = name, w
	return name, w, nil
}

// Client builds the API client for the selected workspace on first use.
func (a *AppContext) Client() (*client.Client, error) {
	if a.cli != nil {
		return a.cli, nil
	}
	name, w, err := a.Workspace()
	if err != nil {
		return nil, err
	}
	if w.Token == "" {
		return nil, fmt.Errorf("workspace %q has no token", name)
	}
	cl := client.New(w)
	cl.Verbose = a.Verbose
	// A desktop session token can expire; mint a fresh one from the cookie.
	if w.Kind() == "session" && w.Domain != "" && w.Cookie != "" {
		cl.Refresh = func(ctx context.Context) (config.Workspace, error) {
			return a.refreshSession(ctx, name, w)
		}
	}
	a.cli = cl
	return cl, nil
}

// Directory builds the name directory for the selected workspace.
func (a *AppContext) Directory() (*client.Directory, error) {
	if a.dir != nil {
		return a.dir, nil
	}
	cl, err := a.Client()
	if err != nil {
		return nil, err
	}
	name, _, _ := a.Workspace()
	dir, _ := config.Dir()
	a.dir = client.NewDirectory(cl, dir, name)
	return a.dir, nil
}

// Out is the root command writer.
func (a *AppContext) Out(c *cli.Command) io.Writer { return c.Root().Writer }

// Render writes an API response body in the selected format.
func (a *AppContext) Render(w io.Writer, body []byte) error {
	if a.Raw || a.JSON() {
		return output.WriteRaw(w, body)
	}
	return a.renderTable(w, body)
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

// successf prints "✓ msg" unless JSON output is selected.
func successf(a *AppContext, w io.Writer, format string, args ...any) {
	if a.JSON() || a.Raw {
		return
	}
	fmt.Fprintf(w, "✓ "+format+"\n", args...)
}
