package cmd

import (
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/host-cli/internal/fakessh"
	"github.com/trebi-ai/trebi-connectors/connectors/host-cli/internal/serve"
	"github.com/trebi-ai/trebi-connectors/connectors/host-cli/internal/sshx"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// sandboxEvery is the interval of the sandbox, so a short check sees events.
const sandboxEvery = 2 * time.Second

// ServeCommand returns the `serve` command: the trebi-connector/1 adapter
// on stdin and stdout.
func ServeCommand(version string) *cli.Command {
	return &cli.Command{
		Name:  "serve",
		Usage: "Serve the trebi-connector/1 protocol on stdin and stdout (for Trebi)",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "sandbox", Usage: "sample a fake SSH server in this process (for conformance checks)"},
		},
		Action: func(c *cli.Context) error {
			ctx, stop := signal.NotifyContext(c.Context, os.Interrupt, syscall.SIGTERM)
			defer stop()
			s := settings(c)
			var opts []serve.Option
			if c.Bool("sandbox") {
				// Each third sample has a full disk and a failed service.
				fake, err := fakessh.Start(func(n int, _ string) (string, int) {
					bad := n%3 == 0
					v := fakessh.Values{CPU: 20 + n%5, DiskPct: 50, FailedUnit: bad}
					if bad {
						v.DiskPct = 95
					}
					return fakessh.Output(v), 0
				})
				if err != nil {
					return err
				}
				defer fake.Close()
				// The fake replaces the server and the key, so no real key goes to it.
				// In Trebi mode with no server or user, the missing input check stays.
				if !s.Trebi || s.Addr != "" && s.User != "" {
					s.Addr, s.User, s.Key, s.Agent, s.HostKey = fake.Addr(), fakessh.User, fake.ClientKey(), "", fake.HostKey()
				}
				opts = append(opts, serve.WithEvery(sandboxEvery))
			}
			var r serve.Runner
			if s.Missing() == nil {
				cl, err := sshx.New(s)
				if err != nil {
					opts = append(opts, serve.WithSetupError(err))
				} else {
					defer cl.Close() //nolint:errcheck // the session ends
					r = cl
				}
			}
			return sdk.Serve(ctx, serve.New(r, s, version, opts...), sdk.WithStateDir(s.StateDir))
		},
	}
}
