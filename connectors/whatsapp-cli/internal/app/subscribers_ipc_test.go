package app

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/ipc"
)

// shortSockDir returns a temp dir with a short path so the Unix socket path
// stays under the OS limit.
func shortSockDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "whatsapp-cli-app-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// TestAnchorSecondaryOverSocket exercises the full anchor→secondary path: a real
// ipc.Server fronting a SubscriberSet (the anchor wiring), a real dialed
// connection issuing a subscribe request (the secondary), and events pushed
// through dispatch. It asserts the secondary receives only its filtered events.
func TestAnchorSecondaryOverSocket(t *testing.T) {
	dir := shortSockDir(t)
	set := NewSubscriberSet()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := func(sctx context.Context, req ipc.Request, w io.Writer) error {
		f := ListenFilter{Categories: map[string]bool{}}
		for _, c := range req.Filter.Categories {
			f.Categories[c] = true
		}
		f.ChatFilter = req.Filter.ChatFilter
		bw := bufio.NewWriter(w)
		return set.Serve(sctx, f, bw, bw.Flush)
	}

	srv, err := ipc.Serve(ctx, dir,
		func(context.Context, ipc.Request) ipc.Response { return ipc.Response{OK: false} },
		stream,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	conn, err := ipc.Dial(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Subscribe to chatA / messages only.
	req := ipc.Request{Cmd: "subscribe", Filter: &ipc.Filter{
		Categories: []string{"messages"}, ChatFilter: "chatA",
	}}
	if err := ipc.WriteRequest(conn, req); err != nil {
		t.Fatal(err)
	}

	// Wait for the anchor to register the subscriber.
	deadline := time.Now().Add(2 * time.Second)
	for set.Count() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("subscriber not registered")
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Push three events; only the two chatA messages should reach the secondary.
	set.dispatch(ceMessage("chatA", "u1", false))
	set.dispatch(ceMessage("chatB", "u2", false)) // filtered out
	set.dispatch(ceMessage("chatA", "u3", false))

	r := bufio.NewReader(conn)
	for i := 0; i < 2; i++ {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := r.ReadBytes('\n')
		if err != nil {
			t.Fatalf("read line %d: %v", i, err)
		}
		var got map[string]any
		if err := json.Unmarshal(line, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got["t"] != "message" {
			t.Fatalf("t = %v", got["t"])
		}
		if !strings.Contains(string(line), "chatA") {
			t.Fatalf("leaked non-chatA event: %s", line)
		}
	}
}

// TestSecondaryUnblocksOnAnchorClose verifies that closing the anchor server
// ends the secondary's stream (EOF), which is how attached listeners learn to
// exit.
func TestSecondaryUnblocksOnAnchorClose(t *testing.T) {
	dir := shortSockDir(t)
	set := NewSubscriberSet()
	ctx := context.Background()

	stream := func(sctx context.Context, _ ipc.Request, w io.Writer) error {
		bw := bufio.NewWriter(w)
		return set.Serve(sctx, ListenFilter{Categories: map[string]bool{"messages": true}}, bw, bw.Flush)
	}
	srv, err := ipc.Serve(ctx, dir,
		func(context.Context, ipc.Request) ipc.Response { return ipc.Response{OK: false} },
		stream,
	)
	if err != nil {
		t.Fatal(err)
	}

	conn, err := ipc.Dial(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := ipc.WriteRequest(conn, ipc.Request{Cmd: "subscribe", Filter: &ipc.Filter{Categories: []string{"messages"}}}); err != nil {
		t.Fatal(err)
	}
	for set.Count() == 0 {
		time.Sleep(time.Millisecond)
	}

	// Close the anchor; the secondary's read should hit EOF promptly.
	go srv.Close()

	r := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := r.ReadBytes('\n'); err == nil {
		t.Fatal("expected EOF/error after anchor close, got a line")
	}
}
