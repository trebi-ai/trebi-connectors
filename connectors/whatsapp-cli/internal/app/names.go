package app

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/store"
)

// knownChatName returns the name of chat from the name cache, the contacts,
// and the store. It does not ask WhatsApp. It returns "" when the name is not
// known.
func (a *App) knownChatName(ctx context.Context, chat types.JID, pushName string) string {
	if name := a.wa.KnownChatName(ctx, chat, pushName); name != "" {
		return name
	}
	if c, err := a.db.GetChat(chat.String()); err == nil {
		return c.Name
	}
	return ""
}

// storeGroup writes a group to the store and its subject to the name cache.
// Empty fields keep the stored values.
func (a *App) storeGroup(g types.GroupInfo) {
	name := strings.TrimSpace(g.GroupName.Name)
	if err := a.db.UpsertGroup(g.JID.String(), name, g.OwnerJID.String(), g.GroupCreated); err != nil {
		slog.Warn("store group", "jid", g.JID, "err", err)
	}
	if name == "" {
		return
	}
	a.wa.RememberGroup(g.JID, name)
	if err := a.db.UpsertChat(g.JID.String(), "group", name, time.Time{}); err != nil {
		slog.Warn("store chat", "jid", g.JID, "err", err)
	}
}

// storeConversationName writes the chat name of a history sync conversation.
func (a *App) storeConversationName(chatID, name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	jid, err := types.ParseJID(chatID)
	if err != nil || jid.IsEmpty() {
		return
	}
	if jid.Server == types.GroupServer {
		a.storeGroup(types.GroupInfo{JID: jid, GroupName: types.GroupName{Name: name}})
		return
	}
	if err := a.db.UpsertChat(jid.String(), chatKind(jid), name, time.Time{}); err != nil {
		slog.Warn("store chat", "jid", jid, "err", err)
	}
}

// storeGroupInfo asks WhatsApp for the info of a group. It stores the
// subject and the participants. It does nothing on failure.
func (a *App) storeGroupInfo(ctx context.Context, group types.JID) {
	gi, err := a.wa.GetGroupInfo(ctx, group)
	if err != nil || gi == nil {
		return
	}
	a.storeGroup(*gi)
	var ps []store.GroupParticipant
	for _, p := range gi.Participants {
		role := "member"
		if p.IsSuperAdmin {
			role = "superadmin"
		} else if p.IsAdmin {
			role = "admin"
		}
		ps = append(ps, store.GroupParticipant{GroupJID: group.String(), UserJID: p.JID.String(), Role: role})
	}
	_ = a.db.ReplaceGroupParticipants(group.String(), ps)
}
