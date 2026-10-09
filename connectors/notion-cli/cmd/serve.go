package cmd

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/notion-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/notion-cli/internal/fakenotion"
	"github.com/trebi-ai/trebi-connectors/connectors/notion-cli/internal/serve"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// sandboxToken is the secret of a sandbox outside Trebi. The fake accepts
// any secret.
const sandboxToken = "sandbox"

// ServeCommand returns the `serve` command: the trebi-connector/1 adapter
// on stdin and stdout.
func ServeCommand(version string) *cli.Command {
	return &cli.Command{
		Name:  "serve",
		Usage: "Serve the trebi-connector/1 protocol on stdin and stdout (for Trebi)",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "sandbox", Usage: "run the adapter against a fake Notion in this process (for conformance checks)"},
		},
		Action: func(c *cli.Context) error {
			ctx, stop := signal.NotifyContext(c.Context, os.Interrupt, syscall.SIGTERM)
			defer stop()
			t, _ := sdk.FromEnv()
			s := settingsFromCtx(c)
			var opts []serve.Option
			var cl *client.Client
			switch {
			case c.Bool("sandbox"):
				fake := fakenotion.Start()
				defer fake.Close()
				token := s.Token
				if !s.Trebi {
					token = sandboxToken // never send a real secret, not even to the fake
				}
				cl = client.New(token)
				cl.BaseURL = fake.URL()
				opts = append(opts, serve.WithSandbox(fake.SubscribeURL()))
			case s.Trebi && s.Token == "":
				cl = client.New("") // Initialize reports the missing input
			default:
				var err error
				if cl, err = clientFromCtx(c); err != nil {
					return err
				}
			}
			a, err := serve.New(cl, version, t.StateDir, opts...)
			if err != nil {
				return err
			}
			return sdk.Serve(ctx, a)
		},
	}
}
