package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/mstodo-cli/cmd"
	"github.com/trebi-ai/trebi-connectors/connectors/mstodo-cli/internal/config"
)

var (
	version = "dev"
	// clientID is the Entra public client of Trebi. The build sets it.
	clientID = ""
)

// init takes the module version when the build did not set one, as with
// go install.
func init() {
	if bi, ok := debug.ReadBuildInfo(); ok && version == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		version = strings.TrimPrefix(bi.Main.Version, "v")
	}
}

func newApp() *cli.App {
	return &cli.App{
		Name:    "mstodo-cli",
		Usage:   "Microsoft To Do CLI",
		Version: version,
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "json", Aliases: []string{"j"}, Usage: "output as JSON"},
			&cli.StringFlag{Name: "client-id", Usage: "Microsoft app id (default: $" + config.EnvClientID + ", then the build)"},
			&cli.StringFlag{Name: "tenant", Usage: "Microsoft tenant: common, consumers, organizations, or a tenant id (default: $" + config.EnvTenant + ", then common)"},
		},
		Before: func(c *cli.Context) error {
			s, err := config.Resolve(config.Flags{ClientID: c.String("client-id"), Tenant: c.String("tenant")}, clientID)
			if err != nil {
				return err
			}
			c.App.Metadata["settings"] = s
			return nil
		},
		Commands: []*cli.Command{
			cmd.AuthCommand(),
			cmd.ListsCommand(),
			cmd.TasksCommand(),
			cmd.ChecklistCommand(),
			cmd.LinksCommand(),
			cmd.AttachmentsCommand(),
			cmd.ExtensionsCommand(),
			cmd.SubscriptionsCommand(),
			cmd.WatchCommand(),
			cmd.ServeCommand(version),
		},
		Metadata: map[string]any{},
	}
}

func main() {
	if err := newApp().Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
