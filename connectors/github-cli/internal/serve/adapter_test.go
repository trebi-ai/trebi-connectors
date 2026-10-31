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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/github-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/github-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/github-cli/internal/fakegithub"
	"github.com/trebi-ai/trebi-connectors/sdk"
	"github.com/trebi-ai/trebi-connectors/sdk/sdktest"
)

// sink records the deliveries of the fake as webhook requests.
type sink struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []sdk.WebhookRequest
}

func newSink(t *testing.T) *sink {
	t.Helper()
	s := &sink{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // a test server
		req := sdk.WebhookRequest{Method: r.Method, Path: r.URL.Path, Body: string(body), Headers: map[string]string{}}
		for _, h := range []string{"x-hub-signature-256", "x-github-event", "x-github-delivery"} {
			req.Headers[h] = r.Header.Get(h)
		}
		s.mu.Lock()
		req.ID = "w" + string(rune('0'+len(s.reqs)))
		s.reqs = append(s.reqs, req)
		s.mu.Unlock()
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *sink) wait(t *testing.T, n int) []sdk.WebhookRequest {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		s.mu.Lock()
		reqs := append([]sdk.WebhookRequest(nil), s.reqs...)
		s.mu.Unlock()
		if len(reqs) >= n {
			return reqs
		}
	}
	t.Fatalf("no %d deliveries", n)
	return nil
}

func start(t *testing.T, token string, hook *sdk.Webhook, opts ...Option) (*sdktest.Conn, *fakegithub.Server, sdk.InitializeResult) {
	t.Helper()
	fake := fakegithub.Start()
	t.Cleanup(fake.Close)
	c, res := startWith(t, fake, t.TempDir(), token, hook, opts...)
	return c, fake, res
}

// startFast runs the adapter with a poll of 10 ms and no X-Poll-Interval.
func startFast(t *testing.T, token string) (*sdktest.Conn, *fakegithub.Server) {
	t.Helper()
	fake := fakegithub.Start()
	t.Cleanup(fake.Close)
	fake.SetPollInterval(0)
	c, _ := startWith(t, fake, t.TempDir(), token, nil, WithPollEvery(10*time.Millisecond))
	return c, fake
}

// startWith runs the adapter against a given fake and state folder.
func startWith(t *testing.T, fake *fakegithub.Server, stateDir, token string, hook *sdk.Webhook, opts ...Option) (*sdktest.Conn, sdk.InitializeResult) {
	t.Helper()
	cl := client.New(token)
	cl.API, cl.Web = fake.URL(), fake.URL()
	a, err := New(cl, "test", stateDir, opts...)
	if err != nil {
		t.Fatal(err)
	}
	c := sdktest.Start(a, sdk.WithStateDir(stateDir))
	t.Cleanup(func() { c.Close() }) //nolint:errcheck,gosec // the test checks the calls
	res, err := c.Initialize(sdk.InitializeParams{Webhook: hook})
	if err != nil {
		t.Fatal(err)
	}
	return c, res
}

// nextEvent waits for the next repository event. It drops the activity
// events, which the fake sends after the first notifications read.
func nextEvent(t *testing.T, c *sdktest.Conn) sdk.Event {
	t.Helper()
	return waitEvent(t, c, func(ev sdk.Event) bool { return !strings.HasPrefix(ev.ID, "notif:") })
}

// nextActivity waits for the next activity event.
func nextActivity(t *testing.T, c *sdktest.Conn) sdk.Event {
	t.Helper()
	return waitEvent(t, c, func(ev sdk.Event) bool { return strings.HasPrefix(ev.ID, "notif:") })
}

func waitEvent(t *testing.T, c *sdktest.Conn, keep func(sdk.Event) bool) sdk.Event {
	t.Helper()
	for {
		m, err := c.WaitNote(sdk.MethodEvent)
		if err != nil {
			t.Fatal(err)
		}
		var ev sdk.Event
		if err := json.Unmarshal(m.Params, &ev); err != nil {
			t.Fatal(err)
		}
		if keep(ev) {
			return ev
		}
	}
}

// ghToken is a gh login on this machine that a test can change.
type ghToken struct {
	mu    sync.Mutex
	token string
}

func (g *ghToken) set(token string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.token = token
}

func (g *ghToken) Token(context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.token == "" {
		return "", config.ErrGHLoggedOut
	}
	return g.token, nil
}

func sub(id, repo string, types ...string) sdk.Subscription {
	return sdk.Subscription{ID: id, Values: map[string][]string{FieldRepository: {repo}}, Types: types}
}

func syncSubs(t *testing.T, c *sdktest.Conn, subs ...sdk.Subscription) []sdk.SubscriptionState {
	t.Helper()
	var res sdk.SyncResult
	if err := c.Call(sdk.MethodSubscriptionsSync, sdk.SyncParams{Subscriptions: append([]sdk.Subscription{}, subs...)}, &res); err != nil {
		t.Fatal(err)
	}
	return res.Subscriptions
}

// waitCalls waits until the fake has n calls of call. The second read of a
// poll starts after the first one found the position.
func waitCalls(t *testing.T, fake *fakegithub.Server, call string, n int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if count(fake.Calls(), call) >= n {
			return
		}
	}
	t.Fatalf("no %d calls of %s", n, call)
}

func count(calls []string, call string) int {
	n := 0
	for _, c := range calls {
		if c == call {
			n++
		}
	}
	return n
}

func TestWebhookFlow(t *testing.T) {
	t.Parallel()
	hooks := newSink(t)
	hook := &sdk.Webhook{URL: hooks.srv.URL + "/in/gh", Secret: "s3cret"}
	c, fake, res := start(t, fakegithub.Token, hook)
	if res.Account == nil || res.Account.ID != "octo" {
		t.Fatalf("account %+v", res.Account)
	}

	var page sdk.OptionPage
	if err := c.Call(sdk.MethodSubscriptionsOptions, sdk.OptionQuery{Field: FieldRepository, Limit: 10}, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Options) != 3 || page.Options[0].Value != fakegithub.AdminRepo || page.Options[0].Description != "Admin" ||
		page.Options[1].Description != "Admin, private" || page.Options[2].Description != "Read only" {
		t.Fatalf("options %+v: every repository, with its access", page.Options)
	}

	st := syncSubs(t, c, sub("s1", fakegithub.AdminRepo, "push", "pull_request_review_comment"), sub("s2", fakegithub.NoAdminRepo))
	if st[0].State != sdk.SubscriptionActive || st[0].Mode != sdk.ModeAPI || st[0].Room.ID != fakegithub.AdminRepo {
		t.Fatalf("s1 %+v", st[0])
	}
	if st[1].State != sdk.SubscriptionPolling || st[1].Mode != sdk.ModePoll || st[1].Message != "You are not an admin of other/lib, so Trebi checks it every minute." {
		t.Fatalf("s2 %+v", st[1])
	}
	gh := fake.Hooks(fakegithub.AdminRepo)
	if len(gh) != 1 || gh[0].Config.URL != hook.URL || strings.Join(gh[0].Events, ",") != "pull_request_review_comment,push" {
		t.Fatalf("hooks %+v", gh)
	}
	if len(fake.Hooks(fakegithub.NoAdminRepo)) != 0 {
		t.Fatal("a read-only repository gets no hook")
	}

	reqs := hooks.wait(t, 3)
	for _, r := range reqs {
		if err := c.Call(sdk.MethodWebhookReceive, r, nil); err != nil {
			t.Fatalf("receive %s: %v", r.Headers["x-github-event"], err)
		}
	}
	ev := nextEvent(t, c)
	if ev.Type != "push" || ev.ID != "delivery-1-push" || ev.Room.ID != fakegithub.AdminRepo || ev.Sender.ID != "octo" || !ev.Sender.Self {
		t.Fatalf("event %+v %+v", ev, ev.Sender)
	}
	if !strings.Contains(ev.Text, "Fix the build") || !strings.Contains(string(ev.Data), `"branch":"main"`) {
		t.Fatalf("event text %q data %s", ev.Text, ev.Data)
	}
	ev = nextEvent(t, c)
	if ev.Type != "pull_request_review_comment" || ev.Thread == nil || ev.Thread.ID != "8" || ev.Sender.ID != "hubot" || ev.Sender.Self ||
		!strings.Contains(ev.Text, "Use a map here.") || !strings.Contains(string(ev.Data), `"path":"cache.go"`) {
		t.Fatalf("review comment %+v data %s", ev, ev.Data)
	}

	forged := reqs[0]
	forged.Body = strings.Replace(forged.Body, "abc123", "abc124", 1)
	var perr *sdk.Error
	if err := c.Call(sdk.MethodWebhookReceive, forged, nil); !errors.As(err, &perr) || perr.Code != sdk.CodeInvalid {
		t.Fatalf("forged body: %v", err)
	}

	fake.NoDeliveries()
	syncSubs(t, c, sub("s1", fakegithub.AdminRepo, "push", "issues"))
	if gh2 := fake.Hooks(fakegithub.AdminRepo); len(gh2) != 1 || gh2[0].ID != gh[0].ID || strings.Join(gh2[0].Events, ",") != "issues,push" {
		t.Fatalf("the second sync must update the one hook: %+v", gh2)
	}
	if n := count(fake.Calls(), "PATCH /repos/octo/app/hooks/1"); n != 1 {
		t.Fatalf("one PATCH for a new event list, got %d", n)
	}
	syncSubs(t, c, sub("s1", fakegithub.AdminRepo, "push", "issues"))
	if n := count(fake.Calls(), "PATCH /repos/octo/app/hooks/1"); n != 1 {
		t.Fatalf("no PATCH when the event list is the same, got %d", n)
	}
	syncSubs(t, c)
	if gh := fake.Hooks(fakegithub.AdminRepo); len(gh) != 0 {
		t.Fatalf("an empty sync must delete the hook: %+v", gh)
	}
}

func TestHookDeleteKeepsRecordWithNoAnswer(t *testing.T) {
	t.Parallel()
	hook := &sdk.Webhook{URL: "http://127.0.0.1:1/in", Secret: "x"}
	fake := fakegithub.Start()
	t.Cleanup(fake.Close)
	fake.NoDeliveries()
	dir := t.TempDir()
	c, _ := startWith(t, fake, dir, fakegithub.Token, hook)
	syncSubs(t, c, sub("s1", fakegithub.AdminRepo))
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	cl := client.New(fakegithub.Token)
	cl.API, cl.Web = fake.URL(), fake.URL()
	a, err := New(cl, "test", dir)
	if err != nil {
		t.Fatal(err)
	}
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	a.reconcile(stopped, hook, nil, nil)
	if len(a.state.Hooks) != 1 {
		t.Fatalf("a delete with no answer must keep the record: %+v", a.state.Hooks)
	}
	a.reconcile(context.Background(), hook, nil, nil)
	if len(a.state.Hooks) != 0 || len(fake.Hooks(fakegithub.AdminRepo)) != 0 {
		t.Fatalf("the next sync must delete the hook: %+v %+v", a.state.Hooks, fake.Hooks(fakegithub.AdminRepo))
	}
}

func TestHookFallsBackToPoll(t *testing.T) {
	t.Parallel()
	hook := &sdk.Webhook{URL: "http://127.0.0.1:1/in", Secret: "x"}
	c, fake, _ := start(t, fakegithub.Token, hook)
	fake.RefuseHooks()
	st := syncSubs(t, c, sub("s1", fakegithub.AdminRepo))
	if st[0].Mode != sdk.ModePoll || st[0].Message != "This login cannot add webhooks to octo/app, so Trebi checks it every minute." {
		t.Fatalf("a refused hook must poll: %+v", st[0])
	}

	c, _, _ = start(t, fakegithub.NoHookScope, hook)
	st = syncSubs(t, c, sub("s1", fakegithub.AdminRepo), sub("s2", "octo/missing"))
	if st[0].Mode != sdk.ModePoll || st[0].Message != "This login cannot add webhooks, so Trebi checks octo/app every minute." {
		t.Fatalf("a token with no hook scope must poll: %+v", st[0])
	}
	if st[1].State != sdk.SubscriptionError || !strings.Contains(st[1].Message, "cannot find octo/missing") {
		t.Fatalf("a missing repository: %+v", st[1])
	}
}

func TestPollFlow(t *testing.T) {
	t.Parallel()
	c, fake := startFast(t, fakegithub.Token)
	st := syncSubs(t, c, sub("s1", fakegithub.AdminRepo, "issues", "create"))
	if st[0].State != sdk.SubscriptionPolling || st[0].Mode != sdk.ModePoll {
		t.Fatalf("state %+v", st[0])
	}
	waitCalls(t, fake, "GET /repos/octo/app/events", 2)
	fake.AddEvent(fakegithub.AdminRepo, "2", "PushEvent")
	fake.AddEvent(fakegithub.AdminRepo, "3", "IssuesEvent")
	fake.AddEvent(fakegithub.AdminRepo, "4", "CreateEvent")
	ev := nextEvent(t, c)
	if ev.ID != "poll:3" || ev.Type != "issues" || ev.Thread == nil || ev.Thread.ID != "7" {
		t.Fatalf("event %+v: only the watched type, after the first poll", ev)
	}
	if ev := nextEvent(t, c); ev.ID != "poll:4" || ev.Type != "create" {
		t.Fatalf("create event %+v", ev)
	}
}

func TestPollRuns(t *testing.T) {
	t.Parallel()
	c, fake := startFast(t, fakegithub.Token)
	syncSubs(t, c, sub("s1", fakegithub.AdminRepo, "workflow_run"))
	waitCalls(t, fake, "GET /repos/octo/app/actions/runs", 2)
	fake.SetRun(fakegithub.AdminRepo, 2, "queued", "")
	ev := nextEvent(t, c)
	if ev.ID != "poll:run:2:queued" || ev.Type != "workflow_run" || !strings.Contains(string(ev.Data), `"action":"requested"`) {
		t.Fatalf("new run %+v %s", ev, ev.Data)
	}
	fake.SetRun(fakegithub.AdminRepo, 2, "in_progress", "")
	time.Sleep(50 * time.Millisecond)
	fake.SetRun(fakegithub.AdminRepo, 2, "completed", "failure")
	ev = nextEvent(t, c)
	if ev.ID != "poll:run:2:completed" || !strings.Contains(string(ev.Data), `"conclusion":"failure"`) {
		t.Fatalf("an in-progress run sends nothing; the end sends one event: %+v %s", ev, ev.Data)
	}
	if n := count(fake.Calls(), "GET /repos/octo/app/actions/runs"); n < 5 {
		t.Fatalf("runs read %d times", n)
	}
}

func TestDeviceLogin(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c, _, res := start(t, "", nil, WithLogin(config.NewLoginStore(dir), "app", &ghToken{}))
	if res.Account != nil {
		t.Fatalf("init %+v", res)
	}
	var methods sdk.AuthMethodsResult
	if err := c.Call(sdk.MethodAuthMethods, nil, &methods); err != nil || len(methods.Methods) != 2 {
		t.Fatalf("methods %+v %v", methods, err)
	}
	if m := methods.Methods[0]; m.ID != LoginGH || m.Type != sdk.LoginExisting || m.Available || !strings.Contains(m.Reason, "not logged in") {
		t.Fatalf("gh method with no gh login %+v", m)
	}
	if m := methods.Methods[1]; m.ID != LoginDevice || m.Type != sdk.LoginBrowser || !m.Available {
		t.Fatalf("device method %+v", m)
	}
	var begin sdk.AuthBeginResult
	if err := c.Call(sdk.MethodAuthBegin, sdk.AuthBeginParams{Method: LoginDevice}, &begin); err != nil {
		t.Fatal(err)
	}
	if begin.Step.Kind != sdk.StepDeviceCode || begin.Step.Code != fakegithub.UserCode {
		t.Fatalf("step %+v", begin.Step)
	}
	m, err := c.WaitNote(sdk.MethodAuthDone)
	if err != nil {
		t.Fatal(err)
	}
	var done sdk.AuthDoneParams
	if err := json.Unmarshal(m.Params, &done); err != nil || !done.OK || done.Account == nil || done.Account.ID != "octo" {
		t.Fatalf("done %+v %v", done, err)
	}
	rec, err := config.NewLoginStore(dir).Load()
	if err != nil || rec.Source != config.SourceDevice || rec.Token != fakegithub.Token {
		t.Fatalf("auth.json %+v %v", rec, err)
	}
}

func TestGHLogin(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	gh := &ghToken{token: fakegithub.Token}
	c, fake, _ := start(t, "", nil, WithLogin(config.NewLoginStore(dir), "app", gh))
	var methods sdk.AuthMethodsResult
	if err := c.Call(sdk.MethodAuthMethods, nil, &methods); err != nil || !methods.Methods[0].Available {
		t.Fatalf("methods %+v %v", methods, err)
	}
	done, err := c.LogIn(LoginGH)
	if err != nil || done.Account == nil || done.Account.ID != "octo" {
		t.Fatalf("login %+v %v", done, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, config.AuthFile))
	if err != nil || string(data) != `{"source":"gh"}` {
		t.Fatalf("auth.json must keep no token: %s %v", data, err)
	}

	// gh logs in again with a new token: a 401 reads it again.
	gh.set(fakegithub.Revoked)
	c2, _ := startWith(t, fake, t.TempDir(), fakegithub.Revoked, nil, WithLogin(config.NewLoginStore(dir), "app", gh), WithSource(config.SourceGH))
	gh.set(fakegithub.Token)
	var st sdk.AuthStatusResult
	if err := c2.Call(sdk.MethodAuthStatus, nil, &st); err != nil || st.State != sdk.StateConnected {
		t.Fatalf("a 401 must read the gh login again: %+v %v", st, err)
	}

	// gh logs out: auth_required, and logout never runs gh.
	gh.set("")
	c3, _ := startWith(t, fake, t.TempDir(), "", nil, WithLogin(config.NewLoginStore(dir), "app", gh), WithSource(config.SourceGH))
	if err := c3.Call(sdk.MethodAuthStatus, nil, &st); err != nil || st.State != sdk.StateAuthRequired {
		t.Fatalf("gh logged out: %+v %v", st, err)
	}
	if err := c3.Call(sdk.MethodAuthLogout, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, config.AuthFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("logout must remove auth.json: %v", err)
	}
}

func TestLoginNeedsAnApp(t *testing.T) {
	t.Parallel()
	c, _, _ := start(t, "", nil, WithLogin(config.NewLoginStore(t.TempDir()), "", &ghToken{}))
	var methods sdk.AuthMethodsResult
	if err := c.Call(sdk.MethodAuthMethods, nil, &methods); err != nil || len(methods.Methods) != 2 || methods.Methods[1].Available || methods.Methods[1].Reason == "" {
		t.Fatalf("methods %+v %v", methods, err)
	}
	if err := c.Call(sdk.MethodAuthBegin, nil, nil); sdk.CodeOf(err) != sdk.CodeInvalid {
		t.Fatalf("begin with no method: %v", err)
	}
}

func TestRevokedToken(t *testing.T) {
	t.Parallel()
	c, _, _ := start(t, fakegithub.Revoked, &sdk.Webhook{URL: "http://127.0.0.1:1/in", Secret: "x"})
	var st sdk.AuthStatusResult
	if err := c.Call(sdk.MethodAuthStatus, nil, &st); err != nil || st.State != sdk.StateAuthRequired {
		t.Fatalf("auth status %+v %v", st, err)
	}
	if s := syncSubs(t, c, sub("s1", fakegithub.AdminRepo)); s[0].State != sdk.SubscriptionError || s[0].Message != "Log in to GitHub again." {
		t.Fatalf("state %+v", s[0])
	}
}

func TestScopes(t *testing.T) {
	t.Parallel()
	fake := fakegithub.Start()
	t.Cleanup(fake.Close)
	for token, want := range map[string]string{fakegithub.Token: "repo,read:org,admin:repo_hook", fakegithub.FineGrained: ""} {
		cl := client.New(token)
		cl.API = fake.URL()
		id, err := cl.Whoami(context.Background())
		if err != nil || strings.Join(id.Scopes, ",") != want || id.Scoped != (want != "") {
			t.Fatalf("%s: %+v %v", token, id, err)
		}
	}
}
