package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/trebi-ai/trebi-connectors/src/whatsapp-cli/internal/ipc"
)

func newSendCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "send",
		Short: "Send messages",
	}
	cmd.AddCommand(newSendTextCmd(flags))
	cmd.AddCommand(newSendFileCmd(flags))
	cmd.AddCommand(newSendChatPresenceCmd(flags))
	cmd.AddCommand(newSendReceiptCmd(flags))
	cmd.AddCommand(newSendPresenceCmd(flags))
	return cmd
}

func newSendReceiptCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var ids []string
	var receiptType string
	var sender string
	var at string

	cmd := &cobra.Command{
		Use:   "receipt",
		Short: "Send a read or played receipt (double checks)",
		Long: `Send a read (blue double checks) or played receipt for incoming messages.

Types: read (default), played (voice notes / view-once).
Delivery receipts (gray double checks) are not sent with this command — mark the
session online with 'send presence --state available' or 'listen --presence available'
so whatsmeow sends them automatically when messages arrive.

For group chats, --sender is required unless the message is already in the local
store (then sender is resolved from the DB). All --id values must share one sender.

If whatsapp-cli listen is running, this is forwarded over its Unix socket.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if chat == "" {
				return fmt.Errorf("--chat is required")
			}
			if _, err := normalizeMsgIDs(ids); err != nil {
				return err
			}
			if _, _, err := parseReceiptType(receiptType); err != nil {
				return fmt.Errorf("--type: %w", err)
			}
			if strings.TrimSpace(at) != "" {
				if _, err := parseTime(at); err != nil {
					return fmt.Errorf("--at: %w", err)
				}
			}

			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			return sendDispatch(flags, ipc.Request{
				Cmd: "send_receipt", Chat: chat, MessageIDs: ids,
				ReceiptType: receiptType, Sender: sender, At: at,
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

				res, err := sendReceiptCore(ctx, a, chat, receiptType, sender, at, ids)
				if err != nil {
					return err
				}
				return printReceiptResult(flags, res)
			})
		},
	}

	cmd.Flags().StringVar(&chat, "chat", "", "chat JID or phone (required)")
	cmd.Flags().StringArrayVar(&ids, "id", nil, "message ID (repeatable; required)")
	cmd.Flags().StringVar(&receiptType, "type", "read", "read or played")
	cmd.Flags().StringVar(&sender, "sender", "", "message sender (required for groups if not in local store)")
	cmd.Flags().StringVar(&at, "at", "", "read timestamp (RFC3339 or YYYY-MM-DD; default now)")
	return cmd
}

func newSendPresenceCmd(flags *rootFlags) *cobra.Command {
	var state string

	cmd := &cobra.Command{
		Use:   "presence",
		Short: "Set global online/offline presence",
		Long: `Update your global presence (available / unavailable).

'available' marks the session online and enables active delivery receipts
(two gray checks when messages are received while connected).

If whatsapp-cli listen is running, this is forwarded over its Unix socket.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, _, err := parsePresenceState(state); err != nil {
				return fmt.Errorf("--state: %w", err)
			}

			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			return sendDispatch(flags, ipc.Request{
				Cmd: "send_presence", State: state,
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

				stateOut, err := sendPresenceCore(ctx, a, state)
				if err != nil {
					return err
				}
				return printPresenceResult(flags, stateOut)
			})
		},
	}

	cmd.Flags().StringVar(&state, "state", "", "available or unavailable (required)")
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
