// Command vnc-cli controls a remote screen over VNC (RFB): capture, type,
// click, wait for the screen, and watch it. It is a port of vncdotool.
package main

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/cmd"
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
	err := cmd.NewRunner(os.Stdin, os.Stdout, os.Stderr).App(version).Run(context.Background(), os.Args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}
	os.Exit(cmd.ExitCode(err))
}
