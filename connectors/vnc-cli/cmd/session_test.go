package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/internal/session"
	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc"
	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc/vnctest"
)

// TestMain runs the test binary as vnc-cli when a test starts a holder.
func TestMain(m *testing.M) {
	if os.Getenv("VNCCLI_TEST_HOLDER") == "1" {
		err := NewRunner(os.Stdin, os.Stdout, os.Stderr).App("test").Run(context.Background(), os.Args)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		os.Exit(ExitCode(err))
	}
	os.Exit(m.Run())
}

// sessionHome is a standalone home with a short path, so that the socket
// path fits. It stops each holder at the end.
func sessionHome(t *testing.T) string {
	t.Helper()
	standalone(t)
	home, err := os.MkdirTemp("/tmp", "vh")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("VNCCLI_TEST_HOLDER", "1")
	t.Cleanup(func() {
		run(t, "", "--session=off", "session", "stop", "--all")
		os.RemoveAll(home)
	})
	return home
}

func status(t *testing.T, addr string) session.Status {
	t.Helper()
	var st session.Status
	res := mustRun(t, "", "-s", addr, "--session=auto", "--json", "session", "status")
	if err := json.Unmarshal([]byte(res.stdout), &st); err != nil {
		t.Fatalf("status %q: %v", res.stdout, err)
	}
	return st
}

func TestSessionAutoStartAndReuse(t *testing.T) {
	sessionHome(t)
	srv, addr := server(t, vnctest.Options{Security: []uint8{vnc.SecVNC}, Password: "pw"})
	t.Setenv("LAB_PW", "pw")
	for range 3 {
		res := mustRun(t, "", "-s", addr, "-p", "LAB_PW", "--session=auto", "info")
		if !strings.Contains(res.stdout, "64x48") {
			t.Fatalf("info: %q", res.stdout)
		}
	}
	mustRun(t, "", "-s", addr, "-p", "LAB_PW", "--session=auto", "type", "ab")
	if n := srv.Served(); n != 1 {
		t.Fatalf("the server had %d connections, want 1", n)
	}
	st := status(t, addr)
	if !st.Connected || st.Width != 64 || st.Clients != 0 || st.IdleLeftSec == nil {
		t.Fatalf("status: %+v", st)
	}
	// The holder has the password only in its env.
	out, _ := osexec.Command("ps", "-ww", "-o", "args=", "-p", strconv.Itoa(st.PID)).Output()
	b := string(out)
	if strings.Contains(b, "pw ") || !strings.Contains(b, "session run") {
		t.Fatalf("holder args: %q", b)
	}

	// Another pixel format is another holder.
	mustRun(t, "", "-s", addr, "-p", "LAB_PW", "--session=auto", "--pixel-format", "rgb565", "info")
	if n := srv.Served(); n != 2 {
		t.Fatalf("the server had %d connections, want 2", n)
	}
	res := mustRun(t, "", "--session=auto", "session", "list")
	if lines := strings.Count(res.stdout, "\n"); lines != 2 {
		t.Fatalf("list: %q", res.stdout)
	}

	mustRun(t, "", "-s", addr, "--session=auto", "session", "stop")
	res = mustRun(t, "", "-s", addr, "--session=auto", "session", "status")
	if !strings.Contains(res.stdout, "No session") {
		t.Fatalf("status after stop: %q", res.stdout)
	}
}

func TestSessionAuthError(t *testing.T) {
	home := sessionHome(t)
	_, addr := server(t, vnctest.Options{Security: []uint8{vnc.SecVNC}, Password: "pw"})
	t.Setenv("LAB_PW", "wrong")
	res := run(t, "", "-s", addr, "-p", "LAB_PW", "--session=auto", "info")
	if res.code() != ExitAuth {
		t.Fatalf("exit %d: %v", res.code(), res.err)
	}
	if strings.Contains(res.err.Error(), "wrong") {
		t.Fatalf("the error has the password: %v", res.err)
	}
	logs, _ := filepath.Glob(filepath.Join(home, ".cli-tools/vnc-cli/run/*.log"))
	for _, l := range logs {
		if b, _ := os.ReadFile(l); strings.Contains(string(b), "wrong") {
			t.Fatalf("the log has the password: %s", b)
		}
	}
	socks, _ := filepath.Glob(filepath.Join(home, ".cli-tools/vnc-cli/run/*.sock"))
	if len(socks) != 0 {
		t.Fatalf("a failed holder left %v", socks)
	}
}

func TestSessionStaleAfterKill(t *testing.T) {
	sessionHome(t)
	srv, addr := server(t, vnctest.Options{})
	mustRun(t, "", "-s", addr, "--session=auto", "info")
	st := status(t, addr)
	if err := syscall.Kill(st.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	mustRun(t, "", "-s", addr, "--session=auto", "info")
	if st2 := status(t, addr); st2.PID == st.PID || srv.Served() != 2 {
		t.Fatalf("no new holder: %d %d, %d connections", st.PID, st2.PID, srv.Served())
	}
}

func TestSessionIdleTimeout(t *testing.T) {
	sessionHome(t)
	_, addr := server(t, vnctest.Options{})
	mustRun(t, "", "-s", addr, "--session=auto", "--idle-timeout", "300ms", "info")
	deadline := time.Now().Add(5 * time.Second)
	for {
		res := mustRun(t, "", "-s", addr, "--session=auto", "session", "status")
		if strings.Contains(res.stdout, "No session") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the holder did not stop: %q", res.stdout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestSessionTrebiMode(t *testing.T) {
	standalone(t)
	state, err := os.MkdirTemp("/tmp", "vt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(state) })
	t.Setenv("TREBI_STATE_DIR", state)

	res := run(t, "", "--session=auto", "info")
	if res.code() != ExitConnect || !strings.Contains(res.err.Error(), "does not run in Trebi") {
		t.Fatalf("no serve: exit %d: %v", res.code(), res.err)
	}
	if res := run(t, "", "session", "start"); res.code() != ExitUsage {
		t.Fatalf("session start in Trebi mode: exit %d: %v", res.code(), res.err)
	}

	// A command forwards to the socket of serve.
	_, addr := server(t, vnctest.Options{})
	h := session.NewHolder(func(ctx context.Context) (*vnc.Conn, error) {
		return vnc.Dial(ctx, vnc.Options{Addr: addr})
	}, addr, "", 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := h.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, _ := session.TrebiPaths(state)
	ln, err := session.Listen(p.Sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- h.Serve(ctx, ln) }()
	defer func() {
		cancel()
		<-done
	}()
	res = mustRun(t, "", "--session=auto", "info")
	if !strings.Contains(res.stdout, "64x48") {
		t.Fatalf("info: %q", res.stdout)
	}
	if res := run(t, "", "--session=auto", "-s", addr, "info"); res.err == nil || !strings.Contains(res.err.Error(), "Trebi sets this value") {
		t.Fatalf("--server in Trebi mode: %v", res.err)
	}
}
