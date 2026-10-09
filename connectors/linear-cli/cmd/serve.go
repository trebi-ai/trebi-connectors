package cmd

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/linear-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/linear-cli/internal/fakelinear"
	"github.com/trebi-ai/trebi-connectors/connectors/linear-cli/internal/serve"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// sandboxKey is the key of a sandbox outside Trebi. The fake accepts any key.
const sandboxKey = "sandbox"

// ServeCommand returns the `serve` command: the trebi-connector/1 adapter
// on stdin and stdout.
func ServeCommand(version string) *cli.Command {
	return &cli.Command{
		Name:  "serve",
		Usage: "Serve the trebi-connector/1 protocol on stdin and stdout (for Trebi)",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "sandbox", Usage: "run the adapter against a fake Linear in this process (for conformance checks)"},
		},
		Action: func(c *cli.Context) error {
			ctx, stop := signal.NotifyContext(c.Context, os.Interrupt, syscall.SIGTERM)
			defer stop()
			s := settingsFromCtx(c)
			var cl *client.Client
			switch {
			case c.Bool("sandbox"):
				fake := fakelinear.Start()
				defer fake.Close()
				key := s.Key
				if !s.Trebi {
					key = sandboxKey // never send a real key, not even to the fake
				}
				cl = client.New(key)
				cl.URL = fake.URL()
			case s.Trebi && s.Key == "":
				cl = client.New("") // Initialize reports the missing input
			default:
				var err error
				if cl, err = clientFromCtx(c); err != nil {
					return err
				}
			}
			return sdk.Serve(ctx, serve.New(cl, version))
		},
	}
}
