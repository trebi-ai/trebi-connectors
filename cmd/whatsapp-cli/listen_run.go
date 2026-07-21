package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	appPkg "github.com/flarco/cli-tools/whatsapp-cli/internal/app"
	"github.com/flarco/cli-tools/whatsapp-cli/internal/ipc"
	"github.com/flarco/cli-tools/whatsapp-cli/internal/lock"
)

// runListen runs `whatsapp-cli listen`, choosing one of two roles:
//
//   - anchor: wins the store LOCK, holds the single WhatsApp connection, serves
//     the socket (send forwarding + subscriptions), and fans events out.
//   - secondary: the store is already locked by an anchor, so it attaches over
//     the socket, registers its filter, and streams matching events to its own
//     stdout until the anchor goes away.
//
// If the direct (anchor) path loses the LOCK race to another process, it falls
// back to the secondary path (one retry), mirroring `whatsapp-cli send`.
func runListen(ctx context.Context, flags *rootFlags, filter appPkg.ListenFilter, maxReconnect time.Duration) error {
	err := runAnchor(ctx, flags, filter, maxReconnect)
	if err == nil || !errors.Is(err, lock.ErrLocked) {
		return err
	}

	// Store is locked by an anchor — attach as a secondary instead.
	if serr := runSecondary(ctx, flags, filter); serr == nil || !errors.Is(serr, errNoAnchor) {
		return serr
	}

	// Raced: anchor released the lock between our lock attempt and dial. Retry
	// the anchor path once.
	return runAnchor(ctx, flags, filter, maxReconnect)
}

// runAnchor attempts to become the anchor. It returns a lock.ErrLocked-wrapped
// error (via newApp) if another process already holds the store.
func runAnchor(ctx context.Context, flags *rootFlags, filter appPkg.ListenFilter, maxReconnect time.Duration) error {
	a, lk, err := newApp(ctx, flags, true, false)
	if err != nil {
		return err
	}
	defer closeApp(a, lk)

	if err := a.EnsureAuthed(); err != nil {
		return err
	}

	subs := appPkg.NewSubscriberSet()

	// Serve both send forwarding and secondary-listener subscriptions.
	srv, err := ipc.Serve(ctx, a.StoreDir(), sendHandler(a), subscribeHandler(subs))
	if err != nil {
		return err
	}
	defer srv.Close()
	fmt.Fprintf(os.Stderr, "Anchor listening; accepting send/subscribe on %s\n", ipc.SocketPath(a.StoreDir()))

	return a.Listen(ctx, appPkg.ListenOptions{
		ListenFilter: filter,
		MaxReconnect: maxReconnect,
		Out:          os.Stdout,
		Subscribers:  subs,
	})
}

// errNoAnchor means no anchor was reachable on the socket.
var errNoAnchor = errors.New("no anchor listening")

// runSecondary attaches to a running anchor and streams filtered events to
// stdout until the anchor closes the connection or ctx is cancelled.
func runSecondary(ctx context.Context, flags *rootFlags, filter appPkg.ListenFilter) error {
	storeDir := resolveStoreDir(flags)

	conn, err := ipc.Dial(storeDir)
	if err != nil {
		return errNoAnchor
	}

	// Send the subscribe request, then stream lines back.
	req := ipc.Request{Cmd: "subscribe", Filter: toIPCFilter(filter)}
	if err := ipc.WriteRequest(conn, req); err != nil {
		_ = conn.Close()
		return errNoAnchor
	}

	fmt.Fprintln(os.Stderr, "Attached to running anchor; streaming events (Ctrl+C to stop)...")

	// Close the connection when ctx is cancelled so the read loop unblocks.
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()

	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			_, _ = w.Write(line)
			_ = w.Flush()
		}
		if err != nil {
			if ctx.Err() != nil {
				fmt.Fprintln(os.Stderr, "\nStopping listen.")
				return nil
			}
			if err == io.EOF {
				fmt.Fprintln(os.Stderr, "\nAnchor closed; exiting.")
				return fmt.Errorf("anchor closed the connection")
			}
			return err
		}
	}
}

// subscribeHandler builds the IPC stream handler that registers a secondary
// listener and streams its filtered events over the connection.
func subscribeHandler(subs *appPkg.SubscriberSet) ipc.StreamHandler {
	return func(ctx context.Context, req ipc.Request, w io.Writer) error {
		filter := fromIPCFilter(req.Filter)
		bw := bufio.NewWriter(w)
		return subs.Serve(ctx, filter, bw, bw.Flush)
	}
}

// toIPCFilter converts an app filter to the wire form.
func toIPCFilter(f appPkg.ListenFilter) *ipc.Filter {
	cats := make([]string, 0, len(f.Categories))
	for c := range f.Categories {
		cats = append(cats, c)
	}
	return &ipc.Filter{
		Categories:  cats,
		ChatFilter:  f.ChatFilter,
		FromFilter:  f.FromFilter,
		ExcludeSelf: f.ExcludeSelf,
		Raw:         f.Raw,
	}
}

// fromIPCFilter converts a wire filter back to an app filter.
func fromIPCFilter(f *ipc.Filter) appPkg.ListenFilter {
	out := appPkg.ListenFilter{Categories: map[string]bool{}}
	if f == nil {
		return out
	}
	for _, c := range f.Categories {
		out.Categories[c] = true
	}
	out.ChatFilter = f.ChatFilter
	out.FromFilter = f.FromFilter
	out.ExcludeSelf = f.ExcludeSelf
	out.Raw = f.Raw
	return out
}
