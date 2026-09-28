package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// Handler executes a one-shot forwarded request (send_*) against the live
// WhatsApp connection and returns the response to send back to the client.
type Handler func(ctx context.Context, req Request) Response

// StreamHandler serves a long-lived "subscribe" connection. It registers the
// subscriber and writes JSONL event lines to w until ctx is cancelled (anchor
// shutting down) or the connection breaks (write error). It returns when the
// subscription ends; the server then closes the connection.
type StreamHandler func(ctx context.Context, req Request, w io.Writer) error

// Server listens on a Unix socket and dispatches requests to handlers.
type Server struct {
	path   string
	ln     net.Listener
	h      Handler
	stream StreamHandler

	// srvCtx is cancelled by either the parent ctx or Close(). Stream handlers
	// observe it so Close() tears down long-lived subscriptions even when the
	// parent ctx is still live.
	srvCtx    context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once
	// wg tracks the accept loop and in-flight connection handlers only, not the
	// ctx watcher (which blocks until ctx is cancelled and must not gate Close).
	wg sync.WaitGroup
}

// Serve binds the socket at SocketPath(storeDir) and serves requests until ctx
// is cancelled. A stale socket file (e.g. left by a crashed daemon) is removed
// before binding. Serve returns after the listener is closed and in-flight
// connections drain.
func Serve(ctx context.Context, storeDir string, h Handler, stream StreamHandler) (*Server, error) {
	path := SocketPath(storeDir)

	// Remove a leftover socket from a previous (crashed) daemon. This is safe:
	// the caller already holds the exclusive store LOCK, so no other daemon is
	// using this socket.
	_ = os.Remove(path)

	// Unix socket paths are limited (~104 bytes on macOS, ~108 on Linux). A deep
	// store dir can overflow it; surface a clear error instead of a cryptic
	// "bind: invalid argument".
	if len(path) >= maxSocketPath {
		return nil, fmt.Errorf("socket path too long (%d >= %d bytes): %s; use a shorter --store / $WHATSAPP_CLI_STORE_DIR", len(path), maxSocketPath, path)
	}

	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", path, err)
	}
	// Restrict to the owner; the store dir is already 0700 but be explicit.
	_ = os.Chmod(path, 0600)

	srvCtx, cancel := context.WithCancel(ctx)
	s := &Server{path: path, ln: ln, h: h, stream: stream, srvCtx: srvCtx, cancel: cancel}

	// Close the listener when the server context is cancelled (parent ctx or
	// Close). Not tracked by wg: it blocks until cancellation.
	go func() {
		<-srvCtx.Done()
		_ = s.ln.Close()
	}()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			conn, err := s.ln.Accept()
			if err != nil {
				// Listener closed (ctx cancel or Close), or a fatal accept error.
				return
			}
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.handleConn(srvCtx, conn)
			}()
		}
	}()

	return s, nil
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	// Bound how long a connected-but-silent client can hold a handler open, so
	// it can't wedge Close()/shutdown. The request line is tiny and sent
	// immediately after connecting.
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	r := bufio.NewReader(conn)
	line, err := r.ReadBytes('\n')
	if err != nil {
		return
	}

	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		writeResponse(conn, Response{OK: false, Error: fmt.Sprintf("invalid request: %v", err)})
		return
	}

	if req.Cmd == "subscribe" {
		if s.stream == nil {
			writeResponse(conn, Response{OK: false, Error: "subscribe not supported"})
			return
		}
		// Long-lived stream: the anchor only writes from here on. Drop the read
		// deadline so the subscription isn't torn down after 10s of quiet.
		_ = conn.SetReadDeadline(time.Time{})
		_ = s.stream(ctx, req, conn)
		return
	}

	resp := s.h(ctx, req)
	writeResponse(conn, resp)
}

func writeResponse(conn net.Conn, resp Response) {
	b, err := json.Marshal(resp)
	if err != nil {
		b, _ = json.Marshal(Response{OK: false, Error: "internal marshal error"})
	}
	b = append(b, '\n')
	_, _ = conn.Write(b)
}

// Close stops the server, removes the socket file, and waits for the accept
// loop and in-flight handlers to finish. Safe to call more than once and safe
// to race with ctx cancellation (both close the listener at most once).
func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		// Cancel first so stream handlers (which block until srvCtx is done)
		// unblock; otherwise wg.Wait() would deadlock on them.
		s.cancel()
		_ = s.ln.Close()
		s.wg.Wait()
		_ = os.Remove(s.path)
	})
	return nil
}
