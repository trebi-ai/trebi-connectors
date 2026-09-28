package cmd

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/sdk"
	"github.com/trebi-ai/trebi-connectors/src/discord-cli/internal/serve"
)

// ServeCommand returns the `serve` command: the trebi-connector/1 adapter
// on stdin and stdout.
func ServeCommand(version string) *cli.Command {
	return &cli.Command{
		Name:  "serve",
		Usage: "Serve the trebi-connector/1 protocol on stdin and stdout (for Trebi)",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "sandbox", Usage: "serve fake rooms with no Discord account (for conformance checks)"},
		},
		Action: func(c *cli.Context) error {
			ctx, stop := signal.NotifyContext(c.Context, os.Interrupt, syscall.SIGTERM)
			defer stop()
			if c.Bool("sandbox") {
				return sdk.Serve(ctx, sdk.NewSandbox(sdk.SandboxConfig{
					Adapter:  sdk.AdapterInfo{Name: serve.Name, Version: version},
					Events:   serve.Events,
					Features: serve.Features,
					Limits:   serve.Limits,
				}))
			}
			cl, err := clientFromCtx(c)
			if err != nil {
				return err
			}
			a, err := serve.New(cl, version, os.Getenv("TREBI_STATE_DIR"))
			if err != nil {
				return err
			}
			return sdk.Serve(ctx, a)
		},
	}
}
