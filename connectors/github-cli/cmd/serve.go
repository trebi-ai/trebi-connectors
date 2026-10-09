package cmd

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/github-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/github-cli/internal/fakegithub"
	"github.com/trebi-ai/trebi-connectors/connectors/github-cli/internal/serve"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// sandboxClientID is the OAuth app of the sandbox. The fake accepts any id.
const sandboxClientID = "sandbox-client"

// ServeCommand returns the `serve` command: the trebi-connector/1 adapter
// on stdin and stdout.
func ServeCommand(version, clientID string) *cli.Command {
	return &cli.Command{
		Name:  "serve",
		Usage: "Serve the trebi-connector/1 protocol on stdin and stdout (for Trebi)",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "sandbox", Usage: "run the adapter against a fake GitHub in this process (for conformance checks)"},
		},
		Action: func(c *cli.Context) error {
			ctx, stop := signal.NotifyContext(c.Context, os.Interrupt, syscall.SIGTERM)
			defer stop()
			t, _ := sdk.FromEnv()
			s := settingsFromCtx(c)
			cl := client.New(s.Token)
			var opts []serve.Option
			if c.Bool("sandbox") {
				fake := fakegithub.Start()
				defer fake.Close()
				if !s.Trebi {
					cl.Token = fakegithub.Token // never send a real token, not even to the fake
				}
				cl.API, cl.Web, clientID = fake.URL(), fake.URL(), sandboxClientID
			} else if !s.Trebi && s.Token == "" {
				return errNoToken
			}
			if s.Login != nil {
				opts = append(opts, serve.WithLogin(s.Login, clientID))
			}
			if s.Input {
				opts = append(opts, serve.WithInputToken())
			}
			a, err := serve.New(cl, version, t.StateDir, opts...)
			if err != nil {
				return err
			}
			return sdk.Serve(ctx, a)
		},
	}
}
