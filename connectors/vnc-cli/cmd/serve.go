package cmd

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/internal/serve"
	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/internal/session"
	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc/vnctest"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// sandboxClipboard is the time between two clipboard changes of the
// sandbox.
const sandboxClipboard = 10 * time.Second

func (r *Runner) serveCommand(version string) *cli.Command {
	return &cli.Command{
		Name:  "serve",
		Usage: "Serve the trebi-connector/1 protocol on stdin and stdout (for Trebi)",
		Description: `serve keeps the connection, sends the watch events, and runs the commands of
jobs on the socket in the state folder.`,
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "sandbox", Usage: "watch a fake screen in this process (for conformance checks)"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
			defer stop()
			s := r.settings
			if c.Bool("sandbox") {
				fake := vnctest.New(vnctest.Options{Width: 320, Height: 200, Name: "Sandbox"})
				addr, err := fake.Listen()
				if err != nil {
					return err
				}
				defer fake.Close()
				go fake.Animate(ctx, time.Second)
				go func() {
					t := time.NewTicker(sandboxClipboard)
					defer t.Stop()
					for {
						select {
						case <-ctx.Done():
							return
						case <-t.C:
							fake.CutText("sandbox " + time.Now().UTC().Format(time.TimeOnly))
						}
					}
				}()
				// The fake replaces the server and the password. In Trebi mode
				// with no server, the missing input check stays.
				if !s.Trebi || s.Server != "" {
					s.Server, s.User, s.Password = addr, "", ""
				}
				r.opts.Addr, r.opts.User = s.Server, s.User
			}
			if s.Missing() != nil {
				return sdk.Serve(ctx, serve.New(nil, nil, s, version), sdk.WithIO(r.stdin, r.stdout), sdk.WithStateDir(s.StateDir))
			}
			dir := s.StateDir
			if dir == "" {
				var err error
				if dir, err = os.MkdirTemp("", "vnc-serve"); err != nil {
					return err
				}
				defer os.RemoveAll(dir)
			}
			p, err := session.TrebiPaths(dir)
			if err != nil {
				return err
			}
			lock, err := session.Lock(p.Lock)
			if errors.Is(err, session.ErrBusy) {
				return errors.New("another serve holds this connection")
			}
			if err != nil {
				return err
			}
			defer lock.Close()
			ln, err := session.Listen(p.Sock)
			if err != nil {
				return err
			}
			level := slog.LevelInfo
			if r.verbose {
				level = slog.LevelDebug
			}
			log := slog.New(slog.NewTextHandler(r.stderr, &slog.HandlerOptions{Level: level}))
			slog.SetDefault(log)
			h := session.NewHolder(r.dialer(s.Password), s.Server, s.User, 0, log)
			return sdk.Serve(ctx, serve.New(h, ln, s, version), sdk.WithIO(r.stdin, r.stdout), sdk.WithStateDir(dir))
		},
	}
}
