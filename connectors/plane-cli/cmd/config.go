package cmd

import (
	"context"
	"fmt"

	cli "github.com/urfave/cli/v3"

	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/output"
)

// ConfigCommand returns the `config` command tree.
func ConfigCommand() *cli.Command {
	return &cli.Command{
		Name:  "config",
		Usage: "Inspect or change the saved configuration",
		Commands: []*cli.Command{
			{
				Name:  "show",
				Usage: "Print the resolved config (API key masked)",
				Action: func(ctx context.Context, c *cli.Command) error {
					a := FromCmd(c)
					red := a.Config.Redacted()
					if a.JSON() {
						return output.WriteJSON(c.Root().Writer, red)
					}
					pairs := [][2]string{
						{"Config file", a.Config.File()},
						{"Host", a.Config.BaseURL()},
						{"Workspace", red.Workspace},
						{"Project", red.Project},
						{"API key", red.APIKey},
					}
					for slug, w := range red.Workspaces {
						pairs = append(pairs, [2]string{"Key for " + slug, w.APIKey})
					}
					return output.KV(c.Root().Writer, pairs)
				},
			},
			{
				Name:  "path",
				Usage: "Print the config file path",
				Action: func(ctx context.Context, c *cli.Command) error {
					fmt.Fprintln(c.Root().Writer, FromCmd(c).Config.File())
					return nil
				},
			},
			savesSetting(&cli.Command{
				Name:      "set",
				Usage:     "Set a config value: workspace, project, host, or api_key (empty value clears it)",
				ArgsUsage: "<key> [value]",
				Action: func(ctx context.Context, c *cli.Command) error {
					cfg, err := config.LoadFile()
					if err != nil {
						return err
					}
					key, value := c.Args().Get(0), c.Args().Get(1)
					fields := map[string]*string{
						"workspace": &cfg.Workspace, "project": &cfg.Project,
						"host": &cfg.Host, "api_key": &cfg.APIKey, "api-key": &cfg.APIKey,
					}
					dst, ok := fields[key]
					if !ok {
						return fmt.Errorf("unknown key %q (use workspace, project, host, or api_key)", key)
					}
					*dst = value
					if err := cfg.Save(); err != nil {
						return err
					}
					a := FromCmd(c)
					successf(a, c.Root().Writer, "Set %s in %s", key, cfg.File())
					return nil
				},
			}),
		},
	}
}
