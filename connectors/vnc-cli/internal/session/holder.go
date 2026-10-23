package session

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc"
)

// Reconnect backoff.
const (
	minBackoff = time.Second
	maxBackoff = 30 * time.Second
)

var (
	errIdle    = errors.New("idle timeout")
	errStopped = errors.New("stopped")
)

// Holder keeps one connection to a screen and runs the commands of the
// clients on the socket. It connects again with backoff when the connection
// drops. A failed login stops it: a retry with the same password can make
// the server block the address.
type Holder struct {
	dial   func(context.Context) (*vnc.Conn, error)
	server string
	user   string
	idle   time.Duration
	log    *slog.Logger

	mu        sync.Mutex
	local     *Local
	ready     chan struct{} // closed when local is set
	clients   int
	idleTimer *time.Timer
	idleEnd   time.Time
	started   time.Time
	stop      context.CancelCauseFunc
}

// NewHolder makes a holder. An idle timeout of 0 keeps it until a client
// sends stop.
func NewHolder(dial func(context.Context) (*vnc.Conn, error), server, user string, idle time.Duration, log *slog.Logger) *Holder {
	return &Holder{dial: dial, server: server, user: user, idle: idle, log: log, ready: make(chan struct{}), started: time.Now()}
}

// Connect makes the first connection and waits for the full screen.
func (h *Holder) Connect(ctx context.Context) error {
	l, err := h.connect(ctx)
	if err != nil {
		return err
	}
	h.set(l)
	return nil
}

func (h *Holder) connect(ctx context.Context) (*Local, error) {
	c, err := h.dial(ctx)
	if err != nil {
		return nil, err
	}
	if err := c.Refresh(ctx, true); err != nil {
		c.Close()
		return nil, err
	}
	return NewLocal(c, true), nil
}

func (h *Holder) set(l *Local) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.local = l
	if l != nil {
		close(h.ready)
	} else {
		h.ready = make(chan struct{})
	}
}

// Serve runs the clients of ln until ctx ends, the idle timeout ends, a
// client sends stop, or a login fails. It returns nil for a normal stop.
func (h *Holder) Serve(ctx context.Context, ln net.Listener) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	h.mu.Lock()
	h.stop = cancel
	h.resetIdle()
	h.mu.Unlock()
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	var wg sync.WaitGroup
	wg.Go(func() { h.keep(ctx, cancel) })
	for {
		nc, err := ln.Accept()
		if err != nil {
			break
		}
		wg.Go(func() { h.handle(ctx, nc) })
	}
	cancel(nil)
	wg.Wait()
	h.mu.Lock()
	if h.idleTimer != nil {
		h.idleTimer.Stop()
	}
	if h.local != nil {
		h.local.Close()
	}
	h.mu.Unlock()
	if err := context.Cause(ctx); !errors.Is(err, errIdle) && !errors.Is(err, errStopped) && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// keep connects again each time the connection drops.
func (h *Holder) keep(ctx context.Context, stop context.CancelCauseFunc) {
	for {
		h.mu.Lock()
		l := h.local
		h.mu.Unlock()
		if l != nil {
			select {
			case <-l.conn.Done():
				h.log.Warn("the connection dropped", "err", l.conn.Err())
				h.set(nil)
				l.Close()
			case <-ctx.Done():
				return
			}
		}
		for wait := minBackoff; ; wait = min(2*wait, maxBackoff) {
			l, err := h.connect(ctx)
			if err == nil {
				h.log.Info("connected", "server", h.server)
				h.set(l)
				break
			}
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, vnc.ErrAuth) {
				h.log.Error("the login failed; the session stops", "err", err)
				stop(err)
				return
			}
			h.log.Warn("connect failed", "err", err, "retry_in", wait)
			if sleep(ctx, wait) != nil {
				return
			}
		}
	}
}

func (h *Holder) addClient(n int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients += n
	h.resetIdle()
}

// resetIdle starts the idle timer when no client is left. Call it with mu.
func (h *Holder) resetIdle() {
	if h.idleTimer != nil {
		h.idleTimer.Stop()
		h.idleTimer = nil
	}
	h.idleEnd = time.Time{}
	if h.idle <= 0 || h.clients > 0 || h.stop == nil {
		return
	}
	h.idleEnd = time.Now().Add(h.idle)
	stop := h.stop
	h.idleTimer = time.AfterFunc(h.idle, func() { stop(errIdle) })
}

// remote waits for a connection.
func (h *Holder) remote(ctx context.Context) (*Local, error) {
	for {
		h.mu.Lock()
		l, ready := h.local, h.ready
		h.mu.Unlock()
		if l != nil {
			return l, nil
		}
		select {
		case <-ready:
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for the connection to %s: %w", h.server, ctx.Err())
		}
	}
}

func (h *Holder) status() *Status {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := &Status{
		Server: h.server, User: h.user, PID: os.Getpid(), Clients: h.clients,
		UptimeSec: int(time.Since(h.started).Seconds()), Connected: h.local != nil,
	}
	if h.local != nil {
		s.Name = h.local.conn.Name()
		sz := h.local.conn.Size()
		s.Width, s.Height = sz.X, sz.Y
	}
	if !h.idleEnd.IsZero() {
		left := int(time.Until(h.idleEnd).Seconds())
		s.IdleLeftSec = &left
	}
	return s
}

// handle runs the requests of one client in order.
func (h *Holder) handle(ctx context.Context, nc net.Conn) {
	defer nc.Close()
	defer context.AfterFunc(ctx, func() { nc.Close() })()
	r := bufio.NewReaderSize(nc, 64<<10)
	w := bufio.NewWriterSize(nc, 64<<10)
	counted := false
	defer func() {
		if counted {
			h.addClient(-1)
		}
	}()
	for {
		var req Request
		pix, err := readMsg(r, &req, func() *Frame { return req.Image })
		if err != nil {
			return
		}
		// A status or stop call is not a client: it does not keep the
		// holder alive.
		if !counted && req.Op != OpStatus && req.Op != OpStop {
			counted = true
			h.addClient(1)
		}
		rctx, cancel := context.WithCancel(ctx)
		if req.Timeout > 0 {
			rctx, cancel = context.WithTimeout(ctx, req.Timeout)
		}
		stopWatch := watchHangup(nc, r, cancel)
		rep, out := h.do(rctx, req, pix)
		stopWatch()
		cancel()
		if err := writeMsg(w, rep, out); err != nil {
			return
		}
		if req.Op == OpStop {
			h.mu.Lock()
			h.stop(errStopped)
			h.mu.Unlock()
			return
		}
	}
}

// watchHangup cancels the request when the client goes away. The client
// sends nothing while it waits for a reply, so a read that ends is a
// hangup.
func watchHangup(nc net.Conn, r *bufio.Reader, cancel func()) (stop func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := r.Peek(1); err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
			cancel()
		}
	}()
	return func() {
		nc.SetReadDeadline(time.Now())
		<-done
		nc.SetReadDeadline(time.Time{})
	}
}

func (h *Holder) do(ctx context.Context, req Request, pix []byte) (rep Reply, out []byte) {
	switch req.Op {
	case OpStatus:
		return Reply{Status: h.status()}, nil
	case OpStop:
		return Reply{}, nil
	}
	l, err := h.remote(ctx)
	if err == nil {
		switch req.Op {
		case OpInfo:
			var info Info
			info, err = l.Info(ctx)
			rep.Info = &info
		case OpCapture:
			var img *image.RGBA
			if img, err = l.Capture(ctx, req.Rect); err == nil {
				rep.Image, out = frameOf(img)
			}
		case OpInput:
			err = l.Input(ctx, req.Actions)
		case OpPaste:
			err = l.Paste(ctx, req.Text)
		case OpClipboard:
			rep.Text, err = l.Clipboard(ctx)
		case OpExpect:
			if req.Image == nil {
				err = errors.New("expect needs an image")
				break
			}
			err = l.Expect(ctx, req.Image.image(pix), req.At, req.Match)
		case OpStable:
			err = l.Stable(ctx, req.For, req.Rect, req.Match)
		default:
			err = fmt.Errorf("unknown operation %q", req.Op)
		}
	}
	if err != nil {
		return Reply{Error: remoteError(err)}, nil
	}
	return rep, out
}
