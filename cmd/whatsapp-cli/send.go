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
	cmd.AddCommand(newSendChatPresenceCmd(flags))
	return cmd
}

func newSendChatPresenceCmd(flags *rootFlags) *cobra.Command {
	var to string
	var state string
	var media string

	cmd := &cobra.Command{
		Use:   "chat-presence",
		Short: "Send chat presence (typing / recording indicator)",
		Long: `Update the "X is typing…" / recording indicator in a chat.

States: composing (default), paused.
Media: text (default), audio (voice-note recording).

If whatsapp-cli listen is running, this is forwarded over its Unix socket.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if to == "" {
				return fmt.Errorf("--to is required")
			}
			// Validate flags early (before connect/IPC).
			if _, _, _, _, err := parseChatPresenceFlags(state, media); err != nil {
				return err
			}

			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			return sendDispatch(flags, ipc.Request{
				Cmd: "send_chat_presence", To: to, State: state, Media: media,
			}, func() error {
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

				toJID, st, med, err := sendChatPresenceCore(ctx, a, to, state, media)
				if err != nil {
					return err
				}
				return printChatPresenceResult(flags, toJID, st, med)
			})
		},
	}

	cmd.Flags().StringVar(&to, "to", "", "recipient phone number or JID (required)")
	cmd.Flags().StringVar(&state, "state", "composing", "composing or paused")
	cmd.Flags().StringVar(&media, "media", "text", "text or audio")
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
