// Command host-cli samples a Linux server over SSH: CPU, memory, disk,
// load, the top processes, and the systemd services.
package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/host-cli/cmd"
	"github.com/trebi-ai/trebi-connectors/connectors/host-cli/internal/config"
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
		Name:    "host-cli",
		Usage:   "Sample a Linux server over SSH: CPU, memory, disk, load, processes, and services",
		Version: version,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "host", Usage: "the server as host or host:port (outside Trebi only; default: " + config.EnvHost + ")"},
			&cli.StringFlag{Name: "user", Usage: "the SSH user (outside Trebi only; default: " + config.EnvUser + ")"},
			&cli.StringFlag{Name: "key", Usage: "the private key file (outside Trebi only; default: " + config.EnvKeyFile + ", the SSH agent, then ~/.ssh/id_*)"},
		},
		Before: func(c *cli.Context) error {
			s, err := config.Resolve(config.Flags{Host: c.String("host"), User: c.String("user"), KeyFile: c.String("key")})
			if err != nil {
				return err
			}
			c.App.Metadata["settings"] = s
			return nil
		},
		Commands: []*cli.Command{cmd.SampleCommand(), cmd.ServeCommand(version)},
		Metadata: map[string]any{},
	}
}

func main() {
	if err := newApp().Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
