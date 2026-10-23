package watch

import (
	"context"
	"errors"
	"image"
	"slices"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc"
)

// DefaultEvents are the kinds that a watch sends when the caller names
// none. Ready and error always go out.
var DefaultEvents = []string{KindChange, KindStable, KindClipboard, KindBell, KindResize, KindDisconnected, KindConnected}

// AllEvents are the kinds that a caller can ask for.
var AllEvents = append(slices.Clone(DefaultEvents), KindCursor)

// Spec is one watch.
type Spec struct {
	Areas   []Area      `json:"areas,omitempty"`
	Ref     image.Point `json:"ref,omitzero"`
	Options Options     `json:"options"`
	// Events are the kinds to send; empty is DefaultEvents.
	Events []string `json:"events,omitempty"`
}

// Source gives a connection with a current screen. It waits while the
// connection is down.
type Source interface {
	Conn(ctx context.Context) (*vnc.Conn, error)
}

// maxDirty is the number of changed rectangles that a watch keeps before
// it merges them.
const maxDirty = 256

// Run watches the screen of src until ctx ends or emit fails. It follows
// the connection through a reconnect: "disconnected", then "connected" and
// "ready" again.
func Run(ctx context.Context, src Source, spec Spec, emit func(Event) error) error {
	kinds := spec.Events
	if len(kinds) == 0 {
		kinds = DefaultEvents
	}
	send := func(ev Event) error {
		if ev.Event == KindReady || ev.Event == KindError || slices.Contains(kinds, ev.Event) {
			return emit(ev)
		}
		return nil
	}
	e := New(spec.Areas, spec.Ref, spec.Options, send)
	var last *vnc.Conn
	for {
		c, err := src.Conn(ctx)
		if err != nil {
			return err
		}
		if c == last {
			// The source has not seen the drop yet.
			if err := sleep(ctx, 100*time.Millisecond); err != nil {
				return err
			}
			continue
		}
		if last != nil {
			if err := send(Event{Event: KindConnected, Time: time.Now()}); err != nil {
				return err
			}
		}
		last = c
		err = e.follow(ctx, c)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var ce connError
		if !errors.As(err, &ce) {
			return err
		}
		if err := send(Event{Event: KindDisconnected, Time: time.Now(), Error: ce.err.Error()}); err != nil {
			return err
		}
	}
}

// connError is the end of a connection, which a reconnect can fix.
type connError struct{ err error }

func (e connError) Error() string { return e.err.Error() }

// follow sends the events of one connection.
func (e *Engine) follow(ctx context.Context, c *vnc.Conn) error {
	sub := c.Subscribe()
	defer sub.Close()
	if err := e.Ready(c.Size(), c.Snapshot, time.Now()); err != nil {
		return err
	}
	var gap time.Duration
	if e.opt.MaxFPS > 0 {
		gap = time.Duration(float64(time.Second) / e.opt.MaxFPS)
	}
	var (
		dirty   []image.Rectangle
		checked time.Time
	)
	for {
		wake := e.Due()
		if len(dirty) > 0 {
			if t := checked.Add(gap); wake.IsZero() || t.Before(wake) {
				wake = t
			}
		}
		nctx, cancel := ctx, context.CancelFunc(func() {})
		if !wake.IsZero() {
			nctx, cancel = context.WithDeadline(ctx, wake)
		}
		b, err := sub.Next(nctx)
		cancel()
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.Is(err, context.DeadlineExceeded):
		case err != nil:
			return connError{err}
		}
		dirty = append(dirty, b.Rects...)
		if len(dirty) > maxDirty {
			u := dirty[0]
			for _, r := range dirty[1:] {
				u = u.Union(r)
			}
			dirty = append(dirty[:0], u)
		}
		for _, v := range b.Events {
			ev := Event{Event: v.Kind, Time: v.At}
			switch v.Kind {
			case vnc.EventClipboard:
				ev.Text = v.Text
			case vnc.EventResize:
				ev.Size = &Size{v.Width, v.Height}
			case vnc.EventCursor:
				if v.Cursor == nil {
					continue
				}
				sz := v.Cursor.Image.Bounds().Size()
				ev.Cursor = &Cursor{Hash: v.Cursor.Hash, X: v.Cursor.HotSpot.X, Y: v.Cursor.HotSpot.Y, W: sz.X, H: sz.Y}
			case vnc.EventBell:
			default:
				continue
			}
			if err := e.emit(ev); err != nil {
				return err
			}
			if v.Kind == vnc.EventResize {
				if err := e.Ready(c.Size(), c.Snapshot, time.Now()); err != nil {
					return err
				}
				dirty = dirty[:0]
			}
		}
		now := time.Now()
		if due := e.Due(); len(dirty) > 0 && now.Sub(checked) >= gap || !due.IsZero() && !now.Before(due) {
			if err := e.Check(c.Snapshot, dirty, now); err != nil {
				return err
			}
			dirty, checked = dirty[:0], now
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
