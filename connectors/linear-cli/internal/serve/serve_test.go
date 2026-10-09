package serve

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/linear-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/linear-cli/internal/fakelinear"
	"github.com/trebi-ai/trebi-connectors/sdk"
	"github.com/trebi-ai/trebi-connectors/sdk/sdktest"
)

const secret = "suggested-secret"

// rig is one adapter session over a fake Linear. hooks gets each delivery
// that the fake posts to the hosted URL.
type rig struct {
	t     *testing.T
	fake  *fakelinear.Server
	dir   string
	url   string
	hooks chan sdk.WebhookRequest
	conn  *sdktest.Conn
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, fake: fakelinear.Start(), dir: t.TempDir(), hooks: make(chan sdk.WebhookRequest, 8)}
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body) //nolint:errcheck // test sink
		h := map[string]string{}
		for k := range req.Header {
			h[strings.ToLower(k)] = req.Header.Get(k)
		}
		r.hooks <- sdk.WebhookRequest{ID: "req", Method: req.Method, Path: req.URL.Path, Headers: h, Body: string(body)}
	}))
	r.url = sink.URL + "/in/linear"
	t.Cleanup(func() {
		if r.conn != nil {
			r.conn.Close() //nolint:errcheck // end of test
		}
		r.fake.Close()
		sink.Close()
	})
	return r
}

// start runs a new session. An empty url means no hosted hook.
func (r *rig) start(url string, opts ...Option) {
	r.t.Helper()
	if r.conn != nil {
		r.conn.Close() //nolint:errcheck // the old session ends
	}
	cl := client.New("test-key")
	cl.URL = r.fake.URL()
	r.conn = sdktest.Start(New(cl, "test", opts...), sdk.WithStateDir(r.dir))
	p := sdk.InitializeParams{}
	if url != "" {
		p.Webhook = &sdk.Webhook{URL: url, Secret: secret}
	}
	res, err := r.conn.Initialize(p)
	if err != nil || res.Account == nil || res.Account.Name != "ana@example.com" {
		r.t.Fatalf("initialize %+v: %v", res, err)
	}
}

func (r *rig) sync(subs ...sdk.Subscription) []sdk.SubscriptionState {
	r.t.Helper()
	var res sdk.SyncResult
	if err := r.conn.Call(sdk.MethodSubscriptionsSync, sdk.SyncParams{Subscriptions: subs}, &res); err != nil {
		r.t.Fatalf("sync: %v", err)
	}
	return res.Subscriptions
}

func (r *rig) counts(creates, updates, deletes int) {
	r.t.Helper()
	if c, u, d := r.fake.Counts(); c != creates || u != updates || d != deletes {
		r.t.Fatalf("creates %d updates %d deletes %d, want %d %d %d", c, u, d, creates, updates, deletes)
	}
}

func sub(id, team string, resources ...string) sdk.Subscription {
	return sdk.Subscription{ID: id, Values: map[string][]string{"team": {team}, "resources": resources}}
}

func TestSync(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		run  func(r *rig)
	}{
		{"webhook creates one and is active", func(r *rig) {
			r.start(r.url)
			st := r.sync(sub("s1", "ENG", "Comment", "Issue"))
			if s := st[0]; s.Mode != sdk.ModeAPI || s.State != sdk.SubscriptionActive || s.Title != "ENG · Issues, Comments" || s.Room == nil || s.Room.ID != "ENG" || s.Room.Name != "Engineering" {
				t.Fatalf("state %+v", s)
			}
			r.counts(1, 0, 0)
			h := r.fake.Webhooks()[0]
			if h.URL != r.url || h.Secret != secret || h.Label != "Trebi" || h.Team == nil || h.Team.Key != "ENG" {
				t.Fatalf("webhook %+v", h)
			}
		}},
		{"second sync is idempotent", func(r *rig) {
			r.start(r.url)
			r.sync(sub("s1", "ENG", "Issue"))
			r.sync(sub("s1", "ENG", "Issue"))
			r.counts(1, 0, 0)
		}},
		{"new resources update the webhook", func(r *rig) {
			r.start(r.url)
			r.sync(sub("s1", "ENG", "Issue"))
			r.sync(sub("s1", "ENG", "Issue", "Comment"))
			r.counts(1, 1, 0)
		}},
		{"removed subscription deletes its webhook", func(r *rig) {
			r.start(r.url)
			r.sync(sub("s1", "ENG", "Issue"), sub("s2", "OPS", "Issue"))
			if st := r.sync(sub("s2", "OPS", "Issue")); len(st) != 1 || st[0].ID != "s2" {
				t.Fatalf("states %+v", st)
			}
			r.counts(2, 0, 1)
			if st := r.sync(); len(st) != 0 || len(r.fake.Webhooks()) != 0 {
				t.Fatalf("states %+v, webhooks %+v", st, r.fake.Webhooks())
			}
		}},
		{"stale url is replaced", func(r *rig) {
			r.start(r.url)
			r.sync(sub("s1", "ENG", "Issue"))
			r.start(r.url + "/new")
			r.sync(sub("s1", "ENG", "Issue"))
			r.counts(2, 0, 1)
			if hs := r.fake.Webhooks(); len(hs) != 1 || hs[0].URL != r.url+"/new" {
				t.Fatalf("webhooks %+v", hs)
			}
		}},
		{"every public team", func(r *rig) {
			r.start(r.url)
			st := r.sync(sub("s1", ""))
			if s := st[0]; s.State != sdk.SubscriptionActive || s.Title != "Every public team · Issues" || s.Room != nil {
				t.Fatalf("state %+v", s)
			}
			if h := r.fake.Webhooks()[0]; !h.AllPublicTeams || h.Team != nil {
				t.Fatalf("webhook %+v", h)
			}
		}},
		{"unknown team", func(r *rig) {
			r.start(r.url)
			if s := r.sync(sub("s1", "NOPE"))[0]; s.State != sdk.SubscriptionError || !strings.Contains(s.Message, "NOPE") {
				t.Fatalf("state %+v", s)
			}
			r.counts(0, 0, 0)
		}},
		{"no admin", func(r *rig) {
			r.fake.SetAdmin(false)
			r.start(r.url)
			if s := r.sync(sub("s1", "ENG"))[0]; s.State != sdk.SubscriptionError || s.Message != adminMessage {
				t.Fatalf("state %+v", s)
			}
		}},
		{"no webhook polls", func(r *rig) {
			r.start("")
			if s := r.sync(sub("s1", "ENG"))[0]; s.Mode != sdk.ModePoll || s.State != sdk.SubscriptionPolling || s.Title != "ENG · Issues" {
				t.Fatalf("state %+v", s)
			}
			r.counts(0, 0, 0)
		}},
		{"losing the webhook deletes it and polls", func(r *rig) {
			r.start(r.url)
			r.sync(sub("s1", "ENG"))
			r.start("")
			if s := r.sync(sub("s1", "ENG"))[0]; s.State != sdk.SubscriptionPolling {
				t.Fatalf("state %+v", s)
			}
			r.counts(1, 0, 1)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			c.run(newRig(t))
		})
	}
}

func sign(body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestReceiveWebhook(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.start(r.url)
	r.sync(sub("s1", "ENG", "Issue"))

	// The fake posts one signed delivery for the new webhook.
	var first sdk.WebhookRequest
	select {
	case first = <-r.hooks:
	case <-time.After(5 * time.Second):
		t.Fatal("no delivery")
	}
	if err := r.conn.Call(sdk.MethodWebhookReceive, first, nil); err != nil {
		t.Fatalf("receive: %v", err)
	}
	note, err := r.conn.WaitNote(sdk.MethodEvent)
	if err != nil {
		t.Fatal(err)
	}
	var ev sdk.Event
	if err := json.Unmarshal(note.Params, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Type != "issue" || ev.ID != first.Headers["linear-delivery"] || ev.Room == nil || ev.Room.ID != "ENG" || ev.Room.Name != "Engineering" || ev.Sender == nil || ev.Sender.Name != "Sam" {
		t.Fatalf("event %+v", ev)
	}

	comment := `{"action":"create","type":"Comment","createdAt":"2026-10-08T10:00:00.000Z","actor":{"id":"b1","name":"Bot","type":"integration"},"data":{"id":"c9","body":"Ship it","issue":{"id":"i1","identifier":"ENG-1","title":"Fix","team":{"key":"ENG"}}}}`
	project := `{"action":"create","type":"Project","data":{"id":"p1"}}`
	cases := []struct {
		name, body, sig, code string
		handshake             bool
		event                 string
	}{
		{name: "good signature", body: comment, sig: sign(comment), event: "comment"},
		{name: "bad signature", body: comment, sig: sign(comment + " "), code: sdk.CodeInvalid},
		{name: "no signature", body: comment, code: sdk.CodeInvalid},
		{name: "other type", body: project, sig: sign(project)},
		{name: "handshake", body: "{}", handshake: true},
	}
	for _, c := range cases {
		req := sdk.WebhookRequest{ID: "r-" + c.name, Method: "POST", Path: "/in/linear", Body: c.body, Handshake: c.handshake,
			Headers: map[string]string{"linear-signature": c.sig, "linear-delivery": "d-" + c.name, "linear-event": "Comment"}}
		err := r.conn.Call(sdk.MethodWebhookReceive, req, nil)
		if c.code != "" {
			if sdk.CodeOf(err) != c.code {
				t.Fatalf("%s: err %v, want %s", c.name, err, c.code)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if c.event == "" {
			continue
		}
		note, err := r.conn.WaitNote(sdk.MethodEvent)
		if err != nil {
			t.Fatal(err)
		}
		var ev sdk.Event
		if err := json.Unmarshal(note.Params, &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Type != c.event || ev.ID != "d-"+c.name || ev.Text != "Ship it" || ev.Room == nil || ev.Room.ID != "ENG" || ev.Sender == nil || !ev.Sender.Bot {
			t.Fatalf("%s: event %+v", c.name, ev)
		}
	}
	// Only the good comment gave an event after the first one.
	if err := r.conn.Call(sdk.MethodPing, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestPoll(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.start("", WithInterval(20*time.Millisecond))
	r.sync(sub("s1", "ENG", "Issue"))
	time.Sleep(50 * time.Millisecond) // a poll sets the cursor
	r.fake.AddIssue("OPS", "Not watched")
	want := r.fake.AddIssue("ENG", "Watched")
	note, err := r.conn.WaitNote(sdk.MethodEvent)
	if err != nil {
		t.Fatal(err)
	}
	var ev sdk.Event
	if err := json.Unmarshal(note.Params, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Type != "issue" || ev.ID != "poll:issue:"+want.ID+":"+want.UpdatedAt || ev.Room == nil || ev.Room.ID != "ENG" || ev.Text != want.Identifier+" Watched" {
		t.Fatalf("event %+v", ev)
	}
}

func TestOptions(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.start("")
	for q, want := range map[string]string{"": "ENG,OPS", "eng": "ENG", "OPER": "OPS", "zzz": ""} {
		var page sdk.OptionPage
		if err := r.conn.Call(sdk.MethodSubscriptionsOptions, sdk.OptionQuery{Field: "team", Query: q, Limit: 10}, &page); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, o := range page.Options {
			got = append(got, o.Value)
		}
		if strings.Join(got, ",") != want {
			t.Fatalf("q %q: %v, want %s", q, got, want)
		}
	}
}

func TestMissingKey(t *testing.T) {
	t.Parallel()
	c := sdktest.Start(New(client.New(""), "test"), sdk.WithStateDir(t.TempDir()))
	defer c.Close() //nolint:errcheck // end of test
	if _, err := c.Initialize(sdk.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	note, err := c.WaitNote(sdk.MethodStatus)
	if err != nil {
		t.Fatal(err)
	}
	var st sdk.Status
	if err := json.Unmarshal(note.Params, &st); err != nil {
		t.Fatal(err)
	}
	if st.State != sdk.StateAuthRequired || st.Reason != sdk.ReasonMissingInput || !strings.Contains(st.Message, "LINEAR_API_KEY") || !strings.Contains(st.Message, "API key") {
		t.Fatalf("status %+v", st)
	}
}
