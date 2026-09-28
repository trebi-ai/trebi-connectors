package sdk_test

import (
	"context"
	"encoding/json"
	"errors"
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
				Features: []string{"rooms.list", "threads", "typing", "seen", "reactions", "edit"},
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
