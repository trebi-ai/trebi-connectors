package session

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc"
	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc/vnctest"
)

// shortDir is a temp folder with a short path: t.TempDir on macOS is too
// long for a socket.
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "vs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

type fixture struct {
	srv  *vnctest.Server
	h    *Holder
	sock string
	done chan error
	stop context.CancelFunc
}

func start(t *testing.T, o vnctest.Options, idle time.Duration) *fixture {
	t.Helper()
	srv := vnctest.New(o)
	addr, err := srv.Listen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	f := &fixture{srv: srv, sock: filepath.Join(shortDir(t), "h.sock"), done: make(chan error, 1)}
	f.h = NewHolder(func(ctx context.Context) (*vnc.Conn, error) {
		return vnc.Dial(ctx, vnc.Options{Addr: addr, Password: o.Password})
	}, addr, "", idle, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.h.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	ln, err := Listen(f.sock)
	if err != nil {
		t.Fatal(err)
	}
	sctx, stop := context.WithCancel(context.Background())
	f.stop = stop
	go func() { f.done <- f.h.Serve(sctx, ln) }()
	t.Cleanup(func() {
		stop()
		<-f.done
	})
	return f
}

func (f *fixture) client(t *testing.T) *Client {
	t.Helper()
	c, err := Open(f.sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestHolderReusesOneConnection(t *testing.T) {
	f := start(t, vnctest.Options{}, 0)
	f.srv.Paint(image.Rect(0, 0, 8, 8), color.RGBA{255, 0, 0, 255})
	red := image.NewRGBA(image.Rect(0, 0, 8, 8))
	draw.Draw(red, red.Rect, image.NewUniform(color.RGBA{255, 0, 0, 255}), image.Point{}, draw.Src)
	ctx := ctxT(t)
	for range 3 {
		c := f.client(t)
		info, err := c.Info(ctx)
		if err != nil || info.Width != 64 || info.Height != 48 {
			t.Fatalf("info: %+v %v", info, err)
		}
		if err := c.Expect(ctx, red, image.Point{}, Match{Fuzz: -1}); err != nil {
			t.Fatal(err)
		}
		if err := c.Stable(ctx, 50*time.Millisecond, image.Rectangle{}, Match{Fuzz: -1}); err != nil {
			t.Fatal(err)
		}
		img, err := c.Capture(ctx, image.Rect(0, 0, 16, 16))
		if err != nil {
			t.Fatal(err)
		}
		if got := img.RGBAAt(1, 1); got != (color.RGBA{255, 0, 0, 255}) {
			t.Fatalf("pixel = %v", got)
		}
		if err := c.Paste(ctx, "hi"); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.srv.Served(); n != 1 {
		t.Fatalf("the server had %d connections, want 1", n)
	}
}

func TestHolderInputDoesNotInterleave(t *testing.T) {
	f := start(t, vnctest.Options{}, 0)
	ctx := ctxT(t)
	const n = 40
	var wg sync.WaitGroup
	for _, ks := range []uint32{'a', 'b'} {
		c := f.client(t)
		wg.Go(func() {
			var acts []Action
			for range n {
				acts = append(acts, Action{Op: OpKey, Keysym: ks, Down: true}, Action{Op: OpKey, Keysym: ks})
			}
			if err := c.Input(ctx, acts); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	ev, err := f.srv.WaitEvents(ctx, 4*n)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(ev); i++ {
		if ev[i].Keysym != ev[i-1].Keysym && i != 2*n {
			t.Fatalf("the input of two clients mixed at event %d", i)
		}
	}
}

func TestHolderIdleTimeout(t *testing.T) {
	f := start(t, vnctest.Options{}, 300*time.Millisecond)
	ctx := ctxT(t)
	c := f.client(t)
	if _, err := c.Info(ctx); err != nil {
		t.Fatal(err)
	}
	// A connected client keeps the holder.
	time.Sleep(500 * time.Millisecond)
	if _, err := c.Info(ctx); err != nil {
		t.Fatal(err)
	}
	c.Close()
	// A status call does not.
	sc := f.client(t)
	st, err := sc.Status(ctx)
	for err == nil && st.Clients > 0 {
		time.Sleep(10 * time.Millisecond)
		st, err = sc.Status(ctx)
	}
	if err != nil || st.IdleLeftSec == nil || !st.Connected {
		t.Fatalf("status: %+v %v", st, err)
	}
	select {
	case err := <-f.done:
		if err != nil {
			t.Fatal(err)
		}
		f.done <- nil
	case <-time.After(3 * time.Second):
		t.Fatal("the holder did not stop after the idle timeout")
	}
	if _, err := os.Stat(f.sock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the socket stays: %v", err)
	}
}

func TestHolderStop(t *testing.T) {
	f := start(t, vnctest.Options{}, 0)
	if err := f.client(t).Stop(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-f.done:
		f.done <- err
	case <-time.After(3 * time.Second):
		t.Fatal("the holder did not stop")
	}
}

func TestHolderReconnects(t *testing.T) {
	f := start(t, vnctest.Options{}, 0)
	ctx := ctxT(t)
	c := f.client(t)
	if _, err := c.Info(ctx); err != nil {
		t.Fatal(err)
	}
	f.srv.Drop()
	deadline := time.Now().Add(5 * time.Second)
	for f.srv.Served() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the holder did not connect again")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// A command during the reconnect waits for the connection.
	if err := c.Input(ctx, []Action{{Op: OpKey, Keysym: 'x', Down: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.srv.WaitEvents(ctx, 1); err != nil {
		t.Fatal(err)
	}
}

func TestHolderTimeoutAndHangup(t *testing.T) {
	f := start(t, vnctest.Options{}, 0)
	c := f.client(t)
	ref := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := range ref.Pix {
		ref.Pix[i] = 0x7f
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := c.Expect(ctx, ref, image.Point{}, Match{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expect: %v", err)
	}
	c.Close()

	// A client that goes away cancels its request.
	c2 := f.client(t)
	go c2.Stable(context.Background(), time.Hour, image.Rectangle{}, Match{})
	time.Sleep(100 * time.Millisecond)
	c2.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		st, err := f.client(t).Status(ctxT(t))
		if err != nil {
			t.Fatal(err)
		}
		if st.Clients == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the request of a gone client still runs: %+v", st)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestHolderErrorKinds(t *testing.T) {
	f := start(t, vnctest.Options{}, 0)
	_, err := f.client(t).Capture(ctxT(t), image.Rect(60, 40, 80, 60))
	var re *RemoteError
	if !errors.As(err, &re) || re.Kind != KindFailed {
		t.Fatalf("off screen: %v", err)
	}
}

func TestPaths(t *testing.T) {
	p, err := StandalonePaths("/h/.cli-tools/vnc-cli", "u@host:1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Sock, "/h/.cli-tools/vnc-cli/run/") || len(filepath.Base(p.Sock)) != len("123456789abc.sock") {
		t.Fatalf("paths: %+v", p)
	}
	if _, err := StandalonePaths("/"+strings.Repeat("x", 90), "k"); err == nil || !strings.Contains(err.Error(), "--session off") {
		t.Fatalf("a long path: %v", err)
	}
	if p, _ := TrebiPaths("/s"); p.Sock != "/s/vnc.sock" || p.Log != "" {
		t.Fatalf("trebi paths: %+v", p)
	}
}

func TestLockAndStaleSocket(t *testing.T) {
	dir := shortDir(t)
	l1, err := Lock(filepath.Join(dir, "a.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(filepath.Join(dir, "a.lock")); !errors.Is(err, ErrBusy) {
		t.Fatalf("second lock: %v", err)
	}
	l1.Close()
	l2, err := Lock(filepath.Join(dir, "a.lock"))
	if err != nil {
		t.Fatal(err)
	}
	l2.Close()

	// A holder that died leaves its socket file.
	sock := filepath.Join(dir, "a.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	if _, err := Open(sock); err == nil {
		t.Fatal("open a stale socket")
	}
	ln, err = Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if st, _ := os.Stat(sock); st.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %v", st.Mode())
	}
}
