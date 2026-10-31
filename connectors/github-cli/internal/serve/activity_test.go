package serve

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/github-cli/internal/fakegithub"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

func TestActivity(t *testing.T) {
	t.Parallel()
	fake := fakegithub.Start()
	t.Cleanup(fake.Close)
	fake.SetPollInterval(0)
	fake.AddNotification("9000", "subscribed", fakegithub.AdminRepo, 7) // old: the first read sends nothing
	state := t.TempDir()
	c, _ := startWith(t, fake, state, fakegithub.Token, nil, WithPollEvery(10*time.Millisecond))

	ev := nextActivity(t, c)
	if ev.Type != "review_requested" || !strings.HasPrefix(ev.ID, "notif:9001:") || ev.Room.ID != fakegithub.AdminRepo ||
		ev.Thread == nil || ev.Thread.ID != "8" || ev.Sender == nil || ev.Sender.ID != "hubot" || ev.Sender.Self {
		t.Fatalf("review request %+v", ev)
	}
	if ev.Text != "hubot asked for your review on octo/app#8: Add a cache" {
		t.Fatalf("text %q", ev.Text)
	}
	var data map[string]any
	if err := json.Unmarshal(ev.Data, &data); err != nil || data["reason"] != "review_requested" || data["number"] != float64(8) ||
		data["subject_type"] != "PullRequest" || data["url"] != "https://github.com/octo/app/pull/8" || data["thread_id"] != "9001" {
		t.Fatalf("data %s %v", ev.Data, err)
	}

	fake.AddNotification("9002", "mention", fakegithub.AdminRepo, 8)
	ev = nextActivity(t, c)
	if ev.Type != "mention" || ev.Sender.ID != "monalisa" || !strings.Contains(ev.Text, "monalisa mentioned you") ||
		!strings.Contains(string(ev.Data), "issuecomment-41") {
		t.Fatalf("mention %+v %s", ev, ev.Data)
	}
	fake.AddNotification("9003", "team_mention", fakegithub.AdminRepo, 8)
	if ev := nextActivity(t, c); ev.Type != "mention" || !strings.Contains(ev.Text, "mentioned your team") {
		t.Fatalf("team mention %+v", ev)
	}
	fake.AddNotification("9004", "assign", fakegithub.AdminRepo, 7)
	if ev := nextActivity(t, c); ev.Type != "assigned" {
		t.Fatalf("assign %+v", ev)
	}
	fake.AddNotification("9005", "comment", fakegithub.AdminRepo, 7)
	if ev := nextActivity(t, c); ev.Type != "activity" || !strings.Contains(ev.Text, "(comment)") {
		t.Fatalf("other reason %+v", ev)
	}

	data2, err := os.ReadFile(filepath.Join(state, activityFile))
	if err != nil || !strings.Contains(string(data2), `"9001"`) || !strings.Contains(string(data2), `"last_modified"`) {
		t.Fatalf("cursor %s %v", data2, err)
	}
	for _, call := range fake.Calls() {
		if strings.Contains(call, "/notifications") && !strings.HasPrefix(call, "GET ") {
			t.Fatalf("the reader must never mark a notification as read: %s", call)
		}
	}
}

func TestActivityFineGrained(t *testing.T) {
	t.Parallel()
	c, fake := startFast(t, fakegithub.FineGrained)
	for {
		m, err := c.WaitNote(sdk.MethodStatus)
		if err != nil {
			t.Fatal(err)
		}
		var st sdk.Status
		if err := json.Unmarshal(m.Params, &st); err != nil {
			t.Fatal(err)
		}
		if st.Message == MsgNoActivity {
			if st.State != sdk.StateConnected || st.Account == nil || st.Account.ID != "octo" {
				t.Fatalf("status %+v", st)
			}
			break
		}
	}
	time.Sleep(100 * time.Millisecond)
	if n := count(fake.Calls(), "GET /notifications"); n != 1 {
		t.Fatalf("a 403 must stop the reads until the next login, got %d reads", n)
	}
}
