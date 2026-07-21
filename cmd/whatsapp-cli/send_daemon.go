package main

import (
	"context"
	"fmt"

	"github.com/flarco/cli-tools/whatsapp-cli/internal/app"
	"github.com/flarco/cli-tools/whatsapp-cli/internal/ipc"
)

// sendHandler builds the IPC handler used by the listen daemon to execute
// forwarded send requests on the live WhatsApp connection.
func sendHandler(a *app.App) ipc.Handler {
	return func(ctx context.Context, req ipc.Request) ipc.Response {
		if req.To == "" {
			return ipc.Response{OK: false, Error: "missing 'to'"}
		}

		switch req.Cmd {
		case "send_text":
			if req.Message == "" {
				return ipc.Response{OK: false, Error: "missing 'message'"}
			}
			toJID, id, err := sendTextCore(ctx, a, req.To, req.Message)
			if err != nil {
				return ipc.Response{OK: false, Error: err.Error()}
			}
			return ipc.Response{OK: true, ID: id, To: toJID}

		case "send_file":
			if req.Path == "" {
				return ipc.Response{OK: false, Error: "missing 'path'"}
			}
			toJID, id, meta, err := sendFileCore(ctx, a, req.To, req.Path, req.Filename, req.Caption, req.Mime)
			if err != nil {
				return ipc.Response{OK: false, Error: err.Error()}
			}
			return ipc.Response{OK: true, ID: id, To: toJID, File: meta}

		default:
			return ipc.Response{OK: false, Error: fmt.Sprintf("unknown cmd %q", req.Cmd)}
		}
	}
}
