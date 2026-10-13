package cmd

import (
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/axiom-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/axiom-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/axiom-cli/internal/fakeaxiom"
	"github.com/trebi-ai/trebi-connectors/connectors/axiom-cli/internal/serve"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// sandboxEvery is the poll interval of the sandbox, so a short check sees events.
const sandboxEvery = time.Second

// ServeCommand returns the `serve` command: the trebi-connector/1 adapter
// on stdin and stdout.
func ServeCommand(version string) *cli.Command {
	return &cli.Command{
		Name:  "serve",
		Usage: "Serve the trebi-connector/1 protocol on stdin and stdout (for Trebi): poll the monitor history",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "sandbox", Usage: "poll a fake Axiom API in this process (for conformance checks)"},
		},
		Action: func(c *cli.Context) error {
			ctx, stop := signal.NotifyContext(c.Context, os.Interrupt, syscall.SIGTERM)
			defer stop()
			r, err := resolved(c)
			if err != nil {
				return err
			}
			every, err := config.Every()
			if err != nil {
				return err
			}
			stateDir, err := config.StateDir()
			if err != nil {
				return err
			}
			var opts []serve.Option
			if c.Bool("sandbox") {
				fake := fakeaxiom.Start()
				defer fake.Close()
				fake.Auto()
				// The fake replaces the API, so no real token goes to it.
				// In Trebi mode with no token, the missing input check stays.
				if !r.Trebi || r.Token != "" {
					r.APIURL, r.Token, r.OrgID = fake.URL(), fakeaxiom.Token, ""
				}
				opts = append(opts, serve.WithEvery(sandboxEvery))
			}
			var api serve.API
			if r.Token != "" {
				api = client.New(r.APIURL, r.Token, r.OrgID)
			}
			return sdk.Serve(ctx, serve.New(api, every, version, opts...), sdk.WithStateDir(stateDir))
		},
	}
}
