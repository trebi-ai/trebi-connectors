package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/trebi-ai/trebi-connectors/src/whatsapp-cli/internal/app"
	"github.com/trebi-ai/trebi-connectors/src/whatsapp-cli/internal/ipc"
	"github.com/trebi-ai/trebi-connectors/src/whatsapp-cli/internal/lock"
	"github.com/trebi-ai/trebi-connectors/src/whatsapp-cli/internal/out"
	"github.com/trebi-ai/trebi-connectors/src/whatsapp-cli/internal/store"
	"github.com/trebi-ai/trebi-connectors/src/whatsapp-cli/internal/wa"
	"go.mau.fi/whatsmeow/types"
)

// sendApp is the subset of *app.App the send core needs. Both the CLI direct
// path and the daemon handler pass an *app.App here.
type sendApp interface {
	WA() app.WAClient
	DB() *store.DB
	SendFile(ctx context.Context, to types.JID, f app.OutFile) (string, map[string]string, error)
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

// parseChatPresenceFlags maps CLI/IPC state+media strings to whatsmeow types.
// mediaCLI is "text" or "audio" (CLI form); wire media for text is empty.
func parseChatPresenceFlags(state, mediaCLI string) (types.ChatPresence, types.ChatPresenceMedia, string, string, error) {
	state = strings.TrimSpace(strings.ToLower(state))
	if state == "" {
		state = "composing"
	}
	var cp types.ChatPresence
	switch state {
	case "composing":
		cp = types.ChatPresenceComposing
	case "paused":
		cp = types.ChatPresencePaused
	default:
		return "", "", "", "", fmt.Errorf("--state must be composing or paused")
	}

	mediaCLI = strings.TrimSpace(strings.ToLower(mediaCLI))
	if mediaCLI == "" {
		mediaCLI = "text"
	}
	var media types.ChatPresenceMedia
	switch mediaCLI {
	case "text":
		media = types.ChatPresenceMediaText
	case "audio":
		media = types.ChatPresenceMediaAudio
	default:
		return "", "", "", "", fmt.Errorf("--media must be text or audio")
	}
	return cp, media, state, mediaCLI, nil
}

// sendChatPresenceCore sends a chat presence (typing/recording) update.
// Returns resolved JID and normalized state/media CLI labels.
func sendChatPresenceCore(ctx context.Context, a sendApp, to, state, mediaCLI string) (string, string, string, error) {
	toJID, err := wa.ParseUserOrJID(to)
	if err != nil {
		return "", "", "", err
	}
	cp, media, stateOut, mediaOut, err := parseChatPresenceFlags(state, mediaCLI)
	if err != nil {
		return "", "", "", err
	}
	if err := a.WA().SendChatPresence(ctx, toJID, cp, media); err != nil {
		return "", "", "", err
	}
	return toJID.String(), stateOut, mediaOut, nil
}

// parseReceiptType maps CLI/IPC receipt type to whatsmeow types.
// Empty defaults to read. delivered is intentionally not supported (use presence).
func parseReceiptType(s string) (types.ReceiptType, string, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		s = "read"
	}
	switch s {
	case "read":
		return types.ReceiptTypeRead, "read", nil
	case "played":
		return types.ReceiptTypePlayed, "played", nil
	case "delivered":
		return "", "", fmt.Errorf("delivered is not supported; mark online with `send presence --state available` or `listen --presence available` so delivery receipts are sent on receive")
	default:
		return "", "", fmt.Errorf("type must be read or played")
	}
}

// parsePresenceState maps available|unavailable.
func parsePresenceState(s string) (types.Presence, string, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return "", "", fmt.Errorf("state is required (available or unavailable)")
	}
	switch s {
	case "available":
		return types.PresenceAvailable, "available", nil
	case "unavailable":
		return types.PresenceUnavailable, "unavailable", nil
	default:
		return "", "", fmt.Errorf("state must be available or unavailable")
	}
}

// normalizeMsgIDs trims and drops empties; rejects empty list.
func normalizeMsgIDs(ids []string) ([]string, error) {
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("at least one --id is required")
	}
	return out, nil
}

// resolveReceiptSender picks the MarkRead sender JID.
// Prefer --sender when set; otherwise load from local store. Groups require a sender.
// Rejects from_me messages when present in the local store.
func resolveReceiptSender(a sendApp, chat types.JID, ids []string, senderFlag string) (types.JID, error) {
	var dbSender string
	var haveDB bool

	chatStr := chat.String()
	for _, id := range ids {
		msg, err := a.DB().GetMessage(chatStr, id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return types.JID{}, fmt.Errorf("lookup message %s: %w", id, err)
		}
		if msg.FromMe {
			return types.JID{}, fmt.Errorf("message %s is from you; receipts are only for incoming messages", id)
		}
		if msg.SenderJID == "" {
			continue
		}
		if haveDB && dbSender != msg.SenderJID {
			return types.JID{}, fmt.Errorf("messages have different senders (%s vs %s); call once per sender", dbSender, msg.SenderJID)
		}
		dbSender = msg.SenderJID
		haveDB = true
	}

	if strings.TrimSpace(senderFlag) != "" {
		flagJID, err := wa.ParseUserOrJID(senderFlag)
		if err != nil {
			return types.JID{}, fmt.Errorf("--sender: %w", err)
		}
		if haveDB && dbSender != flagJID.String() {
			// Allow AD vs non-AD string differences by comparing user+server.
			dbJID, dbErr := types.ParseJID(dbSender)
			if dbErr != nil || dbJID.User != flagJID.User || dbJID.Server != flagJID.Server {
				return types.JID{}, fmt.Errorf("--sender %s does not match stored sender %s", flagJID, dbSender)
			}
		}
		return flagJID, nil
	}

	if haveDB {
		j, err := types.ParseJID(dbSender)
		if err != nil {
			return types.JID{}, fmt.Errorf("stored sender %q: %w", dbSender, err)
		}
		return j, nil
	}

	if wa.IsGroupJID(chat) {
		return types.JID{}, fmt.Errorf("--sender is required for group chats when message is not in the local store")
	}
	// DMs: empty sender is fine for MarkRead.
	return types.JID{}, nil
}

// receiptResult is the normalized output of a successful send receipt.
type receiptResult struct {
	Chat       string
	Type       string
	MessageIDs []string
	Sender     string
	Timestamp  time.Time
}

// sendReceiptCore sends a read/played receipt for the given message IDs.
func sendReceiptCore(ctx context.Context, a sendApp, chat, receiptTypeCLI, senderFlag, at string, ids []string) (receiptResult, error) {
	var zero receiptResult
	ids, err := normalizeMsgIDs(ids)
	if err != nil {
		return zero, err
	}
	rt, typeOut, err := parseReceiptType(receiptTypeCLI)
	if err != nil {
		return zero, err
	}
	chatJID, err := wa.ParseUserOrJID(chat)
	if err != nil {
		return zero, err
	}
	sender, err := resolveReceiptSender(a, chatJID, ids, senderFlag)
	if err != nil {
		return zero, err
	}

	ts := time.Now().UTC()
	if strings.TrimSpace(at) != "" {
		t, err := parseTime(at)
		if err != nil {
			return zero, fmt.Errorf("--at: %w", err)
		}
		ts = t.UTC()
	}

	msgIDs := make([]types.MessageID, len(ids))
	for i, id := range ids {
		msgIDs[i] = types.MessageID(id)
	}
	if err := a.WA().MarkRead(ctx, msgIDs, ts, chatJID, sender, rt); err != nil {
		return zero, err
	}

	res := receiptResult{
		Chat:       chatJID.String(),
		Type:       typeOut,
		MessageIDs: ids,
		Timestamp:  ts,
	}
	if !sender.IsEmpty() {
		res.Sender = sender.String()
	}
	return res, nil
}

// sendPresenceCore sends global available/unavailable presence.
func sendPresenceCore(ctx context.Context, a sendApp, state string) (string, error) {
	p, stateOut, err := parsePresenceState(state)
	if err != nil {
		return "", err
	}
	if err := a.WA().SendPresence(ctx, p); err != nil {
		return "", err
	}
	return stateOut, nil
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
		return printForwardedResult(flags, req, resp)
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
		return printForwardedResult(flags, req, resp)
	}
	return err
}

func printForwardedResult(flags *rootFlags, req ipc.Request, resp ipc.Response) error {
	switch req.Cmd {
	case "send_chat_presence":
		return printChatPresenceResult(flags, resp.To, resp.State, resp.Media)
	case "send_receipt":
		ts, _ := time.Parse(time.RFC3339Nano, resp.Timestamp)
		if ts.IsZero() {
			ts, _ = time.Parse(time.RFC3339, resp.Timestamp)
		}
		return printReceiptResult(flags, receiptResult{
			Chat:       resp.Chat,
			Type:       resp.ReceiptType,
			MessageIDs: resp.MessageIDs,
			Sender:     resp.Sender,
			Timestamp:  ts,
		})
	case "send_presence":
		return printPresenceResult(flags, resp.State)
	default:
		return printSendResult(flags, resp.To, resp.ID, resp.File)
	}
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

func printChatPresenceResult(flags *rootFlags, to, state, media string) error {
	if flags.asJSON {
		return out.WriteJSON(os.Stdout, map[string]any{
			"to":    to,
			"state": state,
			"media": media,
		})
	}
	if media != "" && media != "text" {
		fmt.Fprintf(os.Stdout, "chat-presence %s (media=%s) → %s\n", state, media, to)
		return nil
	}
	fmt.Fprintf(os.Stdout, "chat-presence %s → %s\n", state, to)
	return nil
}

func printReceiptResult(flags *rootFlags, res receiptResult) error {
	if flags.asJSON {
		payload := map[string]any{
			"chat":        res.Chat,
			"type":        res.Type,
			"message_ids": res.MessageIDs,
			"timestamp":   res.Timestamp.UTC().Format(time.RFC3339Nano),
		}
		if res.Sender != "" {
			payload["sender"] = res.Sender
		}
		return out.WriteJSON(os.Stdout, payload)
	}
	n := len(res.MessageIDs)
	msg := "message"
	if n != 1 {
		msg = "messages"
	}
	if res.Sender != "" {
		fmt.Fprintf(os.Stdout, "receipt %s → %s (sender %s, %d %s)\n", res.Type, res.Chat, res.Sender, n, msg)
		return nil
	}
	fmt.Fprintf(os.Stdout, "receipt %s → %s (%d %s)\n", res.Type, res.Chat, n, msg)
	return nil
}

func printPresenceResult(flags *rootFlags, state string) error {
	if flags.asJSON {
		return out.WriteJSON(os.Stdout, map[string]any{
			"state": state,
		})
	}
	fmt.Fprintf(os.Stdout, "presence %s\n", state)
	return nil
}
