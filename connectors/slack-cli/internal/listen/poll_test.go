package listen

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flarco/cli-tools/slack-cli/internal/client"
	"github.com/flarco/cli-tools/slack-cli/internal/config"
)

func TestPollChannelBaselineThenNew(t *testing.T) {
	var phase int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if phase == 0 {
			w.Write([]byte(`{"ok":true,"messages":[{"type":"message","ts":"100.1","user":"U1","text":"old"}]}`))
		} else {
			w.Write([]byte(`{"ok":true,"messages":[{"type":"message","ts":"200.2","user":"U2","text":"new"},{"type":"message","ts":"100.1","user":"U1","text":"old"}]}`))
		}
	}))
	defer srv.Close()
	t.Setenv("SLACK_API_URL", srv.URL)

	cl := client.New(config.Workspace{Token: "xoxb-1"})
	src := &PollSource{Workspace: "acme", Client: cl, Channels: []string{"C1"}}
	src.cursors = map[string]string{"C1": ""}

	out := make(chan Event, 8)
	ctx := context.Background()

	// First poll: baseline, emit nothing.
	if err := src.pollChannel(ctx, "C1", true, out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("baseline emitted %d events", len(out))
	}
	if src.cursors["C1"] != "100.1" {
		t.Errorf("baseline cursor = %q", src.cursors["C1"])
	}

	// Second poll: one new message.
	phase = 1
	if err := src.pollChannel(ctx, "C1", false, out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("second poll emitted %d events, want 1", len(out))
	}
	e := <-out
	if e.TS != "200.2" || e.Text != "new" || e.Channel != "C1" {
		t.Errorf("event = %+v", e)
	}
	if src.cursors["C1"] != "200.2" {
		t.Errorf("cursor not advanced: %q", src.cursors["C1"])
	}
}
