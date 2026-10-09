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
		},
		Before: func(c *cli.Context) error {
			s := config.Resolve()
			s.ClientID = clientID
			c.App.Metadata["settings"] = s
			return nil
		},
		Commands: []*cli.Command{
			cmd.AuthCommand(),
			cmd.ListsCommand(),
			cmd.TasksCommand(),
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
