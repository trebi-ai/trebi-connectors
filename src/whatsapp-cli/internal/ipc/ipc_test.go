package ipc

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shortDir returns a temp dir with a short path so the Unix socket path stays
// under the OS limit (macOS t.TempDir() paths are long enough to overflow it).
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "whatsapp-cli-ipc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

func TestServeRoundTrip(t *testing.T) {
	dir := shortDir(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, err := Serve(ctx, dir, func(_ context.Context, req Request) Response {
		if req.Cmd != "send_text" {
			return Response{OK: false, Error: "unexpected cmd"}
		}
		return Response{OK: true, ID: "MSG123", To: req.To}
	}, nil)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	defer srv.Close()

	conn, err := Dial(dir)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	resp, err := Send(conn, Request{Cmd: "send_text", To: "123@s.whatsapp.net", Message: "hi"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !resp.OK || resp.ID != "MSG123" || resp.To != "123@s.whatsapp.net" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestSubscribeStream(t *testing.T) {
	dir := shortDir(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, err := Serve(ctx, dir,
		func(_ context.Context, _ Request) Response { return Response{OK: false, Error: "no send"} },
		func(sctx context.Context, req Request, w io.Writer) error {
			if req.Cmd != "subscribe" {
				return nil
			}
			// Emit two lines then wait for cancellation.
			_, _ = w.Write([]byte("line1\n"))
			_, _ = w.Write([]byte("line2\n"))
			<-sctx.Done()
			return sctx.Err()
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	conn, err := Dial(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if err := WriteRequest(conn, Request{Cmd: "subscribe", Filter: &Filter{Categories: []string{"messages"}}}); err != nil {
		t.Fatal(err)
	}

	r := bufio.NewReader(conn)
	for _, want := range []string{"line1", "line2"} {
		got, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read %q: %v", want, err)
		}
		if strings.TrimSpace(got) != want {
			t.Fatalf("got %q want %q", strings.TrimSpace(got), want)
		}
	}
}

func TestDialNoDaemon(t *testing.T) {
	dir := shortDir(t)
	if _, err := Dial(dir); err == nil {
		t.Fatal("expected Dial to fail when no daemon is listening")
	}
}

func TestServeRemovesStaleSocket(t *testing.T) {
	dir := shortDir(t)

	// Simulate a leftover socket file from a crashed daemon.
	stale := SocketPath(dir)
	if err := os.WriteFile(stale, []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, err := Serve(ctx, dir, func(_ context.Context, _ Request) Response {
		return Response{OK: true}
	}, nil)
	if err != nil {
		t.Fatalf("Serve should remove stale socket and bind: %v", err)
	}
	defer srv.Close()

	conn, err := Dial(dir)
	if err != nil {
		t.Fatalf("Dial after stale-socket cleanup: %v", err)
	}
	_ = conn.Close()
}

func TestCloseRemovesSocket(t *testing.T) {
	dir := shortDir(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, err := Serve(ctx, dir, func(_ context.Context, _ Request) Response {
		return Response{OK: true}
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = srv.Close()

	if _, err := os.Stat(filepath.Join(dir, SocketName)); !os.IsNotExist(err) {
		t.Fatalf("socket file should be removed after Close, stat err = %v", err)
	}
}
