package cmd

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/mstodo-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/mstodo-cli/internal/fakegraph"
	"github.com/trebi-ai/trebi-connectors/connectors/mstodo-cli/internal/serve"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// ServeCommand returns the `serve` command: the trebi-connector/1 adapter
// on stdin and stdout.
func ServeCommand(version string) *cli.Command {
	return &cli.Command{
		Name:  "serve",
		Usage: "Serve the trebi-connector/1 protocol on stdin and stdout (for Trebi)",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "sandbox", Usage: "run the adapter against a fake Microsoft Graph in this process (for conformance checks)"},
		},
		Action: func(c *cli.Context) error {
			ctx, stop := signal.NotifyContext(c.Context, os.Interrupt, syscall.SIGTERM)
			defer stop()
			s := settingsFromCtx(c)
			cl := client.New(s.ClientID, s.Tenant, client.Session{})
			dir := s.StateDir
			if c.Bool("sandbox") {
				fake := fakegraph.Start()
				fake.AutoNotify = true
				defer fake.Close()
				cl.ClientID, cl.Tenant, cl.GraphURL, cl.LoginBase = fakegraph.ClientID, fakegraph.Tenant, fake.GraphURL(), fake.LoginBase()
				if !s.Trebi { // never mix a fake login with the real one
					tmp, err := os.MkdirTemp("", "mstodo-sandbox-")
					if err != nil {
						return err
					}
					defer os.RemoveAll(tmp) //nolint:errcheck // a temp folder
					dir = tmp
				}
			}
			a, err := serve.New(cl, version, dir)
			if err != nil {
				return err
			}
			return sdk.Serve(ctx, a)
		},
	}
}
