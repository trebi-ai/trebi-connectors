package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/notion-cli/cmd"
	"github.com/trebi-ai/trebi-connectors/connectors/notion-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/notion-cli/internal/config"
)

var version = "dev"

// init takes the module version when the build did not set one, as with
// go install.
func init() {
	if bi, ok := debug.ReadBuildInfo(); ok && version == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		version = strings.TrimPrefix(bi.Main.Version, "v")
	}
}

// newApp builds the command tree.
func newApp() *cli.App {
	return &cli.App{
		Name:    "notion-cli",
		Usage:   "Notion CLI tool",
		Version: version,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "token",
				Usage: "Notion integration secret (outside Trebi only; the default is NOTION_TOKEN, a .env file, then the config file)",
			},
			&cli.BoolFlag{
				Name:    "json",
				Aliases: []string{"j"},
				Usage:   "output as JSON",
			},
		},
		Before: beforeHook,
		Commands: []*cli.Command{
			cmd.AuthCommand(),
			cmd.PagesCommand(),
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

func beforeHook(c *cli.Context) error {
	s, err := config.Resolve(c.String("token"))
	if err != nil {
		return err
	}
	c.App.Metadata["settings"] = s
	if s.Token != "" {
		c.App.Metadata["client"] = client.New(s.Token)
	}
	return nil
}
