package app

import (
	"bytes"
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/wa/fakewa"
	"github.com/trebi-ai/trebi-connectors/sdk"
	"github.com/trebi-ai/trebi-connectors/sdk/sdktest"
)

// sandbox runs the adapter over fakewa.Sandbox on dir, as `serve
// --sandbox` does.
func sandbox(t *testing.T, dir string) (*sdktest.Conn, *App) {
	t.Helper()
	f, err := fakewa.Sandbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(Options{StoreDir: dir, Version: "test", WA: f})
	if err != nil {
		t.Fatal(err)
	}
	c := sdktest.Start(NewAdapter(a), sdk.WithStateDir(dir))
	if _, err := c.Initialize(sdk.InitializeParams{Instance: sdk.InstanceInfo{Key: "k", Name: "wa"}}); err != nil {
		t.Fatal(err)
	}
	return c, a
}

func stop(t *testing.T, c *sdktest.Conn, a *App) {
	t.Helper()
	if err := c.Call(sdk.MethodShutdown, sdk.Empty{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Served(); err != nil {
		t.Fatal(err)
	}
	a.Close()
}

func selfEvent(t *testing.T, c *sdktest.Conn, id string) sdk.Event {
	t.Helper()
	for {
		ev := waitEvent(t, c)
		if ev.ID == id {
			if ev.Sender == nil || !ev.Sender.Self {
				t.Fatalf("event %s is not self: %+v", id, ev)
			}
			return ev
		}
	}
}

func TestSandbox(t *testing.T) {
	dir := t.TempDir()
	c, a := sandbox(t, dir)
	if st := waitStatus(t, c, sdk.StateAuthRequired); st.State != sdk.StateAuthRequired {
		t.Fatal(st)
	}

	var begin sdk.AuthBeginResult
	if err := c.Call(sdk.MethodAuthBegin, sdk.AuthBeginParams{}, &begin); err != nil || begin.Step.Kind != sdk.StepQR {
		t.Fatalf("begin: %+v %v", begin, err)
	}
	if err := c.Call(sdk.MethodAuthCancel, sdk.AuthFlowParams{FlowID: begin.FlowID}, nil); err != nil {
		t.Fatal(err)
	}
	if done, _ := c.WaitNote(sdk.MethodAuthDone); !strings.Contains(string(done.Params), `"ok":false`) {
		t.Fatalf("cancel: %s", done.Params)
	}
	if err := c.Call(sdk.MethodAuthBegin, sdk.AuthBeginParams{}, &begin); err != nil {
		t.Fatal(err)
	}
	if done, _ := c.WaitNote(sdk.MethodAuthDone); !strings.Contains(string(done.Params), `"ok":true`) {
		t.Fatalf("login: %s", done.Params)
	}
	waitStatus(t, c, sdk.StateConnected)

	var rooms sdk.RoomPage
	for deadline := time.Now().Add(5 * time.Second); len(rooms.Rooms) < 2; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("rooms: %+v", rooms)
		}
		if err := c.Call(sdk.MethodRoomsList, sdk.RoomQuery{Limit: 10}, &rooms); err != nil {
			t.Fatal(err)
		}
	}
	room := fakewa.Ana.String()

	var sent sdk.SendResult
	if err := c.Call(sdk.MethodMessagesSend, sdk.SendParams{Room: room, Text: "hi", Key: "k1"}, &sent); err != nil {
		t.Fatal(err)
	}
	selfEvent(t, c, sent.MessageID)

	file := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(file, []byte("a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	var fileSent sdk.SendResult
	if err := c.Call(sdk.MethodMessagesSend, sdk.SendParams{Room: room, Key: "k2", Attachments: []sdk.Attachment{{Path: file, Name: "note.txt", Mime: "text/plain"}}}, &fileSent); err != nil {
		t.Fatal(err)
	}
	ev := selfEvent(t, c, fileSent.MessageID)
	if len(ev.Attachments) != 1 || !strings.HasPrefix(ev.Attachments[0].Path, dir+string(filepath.Separator)) {
		t.Fatalf("attachment path: %+v", ev.Attachments)
	}
	if b, err := os.ReadFile(ev.Attachments[0].Path); err != nil || string(b) != "a file" {
		t.Fatalf("attachment: %q %v", b, err)
	}

	for _, call := range []struct {
		method string
		params any
	}{
		{sdk.MethodTyping, sdk.TypingParams{Room: room}},
		{sdk.MethodMessagesSeen, sdk.SeenParams{Room: room, MessageID: sent.MessageID}},
		{sdk.MethodReactionsAdd, sdk.ReactionParams{Room: room, MessageID: sent.MessageID, Emoji: "👍"}},
	} {
		if err := c.Call(call.method, call.params, nil); err != nil {
			t.Fatalf("%s: %v", call.method, err)
		}
	}
	var opened sdk.RoomResult
	if err := c.Call(sdk.MethodRoomsOpen, sdk.RoomOpenParams{User: fakewa.Bruno.User}, &opened); err != nil || opened.Room.Name != "Bruno Lima" {
		t.Fatalf("open: %+v %v", opened, err)
	}
	var rep sdk.ReplayResult
	if err := c.Call(sdk.MethodEventsReplay, sdk.ReplayParams{After: sent.MessageID, Limit: 10}, &rep); err != nil || !rep.Complete || len(rep.Events) != 1 || rep.Events[0].ID != fileSent.MessageID {
		t.Fatalf("replay: %+v %v", rep, err)
	}
	if !strings.HasPrefix(rep.Events[0].Attachments[0].Path, dir) {
		t.Fatalf("replay attachment path: %+v", rep.Events[0].Attachments)
	}
	if err := c.Call(sdk.MethodRoomsGet, sdk.RoomParams{Room: "conformance-unknown-room"}, nil); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("unknown room: %v", err)
	}
	stop(t, c, a)

	// The state has no absolute path and no WAL left.
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error { //nolint:errcheck // checks in the walk
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := os.ReadFile(p) //nolint:errcheck // a file of the walk
		if bytes.Contains(b, []byte(dir)) {
			t.Errorf("%s holds the state path", p)
		}
		if strings.HasSuffix(p, "-wal") && len(b) > 0 {
			t.Errorf("%s has %d bytes after shutdown", p, len(b))
		}
		return nil
	})

	// A restore under another path stays logged in and keeps the dedupe.
	moved := filepath.Join(t.TempDir(), "restored")
	if err := os.CopyFS(moved, os.DirFS(dir)); err != nil {
		t.Fatal(err)
	}
	c, a = sandbox(t, moved)
	waitStatus(t, c, sdk.StateConnected)
	var again sdk.SendResult
	if err := c.Call(sdk.MethodMessagesSend, sdk.SendParams{Room: room, Text: "hi", Key: "k1"}, &again); err != nil || again.MessageID != sent.MessageID {
		t.Fatalf("dedupe after restore: %+v %v", again, err)
	}
	var next sdk.SendResult
	if err := c.Call(sdk.MethodMessagesSend, sdk.SendParams{Room: room, Text: "new", Key: "k3"}, &next); err != nil || next.MessageID == sent.MessageID || next.MessageID == fileSent.MessageID {
		t.Fatalf("new send after restore: %+v %v", next, err)
	}
	var page sdk.EventPage
	if err := c.Call(sdk.MethodMessagesHistory, sdk.HistoryQuery{Room: room, Limit: 10}, &page); err != nil {
		t.Fatal(err)
	}
	for _, e := range page.Events {
		for _, at := range e.Attachments {
			if at.Path != "" && !strings.HasPrefix(at.Path, moved) {
				t.Fatalf("history path after restore: %s", at.Path)
			}
		}
	}
	stop(t, c, a)
}

func TestNewerSchema(t *testing.T) {
	dir := t.TempDir()
	a, err := New(Options{StoreDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	db, err := sql.Open("sqlite3", filepath.Join(dir, "whatsapp-cli.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 999`); err != nil {
		t.Fatal(err)
	}
	db.Close() //nolint:errcheck // test
	if _, err := New(Options{StoreDir: dir}); err == nil || !strings.Contains(err.Error(), "schema version 999") {
		t.Fatalf("newer schema: %v", err)
	}
}

func TestRemoveDownloadTemps(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, "media", "chat", "msg", ".whatsapp-cli-download-123")
	if err := os.MkdirAll(filepath.Dir(tmp), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmp, []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := New(Options{StoreDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("temp file is still there: %v", err)
	}
}
