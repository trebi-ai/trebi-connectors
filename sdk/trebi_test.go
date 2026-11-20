package sdk_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/sdk"
	"github.com/trebi-ai/trebi-connectors/sdk/sdktest"
)

func TestFromEnv(t *testing.T) {
	t.Setenv(sdk.EnvStateDir, "")
	t.Setenv(sdk.EnvCacheDir, "/c")
	if _, ok := sdk.FromEnv(); ok {
		t.Fatal("no state dir must be outside Trebi")
	}
	t.Setenv(sdk.EnvStateDir, "/s")
	if tr, ok := sdk.FromEnv(); !ok || tr.StateDir != "/s" || tr.CacheDir != "/c" {
		t.Fatalf("got %+v %v", tr, ok)
	}
}

// needsToken reports a missing input from Initialize.
type needsToken struct {
	runner
	pointer bool
}

func (a needsToken) Initialize(ctx context.Context, in sdk.InitializeParams) (sdk.InitializeResult, error) {
	res, _ := a.bare.Initialize(ctx, in) //nolint:errcheck // bare never fails
	if a.pointer {
		return res, &sdk.MissingInput{Name: "DISCORD_TOKEN", Label: "Bot token"}
	}
	return res, sdk.MissingInput{Name: "DISCORD_TOKEN", Label: "Bot token"}
}

func (needsToken) Send(context.Context, sdk.SendParams) (sdk.SendResult, error) {
	return sdk.SendResult{MessageID: "m"}, nil
}

func TestMissingInput(t *testing.T) {
	t.Parallel()
	for _, pointer := range []bool{false, true} {
		a := needsToken{runner: runner{started: make(chan struct{})}, pointer: pointer}
		c := sdktest.Start(a)
		res, err := c.Initialize(sdk.InitializeParams{})
		if err != nil || res.Adapter.Name != "bare" {
			t.Fatalf("initialize %+v: %v", res, err)
		}
		note, err := c.WaitNote(sdk.MethodStatus)
		if err != nil {
			t.Fatal(err)
		}
		var st sdk.Status
		if err := json.Unmarshal(note.Params, &st); err != nil {
			t.Fatal(err)
		}
		if st.State != sdk.StateAuthRequired || st.Reason != sdk.ReasonMissingInput || !strings.Contains(st.Message, "DISCORD_TOKEN") || !strings.Contains(st.Message, "Bot token") {
			t.Fatalf("status %+v", st)
		}
		if err := c.Call(sdk.MethodPing, nil, nil); err != nil {
			t.Fatalf("ping: %v", err)
		}
		if err := c.Call(sdk.MethodMessagesSend, sdk.SendParams{Room: "r", Text: "x"}, nil); sdk.CodeOf(err) != sdk.CodeAuthRequired {
			t.Fatalf("send: %v", err)
		}
		var auth sdk.AuthStatusResult
		if err := c.Call(sdk.MethodAuthStatus, nil, &auth); err != nil || auth.State != sdk.StateAuthRequired {
			t.Fatalf("auth/status %+v: %v", auth, err)
		}
		select {
		case <-a.started:
			t.Fatal("Run must not start with a missing input")
		default:
		}
		if err := c.Call(sdk.MethodShutdown, nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := c.Served(); err != nil {
			t.Fatal(err)
		}
		c.Close() //nolint:errcheck // Serve already returned
	}
}

func startSandbox(t *testing.T, state, cache string) *sdktest.Conn {
	t.Helper()
	cfg := sandboxConfig()
	cfg.Events = []sdk.EventDecl{{Type: "message"}}
	cfg.Features = []string{"rooms.list", "rooms.open", "threads", "threads.create", "history", "replay", "typing", "seen", "reactions", "edit", "attachments.in", "attachments.out"}
	cfg.Limits.MaxText = 100
	c := sdktest.Start(sdk.NewSandbox(cfg), sdk.WithStateDir(state), sdk.WithCacheDir(cache))
	t.Cleanup(func() { c.Close() }) //nolint:errcheck // the test checks the calls
	if _, err := c.Initialize(sdk.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSandboxState(t *testing.T) {
	t.Parallel()
	state, cache := t.TempDir(), t.TempDir()
	c := startSandbox(t, state, cache)
	if st, err := c.WaitNote(sdk.MethodStatus); err != nil || !strings.Contains(string(st.Params), `"auth_required"`) {
		t.Fatalf("empty state: %s %v", st.Params, err)
	}
	if err := c.Call(sdk.MethodAuthBegin, sdk.AuthBeginParams{Method: "qr"}, nil); err != nil {
		t.Fatal(err)
	}
	if done, err := c.WaitNote(sdk.MethodAuthDone); err != nil || !strings.Contains(string(done.Params), `"ok":true`) {
		t.Fatalf("auth/done %s: %v", done.Params, err)
	}

	file := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	send := sdk.SendParams{Room: "sandbox-general", Text: "hi", Key: "k1", Attachments: []sdk.Attachment{{Path: file, Mime: "text/plain"}}}
	var sent sdk.SendResult
	if err := c.Call(sdk.MethodMessagesSend, send, &sent); err != nil {
		t.Fatal(err)
	}
	note, err := c.WaitNote(sdk.MethodEvent)
	if err != nil {
		t.Fatal(err)
	}
	var ev sdk.Event
	if err := json.Unmarshal(note.Params, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.ID != sent.MessageID || ev.Sender == nil || !ev.Sender.Self || ev.Room.Name != "general" || len(ev.Attachments) != 1 {
		t.Fatalf("self event %s", note.Params)
	}
	if p := ev.Attachments[0].Path; !strings.HasPrefix(p, state) {
		t.Fatalf("attachment path %q is not in the state folder", p)
	}

	for _, tc := range []struct {
		method string
		params any
		code   string
	}{
		{sdk.MethodMessagesSend, sdk.SendParams{Room: "nope", Text: "x"}, sdk.CodeNotFound},
		{sdk.MethodMessagesSend, map[string]any{"room": 5}, sdk.CodeInvalid},
		{sdk.MethodMessagesSend, sdk.SendParams{Room: "sandbox-general"}, sdk.CodeInvalid},
		{sdk.MethodTyping, sdk.TypingParams{Room: "nope"}, sdk.CodeNotFound},
		{sdk.MethodMessagesSeen, sdk.SeenParams{Room: "nope", MessageID: "m"}, sdk.CodeNotFound},
		{sdk.MethodReactionsAdd, sdk.ReactionParams{Room: "nope", MessageID: "m", Emoji: "+1"}, sdk.CodeNotFound},
		{sdk.MethodMessagesEdit, sdk.EditParams{Room: "sandbox-general", MessageID: "nope", Text: "x"}, sdk.CodeNotFound},
		{sdk.MethodThreadsCreate, sdk.CreateThreadParams{Room: "nope"}, sdk.CodeNotFound},
		{sdk.MethodThreadsList, sdk.ThreadQuery{Room: "nope"}, sdk.CodeNotFound},
		{sdk.MethodRoomsGet, sdk.RoomParams{Room: "nope"}, sdk.CodeNotFound},
		{sdk.MethodRoomsOpen, sdk.RoomOpenParams{}, sdk.CodeInvalid},
	} {
		if err := c.Call(tc.method, tc.params, nil); sdk.CodeOf(err) != tc.code {
			t.Errorf("%s %v: got %v, want %s", tc.method, tc.params, err, tc.code)
		}
	}

	var room sdk.RoomResult
	if err := c.Call(sdk.MethodRoomsOpen, sdk.RoomOpenParams{User: "bo"}, &room); err != nil {
		t.Fatal(err)
	}
	var th sdk.ThreadResult
	if err := c.Call(sdk.MethodThreadsCreate, sdk.CreateThreadParams{Room: room.Room.ID, Title: "t"}, &th); err != nil {
		t.Fatal(err)
	}
	for _, call := range []struct {
		method string
		params any
	}{
		{sdk.MethodMessagesEdit, sdk.EditParams{Room: "sandbox-general", MessageID: sent.MessageID, Text: "edited"}},
		{sdk.MethodMessagesSeen, sdk.SeenParams{Room: "sandbox-general", MessageID: sent.MessageID}},
		{sdk.MethodReactionsAdd, sdk.ReactionParams{Room: "sandbox-general", MessageID: sent.MessageID, Emoji: "+1"}},
		{sdk.MethodTyping, sdk.TypingParams{Room: room.Room.ID}},
		{sdk.MethodMessagesSend, sdk.SendParams{Room: room.Room.ID, Thread: th.Thread.ID, Text: "in thread", Key: "k2"}},
	} {
		if err := c.Call(call.method, call.params, nil); err != nil {
			t.Fatalf("%s: %v", call.method, err)
		}
	}
	var replay sdk.ReplayResult
	if err := c.Call(sdk.MethodEventsReplay, sdk.ReplayParams{After: sent.MessageID}, &replay); err != nil || !replay.Complete || len(replay.Events) != 1 || replay.Events[0].Text != "in thread" {
		t.Fatalf("replay %+v: %v", replay, err)
	}
	if err := c.Call(sdk.MethodEventsReplay, sdk.ReplayParams{After: "unknown"}, &replay); err != nil || replay.Complete {
		t.Fatalf("replay of an unknown id %+v: %v", replay, err)
	}
	if err := c.Call(sdk.MethodShutdown, nil, nil); err != nil {
		t.Fatal(err)
	}

	// Nothing in the state folder holds its absolute path.
	err = filepath.WalkDir(state, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err == nil && bytes.Contains(b, []byte(state)) {
			t.Errorf("%s holds the absolute state path", p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// A restart on a copy of the state stays logged in and keeps the dedupe.
	moved := filepath.Join(t.TempDir(), "restored")
	if err := os.CopyFS(moved, os.DirFS(state)); err != nil {
		t.Fatal(err)
	}
	c2 := startSandbox(t, moved, t.TempDir())
	if st, err := c2.WaitNote(sdk.MethodStatus); err != nil || !strings.Contains(string(st.Params), `"connected"`) {
		t.Fatalf("restart status %s: %v", st.Params, err)
	}
	var again sdk.SendResult
	if err := c2.Call(sdk.MethodMessagesSend, send, &again); err != nil || again != sent {
		t.Fatalf("send after restart %v: %v", again, err)
	}
	var fresh sdk.SendResult
	if err := c2.Call(sdk.MethodMessagesSend, sdk.SendParams{Room: room.Room.ID, Text: "new", Key: "k3"}, &fresh); err != nil || fresh.MessageID == sent.MessageID {
		t.Fatalf("new send after restart %v: %v", fresh, err)
	}
	var page sdk.EventPage
	if err := c2.Call(sdk.MethodMessagesHistory, sdk.HistoryQuery{Room: "sandbox-general", Limit: 10}, &page); err != nil || len(page.Events) != 1 || page.Events[0].Text != "edited" || !strings.HasPrefix(page.Events[0].Attachments[0].Path, moved) {
		t.Fatalf("history after restore %+v: %v", page, err)
	}
}

func TestNewLogHandlerTrebi(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	slog.New(sdk.NewLogHandler(&buf, true, nil)).Warn("x", "k", 1)
	if got := buf.String(); got != "level=WARN msg=x k=1\n" {
		t.Fatalf("got %q", got)
	}
}

func TestNewLogHandlerStandalone(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	h := sdk.NewLogHandler(&buf, false, nil)
	slog.New(h).Warn("x", "k", 1)
	re := regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} level=WARN msg=x k=1\n$`)
	if !re.MatchString(buf.String()) {
		t.Fatalf("got %q", buf.String())
	}

	buf.Reset()
	at := time.Date(2026, 11, 17, 14, 28, 54, 0, time.UTC)
	r := slog.NewRecord(at, slog.LevelWarn, "x", 0)
	r.AddAttrs(slog.Int("k", 1))
	if err := h.Handle(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if want := at.Local().Format(time.DateTime) + " level=WARN msg=x k=1\n"; buf.String() != want {
		t.Fatalf("got %q, want %q", buf.String(), want)
	}

	// Derived handlers keep the prefix, and each record is one line.
	buf.Reset()
	l := slog.New(h).With("a", 1).WithGroup("g")
	l.Info("y", "b", 2)
	l.Info("z")
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %q", buf.String())
	}
	for _, ln := range lines {
		if len(ln) < 20 || ln[19] != ' ' || !strings.Contains(ln, "level=INFO") || !strings.Contains(ln, "a=1") {
			t.Fatalf("bad line %q", ln)
		}
	}
	if !strings.HasSuffix(lines[0], "msg=y a=1 g.b=2") {
		t.Fatalf("group lost: %q", lines[0])
	}
}

func TestNewLogHandlerOptions(t *testing.T) {
	t.Parallel()
	for _, trebi := range []bool{true, false} {
		var buf bytes.Buffer
		var keys []string
		h := sdk.NewLogHandler(&buf, trebi, &slog.HandlerOptions{
			Level: slog.LevelWarn,
			ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
				keys = append(keys, a.Key)
				if a.Key == "k" {
					a.Value = slog.StringValue("replaced")
				}
				return a
			},
		})
		l := slog.New(h)
		l.Info("dropped")
		if buf.Len() != 0 {
			t.Fatalf("trebi=%v: Info passed the level: %q", trebi, buf.String())
		}
		l.Warn("x", "k", 1)
		if !strings.HasSuffix(buf.String(), "level=WARN msg=x k=replaced\n") {
			t.Fatalf("trebi=%v: got %q", trebi, buf.String())
		}
		for _, k := range keys {
			if k == slog.TimeKey {
				t.Fatalf("trebi=%v: ReplaceAttr saw the time key", trebi)
			}
		}
	}
}

func TestSetDefaultLoggerMode(t *testing.T) {
	old := slog.Default()
	t.Cleanup(func() { slog.SetDefault(old) })
	oldStderr := os.Stderr
	t.Cleanup(func() { os.Stderr = oldStderr })

	read := func() string {
		pr, pw, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		os.Stderr = pw
		sdk.SetDefaultLogger(nil)
		slog.Warn("x", "k", 1)
		pw.Close() //nolint:errcheck // test pipe
		os.Stderr = oldStderr
		b, err := io.ReadAll(pr)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	t.Setenv(sdk.EnvStateDir, t.TempDir())
	if got := read(); got != "level=WARN msg=x k=1\n" {
		t.Fatalf("Trebi mode: %q", got)
	}
	t.Setenv(sdk.EnvStateDir, "")
	if got := read(); !regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} level=WARN msg=x k=1\n$`).MatchString(got) {
		t.Fatalf("standalone: %q", got)
	}
}
