package serve

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/mstodo-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/mstodo-cli/internal/fakegraph"
	"github.com/trebi-ai/trebi-connectors/sdk"
	"github.com/trebi-ai/trebi-connectors/sdk/sdktest"
)

// hookRecv plays the hosted hook: it echoes validationToken and records
// the other requests.
type hookRecv struct {
	srv *httptest.Server

	mu          sync.Mutex
	validations int
	reqs        []sdk.WebhookRequest
}

func newHook(t *testing.T) *hookRecv {
	h := &hookRecv{}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // a test body
		h.mu.Lock()
		defer h.mu.Unlock()
		if tok := r.URL.Query().Get("validationToken"); tok != "" {
			h.validations++
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, tok) //nolint:errcheck // the fake reads it or not
			return
		}
		h.reqs = append(h.reqs, sdk.WebhookRequest{
			ID: "r" + strconv.Itoa(len(h.reqs)), Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
			ContentType: r.Header.Get("Content-Type"), Headers: map[string]string{}, Body: string(body),
			ReceivedAt: time.Now().UTC().Format(time.RFC3339Nano),
		})
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *hookRecv) URL() string { return h.srv.URL + "/in/test" }

func (h *hookRecv) wait(t *testing.T) sdk.WebhookRequest {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		if len(h.reqs) > 0 {
			r := h.reqs[0]
			h.mu.Unlock()
			return r
		}
		h.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no delivery")
	return sdk.WebhookRequest{}
}

type rig struct {
	fake *fakegraph.Fake
	a    *Adapter
	conn *sdktest.Conn
	hook *hookRecv
	dir  string
}

// start runs a logged-in adapter on the fake. hook adds a webhook.
func start(t *testing.T, hook bool, opts ...Option) *rig {
	t.Helper()
	r := &rig{fake: fakegraph.Start(), dir: t.TempDir()}
	t.Cleanup(r.fake.Close)
	cl := client.New(fakegraph.ClientID, client.Session{})
	cl.GraphURL, cl.LoginURL = r.fake.GraphURL(), r.fake.LoginURL()
	a, err := New(cl, "test", r.dir, opts...)
	if err != nil {
		t.Fatal(err)
	}
	r.a = a
	r.conn = sdktest.Start(a, sdk.WithStateDir(r.dir))
	t.Cleanup(func() { r.conn.Close() }) //nolint:errcheck // the test is over
	p := sdk.InitializeParams{Instance: sdk.InstanceInfo{Key: "mstodo", Name: "To Do"}}
	if hook {
		r.hook = newHook(t)
		p.Webhook = &sdk.Webhook{URL: r.hook.URL(), Secret: "unused"}
	}
	if _, err := r.conn.Initialize(p); err != nil {
		t.Fatal(err)
	}
	var res sdk.AuthBeginResult
	if err := r.conn.Call(sdk.MethodAuthBegin, sdk.AuthBeginParams{}, &res); err != nil {
		t.Fatal(err)
	}
	if res.Step.Kind != sdk.StepDeviceCode || res.Step.Code == "" || res.Step.URL == "" {
		t.Fatalf("step %+v", res.Step)
	}
	m, err := r.conn.WaitNote(sdk.MethodAuthDone)
	if err != nil {
		t.Fatal(err)
	}
	var done sdk.AuthDoneParams
	if err := json.Unmarshal(m.Params, &done); err != nil || !done.OK {
		t.Fatalf("auth/done %s", m.Params)
	}
	return r
}

func (r *rig) sync(t *testing.T, subs ...sdk.Subscription) []sdk.SubscriptionState {
	t.Helper()
	if subs == nil {
		subs = []sdk.Subscription{}
	}
	var res sdk.SyncResult
	if err := r.conn.Call(sdk.MethodSubscriptionsSync, sdk.SyncParams{Subscriptions: subs}, &res); err != nil {
		t.Fatal(err)
	}
	return res.Subscriptions
}

func (r *rig) event(t *testing.T) (sdk.Event, taskData) {
	t.Helper()
	m, err := r.conn.WaitNote(sdk.MethodEvent)
	if err != nil {
		t.Fatal(err)
	}
	var ev sdk.Event
	var d taskData
	if err := json.Unmarshal(m.Params, &ev); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		t.Fatal(err)
	}
	return ev, d
}

func (r *rig) entry(id string) entry {
	r.a.mu.Lock()
	defer r.a.mu.Unlock()
	if e := r.a.st.Subs[id]; e != nil {
		return *e
	}
	return entry{}
}

var groceries = sdk.Subscription{ID: "s1", Values: map[string][]string{FieldList: {"list-groceries"}}}

func code(err error) string {
	var e *sdk.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestLogin(t *testing.T) {
	t.Parallel()
	r := start(t, false)
	sess, err := client.LoadSession(filepath.Join(r.dir, "auth.json"))
	if err != nil || sess.AccessToken == "" || sess.RefreshToken == "" || sess.AccountID != "user-sandbox" {
		t.Fatalf("auth.json %+v %v", sess, err)
	}
	var st sdk.AuthStatusResult
	if err := r.conn.Call(sdk.MethodAuthStatus, sdk.Empty{}, &st); err != nil {
		t.Fatal(err)
	}
	if st.State != sdk.StateConnected || st.Account == nil || st.Account.Name != "Sandbox User" {
		t.Fatalf("status %+v", st)
	}
}

func TestLoginNeedsAppID(t *testing.T) {
	t.Parallel()
	a, err := New(client.New("", client.Session{}), "test", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	conn := sdktest.Start(a, sdk.WithStateDir(t.TempDir()))
	defer conn.Close() //nolint:errcheck // the test is over
	if _, err := conn.Initialize(sdk.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	err = conn.Call(sdk.MethodAuthBegin, sdk.AuthBeginParams{}, nil)
	if code(err) != sdk.CodePermanent || !strings.Contains(err.Error(), "no Microsoft app id") {
		t.Fatalf("auth/begin: %v", err)
	}
}

func TestSyncCreatesAndReuses(t *testing.T) {
	t.Parallel()
	r := start(t, true)
	got := r.sync(t, groceries)
	if len(got) != 1 || got[0].Mode != sdk.ModeAPI || got[0].State != sdk.SubscriptionActive || got[0].ExpiresAt == "" {
		t.Fatalf("state %+v", got)
	}
	if got[0].Room == nil || got[0].Room.ID != "list-groceries" || got[0].Room.Name != "Groceries" {
		t.Fatalf("room %+v", got[0].Room)
	}
	subs := r.fake.Subscriptions()
	if len(subs) != 1 {
		t.Fatalf("graph has %d subscriptions", len(subs))
	}
	s := subs[0]
	if s.NotificationURL != r.hook.URL() || s.LifecycleNotificationURL != r.hook.URL()+"/lifecycle" ||
		s.Resource != "/me/todo/lists/list-groceries/tasks" || s.ChangeType != "created,updated,deleted" || s.ClientState == "" {
		t.Fatalf("subscription %+v", s)
	}
	if d := time.Until(s.ExpirationDateTime); d < lifetime-time.Minute || d > lifetime {
		t.Fatalf("expiry in %s", d)
	}
	r.hook.mu.Lock()
	v := r.hook.validations
	r.hook.mu.Unlock()
	if v != 2 {
		t.Fatalf("%d validations, want 2", v)
	}

	again := r.sync(t, groceries)
	if again[0].State != sdk.SubscriptionActive || len(r.fake.Subscriptions()) != 1 || r.fake.Subscriptions()[0].ID != s.ID {
		t.Fatalf("second sync is not idempotent: %+v %+v", again, r.fake.Subscriptions())
	}

	if got := r.sync(t); len(got) != 0 || len(r.fake.Subscriptions()) != 0 || r.entry("s1").ListID != "" {
		t.Fatalf("removal keeps %+v %+v", got, r.fake.Subscriptions())
	}
}

func TestSyncUnknownList(t *testing.T) {
	t.Parallel()
	r := start(t, true)
	got := r.sync(t, sdk.Subscription{ID: "s1", Values: map[string][]string{FieldList: {"nope"}}})
	if got[0].State != sdk.SubscriptionError || got[0].Message == "" {
		t.Fatalf("state %+v", got)
	}
}

func TestRenewal(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	clock := time.Now()
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	r := start(t, true, WithNow(now))
	r.sync(t, groceries)
	first := r.fake.Subscriptions()[0].ExpirationDateTime

	mu.Lock()
	clock = clock.Add(lifetime / 2)
	mu.Unlock()
	if err := r.a.renewDue(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !r.fake.Subscriptions()[0].ExpirationDateTime.Equal(first) {
		t.Fatal("renewed before 75%")
	}

	mu.Lock()
	clock = clock.Add(lifetime / 4)
	mu.Unlock()
	if err := r.a.renewDue(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got, want := r.fake.Subscriptions()[0].ExpirationDateTime, now().Add(lifetime); !got.Equal(want) {
		t.Fatalf("expiry %s, want %s", got, want)
	}
	if e := r.entry("s1"); !e.Since.Equal(now()) {
		t.Fatalf("since %s", e.Since)
	}
}

func TestLifecycleRemovedCreatesAgain(t *testing.T) {
	t.Parallel()
	r := start(t, true)
	r.sync(t, groceries)
	old := r.entry("s1")
	r.fake.Remove(old.GraphID)
	body := `{"value":[{"subscriptionId":"` + old.GraphID + `","lifecycleEvent":"subscriptionRemoved","clientState":"` + old.ClientState + `"}]}`
	err := r.conn.Call(sdk.MethodWebhookReceive, sdk.WebhookRequest{ID: "l1", Method: "POST", Path: "/in/test/lifecycle", Headers: map[string]string{}, Body: body}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.conn.WaitNote(sdk.MethodSubscriptionsChanged); err != nil {
		t.Fatal(err)
	}
	subs := r.fake.Subscriptions()
	if len(subs) != 1 || subs[0].ID == old.GraphID || r.entry("s1").GraphID != subs[0].ID {
		t.Fatalf("subscriptions %+v", subs)
	}
}

func TestReceiveWebhook(t *testing.T) {
	t.Parallel()
	r := start(t, true)
	r.fake.AutoNotify = true
	r.sync(t, groceries)
	req := r.hook.wait(t)

	if err := r.conn.Call(sdk.MethodWebhookReceive, sdk.WebhookRequest{ID: "h", Method: "POST", Query: "validationToken=x", Headers: map[string]string{}, Handshake: true}, nil); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if err := r.conn.Call(sdk.MethodWebhookReceive, req, nil); err != nil {
		t.Fatal(err)
	}
	ev, d := r.event(t)
	sub := r.entry("s1").GraphID
	if ev.Type != TypeTask || ev.Room == nil || ev.Room.ID != "list-groceries" || ev.Text != "A new sandbox task" {
		t.Fatalf("event %+v", ev)
	}
	if !strings.HasPrefix(ev.ID, sub+":me/todo/lists/list-groceries/tasks/"+d.TaskID+":created:") || d.Change != "created" || d.ModifiedAt == "" {
		t.Fatalf("event id %s data %+v", ev.ID, d)
	}

	bad := req
	bad.Body = strings.Replace(req.Body, `"clientState":"`, `"clientState":"x`, 1)
	if err := r.conn.Call(sdk.MethodWebhookReceive, bad, nil); code(err) != sdk.CodeInvalid {
		t.Fatalf("bad clientState: %v", err)
	}
}

func TestReceiveDeletedHasNoFetch(t *testing.T) {
	t.Parallel()
	r := start(t, true)
	r.sync(t, groceries)
	e := r.entry("s1")
	body := `{"value":[{"subscriptionId":"` + e.GraphID + `","subscriptionExpirationDateTime":"2026-10-11T00:00:00Z","changeType":"deleted","resource":"me/todo/lists/list-groceries/tasks/task-gone","resourceData":{"id":"task-gone"},"clientState":"` + e.ClientState + `"}]}`
	if err := r.conn.Call(sdk.MethodWebhookReceive, sdk.WebhookRequest{ID: "d", Method: "POST", Path: "/in/test", Headers: map[string]string{}, Body: body, ReceivedAt: "2026-10-08T10:00:00Z"}, nil); err != nil {
		t.Fatal(err)
	}
	ev, d := r.event(t)
	if ev.ID != e.GraphID+":me/todo/lists/list-groceries/tasks/task-gone:deleted:2026-10-11T00:00:00Z" || d.Change != "deleted" || d.TaskID != "task-gone" {
		t.Fatalf("event %+v %+v", ev, d)
	}
}

func TestPollWithoutWebhook(t *testing.T) {
	t.Parallel()
	r := start(t, false, WithPollEvery(50*time.Millisecond))
	got := r.sync(t, groceries)
	if got[0].Mode != sdk.ModePoll || got[0].State != sdk.SubscriptionPolling || got[0].ExpiresAt != "" {
		t.Fatalf("state %+v", got)
	}
	task := r.fake.AddTask("list-groceries", "Buy eggs")
	ev, d := r.event(t)
	if ev.ID != "poll:"+task.ID+":"+task.LastModifiedDateTime || d.Change != "created" || ev.Text != "Buy eggs" {
		t.Fatalf("event %+v %+v", ev, d)
	}
}

func TestNewerStateIsAnError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, stateFile), []byte(`{"version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := New(client.New("", client.Session{}), "test", dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.AuthStatus(context.Background())
	if err != nil || st.State != sdk.StateError {
		t.Fatalf("status %+v %v", st, err)
	}
}
