package app

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/flarco/cli-tools/whatsapp-cli/internal/wa"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// ListenFilter selects and shapes events for a single listener (the anchor's own
// stdout, or a secondary subscriber attached over the socket).
type ListenFilter struct {
	// Categories is the set of enabled event categories (already expanded — "all" is not a key here).
	Categories map[string]bool
	// ChatFilter, when non-empty, drops events whose chat JID doesn't match.
	ChatFilter string
	// FromFilter, when non-empty, drops events whose sender JID doesn't match.
	FromFilter string
	// ExcludeSelf drops events with IsFromMe == true (Message / FBMessage only).
	ExcludeSelf bool
	// Raw emits json.Marshal(evt) in the payload instead of normalized shapes.
	Raw bool
}

// ListenOptions configures the live event stream for the anchor process.
type ListenOptions struct {
	// ListenFilter is the anchor's own filter, applied to events written to Out.
	ListenFilter
	// MaxReconnect bounds the reconnect retry duration. 0 means unlimited.
	MaxReconnect time.Duration
	// Presence, when non-empty ("available" or "unavailable"), is sent after
	// connect and after each successful reconnect so delivery receipts work.
	Presence string
	// Out is where the anchor's own JSONL lines are written (defaults to os.Stdout).
	Out io.Writer
	// Subscribers, when non-nil, is consulted on every event so secondary
	// listeners attached over the socket receive their own filtered stream.
	Subscribers *SubscriberSet
}

// Listen runs a live event stream until ctx is cancelled. Events are filtered by the
// options and emitted as JSONL lines on opts.Out. Status messages go to stderr.
func (a *App) Listen(ctx context.Context, opts ListenOptions) error {
	if err := a.OpenWA(); err != nil {
		return err
	}

	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	bw := bufio.NewWriter(opts.Out)
	defer bw.Flush()

	var writeMu sync.Mutex
	writeLine := func(line []byte) {
		writeMu.Lock()
		defer writeMu.Unlock()
		bw.Write(line)
		bw.WriteByte('\n')
		bw.Flush()
	}

	disconnected := make(chan struct{}, 1)

	handlerID := a.wa.AddEventHandler(func(evt interface{}) {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "\nevent handler panic (recovered): %v\n", r)
			}
		}()

		// Connection lifecycle side effects first.
		switch evt.(type) {
		case *events.Disconnected:
			select {
			case disconnected <- struct{}{}:
			default:
			}
		}

		eventName, category, chatJID, senderJID, isFromMe, payload := classifyEvent(ctx, a, evt)
		if eventName == "" {
			return
		}

		ce := classifiedEvent{
			evt: evt, name: eventName, category: category,
			chatJID: chatJID, senderJID: senderJID, isFromMe: isFromMe, payload: payload,
		}

		// Anchor's own stdout.
		if line, ok := ce.render(opts.ListenFilter); ok {
			writeLine(line)
		}

		// Secondary subscribers attached over the socket, each with its own filter.
		if opts.Subscribers != nil {
			opts.Subscribers.dispatch(ce)
		}
	})
	defer a.wa.RemoveEventHandler(handlerID)

	if err := a.Connect(ctx, false, nil); err != nil {
		return err
	}
	if err := a.applyListenPresence(ctx, opts.Presence); err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, "Listening for events (Ctrl+C to stop)...")

	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, "\nStopping listen.")
			return nil
		case <-disconnected:
			fmt.Fprintln(os.Stderr, "Reconnecting...")
			if err := a.reconnect(ctx, opts.MaxReconnect); err != nil {
				return err
			}
			if err := a.applyListenPresence(ctx, opts.Presence); err != nil {
				fmt.Fprintf(os.Stderr, "presence after reconnect: %v\n", err)
			}
		}
	}
}

// applyListenPresence sends global presence when configured on the listen anchor.
func (a *App) applyListenPresence(ctx context.Context, presence string) error {
	presence = strings.TrimSpace(strings.ToLower(presence))
	if presence == "" {
		return nil
	}
	var state types.Presence
	switch presence {
	case "available":
		state = types.PresenceAvailable
	case "unavailable":
		state = types.PresenceUnavailable
	default:
		return fmt.Errorf("presence must be available or unavailable")
	}
	if err := a.wa.SendPresence(ctx, state); err != nil {
		return fmt.Errorf("send presence %s: %w", presence, err)
	}
	fmt.Fprintf(os.Stderr, "Presence set to %s\n", presence)
	return nil
}

// classifyEvent maps a whatsmeow event to (name, category, chat, sender, isFromMe, normalizedPayload).
// Returns an empty name for events that should be ignored entirely.
func classifyEvent(ctx context.Context, a *App, evt interface{}) (string, string, string, string, bool, any) {
	switch v := evt.(type) {
	// --- messages ---
	case *events.Message:
		// Group messages arrive as two events: a SenderKeyDistributionMessage
		// carrier (for e2e key setup, no user content) and the real skmsg.
		// Drop the SKDM-only event so consumers don't see a duplicate.
		if isSKDMOnly(v) {
			return "", "", "", "", false, nil
		}
		pm := wa.ParseLiveMessage(v)
		// Persist so media download / offline search work while listen is the
		// only long-running process (same path sync uses).
		_ = a.storeParsedMessage(ctx, pm)
		chat := pm.Chat.String()
		sender := pm.SenderJID
		return "message", "messages", chat, sender, pm.FromMe, normalizeMessage(ctx, a, pm)

	case *events.FBMessage:
		chat := v.Info.Chat.String()
		sender := v.Info.Sender.String()
		return "fb_message", "messages", chat, sender, v.Info.IsFromMe, map[string]any{
			"id":        v.Info.ID,
			"chat":      chat,
			"sender":    sender,
			"timestamp": v.Info.Timestamp.UTC().Format(time.RFC3339Nano),
			"from_me":   v.Info.IsFromMe,
			"push_name": v.Info.PushName,
		}

	case *events.UndecryptableMessage:
		chat := v.Info.Chat.String()
		sender := v.Info.Sender.String()
		return "undecryptable_message", "messages", chat, sender, v.Info.IsFromMe, map[string]any{
			"id":                v.Info.ID,
			"chat":              chat,
			"sender":            sender,
			"timestamp":         v.Info.Timestamp.UTC().Format(time.RFC3339Nano),
			"from_me":           v.Info.IsFromMe,
			"is_unavailable":    v.IsUnavailable,
			"decrypt_fail_mode": string(v.DecryptFailMode),
		}

	// --- receipts ---
	case *events.Receipt:
		chat := v.Chat.String()
		sender := v.Sender.String()
		return "receipt", "receipts", chat, sender, v.IsFromMe, map[string]any{
			"chat":        chat,
			"sender":      sender,
			"is_from_me":  v.IsFromMe,
			"is_group":    v.IsGroup,
			"message_ids": v.MessageIDs,
			"type":        string(v.Type),
			"timestamp":   v.Timestamp.UTC().Format(time.RFC3339Nano),
		}

	// --- presence ---
	case *events.Presence:
		from := v.From.String()
		var lastSeen string
		if !v.LastSeen.IsZero() {
			lastSeen = v.LastSeen.UTC().Format(time.RFC3339Nano)
		}
		return "presence", "presence", from, from, false, map[string]any{
			"from":        from,
			"unavailable": v.Unavailable,
			"last_seen":   lastSeen,
		}

	case *events.ChatPresence:
		chat := v.Chat.String()
		sender := v.Sender.String()
		return "chat_presence", "presence", chat, sender, v.IsFromMe, map[string]any{
			"chat":   chat,
			"sender": sender,
			"state":  string(v.State),
			"media":  string(v.Media),
		}

	// --- connection ---
	case *events.Connected:
		return "connected", "connection", "", "", false, map[string]any{}
	case *events.Disconnected:
		return "disconnected", "connection", "", "", false, map[string]any{}
	case *events.LoggedOut:
		return "logged_out", "connection", "", "", false, map[string]any{
			"on_connect": v.OnConnect,
			"reason":     v.Reason.String(),
		}
	case *events.StreamReplaced:
		return "stream_replaced", "connection", "", "", false, map[string]any{}
	case *events.StreamError:
		return "stream_error", "connection", "", "", false, map[string]any{
			"code": v.Code,
		}
	case *events.ConnectFailure:
		return "connect_failure", "connection", "", "", false, map[string]any{
			"reason":  v.Reason.NumberString(),
			"message": v.Message,
		}
	case *events.KeepAliveTimeout:
		return "keepalive_timeout", "connection", "", "", false, map[string]any{
			"error_count":  v.ErrorCount,
			"last_success": v.LastSuccess.UTC().Format(time.RFC3339Nano),
		}
	case *events.KeepAliveRestored:
		return "keepalive_restored", "connection", "", "", false, map[string]any{}
	case *events.TemporaryBan:
		return "temporary_ban", "connection", "", "", false, map[string]any{
			"code":   int(v.Code),
			"expire": v.Expire.String(),
		}
	case *events.ClientOutdated:
		return "client_outdated", "connection", "", "", false, map[string]any{}
	case *events.ManualLoginReconnect:
		return "manual_login_reconnect", "connection", "", "", false, map[string]any{}
	case *events.OfflineSyncPreview:
		return "offline_sync_preview", "connection", "", "", false, map[string]any{
			"total":            v.Total,
			"app_data_changes": v.AppDataChanges,
			"messages":         v.Messages,
			"notifications":    v.Notifications,
			"receipts":         v.Receipts,
		}
	case *events.OfflineSyncCompleted:
		return "offline_sync_completed", "connection", "", "", false, map[string]any{
			"count": v.Count,
		}

	// --- calls ---
	case *events.CallOffer:
		return "call_offer", "calls", "", v.From.String(), false, callBasic(v.BasicCallMeta)
	case *events.CallAccept:
		return "call_accept", "calls", "", v.From.String(), false, callBasic(v.BasicCallMeta)
	case *events.CallPreAccept:
		return "call_pre_accept", "calls", "", v.From.String(), false, callBasic(v.BasicCallMeta)
	case *events.CallTransport:
		return "call_transport", "calls", "", v.From.String(), false, callBasic(v.BasicCallMeta)
	case *events.CallOfferNotice:
		m := callBasic(v.BasicCallMeta)
		m["media"] = v.Media
		m["type"] = v.Type
		return "call_offer_notice", "calls", "", v.From.String(), false, m
	case *events.CallRelayLatency:
		return "call_relay_latency", "calls", "", v.From.String(), false, callBasic(v.BasicCallMeta)
	case *events.CallTerminate:
		m := callBasic(v.BasicCallMeta)
		m["reason"] = v.Reason
		return "call_terminate", "calls", "", v.From.String(), false, m
	case *events.CallReject:
		return "call_reject", "calls", "", v.From.String(), false, callBasic(v.BasicCallMeta)
	case *events.UnknownCallEvent:
		return "call_unknown", "calls", "", "", false, map[string]any{}

	// --- groups ---
	case *events.JoinedGroup:
		jid := v.GroupInfo.JID.String()
		return "joined_group", "groups", jid, "", false, map[string]any{
			"group":      jid,
			"reason":     v.Reason,
			"type":       v.Type,
			"create_key": v.CreateKey,
			"notify":     v.Notify,
		}
	case *events.GroupInfo:
		jid := v.JID.String()
		var senderStr string
		if v.Sender != nil {
			senderStr = v.Sender.String()
		}
		payload := map[string]any{
			"group":     jid,
			"sender":    senderStr,
			"notify":    v.Notify,
			"timestamp": v.Timestamp.UTC().Format(time.RFC3339Nano),
		}
		if len(v.Join) > 0 {
			payload["join"] = jidsToStrings(v.Join)
		}
		if len(v.Leave) > 0 {
			payload["leave"] = jidsToStrings(v.Leave)
		}
		if len(v.Promote) > 0 {
			payload["promote"] = jidsToStrings(v.Promote)
		}
		if len(v.Demote) > 0 {
			payload["demote"] = jidsToStrings(v.Demote)
		}
		if v.Name != nil {
			payload["name"] = v.Name.Name
		}
		if v.Topic != nil {
			payload["topic"] = v.Topic.Topic
		}
		return "group_info", "groups", jid, senderStr, false, payload
	case *events.Picture:
		jid := v.JID.String()
		return "picture", "groups", jid, v.Author.String(), false, map[string]any{
			"jid":        jid,
			"author":     v.Author.String(),
			"timestamp":  v.Timestamp.UTC().Format(time.RFC3339Nano),
			"remove":     v.Remove,
			"picture_id": v.PictureID,
		}

	// --- appstate ---
	case *events.Contact:
		jid := v.JID.String()
		return "contact", "appstate", jid, jid, false, map[string]any{
			"jid":            jid,
			"timestamp":      v.Timestamp.UTC().Format(time.RFC3339Nano),
			"from_full_sync": v.FromFullSync,
		}
	case *events.PushName:
		jid := v.JID.String()
		return "push_name", "appstate", jid, jid, false, map[string]any{
			"jid": jid,
			"old": v.OldPushName,
			"new": v.NewPushName,
		}
	case *events.BusinessName:
		jid := v.JID.String()
		return "business_name", "appstate", jid, jid, false, map[string]any{
			"jid": jid,
			"old": v.OldBusinessName,
			"new": v.NewBusinessName,
		}
	case *events.Pin:
		jid := v.JID.String()
		return "pin", "appstate", jid, "", false, map[string]any{
			"chat":      jid,
			"timestamp": v.Timestamp.UTC().Format(time.RFC3339Nano),
		}
	case *events.Star:
		chat := v.ChatJID.String()
		return "star", "appstate", chat, v.SenderJID.String(), v.IsFromMe, map[string]any{
			"chat":       chat,
			"sender":     v.SenderJID.String(),
			"is_from_me": v.IsFromMe,
			"message_id": v.MessageID,
			"timestamp":  v.Timestamp.UTC().Format(time.RFC3339Nano),
		}
	case *events.DeleteForMe:
		chat := v.ChatJID.String()
		return "delete_for_me", "appstate", chat, v.SenderJID.String(), v.IsFromMe, map[string]any{
			"chat":       chat,
			"sender":     v.SenderJID.String(),
			"is_from_me": v.IsFromMe,
			"message_id": v.MessageID,
			"timestamp":  v.Timestamp.UTC().Format(time.RFC3339Nano),
		}
	case *events.Mute:
		return "mute", "appstate", v.JID.String(), "", false, map[string]any{
			"chat":      v.JID.String(),
			"timestamp": v.Timestamp.UTC().Format(time.RFC3339Nano),
		}
	case *events.Archive:
		return "archive", "appstate", v.JID.String(), "", false, map[string]any{
			"chat":      v.JID.String(),
			"timestamp": v.Timestamp.UTC().Format(time.RFC3339Nano),
		}
	case *events.MarkChatAsRead:
		return "mark_chat_as_read", "appstate", v.JID.String(), "", false, map[string]any{
			"chat":      v.JID.String(),
			"timestamp": v.Timestamp.UTC().Format(time.RFC3339Nano),
		}
	case *events.ClearChat:
		return "clear_chat", "appstate", v.JID.String(), "", false, map[string]any{
			"chat":      v.JID.String(),
			"timestamp": v.Timestamp.UTC().Format(time.RFC3339Nano),
		}
	case *events.DeleteChat:
		return "delete_chat", "appstate", v.JID.String(), "", false, map[string]any{
			"chat":      v.JID.String(),
			"timestamp": v.Timestamp.UTC().Format(time.RFC3339Nano),
		}
	case *events.PushNameSetting:
		return "push_name_setting", "appstate", "", "", false, map[string]any{
			"timestamp": v.Timestamp.UTC().Format(time.RFC3339Nano),
		}
	case *events.UnarchiveChatsSetting:
		return "unarchive_chats_setting", "appstate", "", "", false, map[string]any{}
	case *events.UserStatusMute:
		return "user_status_mute", "appstate", v.JID.String(), "", false, map[string]any{
			"jid": v.JID.String(),
		}
	case *events.LabelEdit:
		return "label_edit", "appstate", "", "", false, map[string]any{
			"label_id": v.LabelID,
		}
	case *events.LabelAssociationChat:
		return "label_association_chat", "appstate", v.JID.String(), "", false, map[string]any{
			"chat":     v.JID.String(),
			"label_id": v.LabelID,
		}
	case *events.LabelAssociationMessage:
		return "label_association_message", "appstate", v.JID.String(), "", false, map[string]any{
			"chat":     v.JID.String(),
			"label_id": v.LabelID,
		}
	case *events.AppState:
		return "app_state", "appstate", "", "", false, map[string]any{}
	case *events.AppStateSyncComplete:
		return "app_state_sync_complete", "appstate", "", "", false, map[string]any{
			"name": string(v.Name),
		}
	case *events.AppStateSyncError:
		return "app_state_sync_error", "appstate", "", "", false, map[string]any{
			"name": string(v.Name),
		}

	// --- history ---
	case *events.HistorySync:
		return "history_sync", "history", "", "", false, map[string]any{
			"sync_type":     v.Data.GetSyncType().String(),
			"conversations": len(v.Data.Conversations),
		}

	// --- newsletters ---
	case *events.NewsletterJoin:
		return "newsletter_join", "newsletters", "", "", false, map[string]any{}
	case *events.NewsletterLeave:
		return "newsletter_leave", "newsletters", "", "", false, map[string]any{}
	case *events.NewsletterMuteChange:
		return "newsletter_mute_change", "newsletters", "", "", false, map[string]any{}
	case *events.NewsletterLiveUpdate:
		return "newsletter_live_update", "newsletters", "", "", false, map[string]any{}
	case *events.NewsletterMessageMeta:
		return "newsletter_message_meta", "newsletters", "", "", false, map[string]any{}

	// --- media ---
	case *events.MediaRetry:
		chat := v.ChatID.String()
		sender := v.SenderID.String()
		return "media_retry", "media", chat, sender, v.FromMe, map[string]any{
			"chat":       chat,
			"sender":     sender,
			"message_id": v.MessageID,
			"from_me":    v.FromMe,
			"timestamp":  v.Timestamp.UTC().Format(time.RFC3339Nano),
		}

	// --- security ---
	case *events.IdentityChange:
		jid := v.JID.String()
		return "identity_change", "security", jid, jid, false, map[string]any{
			"jid":       jid,
			"timestamp": v.Timestamp.UTC().Format(time.RFC3339Nano),
			"implicit":  v.Implicit,
		}
	case *events.Blocklist:
		return "blocklist", "security", "", "", false, map[string]any{}
	case *events.BlocklistChange:
		return "blocklist_change", "security", "", "", false, map[string]any{}
	case *events.PrivacySettings:
		return "privacy_settings", "security", "", "", false, map[string]any{}
	}

	return "", "", "", "", false, nil
}

func normalizeMessage(ctx context.Context, a *App, pm wa.ParsedMessage) map[string]any {
	chatJID := pm.Chat.String()
	chatName := a.wa.ResolveChatName(ctx, pm.Chat, pm.PushName)

	out := map[string]any{
		"id":        pm.ID,
		"chat":      chatJID,
		"chat_name": chatName,
		"chat_kind": chatKind(pm.Chat),
		"sender":    pm.SenderJID,
		"push_name": pm.PushName,
		"timestamp": pm.Timestamp.UTC().Format(time.RFC3339Nano),
		"from_me":   pm.FromMe,
		"text":      pm.Text,
	}
	if pm.ReplyToID != "" {
		out["reply_to_id"] = pm.ReplyToID
	}
	if pm.ReactionToID != "" {
		out["reaction_to_id"] = pm.ReactionToID
	}
	if emoji := strings.TrimSpace(pm.ReactionEmoji); emoji != "" {
		out["reaction_emoji"] = emoji
	}
	if pm.Media != nil {
		media := map[string]any{
			"type":        pm.Media.Type,
			"mime_type":   pm.Media.MimeType,
			"file_length": pm.Media.FileLength,
			"direct_path": pm.Media.DirectPath,
		}
		if pm.Media.Caption != "" {
			media["caption"] = pm.Media.Caption
		}
		if pm.Media.Filename != "" {
			media["filename"] = pm.Media.Filename
		}
		if len(pm.Media.MediaKey) > 0 {
			media["media_key_b64"] = base64.StdEncoding.EncodeToString(pm.Media.MediaKey)
		}
		if len(pm.Media.FileSHA256) > 0 {
			media["file_sha256_b64"] = base64.StdEncoding.EncodeToString(pm.Media.FileSHA256)
		}
		if len(pm.Media.FileEncSHA256) > 0 {
			media["file_enc_sha256_b64"] = base64.StdEncoding.EncodeToString(pm.Media.FileEncSHA256)
		}
		out["media"] = media
	}
	return out
}

func callBasic(meta types.BasicCallMeta) map[string]any {
	m := map[string]any{
		"from":      meta.From.String(),
		"call_id":   meta.CallID,
		"timestamp": meta.Timestamp.UTC().Format(time.RFC3339Nano),
	}
	if !meta.CallCreator.IsEmpty() {
		m["creator"] = meta.CallCreator.String()
	}
	if !meta.GroupJID.IsEmpty() {
		m["group"] = meta.GroupJID.String()
	}
	return m
}

// isSKDMOnly returns true when a group message carries only a SenderKeyDistributionMessage
// and no user-visible content. whatsmeow dispatches one events.Message per `enc` child in
// the stanza, so for groups the first dispatch is typically the SKDM carrier and the second
// is the real skmsg with text/media. We drop the carrier.
func isSKDMOnly(v *events.Message) bool {
	raw := v.RawMessage
	if raw == nil {
		raw = v.Message
	}
	if raw == nil {
		return false
	}
	if raw.GetSenderKeyDistributionMessage() == nil {
		return false
	}
	// If any user-visible payload is present, keep it.
	if raw.GetConversation() != "" ||
		raw.GetExtendedTextMessage() != nil ||
		raw.GetImageMessage() != nil ||
		raw.GetVideoMessage() != nil ||
		raw.GetAudioMessage() != nil ||
		raw.GetDocumentMessage() != nil ||
		raw.GetStickerMessage() != nil ||
		raw.GetLocationMessage() != nil ||
		raw.GetContactMessage() != nil ||
		raw.GetContactsArrayMessage() != nil ||
		raw.GetReactionMessage() != nil ||
		raw.GetEncReactionMessage() != nil ||
		raw.GetPollCreationMessage() != nil ||
		raw.GetPollUpdateMessage() != nil ||
		raw.GetProtocolMessage() != nil {
		return false
	}
	return true
}

func jidsToStrings(jids []types.JID) []string {
	out := make([]string, len(jids))
	for i, j := range jids {
		out[i] = j.String()
	}
	return out
}
