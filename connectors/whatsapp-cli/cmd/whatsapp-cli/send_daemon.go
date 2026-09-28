package main

import (
	"context"
	"fmt"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/app"
	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/ipc"
)

// sendHandler builds the IPC handler used by the listen daemon to execute
// forwarded send / media requests on the live WhatsApp connection.
func sendHandler(a *app.App) ipc.Handler {
	return func(ctx context.Context, req ipc.Request) ipc.Response {
		switch req.Cmd {
		case "send_text":
			if req.To == "" {
				return ipc.Response{OK: false, Error: "missing 'to'"}
			}
			if req.Message == "" {
				return ipc.Response{OK: false, Error: "missing 'message'"}
			}
			toJID, id, err := sendTextCore(ctx, a, req.To, req.Message)
			if err != nil {
				return ipc.Response{OK: false, Error: err.Error()}
			}
			return ipc.Response{OK: true, ID: id, To: toJID}

		case "send_file":
			if req.To == "" {
				return ipc.Response{OK: false, Error: "missing 'to'"}
			}
			if req.Path == "" {
				return ipc.Response{OK: false, Error: "missing 'path'"}
			}
			toJID, id, meta, err := sendFileCore(ctx, a, req.To, req.Path, req.Filename, req.Caption, req.Mime)
			if err != nil {
				return ipc.Response{OK: false, Error: err.Error()}
			}
			return ipc.Response{OK: true, ID: id, To: toJID, File: meta}

		case "send_chat_presence":
			if req.To == "" {
				return ipc.Response{OK: false, Error: "missing 'to'"}
			}
			toJID, state, media, err := sendChatPresenceCore(ctx, a, req.To, req.State, req.Media)
			if err != nil {
				return ipc.Response{OK: false, Error: err.Error()}
			}
			return ipc.Response{OK: true, To: toJID, State: state, Media: media}

		case "send_receipt":
			if req.Chat == "" {
				return ipc.Response{OK: false, Error: "missing 'chat'"}
			}
			res, err := sendReceiptCore(ctx, a, req.Chat, req.ReceiptType, req.Sender, req.At, req.MessageIDs)
			if err != nil {
				return ipc.Response{OK: false, Error: err.Error()}
			}
			return ipc.Response{
				OK:          true,
				Chat:        res.Chat,
				ReceiptType: res.Type,
				MessageIDs:  res.MessageIDs,
				Sender:      res.Sender,
				Timestamp:   res.Timestamp.UTC().Format(time.RFC3339Nano),
			}

		case "send_presence":
			state, err := sendPresenceCore(ctx, a, req.State)
			if err != nil {
				return ipc.Response{OK: false, Error: err.Error()}
			}
			return ipc.Response{OK: true, State: state}

		case "media_download":
			if req.Chat == "" || req.MsgID == "" {
				return ipc.Response{OK: false, Error: "missing 'chat' or 'msg_id'"}
			}
			// Daemon already holds the live connection — do not reconnect.
			result, err := mediaDownloadCore(ctx, a, req.Chat, req.MsgID, req.Output, false)
			if err != nil {
				return ipc.Response{OK: false, Error: err.Error()}
			}
			return ipc.Response{
				OK:        true,
				Chat:      result.Chat,
				ID:        result.ID,
				Path:      result.Path,
				Bytes:     result.Bytes,
				MediaType: result.MediaType,
				MimeType:  result.MimeType,
			}

		default:
			return ipc.Response{OK: false, Error: fmt.Sprintf("unknown cmd %q", req.Cmd)}
		}
	}
}
