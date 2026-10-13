// Command web-cli reads RSS and Atom feeds and web pages, and watches them
// for Trebi.
package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/web-cli/cmd"
)

var version = "dev"

// init takes the module version when the build did not set one, as with
// go install.
func init() {
	if bi, ok := debug.ReadBuildInfo(); ok && version == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		version = strings.TrimPrefix(bi.Main.Version, "v")
	}
}

func newApp() *cli.App {
	return &cli.App{
		Name:     "web-cli",
		Usage:    "Read RSS and Atom feeds and web pages, and watch them for changes",
		Version:  version,
		Commands: []*cli.Command{cmd.ReadCommand(version), cmd.ServeCommand(version)},
	}
}

func main() {
	if err := newApp().Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
