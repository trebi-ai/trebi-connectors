package serve

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/notion-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/notion-cli/internal/fakenotion"
	"github.com/trebi-ai/trebi-connectors/sdk"
	"github.com/trebi-ai/trebi-connectors/sdk/sdktest"
)

const hookURL = "https://hooks.trebi.test/in/notion"

func start(t *testing.T, hook *sdk.Webhook, opts ...Option) (*sdktest.Conn, *fakenotion.Fake) {
	t.Helper()
	fake := fakenotion.Start()
	t.Cleanup(fake.Close)
	cl := client.New("secret_test")
	cl.BaseURL = fake.URL()
	a, err := New(cl, "test", "", opts...)
	if err != nil {
		t.Fatal(err)
	}
	c := sdktest.Start(a, sdk.WithStateDir(t.TempDir()))
	t.Cleanup(func() { c.Close() }) //nolint:errcheck,gosec // the test checks the calls
	res, err := c.Initialize(sdk.InitializeParams{Webhook: hook})
	if err != nil {
		t.Fatal(err)
	}
	if res.Account == nil || res.Account.ID != fakenotion.BotID {
		t.Fatalf("account %+v", res.Account)
	}
	return c, fake
}

func syncOne(t *testing.T, c *sdktest.Conn) sdk.SubscriptionState {
	t.Helper()
	var res sdk.SyncResult
	err := c.Call(sdk.MethodSubscriptionsSync, sdk.SyncParams{Subscriptions: []sdk.Subscription{{ID: "s1", Values: map[string][]string{}}}}, &res)
	if err != nil || len(res.Subscriptions) != 1 {
		t.Fatalf("sync %+v: %v", res, err)
	}
	return res.Subscriptions[0]
}

func receive(c *sdktest.Conn, body, sig string) error {
	req := sdk.WebhookRequest{ID: "w", Method: "POST", Path: "/in/notion", Body: body, Headers: map[string]string{}}
	if sig != "" {
		req.Headers[SignatureHeader] = sig
	}
	return c.Call(sdk.MethodWebhookReceive, req, nil)
}

func TestWebhookFlow(t *testing.T) {
	t.Parallel()
	c, _ := start(t, &sdk.Webhook{URL: hookURL, Secret: "unused"})

	st := syncOne(t, c)
	if st.Mode != sdk.ModeManual || st.State != sdk.SubscriptionActionRequired || st.Action == nil ||
		st.Action.URL != IntegrationsURL || len(st.Action.Show) != 1 || st.Action.Show[0].Value != hookURL || len(st.Action.Ask) != 0 {
		t.Fatalf("first sync %+v %+v", st, st.Action)
	}

	const token = "secret_verify"
	if err := receive(c, `{"verification_token":"`+token+`"}`, ""); err != nil {
		t.Fatalf("verification: %v", err)
	}
	if _, err := c.WaitNote(sdk.MethodSubscriptionsChanged); err != nil {
		t.Fatal(err)
	}
	st = syncOne(t, c)
	if st.State != sdk.SubscriptionActionRequired || len(st.Action.Show) != 2 ||
		st.Action.Show[1].Label != "Code" || st.Action.Show[1].Value != token || !st.Action.Show[1].Secret ||
		!strings.Contains(st.Action.Text, "Paste this code into Notion") {
		t.Fatalf("sync after verification %+v %+v", st, st.Action)
	}

	body := `{"id":"evt-1","timestamp":"2026-10-08T10:00:00.000Z","type":"page.content_updated",` +
		`"authors":[{"id":"user-ana","type":"person"}],"entity":{"id":"` + fakenotion.PageID + `","type":"page"}}`
	if err := receive(c, body, "sha256=00"); sdk.CodeOf(err) != sdk.CodeInvalid {
		t.Fatalf("bad signature: %v", err)
	}
	if err := receive(c, body, fakenotion.Sign(token, []byte(body))); err != nil {
		t.Fatalf("signed delivery: %v", err)
	}
	m, err := c.WaitNote(sdk.MethodEvent)
	if err != nil {
		t.Fatal(err)
	}
	var ev sdk.Event
	if err := json.Unmarshal(m.Params, &ev); err != nil {
		t.Fatal(err)
	}
	var data pageData
	if err := json.Unmarshal(ev.Data, &data); err != nil {
		t.Fatal(err)
	}
	if ev.ID != "evt-1" || ev.Type != "page" || ev.Room.ID != fakenotion.DatabaseID || ev.Text != "Write the plan" ||
		ev.Sender == nil || ev.Sender.ID != "user-ana" || data.Change != "content_updated" || data.DatabaseID != fakenotion.DatabaseID {
		t.Fatalf("event %+v data %+v", ev, data)
	}
	if _, err := c.WaitNote(sdk.MethodSubscriptionsChanged); err != nil {
		t.Fatal(err)
	}
	if st := syncOne(t, c); st.State != sdk.SubscriptionActive || st.Action != nil {
		t.Fatalf("sync after delivery %+v", st)
	}
	if err := receive(c, `{"verification_token":"`+token+`"}`, ""); err != nil {
		t.Fatalf("the same token again: %v", err)
	}
	if err := receive(c, `{"verification_token":"secret_other"}`, ""); sdk.CodeOf(err) != sdk.CodeInvalid {
		t.Fatalf("another token after verification: %v", err)
	}
}

func TestWebhookWithoutToken(t *testing.T) {
	t.Parallel()
	c, _ := start(t, &sdk.Webhook{URL: hookURL})
	body := `{"id":"evt-1","type":"page.created","entity":{"id":"x","type":"page"}}`
	if err := receive(c, body, fakenotion.Sign("guess", []byte(body))); sdk.CodeOf(err) != sdk.CodeInvalid {
		t.Fatalf("no token: %v", err)
	}
}

func TestDeletedPage(t *testing.T) {
	t.Parallel()
	c, _ := start(t, &sdk.Webhook{URL: hookURL})
	const token = "secret_verify"
	if err := receive(c, `{"verification_token":"`+token+`"}`, ""); err != nil {
		t.Fatal(err)
	}
	body := `{"id":"evt-2","type":"page.deleted","entity":{"id":"page-gone","type":"page"},"data":{"parent":{"id":"db-x","type":"database"}}}`
	if err := receive(c, body, fakenotion.Sign(token, []byte(body))); err != nil {
		t.Fatal(err)
	}
	m, err := c.WaitNote(sdk.MethodEvent)
	if err != nil {
		t.Fatal(err)
	}
	var ev sdk.Event
	if err := json.Unmarshal(m.Params, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.ID != "evt-2" || ev.Room.ID != "db-x" || !strings.Contains(string(ev.Data), `"change":"deleted"`) {
		t.Fatalf("event %+v %s", ev, ev.Data)
	}
}

func TestPoll(t *testing.T) {
	t.Parallel()
	c, fake := start(t, nil, WithPollInterval(20*time.Millisecond))
	if st := syncOne(t, c); st.Mode != sdk.ModePoll || st.State != sdk.SubscriptionPolling || st.Action != nil {
		t.Fatalf("sync %+v", st)
	}
	p := fake.AddPage("New task")
	m, err := c.WaitNote(sdk.MethodEvent)
	if err != nil {
		t.Fatal(err)
	}
	var ev sdk.Event
	if err := json.Unmarshal(m.Params, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.ID != "poll:"+p.ID+":"+p.LastEditedTime || ev.Type != "page" || ev.Text != "New task" || ev.Room.ID != fakenotion.DatabaseID {
		t.Fatalf("event %+v", ev)
	}
}

func TestSandboxSubscribe(t *testing.T) {
	t.Parallel()
	fake := fakenotion.Start()
	t.Cleanup(fake.Close)
	got := make(chan sdk.WebhookRequest, 4)
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // a short local body
		got <- sdk.WebhookRequest{ID: "w", Method: r.Method, Body: string(body),
			Headers: map[string]string{SignatureHeader: r.Header.Get(SignatureHeader)}}
	}))
	t.Cleanup(sink.Close)
	hook := sink.URL
	cl := client.New("sandbox")
	cl.BaseURL = fake.URL()
	a, err := New(cl, "test", "", WithSandbox(fake.SubscribeURL()))
	if err != nil {
		t.Fatal(err)
	}
	c := sdktest.Start(a, sdk.WithStateDir(t.TempDir()))
	t.Cleanup(func() { c.Close() }) //nolint:errcheck,gosec // the test checks the calls
	if _, err := c.Initialize(sdk.InitializeParams{Webhook: &sdk.Webhook{URL: hook}}); err != nil {
		t.Fatal(err)
	}
	if st := syncOne(t, c); st.State != sdk.SubscriptionActionRequired {
		t.Fatalf("sync %+v", st)
	}
	for range 2 {
		select {
		case r := <-got:
			if err := c.Call(sdk.MethodWebhookReceive, r, nil); err != nil {
				t.Fatalf("receive %s: %v", r.Body, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the fake sent no delivery")
		}
	}
	if _, err := c.WaitNote(sdk.MethodEvent); err != nil {
		t.Fatal(err)
	}
	if st := syncOne(t, c); st.State != sdk.SubscriptionActive {
		t.Fatalf("sync after deliveries %+v", st)
	}
}

func TestEmptySync(t *testing.T) {
	t.Parallel()
	c, _ := start(t, nil)
	var res sdk.SyncResult
	if err := c.Call(sdk.MethodSubscriptionsSync, sdk.SyncParams{Subscriptions: []sdk.Subscription{}}, &res); err != nil || len(res.Subscriptions) != 0 {
		t.Fatalf("empty sync %+v: %v", res, err)
	}
	var st sdk.SubscriptionState
	if err := c.Call(sdk.MethodSubscriptionsSubmit, sdk.SubmitParams{ID: "s1", Fields: map[string]string{}}, &st); err != nil || st.ID != "s1" {
		t.Fatalf("submit %+v: %v", st, err)
	}
}

func TestMissingSecret(t *testing.T) {
	t.Parallel()
	a, err := New(client.New(""), "test", "")
	if err != nil {
		t.Fatal(err)
	}
	c := sdktest.Start(a, sdk.WithStateDir(t.TempDir()))
	t.Cleanup(func() { c.Close() }) //nolint:errcheck,gosec // the test checks the calls
	if _, err := c.Initialize(sdk.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	m, err := c.WaitNote(sdk.MethodStatus)
	if err != nil {
		t.Fatal(err)
	}
	var st sdk.Status
	if err := json.Unmarshal(m.Params, &st); err != nil {
		t.Fatal(err)
	}
	if st.State != sdk.StateAuthRequired || st.Reason != sdk.ReasonMissingInput || !strings.Contains(st.Message, "Integration secret") {
		t.Fatalf("status %+v", st)
	}
}
