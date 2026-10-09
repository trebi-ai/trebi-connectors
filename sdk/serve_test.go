package sdk_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/sdk"
	"github.com/trebi-ai/trebi-connectors/sdk/sdktest"
)

// flowAdapter plays the adapter side of the contract transcripts.
type flowAdapter struct {
	result   sdk.InitializeResult
	live     []sdk.Event
	replay   []sdk.Event
	steps    []sdk.Step
	account  *sdk.Account
	loggedIn bool
	synced   []sdk.SubscriptionState
	submit   sdk.SubscriptionState
	hook     sdk.Event

	mu    sync.Mutex
	sends int
}

func (a *flowAdapter) Initialize(context.Context, sdk.InitializeParams) (sdk.InitializeResult, error) {
	return a.result, nil
}

func (a *flowAdapter) Run(ctx context.Context, e sdk.Emitter) error {
	for _, ev := range a.live {
		if err := e.Event(ev); err != nil {
			return err
		}
	}
	<-ctx.Done()
	return nil
}

func (a *flowAdapter) AuthStatus(context.Context) (sdk.AuthState, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.loggedIn {
		return sdk.AuthState{State: sdk.StateAuthRequired}, nil
	}
	return sdk.AuthState{State: sdk.StateConnected, Account: a.account}, nil
}

func (a *flowAdapter) BeginAuth(_ context.Context, _ string, steps sdk.StepSink) error {
	for _, st := range a.steps {
		if err := steps.Step(st); err != nil {
			return err
		}
	}
	a.mu.Lock()
	a.loggedIn = true
	a.mu.Unlock()
	return nil
}

func (a *flowAdapter) SubmitAuth(context.Context, string, map[string]string) error { return nil }
func (a *flowAdapter) Logout(context.Context) error                                { return nil }

func (a *flowAdapter) Send(_ context.Context, m sdk.SendParams) (sdk.SendResult, error) {
	if m.Room == "1180000000000000009" {
		return sdk.SendResult{}, sdk.RateLimited("slow down", 1500*time.Millisecond)
	}
	a.mu.Lock()
	a.sends++
	a.mu.Unlock()
	return sdk.SendResult{MessageID: "1290114512345679000", Thread: m.Thread}, nil
}

func (a *flowAdapter) Replay(context.Context, string, int) ([]sdk.Event, bool, error) {
	return a.replay, true, nil
}

func (a *flowAdapter) ListRooms(context.Context, sdk.RoomQuery) (sdk.RoomPage, error) {
	return sdk.RoomPage{}, nil
}
func (a *flowAdapter) OpenRoom(context.Context, string) (sdk.Room, error) { return sdk.Room{}, nil }
func (a *flowAdapter) ListThreads(context.Context, sdk.ThreadQuery) (sdk.ThreadPage, error) {
	return sdk.ThreadPage{}, nil
}
func (a *flowAdapter) Typing(context.Context, string, string) error        { return nil }
func (a *flowAdapter) Seen(context.Context, string, string) error          { return nil }
func (a *flowAdapter) React(context.Context, string, string, string) error { return nil }
func (a *flowAdapter) Edit(context.Context, sdk.EditParams) error          { return nil }

func (a *flowAdapter) Options(context.Context, sdk.OptionQuery) (sdk.OptionPage, error) {
	return sdk.OptionPage{}, nil
}

func (a *flowAdapter) Sync(context.Context, sdk.SyncParams) (sdk.SyncResult, error) {
	return sdk.SyncResult{Subscriptions: a.synced}, nil
}

func (a *flowAdapter) SubmitSubscription(context.Context, sdk.SubmitParams) (sdk.SubscriptionState, error) {
	return a.submit, nil
}

func (a *flowAdapter) ReceiveWebhook(ctx context.Context, _ sdk.WebhookRequest) error {
	return sdk.EmitterFrom(ctx).Event(a.hook)
}

var (
	ana    = &sdk.Account{ID: "5511999999999@s.whatsapp.net", Name: "Ana"}
	family = &sdk.Room{ID: "120363041234@g.us", Name: "Family", Kind: sdk.RoomGroup}
)

func TestFlows(t *testing.T) {
	t.Parallel()
	t.Run("login.qr", func(t *testing.T) {
		t.Parallel()
		a := &flowAdapter{
			result: sdk.InitializeResult{
				Adapter:  sdk.AdapterInfo{Name: "whatsapp-cli", Version: "1.3.0"},
				Events:   []sdk.EventDecl{{Type: "message"}},
				Features: []string{"rooms.list", "typing", "seen"},
				Limits:   sdk.Limits{MaxText: 65536, Formats: []string{"text"}},
				Login:    []string{"qr"},
			},
			account: ana,
			steps: []sdk.Step{
				{Kind: sdk.StepQR, Data: "2@AbCdEf,GhIjKl,MnOpQr", ExpiresAt: "2026-09-28T08:20:20Z"},
				{Kind: sdk.StepQR, Data: "2@StUvWx,YzAbCd,EfGhIj", ExpiresAt: "2026-09-28T08:20:40Z"},
				{Kind: sdk.StepWait, Message: "Open WhatsApp on your phone"},
			},
		}
		sdktest.Run(t, a, "contract/flow.login.qr.jsonl")
	})
	t.Run("replay", func(t *testing.T) {
		t.Parallel()
		a := &flowAdapter{
			result: sdk.InitializeResult{
				Adapter:  sdk.AdapterInfo{Name: "whatsapp-cli", Version: "1.3.0"},
				Account:  ana,
				Events:   []sdk.EventDecl{{Type: "message"}},
				Features: []string{"replay", "typing"},
				Limits:   sdk.Limits{MaxText: 65536, Formats: []string{"text"}},
				Login:    []string{"qr"},
			},
			account: ana, loggedIn: true,
			replay: []sdk.Event{{
				ID: "3EB0C431B3A2", Type: "message", TS: "2026-09-28T08:14:57.000000Z", Room: family,
				Sender: &sdk.Author{ID: ana.ID, Name: "Ana"}, Text: "Can you check the flight?",
			}},
			live: []sdk.Event{{
				ID: "3EB0C431B3A4", Type: "message", TS: "2026-09-28T08:18:00.000000Z", Room: family,
				Sender: &sdk.Author{ID: "5511888888888@s.whatsapp.net", Name: "Bo"}, Text: "Landed.",
			}},
		}
		sdktest.Run(t, a, "contract/flow.replay.jsonl")
	})
	t.Run("reply", func(t *testing.T) {
		t.Parallel()
		bot := &sdk.Account{ID: "1170000000000000001", Name: "trebi-bot"}
		a := &flowAdapter{
			result: sdk.InitializeResult{
				Adapter:  sdk.AdapterInfo{Name: "discord-cli", Version: "0.4.2"},
				Account:  bot,
				Events:   []sdk.EventDecl{{Type: "message"}, {Type: "reaction"}},
				Features: []string{"rooms.list", "threads", "typing", "seen", "reactions", "edit", "replies"},
				Limits:   sdk.Limits{MaxText: 2000, Formats: []string{"markdown"}},
			},
			account: bot, loggedIn: true,
			live: []sdk.Event{{
				ID: "1290114512345678901", Type: "message", TS: "2026-09-28T08:15:02.000000Z",
				Room:   &sdk.Room{ID: "1180000000000000001", Name: "support", Kind: sdk.RoomChannel},
				Thread: &sdk.Thread{ID: "1290114500000000001", Title: "Refund for order 42"},
				Sender: &sdk.Author{ID: "318000000000000001", Name: "sam"}, Text: "Any news?",
			}},
		}
		sdktest.Run(t, a, "contract/flow.reply.jsonl", sdk.WithStateDir(t.TempDir()))
		if a.sends != 1 {
			t.Fatalf("adapter sent %d times; the second send with one key must be dropped", a.sends)
		}
	})
	t.Run("subscriptions", func(t *testing.T) {
		t.Parallel()
		u := &sdk.Account{ID: "u-1", Name: "Ana"}
		eng := &sdk.Room{ID: "ENG", Name: "Engineering"}
		const manual = "sub_01j9zq5a6b7c8d9e0f1g2h3j4k"
		a := &flowAdapter{
			result: sdk.InitializeResult{
				Adapter:  sdk.AdapterInfo{Name: "linear-cli", Version: "0.1.0"},
				Account:  u,
				Events:   []sdk.EventDecl{{Type: "issue"}, {Type: "comment"}},
				Features: []string{sdk.FeatureSubscriptions, sdk.FeatureWebhooks},
			},
			account: u, loggedIn: true,
			synced: []sdk.SubscriptionState{
				{ID: "sub_01j9zq3k4m5n6p7r8s9t0v1w2x", Title: "ENG · Issues", Room: eng, Mode: sdk.ModeAPI, State: sdk.SubscriptionActive},
				{ID: manual, Title: "Every public team · Issues", Mode: sdk.ModeManual, State: sdk.SubscriptionActionRequired, Action: &sdk.Action{
					Text: "Add a webhook with this URL in Linear, then paste the signing secret.",
					Show: []sdk.ShowValue{{Label: "URL", Value: "https://in.trebi.ai/hook/Zm9vYmFyYmF6cXV4"}},
					Ask:  []sdk.AskField{{Name: "signing_secret", Label: "Signing secret", Secret: true, Required: true}},
				}},
			},
			submit: sdk.SubscriptionState{ID: manual, Title: "Every public team · Issues", Mode: sdk.ModeManual, State: sdk.SubscriptionActive},
			hook: sdk.Event{
				ID: "234d1a4e-b617-4388-90fe-adc3633d6b72", Type: "issue", TS: "2026-10-08T12:00:00Z", Room: eng,
				Sender: &sdk.Author{ID: "u-2", Name: "Sam"}, Text: "ENG-42 Fix login",
			},
		}
		sdktest.Run(t, a, "contract/flow.subscriptions.jsonl")
	})
}

// bare has no optional interface.
type bare struct{}

func (bare) Initialize(context.Context, sdk.InitializeParams) (sdk.InitializeResult, error) {
	return sdk.InitializeResult{Adapter: sdk.AdapterInfo{Name: "bare", Version: "0.1.0"}, Events: []sdk.EventDecl{{Type: "message"}}}, nil
}

type runner struct {
	bare
	started chan struct{}
}

func (r runner) Run(ctx context.Context, e sdk.Emitter) error {
	close(r.started)
	if err := e.Event(sdk.Event{ID: "e1", Type: "message"}); err != nil {
		return err
	}
	if err := e.Event(sdk.Event{ID: "e2", Type: "unknown"}); err == nil {
		return errors.New("an undeclared type must fail")
	}
	if err := e.Event(sdk.Event{ID: "big", Type: "message", Text: strings.Repeat("x", sdk.MaxLine)}); !errors.Is(err, sdk.ErrLineTooLong) {
		return errors.New("an event over 1 MiB must fail")
	}
	<-ctx.Done()
	return nil
}

func TestLifecycle(t *testing.T) {
	t.Parallel()
	r := runner{started: make(chan struct{})}
	c := sdktest.Start(r)
	var res sdk.InitializeResult
	if err := c.Call(sdk.MethodInitialize, sdk.InitializeParams{Protocol: sdk.Protocol}, &res); err != nil {
		t.Fatal(err)
	}
	if res.Protocol != sdk.Protocol || res.Features == nil || res.Login == nil || len(res.Features) != 0 {
		t.Fatalf("result %+v", res)
	}
	if err := c.Call(sdk.MethodPing, nil, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.started:
		t.Fatal("Run started before initialized")
	default:
	}
	if err := c.Notify(sdk.MethodInitialized, nil); err != nil {
		t.Fatal(err)
	}
	st, err := c.WaitNote(sdk.MethodStatus)
	if err != nil || !strings.Contains(string(st.Params), `"connected"`) {
		t.Fatalf("status %s: %v", st.Params, err)
	}
	ev, err := c.WaitNote(sdk.MethodEvent)
	if err != nil || !strings.Contains(string(ev.Params), `"e1"`) {
		t.Fatalf("event %s: %v", ev.Params, err)
	}
	err = c.Call(sdk.MethodMessagesEdit, sdk.EditParams{Room: "r", MessageID: "m", Text: "x"}, nil)
	if sdk.CodeOf(err) != sdk.CodeUnsupported || err.(*sdk.Error).Message != "edit is not a feature of this adapter" {
		t.Fatalf("edit: %v", err)
	}
	if err := c.Call(sdk.MethodMessagesSend, sdk.SendParams{Room: "r", Text: "x"}, nil); sdk.CodeOf(err) != sdk.CodeUnsupported {
		t.Fatalf("send on a bare adapter: %v", err)
	}
	if err := c.Call("bogus/method", nil, nil); sdk.CodeOf(err) != sdk.CodeUnsupported {
		t.Fatalf("unknown method: %v", err)
	}
	start := time.Now()
	if err := c.Call(sdk.MethodShutdown, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Served(); err != nil {
		t.Fatalf("serve after shutdown: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("shutdown took %s", d)
	}
	c.Close() //nolint:errcheck // Serve already returned
}

func TestProtocolMismatch(t *testing.T) {
	t.Parallel()
	c := sdktest.Start(bare{})
	defer c.Close() //nolint:errcheck // the test checks the call
	err := c.Call(sdk.MethodInitialize, sdk.InitializeParams{Protocol: "trebi-connector/2"}, nil)
	if sdk.CodeOf(err) != sdk.CodeInvalid {
		t.Fatalf("got %v", err)
	}
	if err := c.Call(sdk.MethodTyping, sdk.TypingParams{Room: "r"}, nil); sdk.CodeOf(err) != sdk.CodeInvalid {
		t.Fatalf("a request before initialize: %v", err)
	}
}

func TestLineLimit(t *testing.T) {
	t.Parallel()
	c := sdktest.Start(bare{})
	line := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"pad":"` + strings.Repeat("x", sdk.MaxLine) + `"}}`
	go c.Write([]byte(line)) //nolint:errcheck // Serve stops reading in the middle of the line; Close ends the write
	if err := c.Served(); !errors.Is(err, sdk.ErrLineTooLong) {
		t.Fatalf("serve: %v", err)
	}
	c.Close() //nolint:errcheck // Serve already returned
}

func TestDedupeSurvivesRestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	send := func() (sdk.SendResult, int) {
		a := &flowAdapter{result: sdk.InitializeResult{Events: []sdk.EventDecl{{Type: "message"}}}, loggedIn: true}
		c := sdktest.Start(a, sdk.WithStateDir(dir))
		defer c.Close() //nolint:errcheck // the test checks the result
		if _, err := c.Initialize(sdk.InitializeParams{}); err != nil {
			t.Fatal(err)
		}
		var r sdk.SendResult
		if err := c.Call(sdk.MethodMessagesSend, sdk.SendParams{Room: "r", Text: "hi", Key: "k1"}, &r); err != nil {
			t.Fatal(err)
		}
		return r, a.sends
	}
	first, n1 := send()
	second, n2 := send()
	if n1 != 1 || n2 != 0 || first != second {
		t.Fatalf("first %v (%d sends), second %v (%d sends)", first, n1, second, n2)
	}
}

func sandboxConfig() sdk.SandboxConfig {
	return sdk.SandboxConfig{
		Adapter:    sdk.AdapterInfo{Name: "sandbox-cli", Version: "1.0.0"},
		Features:   []string{"rooms.list", "rooms.open", "threads", "history", "typing", "seen", "reactions", "attachments.in"},
		Limits:     sdk.Limits{MaxText: 10, Formats: []string{"text"}},
		Login:      []string{"qr", "input"},
		LoginDelay: 10 * time.Millisecond,
	}
}

func TestSandbox(t *testing.T) {
	t.Parallel()
	c := sdktest.Start(sdk.NewSandbox(sandboxConfig()))
	defer c.Close() //nolint:errcheck // the test checks the calls
	res, err := c.Initialize(sdk.InitializeParams{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Features, ",") != strings.Join(sandboxConfig().Features, ",") || res.Account != nil {
		t.Fatalf("initialize %+v", res)
	}
	if st, err := c.WaitNote(sdk.MethodStatus); err != nil || !strings.Contains(string(st.Params), `"auth_required"`) {
		t.Fatalf("status %s: %v", st.Params, err)
	}
	send := sdk.SendParams{Room: "sandbox-general", Text: "hello", Format: "text", Key: "k1"}
	if err := c.Call(sdk.MethodMessagesSend, send, nil); sdk.CodeOf(err) != sdk.CodeAuthRequired {
		t.Fatalf("send before login: %v", err)
	}
	var begin sdk.AuthBeginResult
	if err := c.Call(sdk.MethodAuthBegin, sdk.AuthBeginParams{Kind: "qr"}, &begin); err != nil || begin.Step.Kind != sdk.StepQR {
		t.Fatalf("auth/begin %+v: %v", begin, err)
	}
	done, err := c.WaitNote(sdk.MethodAuthDone)
	if err != nil || !strings.Contains(string(done.Params), `"ok":true`) {
		t.Fatalf("auth/done %s: %v", done.Params, err)
	}
	var sent, again sdk.SendResult
	if err := c.Call(sdk.MethodMessagesSend, send, &sent); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(sdk.MethodMessagesSend, send, &again); err != nil || again != sent {
		t.Fatalf("second send %v: %v", again, err)
	}
	var page sdk.EventPage
	if err := c.Call(sdk.MethodMessagesHistory, sdk.HistoryQuery{Room: "sandbox-general", Limit: 10}, &page); err != nil || len(page.Events) != 1 {
		t.Fatalf("history %+v: %v", page, err)
	}
	long := send
	long.Text, long.Key = strings.Repeat("x", 11), "k2"
	if err := c.Call(sdk.MethodMessagesSend, long, nil); sdk.CodeOf(err) != sdk.CodeInvalid {
		t.Fatalf("long text: %v", err)
	}
	var rooms sdk.RoomPage
	if err := c.Call(sdk.MethodRoomsList, sdk.RoomQuery{Limit: 2}, &rooms); err != nil || len(rooms.Rooms) != 2 || rooms.Next == "" {
		t.Fatalf("rooms %+v: %v", rooms, err)
	}
	var room sdk.RoomResult
	if err := c.Call(sdk.MethodRoomsGet, sdk.RoomParams{Room: "sandbox-team"}, &room); err != nil || room.Room.Kind != sdk.RoomGroup {
		t.Fatalf("rooms/get %+v: %v", room, err)
	}
	var threads sdk.ThreadPage
	if err := c.Call(sdk.MethodThreadsList, sdk.ThreadQuery{Room: "sandbox-general"}, &threads); err != nil || len(threads.Threads) != 1 {
		t.Fatalf("threads %+v: %v", threads, err)
	}
	if err := c.Call(sdk.MethodThreadsCreate, sdk.CreateThreadParams{Room: "sandbox-general"}, nil); sdk.CodeOf(err) != sdk.CodeUnsupported {
		t.Fatalf("threads/create is not configured: %v", err)
	}
	for _, m := range []string{sdk.MethodTyping, sdk.MethodMessagesSeen, sdk.MethodReactionsAdd} {
		if err := c.Call(m, map[string]string{"room": "sandbox-general", "message_id": sent.MessageID, "emoji": "👍"}, nil); err != nil {
			t.Fatalf("%s: %v", m, err)
		}
	}
	if err := c.Call(sdk.MethodAuthLogout, nil, nil); err != nil {
		t.Fatal(err)
	}
	var auth sdk.AuthStatusResult
	if err := c.Call(sdk.MethodAuthStatus, nil, &auth); err != nil || auth.State != sdk.StateAuthRequired {
		t.Fatalf("auth/status %+v: %v", auth, err)
	}
}

func TestSandboxLoginCancelAndInput(t *testing.T) {
	t.Parallel()
	cfg := sandboxConfig()
	cfg.LoginDelay = time.Minute
	c := sdktest.Start(sdk.NewSandbox(cfg))
	defer c.Close() //nolint:errcheck // the test checks the calls
	if _, err := c.Initialize(sdk.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	var begin sdk.AuthBeginResult
	if err := c.Call(sdk.MethodAuthBegin, nil, &begin); err != nil || begin.Step.Kind != sdk.StepQR {
		t.Fatalf("auth/begin %+v: %v", begin, err)
	}
	if err := c.Call(sdk.MethodAuthCancel, sdk.AuthFlowParams{FlowID: begin.FlowID}, nil); err != nil {
		t.Fatal(err)
	}
	done, err := c.WaitNote(sdk.MethodAuthDone)
	if err != nil || !strings.Contains(string(done.Params), `"ok":false`) {
		t.Fatalf("auth/done %s: %v", done.Params, err)
	}
	if err := c.Call(sdk.MethodAuthBegin, sdk.AuthBeginParams{Kind: "url"}, nil); sdk.CodeOf(err) != sdk.CodeInvalid {
		t.Fatalf("an undeclared kind: %v", err)
	}
	if err := c.Call(sdk.MethodAuthBegin, sdk.AuthBeginParams{Kind: "input"}, &begin); err != nil || begin.Step.Kind != sdk.StepInput {
		t.Fatalf("input begin %+v: %v", begin, err)
	}
	if err := c.Call(sdk.MethodAuthSubmit, sdk.AuthSubmitParams{FlowID: begin.FlowID, Fields: map[string]string{"code": "1"}}, nil); err != nil {
		t.Fatal(err)
	}
	for {
		done, err = c.WaitNote(sdk.MethodAuthDone)
		if err != nil {
			t.Fatal(err)
		}
		var p sdk.AuthDoneParams
		if err := json.Unmarshal(done.Params, &p); err != nil {
			t.Fatal(err)
		}
		if p.FlowID == begin.FlowID {
			if !p.OK || p.Account == nil {
				t.Fatalf("input login %+v", p)
			}
			return
		}
	}
}

// watcher syncs with no submit and drops each state but the first.
type watcher struct{ bare }

func (watcher) Options(context.Context, sdk.OptionQuery) (sdk.OptionPage, error) {
	return sdk.OptionPage{}, nil
}

func (watcher) Sync(_ context.Context, p sdk.SyncParams) (sdk.SyncResult, error) {
	var res sdk.SyncResult
	for _, s := range p.Subscriptions[:min(1, len(p.Subscriptions))] {
		res.Subscriptions = append(res.Subscriptions, sdk.SubscriptionState{ID: s.ID, Mode: sdk.ModePoll, State: sdk.SubscriptionPolling})
	}
	return res, nil
}

func TestSubscriptionGuards(t *testing.T) {
	t.Parallel()
	c := sdktest.Start(watcher{})
	defer c.Close() //nolint:errcheck // the test checks the calls
	init, err := c.Initialize(sdk.InitializeParams{Protocol: sdk.Protocol})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(init.Features, []string{sdk.FeatureSubscriptions}) {
		t.Fatalf("features %v", init.Features)
	}
	var page sdk.OptionPage
	if err := c.Call(sdk.MethodSubscriptionsOptions, sdk.OptionQuery{Field: "x"}, &page); err != nil || page.Options == nil {
		t.Fatalf("options: %v %v", page, err)
	}
	one := sdk.SyncParams{Subscriptions: []sdk.Subscription{{ID: "a"}}}
	if err := c.Call(sdk.MethodSubscriptionsSync, one, nil); err != nil {
		t.Fatalf("sync one: %v", err)
	}
	two := sdk.SyncParams{Subscriptions: []sdk.Subscription{{ID: "a"}, {ID: "b"}}}
	if err := c.Call(sdk.MethodSubscriptionsSync, two, nil); sdk.CodeOf(err) != sdk.CodePermanent {
		t.Fatalf("a short sync result: %v", err)
	}
	if err := c.Call(sdk.MethodSubscriptionsSubmit, sdk.SubmitParams{ID: "a"}, nil); sdk.CodeOf(err) != sdk.CodeUnsupported {
		t.Fatalf("submit with no submitter: %v", err)
	}
	if err := c.Call(sdk.MethodWebhookReceive, sdk.WebhookRequest{ID: "w"}, nil); sdk.CodeOf(err) != sdk.CodeUnsupported {
		t.Fatalf("webhook with no receiver: %v", err)
	}
}

func TestVerifyHMAC(t *testing.T) {
	t.Parallel()
	secret := []byte("s3cret")
	body := `{"a":1}`
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(body))
	sum := mac.Sum(nil)
	cases := []struct {
		name, header, prefix string
		enc                  sdk.Encoding
		want                 bool
	}{
		{"hex with prefix", "sha256=" + hex.EncodeToString(sum), "sha256=", sdk.EncodingHex, true},
		{"upper hex", strings.ToUpper(hex.EncodeToString(sum)), "", sdk.EncodingHex, true},
		{"base64", base64.StdEncoding.EncodeToString(sum), "", sdk.EncodingBase64, true},
		{"wrong prefix", "sha1=" + hex.EncodeToString(sum), "sha256=", sdk.EncodingHex, false},
		{"wrong sum", "sha256=00", "sha256=", sdk.EncodingHex, false},
		{"empty", "", "", sdk.EncodingHex, false},
	}
	for _, tc := range cases {
		r := sdk.WebhookRequest{Body: body, Headers: map[string]string{"x-sig": tc.header}}
		if got := r.VerifyHMAC(secret, "X-Sig", tc.prefix, sha256.New, tc.enc); got != tc.want {
			t.Errorf("%s: got %v", tc.name, got)
		}
	}
}

func TestSandboxSubscriptions(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var got []sdk.WebhookRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // a short read fails the signature check
		mu.Lock()
		got = append(got, sdk.WebhookRequest{
			ID: strconv.Itoa(len(got)), Method: r.Method, Body: string(body),
			Headers: map[string]string{sdk.SandboxSignature: r.Header.Get(sdk.SandboxSignature)},
		})
		mu.Unlock()
	}))
	defer srv.Close()
	cfg := sandboxConfig()
	cfg.Login, cfg.Subscriptions = nil, true
	c := sdktest.Start(sdk.NewSandbox(cfg))
	defer c.Close() //nolint:errcheck // the test checks the calls
	res, err := c.Initialize(sdk.InitializeParams{Webhook: &sdk.Webhook{URL: srv.URL, Secret: "s3cret"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(res.Features, sdk.FeatureSubscriptions) || !slices.Contains(res.Features, sdk.FeatureWebhooks) {
		t.Fatalf("features %v", res.Features)
	}
	var opts sdk.OptionPage
	if err := c.Call(sdk.MethodSubscriptionsOptions, sdk.OptionQuery{Field: "target"}, &opts); err != nil || len(opts.Options) != 3 {
		t.Fatalf("options %+v: %v", opts, err)
	}
	subs := sdk.SyncParams{Subscriptions: []sdk.Subscription{
		{ID: "a", Values: map[string][]string{"target": {sdk.SandboxAlpha}}},
		{ID: "m", Values: map[string][]string{"target": {sdk.SandboxManual}}},
	}}
	var sync sdk.SyncResult
	if err := c.Call(sdk.MethodSubscriptionsSync, subs, &sync); err != nil {
		t.Fatal(err)
	}
	if a, m := sync.Subscriptions[0], sync.Subscriptions[1]; a.State != sdk.SubscriptionActive || a.Mode != sdk.ModeAPI ||
		m.State != sdk.SubscriptionActionRequired || m.Action == nil || m.Action.Show[0].Value != srv.URL {
		t.Fatalf("sync %+v", sync)
	}
	var st sdk.SubscriptionState
	if err := c.Call(sdk.MethodSubscriptionsSubmit, sdk.SubmitParams{ID: "m", Fields: map[string]string{"code": "1"}}, &st); err != nil || st.State != sdk.SubscriptionActive {
		t.Fatalf("submit %+v: %v", st, err)
	}
	if err := c.Call(sdk.MethodSubscriptionsSync, subs, &sync); err != nil || sync.Subscriptions[1].State != sdk.SubscriptionActive {
		t.Fatalf("sync after submit %+v: %v", sync, err)
	}
	mu.Lock()
	reqs := slices.Clone(got)
	mu.Unlock()
	if len(reqs) != 2 {
		t.Fatalf("deliveries %d, want one for alpha and one after submit", len(reqs))
	}
	for _, r := range reqs {
		if err := c.Call(sdk.MethodWebhookReceive, r, nil); err != nil {
			t.Fatalf("receive: %v", err)
		}
	}
	if ev, err := c.WaitNote(sdk.MethodEvent); err != nil || !strings.Contains(string(ev.Params), sdk.SandboxAlpha) {
		t.Fatalf("event %s: %v", ev.Params, err)
	}
	forged := reqs[0]
	forged.Body = strings.Replace(forged.Body, "hello", "hellx", 1)
	if err := c.Call(sdk.MethodWebhookReceive, forged, nil); sdk.CodeOf(err) != sdk.CodeInvalid {
		t.Fatalf("forged: %v", err)
	}
	if err := c.Call(sdk.MethodSubscriptionsSync, sdk.SyncParams{Subscriptions: []sdk.Subscription{}}, &sync); err != nil || len(sync.Subscriptions) != 0 {
		t.Fatalf("empty sync %+v: %v", sync, err)
	}
}

func TestSandboxSubscriptionsPoll(t *testing.T) {
	t.Parallel()
	cfg := sandboxConfig()
	cfg.Login, cfg.Subscriptions = nil, true
	c := sdktest.Start(sdk.NewSandbox(cfg))
	defer c.Close() //nolint:errcheck // the test checks the calls
	if _, err := c.Initialize(sdk.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	var sync sdk.SyncResult
	subs := sdk.SyncParams{Subscriptions: []sdk.Subscription{{ID: "m", Values: map[string][]string{"target": {sdk.SandboxManual}}}}}
	if err := c.Call(sdk.MethodSubscriptionsSync, subs, &sync); err != nil || sync.Subscriptions[0].State != sdk.SubscriptionPolling {
		t.Fatalf("sync %+v: %v", sync, err)
	}
}
