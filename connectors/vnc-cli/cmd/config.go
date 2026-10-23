package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/internal/config"
)

func (r *Runner) configCommand() *cli.Command {
	return &cli.Command{
		Name:  "config",
		Usage: "Keep named servers in ~/.cli-tools/vnc-cli/config.toml (outside Trebi only)",
		Commands: []*cli.Command{
			{
				Name:      "set",
				Usage:     "Add or change a named server",
				ArgsUsage: "NAME",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "address", Required: true, Usage: "the server address"},
					&cli.StringFlag{Name: "login", Usage: "the user name"},
					&cli.StringFlag{Name: "secret-env", Usage: "the env name that holds the password"},
					&cli.BoolFlag{Name: "default", Usage: "use this server when no --server or --target is given"},
				},
				Action: func(ctx context.Context, c *cli.Command) error {
					if c.NArg() != 1 {
						return usage(fmt.Errorf("config set needs one NAME"))
					}
					t := config.Target{Server: c.String("address"), User: c.String("login"), PasswordEnv: c.String("secret-env")}
					return config.SetTarget(c.Args().First(), t, c.Bool("default"))
				},
			},
			{
				Name:      "rm",
				Usage:     "Remove a named server",
				ArgsUsage: "NAME",
				Action: func(ctx context.Context, c *cli.Command) error {
					if c.NArg() != 1 {
						return usage(fmt.Errorf("config rm needs one NAME"))
					}
					return config.RemoveTarget(c.Args().First())
				},
			},
			{
				Name:  "list",
				Usage: "Show the named servers",
				Action: func(ctx context.Context, c *cli.Command) error {
					if r.settings.Trebi {
						return r.print(map[string]any{"trebi": true}, "Trebi mode: Trebi sets the server")
					}
					f, err := config.Load(r.settings.Dir)
					if err != nil {
						return err
					}
					var b strings.Builder
					for _, n := range f.Names() {
						t := f.Targets[n]
						mark := " "
						if n == f.Default {
							mark = "*"
						}
						fmt.Fprintf(&b, "%s %s\t%s\tuser=%s\tpassword_env=%s\n", mark, n, t.Server, t.User, t.PasswordEnv)
					}
					return r.print(f, strings.TrimSuffix(b.String(), "\n"))
				},
			},
		},
	}
}
