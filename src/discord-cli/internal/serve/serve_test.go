package serve

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/trebi-ai/trebi-connectors/sdk"
	"github.com/trebi-ai/trebi-connectors/sdk/sdktest"
	"github.com/trebi-ai/trebi-connectors/src/discord-cli/internal/client"
)

// fakeDiscord is a REST API and a gateway. Routes answer "METHOD /path";
// the gateway sends READY and then the dispatches.
type fakeDiscord struct {
	t        *testing.T
	rest     *httptest.Server
	gw       *httptest.Server
	routes   map[string]func(body []byte) (int, any)
	dispatch []map[string]any

	mu   sync.Mutex
	seen []string // "METHOD /path?query body"
}

func newFake(t *testing.T) *fakeDiscord {
	f := &fakeDiscord{t: t, routes: map[string]func([]byte) (int, any){
		"GET /users/@me": func([]byte) (int, any) { return 200, map[string]any{"id": "9", "username": "trebi-bot", "bot": true} },
	}}
	f.rest = httptest.NewServer(http.HandlerFunc(f.serveREST))
	f.gw = httptest.NewServer(http.HandlerFunc(f.serveGateway))
	t.Cleanup(f.rest.Close)
	t.Cleanup(f.gw.Close)
	return f
}

func (f *fakeDiscord) serveREST(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body) //nolint:errcheck // test server
	key := r.Method + " " + r.URL.Path
	f.mu.Lock()
	f.seen = append(f.seen, strings.TrimSpace(key+"?"+r.URL.RawQuery+" "+string(body)))
	route, ok := f.routes[key]
	f.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	code, v := route(body)
	if code == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "2.5")
	}
	w.WriteHeader(code)
	if v != nil {
		json.NewEncoder(w).Encode(v) //nolint:errcheck // test server
	}
}

func (f *fakeDiscord) serveGateway(w http.ResponseWriter, r *http.Request) {
	up := websocket.Upgrader{}
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.WriteJSON(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 60000}}) //nolint:errcheck // test server
	var identify map[string]any
	if conn.ReadJSON(&identify) != nil {
		return
	}
	conn.WriteJSON(map[string]any{"op": 0, "t": "READY", "s": 1, "d": map[string]any{ //nolint:errcheck // test server
		"v": 10, "session_id": "s1", "user": map[string]any{"id": "9", "username": "trebi-bot"},
	}})
	for i, d := range f.dispatch {
		conn.WriteJSON(map[string]any{"op": 0, "s": i + 2, "t": d["t"], "d": d["d"]}) //nolint:errcheck // test server
	}
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (f *fakeDiscord) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

func (f *fakeDiscord) adapter(t *testing.T) *Adapter {
	cl := client.New("token")
	cl.BaseURL = f.rest.URL
	a, err := New(cl, "1.2.3", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.dialURL = "ws" + strings.TrimPrefix(f.gw.URL, "http")
	return a
}

func start(t *testing.T, a sdk.Adapter) (*sdktest.Conn, sdk.InitializeResult) {
	t.Helper()
	c := sdktest.Start(a, sdk.WithStateDir(t.TempDir()))
	t.Cleanup(func() { c.Close() }) //nolint:errcheck // the test checks its own errors
	res, err := c.Initialize(sdk.InitializeParams{})
	if err != nil {
		t.Fatal(err)
	}
	return c, res
}

func guild() map[string]any {
	return map[string]any{"t": "GUILD_CREATE", "d": map[string]any{
		"id": "1", "name": "Acme",
		"channels": []any{map[string]any{"id": "100", "type": 0, "name": "support"}},
		"threads":  []any{map[string]any{"id": "200", "type": 11, "name": "Refund", "parent_id": "100"}},
	}}
}

func TestEvents(t *testing.T) {
	f := newFake(t)
	f.dispatch = []map[string]any{
		guild(),
		{"t": "MESSAGE_CREATE", "d": map[string]any{
			"id": "1290114512345678901", "channel_id": "200", "guild_id": "1", "type": 19, "content": "Any news?",
			"timestamp":         "2026-09-28T08:15:02.000000+00:00",
			"author":            map[string]any{"id": "318", "username": "sam"},
			"message_reference": map[string]any{"message_id": "1290114500000000009"},
			"attachments":       []any{map[string]any{"id": "a1", "filename": "a.png", "size": 3, "url": "https://cdn/a.png", "content_type": "image/png"}},
		}},
		{"t": "MESSAGE_REACTION_ADD", "d": map[string]any{
			"user_id": "318", "channel_id": "100", "message_id": "555", "guild_id": "1",
			"emoji": map[string]any{"name": "👍"}, "member": map[string]any{"user": map[string]any{"id": "318", "username": "sam"}},
		}},
	}
	c, res := start(t, f.adapter(t))
	if res.Account == nil || res.Account.ID != "9" || len(res.Features) != 12 || res.Limits.MaxText != 2000 {
		t.Fatalf("initialize: %+v", res)
	}
	st, err := c.WaitNote(sdk.MethodStatus)
	if err != nil || !strings.Contains(string(st.Params), `"connected"`) {
		t.Fatalf("status %s %v", st.Params, err)
	}
	var msg, react sdk.Event
	for _, ev := range []*sdk.Event{&msg, &react} {
		n, err := c.WaitNote(sdk.MethodEvent)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(n.Params, ev); err != nil {
			t.Fatal(err)
		}
	}
	if msg.ID != "1290114512345678901" || msg.Room.ID != "100" || msg.Room.Name != "support" || msg.Room.Kind != sdk.RoomChannel ||
		msg.Thread == nil || msg.Thread.ID != "200" || msg.Thread.Title != "Refund" || msg.Sender.Name != "sam" ||
		msg.ReplyTo != "1290114500000000009" || msg.TS != "2026-09-28T08:15:02.000000Z" || len(msg.Attachments) != 1 {
		t.Fatalf("message event: %+v", msg)
	}
	if react.ID != "555.318.👍" || react.Type != "reaction" || string(react.Data) != `{"emoji":"👍","message_id":"555"}` {
		t.Fatalf("reaction event: %+v %s", react, react.Data)
	}
}

func TestRequests(t *testing.T) {
	f := newFake(t)
	f.dispatch = []map[string]any{guild()}
	f.routes["POST /channels/200/messages"] = func([]byte) (int, any) { return 200, map[string]any{"id": "700", "channel_id": "200"} }
	f.routes["PUT /channels/200/messages/700/reactions/👀/@me"] = func([]byte) (int, any) { return 204, nil }
	f.routes["POST /channels/100/typing"] = func([]byte) (int, any) { return 204, nil }
	f.routes["GET /channels/100/messages"] = func([]byte) (int, any) {
		return 200, []any{
			map[string]any{"id": "12", "channel_id": "100", "content": "new", "timestamp": "2026-09-28T08:00:02Z"},
			map[string]any{"id": "11", "channel_id": "100", "content": "old", "timestamp": "2026-09-28T08:00:01Z"},
		}
	}
	f.routes["POST /channels/100/threads"] = func([]byte) (int, any) {
		return 201, map[string]any{"id": "201", "type": 11, "name": "Ops", "parent_id": "100"}
	}
	f.routes["POST /channels/300/messages"] = func([]byte) (int, any) { return 429, map[string]any{"message": "slow"} }
	c, _ := start(t, f.adapter(t))
	if _, err := c.WaitNote(sdk.MethodStatus); err != nil {
		t.Fatal(err)
	}

	var sent sdk.SendResult
	if err := c.Call(sdk.MethodMessagesSend, sdk.SendParams{Room: "100", Thread: "200", Text: "Hi", Format: "markdown", ReplyTo: "650", Key: "k1"}, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.MessageID != "700" || sent.Thread != "200" {
		t.Fatalf("send: %+v", sent)
	}
	if err := c.Call(sdk.MethodMessagesSeen, sdk.SeenParams{Room: "100", MessageID: "700"}, nil); err != nil {
		t.Fatalf("seen must go to the thread of the message: %v", err)
	}
	if err := c.Call(sdk.MethodTyping, sdk.TypingParams{Room: "100"}, nil); err != nil {
		t.Fatal(err)
	}
	var page sdk.EventPage
	if err := c.Call(sdk.MethodMessagesHistory, sdk.HistoryQuery{Room: "100", Limit: 2}, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 || page.Events[0].Text != "old" || page.Next != "11" {
		t.Fatalf("history: %+v", page)
	}
	var th sdk.ThreadResult
	if err := c.Call(sdk.MethodThreadsCreate, sdk.CreateThreadParams{Room: "100", Title: "Ops"}, &th); err != nil || th.Thread.ID != "201" {
		t.Fatalf("thread: %+v %v", th, err)
	}
	err := c.Call(sdk.MethodMessagesSend, sdk.SendParams{Room: "300", Text: "x", Format: "markdown", Key: "k2"}, nil)
	if e, ok := err.(*sdk.Error); !ok || e.Code != sdk.CodeRateLimited || e.RetryAfter != 2500*time.Millisecond {
		t.Fatalf("429: %#v", err)
	}
	var reply string
	for _, r := range f.requests() {
		if strings.HasPrefix(r, "POST /channels/200/messages") {
			reply = r
		}
	}
	if !strings.Contains(reply, `"message_reference":{"fail_if_not_exists":false,"message_id":"650"}`) {
		t.Fatalf("send body: %s", reply)
	}
}

func TestAttachmentOut(t *testing.T) {
	f := newFake(t)
	var got []byte
	f.routes["POST /channels/100/messages"] = func(body []byte) (int, any) {
		got = body
		return 200, map[string]any{"id": "701"}
	}
	path := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(path, []byte("file body"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, _ := start(t, f.adapter(t))
	if err := c.Call(sdk.MethodMessagesSend, sdk.SendParams{Room: "100", Text: "see file", Format: "markdown", Key: "k", Attachments: []sdk.Attachment{{Path: path}}}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `filename="note.txt"`) || !strings.Contains(string(got), "file body") || !strings.Contains(string(got), `"content":"see file"`) {
		t.Fatalf("multipart body: %s", got)
	}
}

func TestReplay(t *testing.T) {
	f := newFake(t)
	f.routes["GET /users/@me/guilds"] = func([]byte) (int, any) { return 200, []any{map[string]any{"id": "1", "name": "Acme"}} }
	f.routes["GET /guilds/1/channels"] = func([]byte) (int, any) {
		return 200, []any{
			map[string]any{"id": "100", "type": 0, "name": "support", "guild_id": "1", "last_message_id": "300"},
			map[string]any{"id": "101", "type": 0, "name": "quiet", "guild_id": "1", "last_message_id": "100"},
		}
	}
	f.routes["GET /guilds/1/threads/active"] = func([]byte) (int, any) { return 200, map[string]any{"threads": []any{}} }
	f.routes["GET /channels/100/messages"] = func([]byte) (int, any) {
		return 200, []any{
			map[string]any{"id": "300", "channel_id": "100", "content": "b", "timestamp": "2026-09-28T08:00:02Z"},
			map[string]any{"id": "200", "channel_id": "100", "content": "a", "timestamp": "2026-09-28T08:00:01Z"},
		}
	}
	a := f.adapter(t)
	evs, complete, err := a.Replay(t.Context(), "150", 10)
	if err != nil || !complete || len(evs) != 2 || evs[0].ID != "200" || evs[1].ID != "300" {
		t.Fatalf("replay: %+v %v %v", evs, complete, err)
	}
	for _, r := range f.requests() {
		if strings.HasPrefix(r, "GET /channels/101/messages") {
			t.Fatal("replay must skip a channel with no newer message")
		}
	}
	if _, complete, _ := a.Replay(t.Context(), "not-a-snowflake", 10); complete { //nolint:errcheck // only complete matters
		t.Fatal("a bad cursor is not complete")
	}
}

func TestRevokedToken(t *testing.T) {
	f := newFake(t)
	f.routes["GET /users/@me"] = func([]byte) (int, any) { return 401, map[string]any{"message": "401: Unauthorized"} }
	c, res := start(t, f.adapter(t))
	if res.Account != nil {
		t.Fatal("no account with a bad token")
	}
	st, err := c.WaitNote(sdk.MethodStatus)
	if err != nil || !strings.Contains(string(st.Params), `"auth_required","reason":"revoked"`) {
		t.Fatalf("status %s %v", st.Params, err)
	}
}

func TestSandboxDeclaresTheSameFeatures(t *testing.T) {
	_, res := start(t, sdk.NewSandbox(sdk.SandboxConfig{Adapter: sdk.AdapterInfo{Name: Name}, Events: Events, Features: Features, Limits: Limits}))
	if strings.Join(res.Features, ",") != strings.Join(Features, ",") || len(res.Login) != 0 {
		t.Fatalf("sandbox: %+v", res)
	}
}
