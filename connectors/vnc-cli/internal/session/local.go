// Package session runs the commands of vnc-cli on one connection, in this
// process or in a background holder.
package session

import (
	"context"
	"errors"
	"fmt"
	"image"
	"sync"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc"
)

// Input operations.
const (
	OpKey    = "key"    // Keysym, Down
	OpMove   = "move"   // X, Y; with Step, in steps of Step pixels and Wait between
	OpButton = "button" // Button (a mask), Down
	OpWait   = "wait"   // Wait
)

// Action is one input step.
type Action struct {
	Op     string        `json:"op"`
	Keysym uint32        `json:"keysym,omitempty"`
	Down   bool          `json:"down,omitempty"`
	X      int           `json:"x,omitempty"`
	Y      int           `json:"y,omitempty"`
	Button uint8         `json:"button,omitempty"`
	Wait   time.Duration `json:"wait,omitempty"`
	Step   int           `json:"step,omitempty"`
}

// Match sets how near two screens must be. Fuzz -1 takes the default of
// the pixel format.
type Match struct {
	Fuzz int `json:"fuzz"`
	Blur int `json:"blur,omitempty"`
}

func (m Match) fuzz(pf vnc.PixelFormat) int {
	if m.Fuzz < 0 {
		return pf.DefaultFuzz()
	}
	return m.Fuzz
}

// Info describes the remote screen.
type Info struct {
	Name   string            `json:"name"`
	Width  int               `json:"width"`
	Height int               `json:"height"`
	Facts  map[string]string `json:"facts"`
}

// ErrOffScreen is an area that is not fully on the screen.
var ErrOffScreen = errors.New("the area is not on the screen")

// Local runs the commands on a connection in this process. Live is true in
// a holder that keeps the screen current, so a capture does not ask for a
// full update.
type Local struct {
	conn *vnc.Conn
	live bool

	mu      sync.Mutex
	x, y    int
	buttons uint8
}

// NewLocal wraps a connection.
func NewLocal(c *vnc.Conn, live bool) *Local { return &Local{conn: c, live: live} }

// Conn is the connection.
func (l *Local) Conn() *vnc.Conn { return l.conn }

// Close closes the connection.
func (l *Local) Close() error { return l.conn.Close() }

// Info describes the screen.
func (l *Local) Info(ctx context.Context) (Info, error) {
	if err := l.fresh(ctx); err != nil {
		return Info{}, err
	}
	sz := l.conn.Size()
	return Info{Name: l.conn.Name(), Width: sz.X, Height: sz.Y, Facts: l.conn.Facts()}, nil
}

// fresh makes sure that the client has the full screen.
func (l *Local) fresh(ctx context.Context) error {
	if l.live {
		return l.conn.Err()
	}
	return l.conn.Refresh(ctx, true)
}

// Capture copies the area r of the screen. An empty r is the full screen.
func (l *Local) Capture(ctx context.Context, r image.Rectangle) (*image.RGBA, error) {
	if err := l.fresh(ctx); err != nil {
		return nil, err
	}
	if err := l.onScreen(r); err != nil {
		return nil, err
	}
	return l.conn.Snapshot(r), nil
}

func (l *Local) onScreen(r image.Rectangle) error {
	sz := l.conn.Size()
	if !r.Empty() && !r.In(image.Rectangle{Max: sz}) {
		return fmt.Errorf("%w: %v is outside %dx%d", ErrOffScreen, r, sz.X, sz.Y)
	}
	return nil
}

// Input sends the actions in order. The pointer position and the buttons
// stay from one call to the next.
func (l *Local) Input(ctx context.Context, actions []Action) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, a := range actions {
		var err error
		switch a.Op {
		case OpKey:
			err = l.conn.Key(a.Keysym, a.Down)
		case OpMove:
			err = l.move(ctx, a)
		case OpButton:
			if a.Down {
				l.buttons |= a.Button
			} else {
				l.buttons &^= a.Button
			}
			err = l.conn.Pointer(l.x, l.y, l.buttons)
		case OpWait:
			err = sleep(ctx, a.Wait)
		default:
			err = fmt.Errorf("unknown input operation %q", a.Op)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// move goes to a.X, a.Y. With a.Step, it goes in a line of steps, as
// vncdotool mouseDrag.
func (l *Local) move(ctx context.Context, a Action) error {
	if a.Step > 0 {
		ox, oy := l.x, l.y
		dx, dy := a.X-ox, a.Y-oy
		n := max(abs(dx), abs(dy))
		for s := a.Step; s < n; s += a.Step {
			if err := l.conn.Pointer(ox+dx*s/n, oy+dy*s/n, l.buttons); err != nil {
				return err
			}
			if err := sleep(ctx, a.Wait); err != nil {
				return err
			}
		}
	}
	l.x, l.y = a.X, a.Y
	return l.conn.Pointer(l.x, l.y, l.buttons)
}

func abs(v int) int { return max(v, -v) }

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Paste sets the clipboard of the remote computer.
func (l *Local) Paste(_ context.Context, text string) error { return l.conn.SetClipboard(text) }

// clipboardWait is how long a new connection waits for the clipboard that
// a server sends after the handshake.
const clipboardWait = time.Second

// Clipboard is the last clipboard text of the remote computer.
func (l *Local) Clipboard(ctx context.Context) (string, error) {
	if text := l.conn.Clipboard(); text != "" || l.live {
		return text, l.conn.Err()
	}
	sub := l.conn.Subscribe()
	defer sub.Close()
	wctx, cancel := context.WithTimeout(ctx, clipboardWait)
	defer cancel()
	for {
		b, err := sub.Next(wctx)
		if err != nil {
			if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
				return "", nil
			}
			return "", err
		}
		for _, e := range b.Events {
			if e.Kind == vnc.EventClipboard {
				return e.Text, nil
			}
		}
	}
}

// Expect waits until the area at the point matches ref.
func (l *Local) Expect(ctx context.Context, ref image.Image, at image.Point, m Match) error {
	sub := l.conn.Subscribe()
	defer sub.Close()
	if err := l.fresh(ctx); err != nil {
		return err
	}
	area := image.Rectangle{Min: at, Max: at.Add(ref.Bounds().Size())}
	for {
		if err := l.onScreen(area); err != nil {
			return err
		}
		if vnc.Match(l.conn.Snapshot(area), ref, m.fuzz(l.conn.PixelFormat()), m.Blur) {
			return nil
		}
		if err := waitChange(ctx, sub, area); err != nil {
			return err
		}
	}
}

// waitChange waits for an update that touches the area, or a resize.
func waitChange(ctx context.Context, sub *vnc.Subscription, area image.Rectangle) error {
	for {
		b, err := sub.Next(ctx)
		if err != nil {
			return err
		}
		if touches(b, area) {
			return nil
		}
	}
}

func touches(b vnc.Batch, area image.Rectangle) bool {
	for _, r := range b.Rects {
		if r.Overlaps(area) {
			return true
		}
	}
	for _, e := range b.Events {
		if e.Kind == vnc.EventResize {
			return true
		}
	}
	return false
}

// Stable waits until the area shows no change for d. A change within the
// fuzz does not count (vncdotool specs/screen-stability.md).
func (l *Local) Stable(ctx context.Context, d time.Duration, r image.Rectangle, m Match) error {
	sub := l.conn.Subscribe()
	defer sub.Close()
	if err := l.fresh(ctx); err != nil {
		return err
	}
	if r.Empty() {
		r = image.Rectangle{Max: l.conn.Size()}
	}
	if err := l.onScreen(r); err != nil {
		return err
	}
	fuzz := m.fuzz(l.conn.PixelFormat())
	base := l.conn.Snapshot(r)
	end := time.Now().Add(d)
	for {
		wctx, cancel := context.WithDeadline(ctx, end)
		err := waitChange(wctx, sub, r)
		cancel()
		switch {
		case err == nil:
		case ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded):
			return nil
		default:
			return err
		}
		if err := l.onScreen(r); err != nil {
			return err
		}
		if now := l.conn.Snapshot(r); !vnc.Match(now, base, fuzz, m.Blur) {
			base, end = now, time.Now().Add(d)
		}
	}
}
