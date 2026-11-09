package app

import (
	"context"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"go.mau.fi/whatsmeow/types"

	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/store"
	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/wa"
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

// resolveMentions sets pm.Mentions from pm.MentionedJIDs and writes each
// known name into the text and the caption. It uses local data only.
func (a *App) resolveMentions(ctx context.Context, pm *wa.ParsedMessage) {
	if len(pm.MentionedJIDs) == 0 {
		return
	}
	pm.Mentions = make([]wa.Mention, len(pm.MentionedJIDs))
	for i, raw := range pm.MentionedJIDs {
		pm.Mentions[i] = a.mentionOf(ctx, raw)
	}
	pm.Text = rewriteMentions(pm.Text, pm.MentionedJIDs, pm.Mentions)
	if pm.Media != nil {
		pm.Media.Caption = rewriteMentions(pm.Media.Caption, pm.MentionedJIDs, pm.Mentions)
	}
}

// mentionOf resolves one mentioned JID. A failed lookup gives no name.
func (a *App) mentionOf(ctx context.Context, raw string) wa.Mention {
	m := wa.Mention{ID: raw}
	jid, err := types.ParseJID(raw)
	if err != nil || jid.IsEmpty() {
		return m
	}
	jid = jid.ToNonAD()
	var lid types.JID
	if jid.Server == types.HiddenUserServer {
		lid = jid
	}
	pn, err := a.wa.PNForLID(ctx, jid)
	if err != nil {
		pn = types.JID{}
	}
	if !pn.IsEmpty() {
		m.ID = pn.String()
	}

	acct, acctName := a.wa.Account()
	acctLID := a.wa.AccountLID()
	if (!pn.IsEmpty() && pn == acct) || (!lid.IsEmpty() && lid == acctLID) {
		m.Me = true
		m.Name = strings.TrimSpace(acctName)
		if m.Name == "" {
			m.Name = "me"
		}
		return m
	}

	if !pn.IsEmpty() {
		stored, _ := a.db.GetContact(pn.String())
		if s := strings.TrimSpace(stored.Alias); s != "" {
			m.Name = s
			return m
		}
		if info, err := a.wa.GetContact(ctx, pn); err == nil {
			if s := wa.BestContactName(info); s != "" {
				m.Name = s
				return m
			}
		}
		if s := strings.TrimSpace(stored.Name); s != "" {
			m.Name = s
			return m
		}
	}
	if s, _ := a.db.LastSenderName(pn.String(), lid.String()); s != "" {
		m.Name = s
		return m
	}
	if !pn.IsEmpty() {
		m.Name = "+" + pn.User
	}
	return m
}

// rewriteMentions changes "@<user>" of each raw JID to "@<name>". A token
// that a digit follows is a longer number, so it stays.
func rewriteMentions(text string, raw []string, ms []wa.Mention) string {
	for i, m := range ms {
		if m.Name == "" || i >= len(raw) {
			continue
		}
		user, _, _ := strings.Cut(raw[i], "@")
		user, _, _ = strings.Cut(user, ":")
		if user == "" {
			continue
		}
		token := "@" + user
		var b strings.Builder
		rest := text
		for {
			j := strings.Index(rest, token)
			if j < 0 {
				b.WriteString(rest)
				break
			}
			b.WriteString(rest[:j])
			rest = rest[j+len(token):]
			if r, _ := utf8.DecodeRuneInString(rest); unicode.IsDigit(r) {
				b.WriteString(token)
			} else {
				b.WriteString("@" + m.Name)
			}
		}
		text = b.String()
	}
	return text
}
