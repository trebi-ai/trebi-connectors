package cmd

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/fakemail"
	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/mail"
	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/serve"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// Fixed fake credentials of a sandbox outside Trebi.
const (
	sandboxAddress  = "sandbox@example.com"
	sandboxPassword = "sandbox"
)

const sandboxWelcome = "From: Trebi <hello@example.com>\r\nTo: sandbox@example.com\r\nSubject: Welcome to the sandbox\r\nMessage-ID: <welcome@example.com>\r\n\r\nThis message is in a fake mailbox.\r\n"

// ServeCommand returns the `serve` command: the trebi-connector/1 adapter
// on stdin and stdout.
func ServeCommand(version string) *cli.Command {
	return &cli.Command{
		Name:  "serve",
		Usage: "Serve the trebi-connector/1 protocol on stdin and stdout (for Trebi)",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "sandbox", Usage: "run the adapter against a fake mail server in this process (for conformance checks)"},
		},
		Action: func(c *cli.Context) error {
			ctx, stop := signal.NotifyContext(c.Context, os.Interrupt, syscall.SIGTERM)
			defer stop()
			s := settingsFromCtx(c)
			var opts []mail.Option
			if c.Bool("sandbox") {
				if !s.Trebi {
					s.Address, s.Password = sandboxAddress, sandboxPassword // never send a real secret, not even to the fake
				}
				fake, err := fakemail.Start(s.Address, s.Password)
				if err != nil {
					return err
				}
				defer fake.Close()
				if err := fake.Deliver("INBOX", []byte(sandboxWelcome)); err != nil {
					return err
				}
				s.IMAPHost, s.SMTPHost, s.Drafts, s.Sent = fake.IMAPAddr(), fake.SMTPAddr(), "Drafts", "Sent"
				opts = append(opts, mail.WithTLS(fake.ClientTLS()))
			}
			return sdk.Serve(ctx, serve.New(mail.New(s, opts...), s, version))
		},
	}
}
