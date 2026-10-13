package serve

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/trebi-ai/trebi-connectors/sdk"

	"github.com/flarco/cli-tools/slack-cli/internal/client"
)

type names struct {
	users    map[string]client.User
	channels map[string]client.Channel
}

func (n names) User(_ context.Context, id string) (client.User, bool) {
	u, ok := n.users[id]
	return u, ok
}

func (n names) Channel(_ context.Context, id string) (client.Channel, bool) {
	c, ok := n.channels[id]
	return c, ok
}

func testTeam() *Team {
	return &Team{
		Workspace: "acme", ID: "T1", Title: "Acme", SelfID: "U0ME",
		Names: names{
			users: map[string]client.User{
				"U0ME":  {ID: "U0ME", Name: "me"},
				"U0ANA": {ID: "U0ANA", Name: "ana", DisplayName: "Ana"},
				"U0BOT": {ID: "U0BOT", Name: "helper", Bot: true},
			},
			channels: map[string]client.Channel{
				"C0GEN": {ID: "C0GEN", Name: "general"},
				"D0ANA": {ID: "D0ANA", IM: true, User: "U0ANA"},
				"G0GRP": {ID: "G0GRP", Name: "mpdm-me--ana--bo-1", MPIM: true},
			},
		},
	}
}

func mapOne(t *testing.T, raw string) []sdk.Event {
	t.Helper()
	return testTeam().Events(context.Background(), json.RawMessage(raw))
}

func TestMessage(t *testing.T) {
	evs := mapOne(t, `{"type":"message","channel":"C0GEN","user":"U0ANA","text":"see <#C0GEN> &amp; <https://x.io|x>","ts":"1767225600.000100"}`)
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1", len(evs))
	}
	ev := evs[0]
	if ev.ID != "T1/C0GEN/1767225600.000100" || ev.Type != TypeMessage {
		t.Errorf("id %q type %q", ev.ID, ev.Type)
	}
	if ev.Room.ID != "T1/C0GEN" || ev.Room.Name != "#general" || ev.Room.Kind != sdk.RoomChannel || ev.Room.Parent != "Acme" {
		t.Errorf("room %+v", ev.Room)
	}
	if ev.Sender.ID != "U0ANA" || ev.Sender.Name != "Ana" || ev.Sender.Bot || ev.Sender.Self {
		t.Errorf("sender %+v", ev.Sender)
	}
	if ev.Text != "see #general & x (https://x.io)" {
		t.Errorf("text %q", ev.Text)
	}
	if ev.Thread != nil || ev.ReplyTo != "" {
		t.Errorf("thread %+v reply_to %q", ev.Thread, ev.ReplyTo)
	}
	if ev.TS != "2026-01-01T00:00:00.000100Z" {
		t.Errorf("ts %q", ev.TS)
	}
	var d map[string]any
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		t.Fatal(err)
	}
	if d["team_id"] != "T1" || d["team_name"] != "Acme" || d["workspace"] != "acme" || d["event"] == nil {
		t.Errorf("data %v", d)
	}
}

func TestThreadReply(t *testing.T) {
	ev := mapOne(t, `{"type":"message","channel":"C0GEN","user":"U0ANA","text":"ok","ts":"1767225600.000200","thread_ts":"1767225600.000100"}`)[0]
	if ev.Thread == nil || ev.Thread.ID != "1767225600.000100" {
		t.Fatalf("thread %+v", ev.Thread)
	}
	if ev.ReplyTo != "T1/C0GEN/1767225600.000100" {
		t.Errorf("reply_to %q", ev.ReplyTo)
	}
}

func TestDM(t *testing.T) {
	ev := mapOne(t, `{"type":"message","channel":"D0ANA","user":"U0ANA","text":"hi","ts":"1767225600.000300"}`)[0]
	if ev.Room.Kind != sdk.RoomDM || ev.Room.Name != "Ana" || ev.Room.ID != "T1/D0ANA" {
		t.Errorf("room %+v", ev.Room)
	}
}

func TestGroup(t *testing.T) {
	ev := mapOne(t, `{"type":"message","channel":"G0GRP","user":"U0ME","text":"on it","ts":"1767225600.000900"}`)[0]
	if ev.Room.Kind != sdk.RoomGroup || ev.Room.Name != "me, ana, bo" {
		t.Errorf("room %+v", ev.Room)
	}
	if !ev.Sender.Self {
		t.Errorf("sender %+v, want self", ev.Sender)
	}
}

func TestBot(t *testing.T) {
	ev := mapOne(t, `{"type":"message","subtype":"bot_message","channel":"C0GEN","bot_id":"B0CI","username":"CI","text":"","attachments":[{"title":"Build 42","text":"passed"}],"ts":"1767225600.000400"}`)[0]
	if ev.Sender == nil || !ev.Sender.Bot || ev.Sender.ID != "B0CI" || ev.Sender.Name != "CI" {
		t.Errorf("sender %+v", ev.Sender)
	}
	if ev.Text != "Build 42\npassed" {
		t.Errorf("text %q", ev.Text)
	}
	ev = mapOne(t, `{"type":"message","channel":"C0GEN","user":"U0BOT","text":"beep","ts":"1767225600.000410"}`)[0]
	if !ev.Sender.Bot {
		t.Errorf("sender %+v, want bot from the user record", ev.Sender)
	}
	ev = mapOne(t, `{"type":"message","channel":"C0GEN","username":"alerts","text":"disk full","ts":"1767225600.000420"}`)[0]
	if ev.Sender == nil || !ev.Sender.Bot || ev.Sender.Name != "alerts" {
		t.Errorf("webhook sender %+v", ev.Sender)
	}
}

func TestMention(t *testing.T) {
	evs := mapOne(t, `{"type":"message","channel":"C0GEN","user":"U0ANA","text":"<@U0ME> look","ts":"1767225600.000100"}`)
	if len(evs) != 2 || evs[1].Type != TypeMention || evs[1].ID != evs[0].ID+".mention" {
		t.Fatalf("events %+v", evs)
	}
	if evs[0].Text != "@me look" {
		t.Errorf("text %q", evs[0].Text)
	}
	if evs := mapOne(t, `{"type":"message","channel":"C0GEN","user":"U0ME","text":"<@U0ME> note","ts":"1767225600.000101"}`); len(evs) != 1 {
		t.Errorf("own message gives %d events, want 1", len(evs))
	}
}

func TestEdit(t *testing.T) {
	evs := mapOne(t, `{"type":"message","subtype":"message_changed","channel":"C0GEN","message":{"user":"U0ANA","text":"new","ts":"1767225600.000100","edited":{"user":"U0ANA","ts":"1767225600.000700"}},"previous_message":{"user":"U0ANA","text":"old","ts":"1767225600.000100"},"ts":"1767225600.000700"}`)
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1", len(evs))
	}
	ev := evs[0]
	if ev.Type != TypeEdited || ev.ID != "T1/C0GEN/1767225600.000100.edit.1767225600.000700" || ev.Text != "new" {
		t.Errorf("event %+v", ev)
	}
	var d map[string]any
	_ = json.Unmarshal(ev.Data, &d)
	if d["previous_text"] != "old" || d["ts"] != "1767225600.000100" {
		t.Errorf("data %v", d)
	}
	// An unfurl changes the message but not the text.
	unfurl := `{"type":"message","subtype":"message_changed","channel":"C0GEN","message":{"user":"U0ANA","text":"same","ts":"1767225600.000100","attachments":[{"title":"x"}]},"previous_message":{"user":"U0ANA","text":"same","ts":"1767225600.000100"},"ts":"1767225600.000710"}`
	if evs := mapOne(t, unfurl); len(evs) != 0 {
		t.Errorf("unfurl gives %d events, want 0", len(evs))
	}
}

func TestDelete(t *testing.T) {
	evs := mapOne(t, `{"type":"message","subtype":"message_deleted","channel":"C0GEN","deleted_ts":"1767225600.000200","previous_message":{"user":"U0ANA","text":"oops","ts":"1767225600.000200","thread_ts":"1767225600.000100"},"ts":"1767225600.000800"}`)
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1", len(evs))
	}
	ev := evs[0]
	if ev.Type != TypeDeleted || ev.ID != "T1/C0GEN/1767225600.000200.delete" || ev.Sender.ID != "U0ANA" || ev.Thread == nil {
		t.Errorf("event %+v", ev)
	}
}

func TestReactionAndJoin(t *testing.T) {
	ev := mapOne(t, `{"type":"reaction_added","user":"U0ANA","reaction":"+1","item":{"type":"message","channel":"C0GEN","ts":"1767225600.000100"},"event_ts":"1767225600.000500"}`)[0]
	if ev.Type != TypeReact || ev.ID != "T1/C0GEN/1767225600.000100.U0ANA.+1" || ev.Text != ":+1:" {
		t.Errorf("reaction %+v", ev)
	}
	ev = mapOne(t, `{"type":"member_joined_channel","user":"U0ANA","channel":"C0GEN","event_ts":"1767225600.000600"}`)[0]
	if ev.Type != TypeJoin || ev.Sender.ID != "U0ANA" || ev.ID != "T1/C0GEN.join.U0ANA.1767225600.000600" {
		t.Errorf("join %+v", ev)
	}
}

func TestIgnored(t *testing.T) {
	for _, raw := range []string{
		`{"type":"hello"}`,
		`{"type":"user_typing","channel":"C0GEN","user":"U0ANA"}`,
		`{"type":"message","subtype":"channel_topic","channel":"C0GEN","user":"U0ANA","ts":"1"}`,
		`not json`,
	} {
		if evs := mapOne(t, raw); len(evs) != 0 {
			t.Errorf("%s gives %d events", raw, len(evs))
		}
	}
}
