package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	cli "github.com/urfave/cli/v3"

	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/cmd"
	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/serve"
	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/spec"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

var version = "dev"

func main() {
	sdk.SetDefaultLogger(nil)
	args := os.Args
	if filepath.Base(args[0]) == "plane-op" {
		// The operations of the Trebi manifest run as plane-op <name>.
		args = append([]string{"plane-cli", "op"}, args[1:]...)
	}
	if err := run(context.Background(), args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	s, err := spec.Load()
	if err != nil {
		return fmt.Errorf("load spec: %w", err)
	}

	commands := []*cli.Command{cmd.AuthCommand(), cmd.ConfigCommand(), cmd.APICommand(), cmd.ListenCommand(), cmd.ServeCommand(), cmd.OpCommand()}
	commands = append(commands, cmd.ResourceCommands(s, cmd.UploadCommands(s))...)

	app := &cli.Command{
		Name:    "plane-cli",
		Usage:   "Command-line client for the Plane (plane.so) REST API",
		Version: version,
		Flags: []cli.Flag{
			// The config package reads PLANE_API_KEY and PLANE_HOST, so that
			// these flags come only from the command line.
			&cli.StringFlag{Name: "api-key", Aliases: []string{"k"}, Usage: "API key (overrides PLANE_API_KEY and the config)"},
			&cli.StringFlag{Name: "host", Usage: "API host (overrides PLANE_HOST; default https://api.plane.so)"},
			&cli.StringFlag{Name: "workspace", Aliases: []string{"w"}, Sources: cli.EnvVars("PLANE_WORKSPACE"), Usage: "Workspace slug"},
			&cli.StringFlag{Name: "project", Aliases: []string{"p"}, Sources: cli.EnvVars("PLANE_PROJECT"), Usage: "Project id or identifier (for example ENG)"},
			&cli.BoolFlag{Name: "workspace-scope", Aliases: []string{"W"}, Usage: "Call the workspace endpoint even when a project is set (commands with both variants)"},
			&cli.StringFlag{Name: "output", Aliases: []string{"o"}, Value: "table", Usage: "Output format: table, json"},
			&cli.BoolFlag{Name: "json", Aliases: []string{"j"}, Usage: "Shorthand for --output json"},
			&cli.StringFlag{Name: "columns", Aliases: []string{"c"}, Usage: "Table columns, comma-separated (dotted paths allowed, for example state.name)"},
			&cli.BoolFlag{Name: "verbose", Aliases: []string{"V"}, Sources: cli.EnvVars("PLANE_DEBUG"), Usage: "Print HTTP request/response details to stderr"},
		},
		EnableShellCompletion: true,
		Before: func(ctx context.Context, c *cli.Command) (context.Context, error) {
			if config.Trebi() && (c.IsSet("api-key") || c.IsSet("host")) {
				return ctx, sdk.ErrTrebiSets
			}
			for _, f := range []struct {
				flag string
				set  func(string)
			}{
				{"api-key", cfg.SetAPIKey},
				{"host", cfg.SetHost},
				{"workspace", func(v string) { cfg.Workspace = v }},
				{"project", func(v string) { cfg.Project = v }},
			} {
				if v := strings.TrimSpace(c.String(f.flag)); v != "" {
					f.set(v)
				}
			}
			if config.Trebi() && cfg.Workspace == "" {
				// The first workspace of the connection is the default.
				if sc, err := serve.ParseScope(os.Getenv(serve.InputProjects)); err == nil {
					cfg.Workspace = sc.Workspaces()[0]
				}
			}
			cfg.UseWorkspaceCredentials()
			format := c.String("output")
			if c.Bool("json") {
				format = "json"
			}
			var cols []string
			for _, col := range strings.Split(c.String("columns"), ",") {
				if col = strings.TrimSpace(col); col != "" {
					cols = append(cols, col)
				}
			}
			cmd.AttachAppContext(c, &cmd.AppContext{
				Config:       cfg,
				OutputFormat: format,
				Columns:      cols,
				Verbose:      c.Bool("verbose"),
			})
			return ctx, nil
		},
		Commands: commands,
	}

	keepCommas(app)

	if err := app.Run(ctx, args); err != nil {
		var exitErr cli.ExitCoder
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	return nil
}

// keepCommas stops cli from splitting slice flags at commas, because filter
// values contain commas (expand=a,b). Array flags split commas themselves.
func keepCommas(c *cli.Command) {
	c.DisableSliceFlagSeparator = true
	for _, sub := range c.Commands {
		keepCommas(sub)
	}
}
