package listen

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/flarco/cli-tools/slack-cli/internal/client"
	"github.com/flarco/cli-tools/slack-cli/internal/config"
)

// testClient builds a client with the given token (no network).
func testClient(t *testing.T, token string) *client.Client {
	t.Helper()
	return client.New(config.Workspace{Token: token})
}

func TestParseEvent(t *testing.T) {
	raw := json.RawMessage(`{"type":"message","channel":"C1","user":"U1","ts":"123.1","text":"hi"}`)
	e, ok := parseEvent("acme", "T1", raw)
	if !ok {
		t.Fatal("expected an event")
	}
	if e.Type != "message" || e.Channel != "C1" || e.User != "U1" || e.TS != "123.1" || e.Text != "hi" {
		t.Errorf("event = %+v", e)
	}
	if e.Workspace != "acme" || e.TeamID != "T1" {
		t.Errorf("missing workspace labels: %+v", e)
	}
}

func TestParseEventChannelObject(t *testing.T) {
	// Some events carry channel as an object.
	raw := json.RawMessage(`{"type":"channel_created","channel":{"id":"C9","name":"new"}}`)
	e, ok := parseEvent("acme", "", raw)
	if !ok || e.Channel != "C9" {
		t.Errorf("channel object not parsed: %+v (%v)", e, ok)
	}
}

func TestParseEventFallsBackToEventTS(t *testing.T) {
	raw := json.RawMessage(`{"type":"reaction_added","event_ts":"999.9"}`)
	e, _ := parseEvent("a", "", raw)
	if e.TS != "999.9" {
		t.Errorf("ts = %q", e.TS)
	}
}

func TestParseEventRejectsNonEvent(t *testing.T) {
	if _, ok := parseEvent("a", "", json.RawMessage(`{"reply_to":1}`)); ok {
		t.Error("frame without type should be rejected")
	}
}

func TestFilterTypesAndChannels(t *testing.T) {
	f := Filter{
		Types:    map[string]bool{"message": true},
		Channels: map[string]bool{"C1": true},
	}
	if !f.Match(Event{Type: "message", Channel: "C1"}) {
		t.Error("should match")
	}
	if f.Match(Event{Type: "reaction_added", Channel: "C1"}) {
		t.Error("wrong type should not match")
	}
	if f.Match(Event{Type: "message", Channel: "C2"}) {
		t.Error("wrong channel should not match")
	}
}

func TestFilterMentions(t *testing.T) {
	f := Filter{Mentions: true, SelfID: "U9"}
	if !f.Match(Event{Type: "message", Text: "hey <@U9> look"}) {
		t.Error("mention should match")
	}
	if f.Match(Event{Type: "message", Text: "nobody here"}) {
		t.Error("no mention should not match")
	}
}

func TestFilterDMsAndNoSelf(t *testing.T) {
	dm := Filter{DMs: true}
	if !dm.Match(Event{Channel: "D1"}) || dm.Match(Event{Channel: "C1"}) {
		t.Error("DM filter wrong")
	}
	ns := Filter{NoSelf: true, SelfID: "U1"}
	if ns.Match(Event{User: "U1"}) {
		t.Error("self event should be dropped")
	}
	if !ns.Match(Event{User: "U2"}) {
		t.Error("other event should pass")
	}
}

func TestFilterThread(t *testing.T) {
	f := Filter{Thread: "100.1"}
	if !f.Match(Event{ThreadTS: "100.1"}) {
		t.Error("thread reply should match")
	}
	if !f.Match(Event{TS: "100.1"}) {
		t.Error("thread root should match")
	}
	if f.Match(Event{ThreadTS: "200.2"}) {
		t.Error("other thread should not match")
	}
}

func TestWSURLEagerTogglesLazyChannels(t *testing.T) {
	cl := testClient(t, "xoxc-1")
	base := &WSSource{Workspace: "acme", TeamID: "T1", Client: cl}
	if !strings.Contains(base.url(), "lazy_channels=1") {
		t.Error("default should keep lazy_channels")
	}
	eager := &WSSource{Workspace: "acme", TeamID: "T1", Client: cl, Eager: true}
	if strings.Contains(eager.url(), "lazy_channels") {
		t.Error("--eager should drop lazy_channels")
	}
	if !strings.Contains(eager.url(), "token=xoxc-1") || !strings.Contains(eager.url(), "gateway_server=T1-1") {
		t.Errorf("url missing token/team: %s", eager.url())
	}
}

func TestNewer(t *testing.T) {
	if !newer("2.0", "1.0") || newer("1.0", "2.0") || !newer("1.0", "") {
		t.Error("newer comparison wrong")
	}
}
