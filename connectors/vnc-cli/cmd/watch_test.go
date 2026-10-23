package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/internal/watch"
	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc/vnctest"
)

// lines sends each full line written to it as an event.
type lines struct {
	mu  sync.Mutex
	buf bytes.Buffer
	ch  chan watch.Event
	t   *testing.T
}

func (l *lines) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Write(p)
	for {
		line, err := l.buf.ReadBytes('\n')
		if err != nil {
			l.buf.Write(line)
			return len(p), nil
		}
		var ev watch.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			l.t.Errorf("line %q: %v", line, err)
			continue
		}
		l.ch <- ev
	}
}

type watchRun struct {
	t      *testing.T
	evs    chan watch.Event
	cancel context.CancelFunc
	done   chan error
	stderr *bytes.Buffer
}

func startWatch(t *testing.T, args ...string) *watchRun {
	t.Helper()
	w := &watchRun{t: t, evs: make(chan watch.Event, 100), done: make(chan error, 1), stderr: &bytes.Buffer{}}
	r := NewRunner(strings.NewReader(""), &lines{ch: w.evs, t: t}, w.stderr)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	w.cancel = cancel
	go func() { w.done <- r.App("test").Run(ctx, append([]string{"vnc-cli"}, args...)) }()
	t.Cleanup(func() {
		cancel()
		<-w.done
	})
	return w
}

// next waits for the next event of a kind that is not in skip.
func (w *watchRun) next(skip ...string) watch.Event {
	w.t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev := <-w.evs:
			if !slices.Contains(skip, ev.Event) {
				return ev
			}
		case err := <-w.done:
			w.t.Fatalf("watch ended: %v; stderr: %s", err, w.stderr)
		case <-timeout:
			w.t.Fatalf("no event in time; stderr: %s", w.stderr)
		}
	}
}

func (w *watchRun) expect(kind string, skip ...string) watch.Event {
	w.t.Helper()
	ev := w.next(skip...)
	if ev.Event != kind {
		w.t.Fatalf("event %+v, want %s", ev, kind)
	}
	return ev
}

func TestWatch(t *testing.T) {
	for _, mode := range []string{"off", "auto"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "auto" {
				sessionHome(t)
			} else {
				standalone(t)
			}
			srv, addr := server(t, vnctest.Options{})
			w := startWatch(t, "-s", addr, "--session="+mode, "watch", "--settle", "200ms", "--max-fps", "0",
				"-a", "top=0,0,64,24", "-a", "bottom=0,24,64,24", "--events", "change,stable,clipboard,bell,resize,disconnected,connected")
			ready := w.expect(watch.KindReady)
			if *ready.Ref != (watch.Size{Width: 64, Height: 48}) || len(ready.Areas) != 2 {
				t.Fatalf("ready = %+v", ready)
			}

			srv.Paint(image.Rect(4, 30, 20, 40), color.RGBA{255, 255, 255, 255})
			ev := w.expect(watch.KindChange)
			if ev.Area != "bottom" || *ev.BBox != (watch.Rect{X: 4, Y: 30, W: 16, H: 10}) {
				t.Fatalf("change = %+v", ev)
			}
			if ev = w.expect(watch.KindStable); ev.Area != "bottom" || ev.Pixels != 160 {
				t.Fatalf("stable = %+v", ev)
			}

			srv.CutText("café")
			if ev = w.expect(watch.KindClipboard); ev.Text != "café" {
				t.Fatalf("clipboard = %+v", ev)
			}
			srv.Bell()
			w.expect(watch.KindBell)

			srv.Resize(128, 96)
			if ev = w.expect(watch.KindResize); *ev.Size != (watch.Size{Width: 128, Height: 96}) {
				t.Fatalf("resize = %+v", ev)
			}
			if ev = w.expect(watch.KindReady, watch.KindChange, watch.KindStable); *ev.Picture != (watch.Rect{W: 128, H: 96}) || *ev.Ref != (watch.Size{Width: 64, Height: 48}) {
				t.Fatalf("ready after resize = %+v", ev)
			}
			// The areas scale with the screen: bbox stays in reference pixels.
			srv.Paint(image.Rect(0, 0, 32, 16), color.RGBA{255, 0, 0, 255})
			if ev = w.expect(watch.KindChange, watch.KindStable); ev.Area != "top" || *ev.BBox != (watch.Rect{W: 16, H: 8}) {
				t.Fatalf("change after resize = %+v", ev)
			}

			srv.Drop()
			w.expect(watch.KindDisconnected, watch.KindStable)
			w.expect(watch.KindConnected)
			w.expect(watch.KindReady)
			if n := srv.Served(); n != 2 {
				t.Fatalf("the server had %d connections, want 2", n)
			}
		})
	}
}

func TestWatchOnce(t *testing.T) {
	standalone(t)
	srv, addr := server(t, vnctest.Options{})
	go func() {
		time.Sleep(300 * time.Millisecond)
		srv.Paint(image.Rect(0, 0, 20, 20), color.RGBA{255, 255, 255, 255})
	}()
	res := run(t, "", "-s", addr, "watch", "--settle", "100ms", "--once", "-d", "10s")
	if res.err != nil {
		t.Fatalf("watch --once: %v; %s", res.err, res.stderr)
	}
	kinds := []string{}
	for _, l := range strings.Split(strings.TrimSpace(res.stdout), "\n") {
		var ev watch.Event
		if err := json.Unmarshal([]byte(l), &ev); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, ev.Event)
	}
	if strings.Join(kinds, ",") != "ready,change,stable" {
		t.Fatalf("events = %v", kinds)
	}

	res = run(t, "", "-s", addr, "watch", "--once", "-d", "300ms")
	if res.code() != ExitUnexpected || !strings.Contains(res.err.Error(), "no stable event within 300ms") {
		t.Fatalf("watch --once with no change: %v (exit %d)", res.err, res.code())
	}
	if res = run(t, "", "-s", addr, "watch", "-d", "300ms"); res.err != nil {
		t.Fatalf("watch -d: %v", res.err)
	}
}

func TestWatchUsage(t *testing.T) {
	standalone(t)
	_, addr := server(t, vnctest.Options{})
	for _, args := range [][]string{
		{"-a", "1,2,3"},
		{"--ref", "x"},
		{"--events", "change,nope"},
		{"--settle", "-1s"},
	} {
		res := run(t, "", append([]string{"-s", addr, "watch"}, args...)...)
		if res.code() != ExitUsage {
			t.Errorf("watch %v: exit %d (%v), want usage", args, res.code(), res.err)
		}
	}
	res := run(t, "", "-s", addr, "watch", "-a", "x=100,0,10,10", "-d", "2s")
	if res.code() != ExitFailed {
		t.Fatalf("an area outside the screen: exit %d (%v)", res.code(), res.err)
	}
}
