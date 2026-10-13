package cmd

import (
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/web-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/web-cli/internal/fakeweb"
	"github.com/trebi-ai/trebi-connectors/connectors/web-cli/internal/serve"
	"github.com/trebi-ai/trebi-connectors/connectors/web-cli/internal/watch"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// sandboxEvery is the interval of the sandbox, so a short check sees events.
const sandboxEvery = time.Second

// ServeCommand returns the `serve` command: the trebi-connector/1 adapter
// on stdin and stdout.
func ServeCommand(version string) *cli.Command {
	return &cli.Command{
		Name:  "serve",
		Usage: "Serve the trebi-connector/1 protocol on stdin and stdout (for Trebi); outside Trebi it reads WEB_URLS and WEB_EVERY",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "sandbox", Usage: "watch a fake site in this process (for conformance checks)"},
		},
		Action: func(c *cli.Context) error {
			ctx, stop := signal.NotifyContext(c.Context, os.Interrupt, syscall.SIGTERM)
			defer stop()
			s, err := config.Resolve()
			if err != nil {
				return err
			}
			var opts []serve.Option
			if c.Bool("sandbox") {
				fake := fakeweb.Start()
				defer fake.Close()
				fake.Auto()
				if s.URLsGiven || !s.Trebi { // the fake replaces the given addresses
					s.Targets = []config.Target{{URL: fake.URL(fakeweb.RSSPath)}, {URL: fake.URL(fakeweb.AtomPath)}, {URL: fake.URL(fakeweb.PagePath), Selector: "#price"}}
				}
				opts = append(opts, serve.WithEvery(sandboxEvery))
			}
			return sdk.Serve(ctx, serve.New(watch.NewFetcher(nil, version), s, version, opts...), sdk.WithStateDir(s.StateDir))
		},
	}
}
