package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	"github.com/trebi-ai/trebi-connectors/connectors/axiom-cli/cmd"
	"github.com/urfave/cli/v2"
)

var version = "dev"

// init takes the module version when the build did not set one, as with
// go install.
func init() {
	if bi, ok := debug.ReadBuildInfo(); ok && version == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		version = strings.TrimPrefix(bi.Main.Version, "v")
	}
}

func main() {
	app := &cli.App{
		Name:    "axiom-cli",
		Usage:   "Query and inspect Axiom (axiom.co) via its REST API",
		Version: version,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "token",
				Usage:   "Axiom API token (xaat-...) or personal access token (xapt-...) (outside Trebi only)",
				EnvVars: []string{"AXIOM_TOKEN"},
			},
			&cli.StringFlag{
				Name:    "org-id",
				Usage:   "Axiom org id (sent as X-Axiom-Org-ID; required for personal tokens) (outside Trebi only)",
				EnvVars: []string{"AXIOM_ORG_ID"},
			},
			&cli.StringFlag{
				Name:    "api-url",
				Usage:   "Axiom API base URL",
				EnvVars: []string{"AXIOM_URL"},
				Value:   "https://api.axiom.co",
			},
			&cli.BoolFlag{
				Name:  "compact",
				Usage: "Print JSON on a single line",
			},
		},
		Commands: []*cli.Command{
			cmd.AuthCommand(),
			cmd.DatasetsCommand(),
			cmd.QueryCommand(),
			cmd.IngestCommand(),
			cmd.MonitorsCommand(),
			cmd.NotifiersCommand(),
			cmd.AnnotationsCommand(),
			cmd.OrgsCommand(),
			cmd.UsersCommand(),
			cmd.TokensCommand(),
			cmd.DashboardsCommand(),
			cmd.ViewsCommand(),
			cmd.VirtualFieldsCommand(),
			cmd.StarredCommand(),
			cmd.GroupsCommand(),
			cmd.RolesCommand(),
			cmd.ServeCommand(version),
		},
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
