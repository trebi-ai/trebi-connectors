package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/github-cli/cmd"
	"github.com/trebi-ai/trebi-connectors/connectors/github-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/github-cli/internal/config"
)

var version = "dev"

// clientID is the Trebi GitHub OAuth app of the device login. The release
// build sets it.
var clientID = ""

// init takes the module version when the build did not set one, as with
// go install.
func init() {
	if bi, ok := debug.ReadBuildInfo(); ok && version == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		version = strings.TrimPrefix(bi.Main.Version, "v")
	}
}

func newApp() *cli.App {
	return &cli.App{
		Name:    "github-cli",
		Usage:   "GitHub CLI tool: repositories, events, and webhooks",
		Version: version,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "token", Usage: "GitHub token (outside Trebi only; the default is GITHUB_TOKEN, GH_TOKEN, then the config file)"},
			&cli.BoolFlag{Name: "json", Aliases: []string{"j"}, Usage: "output as JSON"},
		},
		Before: func(c *cli.Context) error {
			s, err := config.Resolve(c.String("token"))
			if err != nil {
				return err
			}
			c.App.Metadata["settings"] = s
			c.App.Metadata["client"] = client.New(s.Token)
			return nil
		},
		Commands: []*cli.Command{
			cmd.ReposCommand(),
			cmd.EventsCommand(),
			cmd.HooksCommand(),
			cmd.ServeCommand(version, clientID),
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
