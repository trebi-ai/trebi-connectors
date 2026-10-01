package app

import (
	"encoding/json"
	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/wa/fakewa"
	"strings"
	"testing"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/trebi-ai/trebi-connectors/sdk"
	"github.com/trebi-ai/trebi-connectors/sdk/sdktest"
)

var (
	peer  = types.JID{User: "5511999999999", Server: types.DefaultUserServer}
	group = types.JID{User: "120363000000000001", Server: types.GroupServer}
)

func startAdapter(t *testing.T, f *fakewa.Client) (*sdktest.Conn, sdk.InitializeResult) {
	t.Helper()
	a := newTestApp(t)
	a.wa = f
	c := sdktest.Start(NewAdapter(a), sdk.WithStateDir(t.TempDir()))
	t.Cleanup(func() { c.Close() })
	res, err := c.Initialize(sdk.InitializeParams{Instance: sdk.InstanceInfo{Key: "k", Name: "wa"}})
	if err != nil {
		t.Fatal(err)
	}
	return c, res
}

func waitStatus(t *testing.T, c *sdktest.Conn, want string) sdk.Status {
	t.Helper()
	for {
		m, err := c.WaitNote(sdk.MethodStatus)
		if err != nil {
			t.Fatalf("wait status %s: %v", want, err)
		}
		var st sdk.Status
		if err := json.Unmarshal(m.Params, &st); err != nil {
			t.Fatal(err)
		}
		if st.State == want {
			return st
		}
	}
}

func waitEvent(t *testing.T, c *sdktest.Conn) sdk.Event {
	t.Helper()
	m, err := c.WaitNote(sdk.MethodEvent)
	if err != nil {
		t.Fatal(err)
	}
	var ev sdk.Event
	if err := json.Unmarshal(m.Params, &ev); err != nil {
		t.Fatal(err)
	}
	return ev
}

func live(id string, chat, sender types.JID, ts time.Time, msg *waProto.Message) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: sender, IsGroup: chat.Server == types.GroupServer},
			ID:            id, Timestamp: ts, PushName: "Sam",
		},
		Message: msg,
	}
}

func TestAdapterEvents(t *testing.T) {
	f := fakewa.New()
	c, res := startAdapter(t, f)
	if res.Account == nil || res.Account.ID != "5511000000000@s.whatsapp.net" || len(res.Features) != 10 || res.Login[0] != "qr" {
		t.Fatalf("initialize: %+v", res)
	}
	waitStatus(t, c, sdk.StateConnected)

	ts := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)
	f.Emit(live("M1", peer, peer, ts, &waProto.Message{Conversation: proto.String("Any news?")}))
	f.Emit(live("M2", peer, peer, ts.Add(time.Second), &waProto.Message{ReactionMessage: &waProto.ReactionMessage{
		Key: &waProto.MessageKey{ID: proto.String("M1")}, Text: proto.String("👍"),
	}}))
	f.Emit(live("M3", group, peer, ts.Add(2*time.Second), &waProto.Message{ImageMessage: &waProto.ImageMessage{
		Caption: proto.String("look"), Mimetype: proto.String("image/png"), DirectPath: proto.String("/d"),
		MediaKey: []byte("k"), FileLength: proto.Uint64(4),
	}}))
	f.Emit(live("M4", types.JID{User: "status", Server: types.BroadcastServer}, peer, ts, &waProto.Message{Conversation: proto.String("story")}))
	f.Emit(live("M5", peer, peer, ts.Add(3*time.Second), &waProto.Message{ExtendedTextMessage: &waProto.ExtendedTextMessage{
		Text: proto.String("Still waiting"), ContextInfo: &waProto.ContextInfo{StanzaID: proto.String("M1")},
	}}))
	f.Emit(live("M6", peer, peer, ts.Add(4*time.Second), &waProto.Message{ButtonsResponseMessage: &waProto.ButtonsResponseMessage{
		SelectedButtonID: proto.String("b1"), Response: &waProto.ButtonsResponseMessage_SelectedDisplayText{SelectedDisplayText: "Yes"},
	}}))

	msg := waitEvent(t, c)
	if msg.ID != "M1" || msg.Type != "message" || msg.Text != "Any news?" || msg.Room.ID != peer.String() || msg.Room.Kind != "dm" ||
		msg.Sender.ID != peer.String() || msg.Sender.Self || msg.TS != "2026-09-28T08:00:00.000000Z" {
		t.Fatalf("message: %+v", msg)
	}
	react := waitEvent(t, c)
	if react.Type != "reaction" || !strings.Contains(string(react.Data), `"message_id":"M1"`) {
		t.Fatalf("reaction: %+v %s", react, react.Data)
	}
	img := waitEvent(t, c)
	if img.Room.Kind != "group" || img.Text != "look" || len(img.Attachments) != 1 || img.Attachments[0].Path == "" || img.Attachments[0].Mime != "image/png" {
		t.Fatalf("image: %+v", img)
	}
	if reply := waitEvent(t, c); reply.ID != "M5" || reply.ReplyTo != "M1" || reply.Text != "Still waiting" {
		t.Fatalf("reply: %+v", reply)
	}
	if button := waitEvent(t, c); button.ID != "M6" || button.Text != "Yes" {
		t.Fatalf("button: %+v", button)
	}
	var rep sdk.ReplayResult
	if err := c.Call(sdk.MethodEventsReplay, sdk.ReplayParams{After: "M3", Limit: 10}, &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Events) != 2 || rep.Events[0].ID != "M5" || rep.Events[0].ReplyTo != "M1" || rep.Events[1].Text != "Yes" {
		t.Fatalf("stored reply: %+v", rep.Events)
	}

	f.Emit(&events.LoggedOut{})
	if st := waitStatus(t, c, sdk.StateAuthRequired); st.Reason != sdk.ReasonRevoked {
		t.Fatalf("logged out: %+v", st)
	}
}

func TestAdapterRequests(t *testing.T) {
	f := fakewa.New()
	c, _ := startAdapter(t, f)
	waitStatus(t, c, sdk.StateConnected)
	ts := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)
	for i, id := range []string{"H1", "H2", "H3"} {
		f.Emit(live(id, peer, peer, ts.Add(time.Duration(i)*time.Second), &waProto.Message{Conversation: proto.String("m" + id)}))
		waitEvent(t, c)
	}

	var sent sdk.SendResult
	if err := c.Call(sdk.MethodMessagesSend, sdk.SendParams{Room: peer.String(), Text: "Hi", Format: "text", ReplyTo: "H2", Key: "k1"}, &sent); err != nil {
		t.Fatal(err)
	}
	quoted := f.Sent()[0].GetExtendedTextMessage().GetContextInfo()
	if sent.MessageID != "S1" || quoted.GetStanzaID() != "H2" || quoted.GetParticipant() != peer.String() {
		t.Fatalf("send: %+v %v", sent, quoted)
	}

	if err := c.Call(sdk.MethodTyping, sdk.TypingParams{Room: peer.String()}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(sdk.MethodMessagesSeen, sdk.SeenParams{Room: peer.String(), MessageID: "H3"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(sdk.MethodReactionsAdd, sdk.ReactionParams{Room: peer.String(), MessageID: "H3", Emoji: "👍"}, nil); err != nil {
		t.Fatal(err)
	}
	if typing, reads, reactions := f.Typing(), f.Reads(), f.Reactions(); len(typing) != 1 || reads[0] != peer.String()+"|"+peer.String()+"|H3" || reactions[0] != peer.String()+"|"+peer.String()+"|H3|👍" {
		t.Fatalf("typing %v reads %v reactions %v", typing, reads, reactions)
	}

	var page sdk.EventPage
	if err := c.Call(sdk.MethodMessagesHistory, sdk.HistoryQuery{Room: peer.String(), Before: "S1", Limit: 2}, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 || page.Events[0].ID != "H2" || page.Events[1].ID != "H3" || page.Next != "H2" {
		t.Fatalf("history: %+v", page)
	}

	var rep sdk.ReplayResult
	if err := c.Call(sdk.MethodEventsReplay, sdk.ReplayParams{After: "H1", Limit: 10}, &rep); err != nil {
		t.Fatal(err)
	}
	if !rep.Complete || len(rep.Events) != 3 || rep.Events[0].ID != "H2" || rep.Events[2].ID != "S1" || !rep.Events[2].Sender.Self || rep.Events[2].ReplyTo != "H2" {
		t.Fatalf("replay: %+v", rep)
	}
	if err := c.Call(sdk.MethodEventsReplay, sdk.ReplayParams{After: "gone", Limit: 10}, &rep); err != nil || rep.Complete {
		t.Fatalf("replay gone: %+v %v", rep, err)
	}

	var rooms sdk.RoomPage
	if err := c.Call(sdk.MethodRoomsList, sdk.RoomQuery{Limit: 10}, &rooms); err != nil {
		t.Fatal(err)
	}
	if len(rooms.Rooms) != 1 || rooms.Rooms[0].ID != peer.String() || rooms.Rooms[0].Kind != "dm" {
		t.Fatalf("rooms: %+v", rooms)
	}
	var opened sdk.RoomResult
	if err := c.Call(sdk.MethodRoomsOpen, sdk.RoomOpenParams{User: "+55 11 988887777"}, &opened); err != nil {
		t.Fatal(err)
	}
	if opened.Room.ID != "5511988887777@s.whatsapp.net" {
		t.Fatalf("open: %+v", opened)
	}

	err := c.Call(sdk.MethodMessagesSeen, sdk.SeenParams{Room: peer.String(), MessageID: "nope"}, nil)
	if sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("seen unknown: %v", err)
	}
}

func TestAdapterLogin(t *testing.T) {
	f := fakewa.New()
	f.SetAuthed(false)
	c, res := startAdapter(t, f)
	if res.Account != nil {
		t.Fatalf("account before login: %+v", res.Account)
	}
	waitStatus(t, c, sdk.StateAuthRequired)

	var begin sdk.AuthBeginResult
	if err := c.Call(sdk.MethodAuthBegin, sdk.AuthBeginParams{Kind: "qr"}, &begin); err != nil {
		t.Fatal(err)
	}
	if begin.Step.Kind != "qr" || begin.Step.Data != "qr-code-1" || begin.Step.ExpiresAt == "" {
		t.Fatalf("begin: %+v", begin)
	}
	done, err := c.WaitNote(sdk.MethodAuthDone)
	if err != nil || !strings.Contains(string(done.Params), `"ok":true`) {
		t.Fatalf("done: %s %v", done.Params, err)
	}
	waitStatus(t, c, sdk.StateConnected)

	if err := c.Call(sdk.MethodAuthLogout, sdk.Empty{}, nil); err != nil {
		t.Fatal(err)
	}
	if st := waitStatus(t, c, sdk.StateAuthRequired); st.Reason != sdk.ReasonLoggedOut {
		t.Fatalf("logout: %+v", st)
	}
}
