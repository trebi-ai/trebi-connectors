package app

import (
	"context"
	"testing"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

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
			Message:          &waProto.Message{Conversation: proto.String("hi")},
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
	ev, ok := d.liveEvent(ctx, live("L1", group, peer, time.Now(), &waProto.Message{Conversation: proto.String("hi")}))
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
