package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/app"
	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/ipc"
	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/lock"
	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/out"
)

func newMediaCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "media",
		Short: "Media download",
	}
	cmd.AddCommand(newMediaDownloadCmd(flags))
	return cmd
}

type mediaDownloadResult struct {
	Chat      string
	ID        string
	Path      string
	Bytes     int64
	MediaType string
	MimeType  string
}

// mediaDownloadCore downloads media for a stored message. Shared by the CLI
// direct path and the listen daemon IPC handler.
func mediaDownloadCore(ctx context.Context, a *app.App, chat, id, outputPath string, connect bool) (mediaDownloadResult, error) {
	info, err := a.DB().GetMediaDownloadInfo(chat, id)
	if err != nil {
		return mediaDownloadResult{}, err
	}
	if info.MediaType == "" || info.DirectPath == "" || len(info.MediaKey) == 0 {
		return mediaDownloadResult{}, fmt.Errorf("message has no downloadable media metadata (run `whatsapp-cli sync` first)")
	}

	target, err := a.ResolveMediaOutputPath(info, outputPath)
	if err != nil {
		return mediaDownloadResult{}, err
	}

	if connect {
		if err := a.Connect(ctx, false, nil); err != nil {
			return mediaDownloadResult{}, err
		}
	}

	bytes, err := a.WA().DownloadMediaToFile(ctx, info.DirectPath, info.FileEncSHA256, info.FileSHA256, info.MediaKey, info.FileLength, info.MediaType, "", target)
	if err != nil {
		return mediaDownloadResult{}, err
	}
	_ = a.MarkMediaDownloaded(info, target, time.Now().UTC())

	return mediaDownloadResult{
		Chat:      info.ChatJID,
		ID:        info.MsgID,
		Path:      target,
		Bytes:     bytes,
		MediaType: info.MediaType,
		MimeType:  info.MimeType,
	}, nil
}

func newMediaDownloadCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var id string
	var outputPath string

	cmd := &cobra.Command{
		Use:   "download",
		Short: "Download media for a message",
		Long: `Download media (image/video/audio/document) for a stored message.

If whatsapp-cli listen is running, this is forwarded over its Unix socket so you
do not need to stop the listener.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if chat == "" || id == "" {
				return fmt.Errorf("--chat and --id are required")
			}

			req := ipc.Request{Cmd: "media_download", Chat: chat, MsgID: id, Output: outputPath}
			return mediaDispatch(flags, req, func() error {
				ctx, cancel := withTimeout(context.Background(), flags)
				defer cancel()

				a, lk, err := newApp(ctx, flags, true, false)
				if err != nil {
					return err
				}
				defer closeApp(a, lk)

				if err := a.EnsureAuthed(); err != nil {
					return err
				}

				result, err := mediaDownloadCore(ctx, a, chat, id, outputPath, true)
				if err != nil {
					return err
				}
				return printMediaDownloadResult(flags, result)
			})
		},
	}

	cmd.Flags().StringVar(&chat, "chat", "", "chat JID")
	cmd.Flags().StringVar(&id, "id", "", "message ID")
	cmd.Flags().StringVar(&outputPath, "output", "", "output file or directory (default: store media dir)")
	_ = cmd.MarkFlagRequired("chat")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

// mediaDispatch prefers the listen daemon socket, else runs direct (acquires lock).
func mediaDispatch(flags *rootFlags, req ipc.Request, direct func() error) error {
	if resp, ok := forwardSend(flags, req); ok {
		if !resp.OK {
			return fmt.Errorf("%s", resp.Error)
		}
		return printMediaDownloadResult(flags, mediaDownloadResult{
			Chat:      resp.Chat,
			ID:        resp.ID,
			Path:      resp.Path,
			Bytes:     resp.Bytes,
			MediaType: resp.MediaType,
			MimeType:  resp.MimeType,
		})
	}

	err := direct()
	if err == nil || !errors.Is(err, lock.ErrLocked) {
		return err
	}

	if resp, ok := forwardSend(flags, req); ok {
		if !resp.OK {
			return fmt.Errorf("%s", resp.Error)
		}
		return printMediaDownloadResult(flags, mediaDownloadResult{
			Chat:      resp.Chat,
			ID:        resp.ID,
			Path:      resp.Path,
			Bytes:     resp.Bytes,
			MediaType: resp.MediaType,
			MimeType:  resp.MimeType,
		})
	}
	return err
}

func printMediaDownloadResult(flags *rootFlags, result mediaDownloadResult) error {
	if flags.asJSON {
		return out.WriteJSON(os.Stdout, map[string]any{
			"chat":       result.Chat,
			"id":         result.ID,
			"path":       result.Path,
			"bytes":      result.Bytes,
			"media_type": result.MediaType,
			"mime_type":  result.MimeType,
			"downloaded": true,
		})
	}
	fmt.Fprintf(os.Stdout, "%s (%d bytes)\n", result.Path, result.Bytes)
	return nil
}
