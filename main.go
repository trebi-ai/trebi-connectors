package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	cli "github.com/urfave/cli/v3"

	"github.com/flarco/cli-tools/slack-cli/cmd"
	"github.com/flarco/cli-tools/slack-cli/internal/config"
	"github.com/flarco/cli-tools/slack-cli/internal/spec"
)

var version = "dev"

func main() {
	if err := run(context.Background(), os.Args); err != nil {
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
	app := newApp(cfg, s)
	return app.Run(ctx, args)
}

// newApp builds the root command from the config and spec.
func newApp(cfg *config.Config, s *spec.Spec) *cli.Command {
	extra := cmd.HelperCommands()
	resources := cmd.ResourceCommands(s, extra)
	// Hand-written top-level commands. Where a name matches a spec noun
	// (auth, api), merge the spec methods in as subcommands; the hand-written
	// subcommands win on a name collision.
	tops := []*cli.Command{cmd.AuthCommand(), cmd.APICommand(), cmd.ListenCommand()}
	commands := mergeTopLevel(tops, resources)

	app := &cli.Command{
		Name:                  "slack-cli",
		Usage:                 "Command-line client for the Slack Web API",
		Version:               version,
		EnableShellCompletion: true,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "workspace", Aliases: []string{"w"}, Sources: cli.EnvVars("SLACK_WORKSPACE"), Usage: "Workspace name, team id, or domain"},
			&cli.StringFlag{Name: "output", Aliases: []string{"o"}, Value: "table", Usage: "Output format: table, json"},
			&cli.BoolFlag{Name: "json", Aliases: []string{"j"}, Usage: "Shorthand for --output json"},
			&cli.BoolFlag{Name: "raw", Usage: "Print the full Slack JSON response"},
			&cli.StringFlag{Name: "columns", Aliases: []string{"c"}, Usage: "Table columns, comma-separated (dotted paths allowed)"},
			&cli.BoolFlag{Name: "no-resolve", Usage: "Do not resolve channel/user names to ids"},
			&cli.BoolFlag{Name: "verbose", Aliases: []string{"V"}, Sources: cli.EnvVars("SLACK_DEBUG"), Usage: "Print HTTP request/response details to stderr"},
		},
		Before: func(ctx context.Context, c *cli.Command) (context.Context, error) {
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
				Config:        cfg,
				WorkspaceFlag: strings.TrimSpace(c.String("workspace")),
				OutputFormat:  format,
				Raw:           c.Bool("raw"),
				NoResolve:     c.Bool("no-resolve"),
				Columns:       cols,
				Verbose:       c.Bool("verbose"),
			})
			return ctx, nil
		},
		Commands: commands,
	}

	keepCommas(app)
	return app
}

// mergeTopLevel returns tops followed by the resource commands, with a spec
// noun merged into a same-named hand-written command instead of duplicating.
// The hand-written command keeps its own flags and action; the spec noun's
// subcommands are added where names do not collide.
func mergeTopLevel(tops, resources []*cli.Command) []*cli.Command {
	byName := map[string]*cli.Command{}
	for _, t := range tops {
		byName[t.Name] = t
	}
	var out []*cli.Command
	out = append(out, tops...)
	for _, r := range resources {
		top, ok := byName[r.Name]
		if !ok {
			out = append(out, r)
			continue
		}
		have := map[string]bool{}
		for _, sc := range top.Commands {
			have[sc.Name] = true
		}
		for _, sc := range r.Commands {
			if !have[sc.Name] {
				top.Commands = append(top.Commands, sc)
			}
		}
	}
	return out
}

// keepCommas stops cli from splitting slice flags at commas, so a JSON value
// in --param or --data survives.
func keepCommas(c *cli.Command) {
	c.DisableSliceFlagSeparator = true
	for _, sub := range c.Commands {
		keepCommas(sub)
	}
}
