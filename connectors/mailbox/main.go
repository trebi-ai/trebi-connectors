// Command mailbox reads and writes one mail account over IMAP and SMTP.
package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/cmd"
	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/config"
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
		Name:    "mailbox",
		Usage:   "Search, read, draft, and send mail over IMAP and SMTP",
		Version: version,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "provider", Usage: "the hosts and folders of gmail, icloud, fastmail, or outlook (default: " + config.EnvProvider + ")"},
		},
		Before: func(c *cli.Context) error {
			s, err := config.Resolve(c.String("provider"))
			if err != nil {
				return err
			}
			c.App.Metadata["settings"] = s
			return nil
		},
		Commands: []*cli.Command{cmd.OpCommand(), cmd.ServeCommand(version)},
		Metadata: map[string]any{},
	}
}

func main() {
	if err := newApp().Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
