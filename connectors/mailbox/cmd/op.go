// Package cmd holds the commands of mailbox.
package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/mail"
)

// maxInput caps the JSON input of one operation.
const maxInput = 32 << 20

// settingsFromCtx returns what config.Resolve found in the before hook.
func settingsFromCtx(c *cli.Context) config.Settings {
	s, _ := c.App.Metadata["settings"].(config.Settings) //nolint:errcheck // the zero value has no account
	return s
}

// OpCommand returns `op`: one operation with a JSON input on stdin and a
// JSON output on stdout. Trebi runs it for `trebi connector call`.
func OpCommand() *cli.Command {
	return &cli.Command{
		Name:  "op",
		Usage: "Run one operation: JSON input on stdin, JSON output on stdout",
		Subcommands: []*cli.Command{
			opCommand("search", "Find messages (input: query, from, since, unread, folder, limit)", func(c *cli.Context, mb *mail.Mailbox, in []byte) (any, error) {
				var q mail.SearchQuery
				if err := decode(in, &q); err != nil {
					return nil, err
				}
				msgs, err := mb.Search(c.Context, q)
				return map[string]any{"messages": msgs}, err
			}),
			opCommand("read", "Read one message as text (input: id, html)", func(c *cli.Context, mb *mail.Mailbox, in []byte) (any, error) {
				var q struct {
					ID   string `json:"id"`
					HTML bool   `json:"html"`
				}
				if err := decode(in, &q); err != nil {
					return nil, err
				}
				if q.ID == "" {
					return nil, errors.New("id: give the id of a message from search")
				}
				msg, err := mb.Read(c.Context, q.ID, q.HTML)
				return map[string]any{"message": msg}, err
			}),
			opCommand("draft", "Save a draft (input: to, cc, bcc, subject, text, in_reply_to, or mime_base64)", func(c *cli.Context, mb *mail.Mailbox, in []byte) (any, error) {
				var d mail.Compose
				if err := decode(in, &d); err != nil {
					return nil, err
				}
				return mb.Draft(c.Context, d)
			}),
			opCommand("send", "Send a message (input: to, cc, bcc, subject, text, in_reply_to, or mime_base64)", func(c *cli.Context, mb *mail.Mailbox, in []byte) (any, error) {
				var d mail.Compose
				if err := decode(in, &d); err != nil {
					return nil, err
				}
				return mb.Send(c.Context, d)
			}),
		},
	}
}

type opFunc func(c *cli.Context, mb *mail.Mailbox, in []byte) (any, error)

func opCommand(name, usage string, run opFunc) *cli.Command {
	return &cli.Command{
		Name:  name,
		Usage: usage,
		Action: func(c *cli.Context) error {
			ctx, stop := signal.NotifyContext(c.Context, os.Interrupt, syscall.SIGTERM)
			defer stop()
			c.Context = ctx
			s := settingsFromCtx(c)
			if miss := s.Missing(); miss != nil {
				if s.Trebi {
					return fmt.Errorf("%s: set it in the connection settings", miss.Error())
				}
				return fmt.Errorf("%s: set %s", miss.Error(), miss.Name)
			}
			in, err := io.ReadAll(io.LimitReader(c.App.Reader, maxInput+1))
			if err != nil {
				return err
			}
			if len(in) > maxInput {
				return fmt.Errorf("the input is larger than %d bytes", maxInput)
			}
			out, err := run(c, mail.New(s), in)
			if err != nil {
				return err
			}
			enc := json.NewEncoder(c.App.Writer)
			enc.SetEscapeHTML(false)
			return enc.Encode(out)
		},
	}
}

// decode reads a JSON object. Empty input is an empty object. An unknown
// field is an error.
func decode(in []byte, v any) error {
	in = bytes.TrimSpace(in)
	if len(in) == 0 {
		return nil
	}
	d := json.NewDecoder(bytes.NewReader(in))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return fmt.Errorf("input: %w", err)
	}
	return nil
}
