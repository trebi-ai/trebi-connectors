package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/flarco/cli-tools/whatsapp-cli/internal/app"
	"github.com/flarco/cli-tools/whatsapp-cli/internal/ipc"
	"github.com/flarco/cli-tools/whatsapp-cli/internal/lock"
	"github.com/flarco/cli-tools/whatsapp-cli/internal/out"
	"github.com/flarco/cli-tools/whatsapp-cli/internal/store"
	"github.com/flarco/cli-tools/whatsapp-cli/internal/wa"
)

// sendApp is the subset of *app.App the send core needs. Both the CLI direct
// path and the daemon handler pass an *app.App here.
type sendApp interface {
	WA() app.WAClient
	DB() *store.DB
}

// sendTextCore sends a text message and records it in the store. It is shared by
// the `send text` CLI path and the listen-daemon IPC handler so both produce
// identical DB writes.
// Returns the resolved recipient JID string and the sent message id.
func sendTextCore(ctx context.Context, a sendApp, to, message string) (string, string, error) {
	toJID, err := wa.ParseUserOrJID(to)
	if err != nil {
		return "", "", err
	}

	msgID, err := a.WA().SendText(ctx, toJID, message)
	if err != nil {
		return "", "", err
	}

	now := time.Now().UTC()
	chatName := a.WA().ResolveChatName(ctx, toJID, "")
	kind := chatKindFromJID(toJID)
	_ = a.DB().UpsertChat(toJID.String(), kind, chatName, now)
	_ = a.DB().UpsertMessage(store.UpsertMessageParams{
		ChatJID:    toJID.String(),
		ChatName:   chatName,
		MsgID:      string(msgID),
		SenderJID:  "",
		SenderName: "me",
		Timestamp:  now,
		FromMe:     true,
		Text:       message,
	})

	return toJID.String(), string(msgID), nil
}

// sendFileCore sends a file message and records it in the store. It is shared by
// the `send file` CLI path and the listen-daemon IPC handler.
// Returns the resolved recipient JID string, the sent message id, and file meta.
func sendFileCore(ctx context.Context, a sendApp, to, filePath, filename, caption, mimeOverride string) (string, string, map[string]string, error) {
	toJID, err := wa.ParseUserOrJID(to)
	if err != nil {
		return "", "", nil, err
	}
	id, meta, err := sendFile(ctx, a, toJID, filePath, filename, caption, mimeOverride)
	if err != nil {
		return "", "", nil, err
	}
	return toJID.String(), id, meta, nil
}

// forwardSend tries to hand the request to a running `whatsapp-cli listen` daemon over
// its Unix socket. It returns (resp, true) if a daemon accepted the request
// (whether the send succeeded or not — check resp.OK), or (_, false) if no live
// daemon is reachable, in which case the caller sends directly.
func forwardSend(flags *rootFlags, req ipc.Request) (ipc.Response, bool) {
	storeDir := resolveStoreDir(flags)

	conn, err := ipc.Dial(storeDir)
	if err != nil {
		return ipc.Response{}, false
	}
	resp, err := ipc.Send(conn, req)
	if err != nil {
		return ipc.Response{}, false
	}
	return resp, true
}

// sendDispatch routes a send either to a running `whatsapp-cli listen` daemon (over the
// Unix socket) or, if none is reachable, performs it directly via the `direct`
// callback (which acquires the store lock). If the direct path fails because the
// store is locked, a daemon started in the race window between our first dial
// and lock attempt — so we re-dial once and forward.
func sendDispatch(flags *rootFlags, req ipc.Request, direct func() error) error {
	if resp, ok := forwardSend(flags, req); ok {
		if !resp.OK {
			return fmt.Errorf("%s", resp.Error)
		}
		return printSendResult(flags, resp.To, resp.ID, resp.File)
	}

	err := direct()
	if err == nil || !errors.Is(err, lock.ErrLocked) {
		return err
	}

	// Lost the race to a daemon; forward to it instead.
	if resp, ok := forwardSend(flags, req); ok {
		if !resp.OK {
			return fmt.Errorf("%s", resp.Error)
		}
		return printSendResult(flags, resp.To, resp.ID, resp.File)
	}
	return err
}

// printSendResult writes the success output for a send, matching the format of
// both the direct and forwarded paths. meta is non-nil only for file sends.
func printSendResult(flags *rootFlags, to, id string, meta map[string]string) error {
	if flags.asJSON {
		payload := map[string]any{
			"sent": true,
			"to":   to,
			"id":   id,
		}
		if meta != nil {
			payload["file"] = meta
		}
		return out.WriteJSON(os.Stdout, payload)
	}
	if meta != nil {
		fmt.Fprintf(os.Stdout, "Sent %s to %s (id %s)\n", meta["name"], to, id)
		return nil
	}
	fmt.Fprintf(os.Stdout, "Sent to %s (id %s)\n", to, id)
	return nil
}
