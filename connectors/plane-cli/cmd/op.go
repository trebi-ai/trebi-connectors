package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	cli "github.com/urfave/cli/v3"

	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/serve"
)

// OpCommand returns `op`, the read-only operations of the Trebi connector.
// The plane-op link runs it: `plane-op search` is `plane-cli op search`.
func OpCommand() *cli.Command {
	return &cli.Command{
		Name:      "op",
		Usage:     "Run a read-only Trebi operation (search, get, list) with JSON on stdin",
		ArgsUsage: "<search|get|list>",
		Description: `Reads one JSON object on stdin and writes JSON on stdout.

  search  {"query": "login bug", "project": "ENG", "limit": 20}
  get     {"key": "ENG-42"}
  list    {"project": "ENG", "state_group": "started", "label": "agent", "limit": 50}

Each input also takes "workspace". The default is the first workspace of
PLANE_PROJECTS, then the configured workspace. When PLANE_PROJECTS is set,
a project outside it is an error.

Example:
  echo '{"key":"ENG-42"}' | plane-cli op get`,
		Action: func(ctx context.Context, c *cli.Command) error {
			name := c.Args().First()
			if name == "" || c.Args().Len() > 1 {
				return fmt.Errorf("use one of: %s", strings.Join(serve.OpNames, ", "))
			}
			cfg := FromCmd(c).Config
			o := serve.Ops{Default: cfg.Workspace, Workspace: workspaceAccess(cfg)}
			if s := strings.TrimSpace(os.Getenv(serve.InputProjects)); s != "" {
				sc, err := serve.ParseScope(s)
				if err != nil {
					return fmt.Errorf("%s: %w", serve.InputProjects, err)
				}
				o.Scope = sc
			}
			return o.Run(ctx, name, os.Stdin, os.Stdout)
		},
	}
}
