package app

import (
	"context"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/store"
	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/wa"
	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/wa/fakewa"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

type nopEmitter struct{}

func (nopEmitter) Event(sdk.Event) error       { return nil }
func (nopEmitter) Status(sdk.Status) error     { return nil }
func (nopEmitter) SubscriptionsChanged() error { return nil }

func newNamesAdapter(t *testing.T) (*Adapter, *fakewa.Client) {
	t.Helper()
	a := newTestApp(t)
	f := fakewa.New()
	a.wa = f
	return NewAdapter(a), f
}

func historyConv(chat types.JID, name, id string) *waHistorySync.Conversation {
	return &waHistorySync.Conversation{
		ID:   proto.String(chat.String()),
		Name: proto.String(name),
		Messages: []*waHistorySync.HistorySyncMsg{{Message: &waWeb.WebMessageInfo{
			Key:              &waCommon.MessageKey{RemoteJID: proto.String(chat.String()), FromMe: proto.Bool(false), ID: proto.String(id), Participant: proto.String(peer.String())},
			MessageTimestamp: proto.Uint64(uint64(time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC).Unix())),
			Message:          &waE2E.Message{Conversation: proto.String("hi")},
		}}},
	}
}

func TestHistoryStoresConversationName(t *testing.T) {
	d, f := newNamesAdapter(t)
	ctx := context.Background()
	d.storeHistory(ctx, &events.HistorySync{Data: &waHistorySync.HistorySync{
		Conversations: []*waHistorySync.Conversation{historyConv(group, "Family", "H1")},
	}})

	if n := f.GroupInfoCalls(); n != 0 {
		t.Fatalf("history called WhatsApp %d times", n)
	}
	c, err := d.app.db.GetChat(group.String())
	if err != nil || c.Name != "Family" {
		t.Fatalf("chat: %+v %v", c, err)
	}
	page, err := d.History(ctx, sdk.HistoryQuery{Room: group.String(), Limit: 10})
	if err != nil || len(page.Events) != 1 || page.Events[0].Room.Name != "Family" {
		t.Fatalf("history page: %+v %v", page, err)
	}
	if name := d.app.wa.KnownChatName(ctx, group, ""); name != "Family" {
		t.Fatalf("cache: %q", name)
	}
}

func TestLiveGroupEventNeverTakesSenderName(t *testing.T) {
	d, _ := newNamesAdapter(t)
	ctx := context.Background()
	ev, ok := d.liveEvent(ctx, live("L1", group, peer, time.Now(), &waE2E.Message{Conversation: proto.String("hi")}))
	if !ok || ev.Room.Name != "" || ev.Room.ID != group.String() {
		t.Fatalf("unknown group: %+v", ev.Room)
	}
	if c, err := d.app.db.GetChat(group.String()); err != nil || c.Name != "" {
		t.Fatalf("chat: %+v %v", c, err)
	}
}

func TestConnectRefreshesGroups(t *testing.T) {
	d, f := newNamesAdapter(t)
	ctx := context.Background()
	f.AddGroup(&types.GroupInfo{JID: group, GroupName: types.GroupName{Name: "Family"}})

	connect := func() {
		t.Helper()
		if _, err := d.handle(ctx, nopEmitter{}, &events.Connected{}); err != nil {
			t.Fatal(err)
		}
		d.bg.Wait()
	}
	name := func(jid types.JID) string {
		c, _ := d.app.db.GetChat(jid.String())
		return c.Name
	}

	connect()
	if got := name(group); got != "Family" {
		t.Fatalf("first connect: %q", got)
	}
	if got := d.app.wa.KnownChatName(ctx, group, ""); got != "Family" {
		t.Fatalf("cache: %q", got)
	}

	other := types.JID{User: "120363000000000002", Server: types.GroupServer}
	f.AddGroup(&types.GroupInfo{JID: other, GroupName: types.GroupName{Name: "Work"}})
	connect()
	if got := name(other); got != "" {
		t.Fatalf("a reconnect within %s loaded the groups again: %q", groupRefreshEvery, got)
	}
	d.groupsAt = d.groupsAt.Add(-groupRefreshEvery)
	connect()
	if got := name(other); got != "Work" {
		t.Fatalf("a reconnect after %s: %q", groupRefreshEvery, got)
	}
}

func TestGroupInfoEventRenames(t *testing.T) {
	d, _ := newNamesAdapter(t)
	ctx := context.Background()
	if err := d.app.db.UpsertChat(group.String(), "group", "Old", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.handle(ctx, nopEmitter{}, &events.GroupInfo{JID: group, Name: &types.GroupName{Name: "New"}}); err != nil {
		t.Fatal(err)
	}
	if c, err := d.app.db.GetChat(group.String()); err != nil || c.Name != "New" {
		t.Fatalf("chat: %+v %v", c, err)
	}
	if got := d.app.wa.KnownChatName(ctx, group, ""); got != "New" {
		t.Fatalf("cache: %q", got)
	}
}

func TestRewriteMentions(t *testing.T) {
	for _, tc := range []struct {
		name, text, want string
		ms               []wa.Mention
	}{
		{"longer number stays", "@123 and @1234", "@Ana and @1234", []wa.Mention{{Name: "Ana"}}},
		{"empty name stays", "@123 hi", "@123 hi", []wa.Mention{{}}},
		{"end and punctuation", "hi @123, see @123", "hi @Ana, see @Ana", []wa.Mention{{Name: "Ana"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := rewriteMentions(tc.text, []string{"123@lid"}, tc.ms); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveMentions(t *testing.T) {
	a := newTestApp(t)
	f := fakewa.New()
	a.wa = f
	ts := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	jid := func(user, server string) types.JID { return types.JID{User: user, Server: server} }

	anaLID, anaPN := jid("200000000000001", types.HiddenUserServer), jid("5511900000001", types.DefaultUserServer)
	f.AddLID(anaLID, anaPN)
	f.AddContact(anaPN, types.ContactInfo{Found: true, FullName: "Ana Maria"})

	boss := jid("5511900000002", types.DefaultUserServer)
	f.AddContact(boss, types.ContactInfo{Found: true, FullName: "Carlos Silva"})
	if err := a.db.UpsertContact(boss.String(), boss.User, "", "Carlos Silva", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := a.db.SetAlias(boss.String(), "Boss"); err != nil {
		t.Fatal(err)
	}

	pierreLID, pierrePN := jid("19503530426554", types.HiddenUserServer), jid("5511900000003", types.DefaultUserServer)
	f.AddLID(pierreLID, pierrePN)
	if err := a.db.UpsertChat(group.String(), "group", "Family", ts); err != nil {
		t.Fatal(err)
	}
	if err := a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: group.String(), MsgID: "P1", SenderJID: pierrePN.String(), SenderName: "Pierre-Yves Brunet", Timestamp: ts, Text: "hello"}); err != nil {
		t.Fatal(err)
	}

	nobody := jid("5511900000004", types.DefaultUserServer)
	unknownLID := jid("300000000000009", types.HiddenUserServer)

	for _, tc := range []struct {
		name string
		raw  types.JID
		want wa.Mention
	}{
		{"lid to contact", anaLID, wa.Mention{ID: anaPN.String(), Name: "Ana Maria"}},
		{"own lid", fakewa.MeLID, wa.Mention{ID: fakewa.Me.String(), Name: "Me", Me: true}},
		{"own phone", fakewa.Me, wa.Mention{ID: fakewa.Me.String(), Name: "Me", Me: true}},
		{"alias wins", boss, wa.Mention{ID: boss.String(), Name: "Boss"}},
		{"stored push name", pierreLID, wa.Mention{ID: pierrePN.String(), Name: "Pierre-Yves Brunet"}},
		{"phone only", nobody, wa.Mention{ID: nobody.String(), Name: "+" + nobody.User}},
		{"unknown lid", unknownLID, wa.Mention{ID: unknownLID.String()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := "@" + tc.raw.User + " can you check?"
			pm := wa.ParsedMessage{Text: text, MentionedJIDs: []string{tc.raw.String()}}
			a.resolveMentions(context.Background(), &pm)
			if len(pm.Mentions) != 1 || pm.Mentions[0] != tc.want {
				t.Fatalf("mentions: %+v, want %+v", pm.Mentions, tc.want)
			}
			want := text
			if tc.want.Name != "" {
				want = "@" + tc.want.Name + " can you check?"
			}
			if pm.Text != want {
				t.Fatalf("text: %q, want %q", pm.Text, want)
			}
		})
	}
}
