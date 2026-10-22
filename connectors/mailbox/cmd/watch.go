package cmd

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/mail"
	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/watch"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// WatchCommand returns `watch`: one JSON line on stdout for each new
// message in INBOX. The cursor is in the state folder of the config.
func WatchCommand() *cli.Command {
	return &cli.Command{
		Name:  "watch",
		Usage: "Print one JSON line for each new message in INBOX (the same events as serve)",
		Action: func(c *cli.Context) error {
			s := settingsFromCtx(c)
			if s.Trebi {
				return errors.New("trebi runs serve for this connection; watch is for use outside Trebi")
			}
			if miss := s.Missing(); miss != nil {
				return errors.New(miss.Error() + ": set " + miss.Name)
			}
			if s.StateDir == "" {
				return errors.New("no home folder for the cursor")
			}
			ctx, stop := signal.NotifyContext(c.Context, os.Interrupt, syscall.SIGTERM)
			defer stop()
			enc := json.NewEncoder(c.App.Writer)
			enc.SetEscapeHTML(false)
			w := watch.NewWatcher(mail.New(s), watch.NewFileStore(s.StateDir), time.Now, watch.WithStatus(func(st sdk.Status) {
				slog.Info("status", "state", st.State, "message", st.Message)
			}))
			return w.Run(ctx, func(e sdk.Event) error { return enc.Encode(e) })
		},
	}
}
