package serve

import (
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
	cl := client.New(token)
	cl.API, cl.Web = fake.URL(), fake.URL()
	a, err := New(cl, "test", t.TempDir(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	c := sdktest.Start(a, sdk.WithStateDir(t.TempDir()))
	t.Cleanup(func() { c.Close() }) //nolint:errcheck,gosec // the test checks the calls
	res, err := c.Initialize(sdk.InitializeParams{Webhook: hook})
	if err != nil {
		t.Fatal(err)
	}
	return c, fake, res
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
	if len(page.Options) != 2 || page.Options[0].Value != fakegithub.AdminRepo {
		t.Fatalf("options %+v: only admin repositories can get a hook", page.Options)
	}

	st := syncSubs(t, c, sub("s1", fakegithub.AdminRepo, "push"), sub("s2", fakegithub.NoAdminRepo))
	if st[0].State != sdk.SubscriptionActive || st[0].Mode != sdk.ModeAPI || st[0].Room.ID != fakegithub.AdminRepo {
		t.Fatalf("s1 %+v", st[0])
	}
	if st[1].State != sdk.SubscriptionError || !strings.Contains(st[1].Message, "admin access to other/lib") {
		t.Fatalf("s2 %+v", st[1])
	}
	gh := fake.Hooks(fakegithub.AdminRepo)
	if len(gh) != 1 || gh[0].Config.URL != hook.URL || strings.Join(gh[0].Events, ",") != "push" {
		t.Fatalf("hooks %+v", gh)
	}

	reqs := hooks.wait(t, 2)
	for _, r := range reqs {
		if err := c.Call(sdk.MethodWebhookReceive, r, nil); err != nil {
			t.Fatalf("receive %s: %v", r.Headers["x-github-event"], err)
		}
	}
	m, err := c.WaitNote(sdk.MethodEvent)
	if err != nil {
		t.Fatal(err)
	}
	var ev sdk.Event
	if err := json.Unmarshal(m.Params, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Type != "push" || ev.ID != "delivery-1-push" || ev.Room.ID != fakegithub.AdminRepo || ev.Sender.ID != "octo" || !ev.Sender.Self {
		t.Fatalf("event %+v %+v", ev, ev.Sender)
	}
	if !strings.Contains(ev.Text, "Fix the build") || !strings.Contains(string(ev.Data), `"branch":"main"`) {
		t.Fatalf("event text %q data %s", ev.Text, ev.Data)
	}

	forged := reqs[0]
	forged.Body = strings.Replace(forged.Body, "abc123", "abc124", 1)
	var perr *sdk.Error
	if err := c.Call(sdk.MethodWebhookReceive, forged, nil); !errors.As(err, &perr) || perr.Code != sdk.CodeInvalid {
		t.Fatalf("forged body: %v", err)
	}

	fake.NoDeliveries()
	syncSubs(t, c, sub("s1", fakegithub.AdminRepo, "push", "issues"))
	if gh := fake.Hooks(fakegithub.AdminRepo); len(gh) != 1 || strings.Join(gh[0].Events, ",") != "issues,push" {
		t.Fatalf("the second sync must update the one hook: %+v", gh)
	}
	syncSubs(t, c)
	if gh := fake.Hooks(fakegithub.AdminRepo); len(gh) != 0 {
		t.Fatalf("an empty sync must delete the hook: %+v", gh)
	}
}

func TestPollFlow(t *testing.T) {
	t.Parallel()
	c, fake, _ := start(t, fakegithub.Token, nil, WithPollEvery(10*time.Millisecond))
	fake.SetPollInterval(0)
	st := syncSubs(t, c, sub("s1", fakegithub.AdminRepo, "issues"))
	if st[0].State != sdk.SubscriptionPolling || st[0].Mode != sdk.ModePoll {
		t.Fatalf("state %+v", st[0])
	}
	time.Sleep(100 * time.Millisecond) // the first poll finds the position
	fake.AddEvent(fakegithub.AdminRepo, "2", "PushEvent")
	fake.AddEvent(fakegithub.AdminRepo, "3", "IssuesEvent")
	m, err := c.WaitNote(sdk.MethodEvent)
	if err != nil {
		t.Fatal(err)
	}
	var ev sdk.Event
	if err := json.Unmarshal(m.Params, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.ID != "poll:3" || ev.Type != "issues" || ev.Thread == nil || ev.Thread.ID != "7" {
		t.Fatalf("event %+v: only the watched type, after the first poll", ev)
	}
}

func TestDeviceLogin(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c, _, res := start(t, "", nil, WithLogin(config.NewLoginStore(dir), "app"))
	if res.Account != nil || len(res.Login) != 1 || res.Login[0] != sdk.StepDeviceCode {
		t.Fatalf("init %+v", res)
	}
	var begin sdk.AuthBeginResult
	if err := c.Call(sdk.MethodAuthBegin, sdk.AuthBeginParams{}, &begin); err != nil {
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
	data, err := os.ReadFile(filepath.Join(dir, config.AuthFile))
	if err != nil || !strings.Contains(string(data), fakegithub.Token) {
		t.Fatalf("auth.json %s %v", data, err)
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
