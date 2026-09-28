package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/ipc"
)

func newSendFileCmd(flags *rootFlags) *cobra.Command {
	var to string
	var filePath string
	var filename string
	var caption string
	var mimeOverride string

	cmd := &cobra.Command{
		Use:   "file",
		Short: "Send a file (image/video/audio/document)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if to == "" || filePath == "" {
				return fmt.Errorf("--to and --file are required")
			}

			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			// Forward to a running `whatsapp-cli listen` daemon if present; the
			// daemon reads the file path itself, so it must be reachable on
			// the same host (it is — the socket is host-local).
			return sendDispatch(flags, ipc.Request{
				Cmd: "send_file", To: to, Path: filePath,
				Filename: filename, Caption: caption, Mime: mimeOverride,
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

				toJID, msgID, meta, err := sendFileCore(ctx, a, to, filePath, filename, caption, mimeOverride)
				if err != nil {
					return err
				}
				return printSendResult(flags, toJID, msgID, meta)
			})
		},
	}

	cmd.Flags().StringVar(&to, "to", "", "recipient phone number or JID")
	cmd.Flags().StringVar(&filePath, "file", "", "path to file")
	cmd.Flags().StringVar(&filename, "filename", "", "display name for the file (defaults to basename of --file)")
	cmd.Flags().StringVar(&caption, "caption", "", "caption (images/videos/documents)")
	cmd.Flags().StringVar(&mimeOverride, "mime", "", "override detected mime type")
	return cmd
}
