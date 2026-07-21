package main

import (
	"context"
	"fmt"

	"github.com/flarco/cli-tools/whatsapp-cli/internal/ipc"
	"github.com/spf13/cobra"
)

func newSendCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "send",
		Short: "Send messages",
	}
	cmd.AddCommand(newSendTextCmd(flags))
	cmd.AddCommand(newSendFileCmd(flags))
	return cmd
}

func newSendTextCmd(flags *rootFlags) *cobra.Command {
	var to string
	var message string

	cmd := &cobra.Command{
		Use:   "text",
		Short: "Send a text message",
		RunE: func(cmd *cobra.Command, args []string) error {
			if to == "" || message == "" {
				return fmt.Errorf("--to and --message are required")
			}

			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			// If a `whatsapp-cli listen` daemon is running, forward the send to it
			// (it holds the single live WhatsApp connection). Otherwise send
			// directly, acquiring the store lock ourselves.
			return sendDispatch(flags, ipc.Request{Cmd: "send_text", To: to, Message: message}, func() error {
				a, lk, err := newApp(ctx, flags, true, false)
				if err != nil {
					return err
				}
				defer closeApp(a, lk)

				if err := a.EnsureAuthed(); err != nil {
					return err
				}
				if err := a.Connect(ctx, false, nil); err != nil {
					return err
				}

				toJID, msgID, err := sendTextCore(ctx, a, to, message)
				if err != nil {
					return err
				}
				return printSendResult(flags, toJID, msgID, nil)
			})
		},
	}

	cmd.Flags().StringVar(&to, "to", "", "recipient phone number or JID")
	cmd.Flags().StringVar(&message, "message", "", "message text")
	return cmd
}
