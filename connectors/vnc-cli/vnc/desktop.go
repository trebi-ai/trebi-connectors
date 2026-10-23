package vnc

import (
	"context"
	"encoding/binary"
	"fmt"
	"image"
)

// Screen is one screen of the ExtendedDesktopSize layout.
type Screen struct {
	ID    uint32
	Rect  image.Rectangle
	Flags uint32
}

// SetDesktopSize result codes (rfbproto ExtendedDesktopSize).
const (
	ResizeProhibited     uint16 = 1
	ResizeOutOfResources uint16 = 2
	ResizeInvalidLayout  uint16 = 3
)

// ResizeError is a refusal of SetDesktopSize.
type ResizeError struct{ Status uint16 }

func (e *ResizeError) Error() string {
	switch e.Status {
	case ResizeProhibited:
		return "the server does not allow a resize"
	case ResizeOutOfResources:
		return "the server has no resources for this size"
	case ResizeInvalidLayout:
		return "the server refused the screen layout"
	}
	return fmt.Sprintf("the server refused the resize with status %d", e.Status)
}

const msgSetDesktopSize = 251

// extDesktopSizeDecoder reads ExtendedDesktopSize: x is the reason, y the
// status, then the screens.
type extDesktopSizeDecoder struct{}

func (extDesktopSizeDecoder) decode(c *Conn, r image.Rectangle) (bool, error) {
	b, err := c.r.bytes(4)
	if err != nil {
		return false, err
	}
	b, err = c.r.bytes(int(b[0]) * 16)
	if err != nil {
		return false, err
	}
	screens := make([]Screen, len(b)/16)
	for i := range screens {
		s := b[i*16:]
		x, y := int(binary.BigEndian.Uint16(s[4:])), int(binary.BigEndian.Uint16(s[6:]))
		screens[i] = Screen{
			ID:    binary.BigEndian.Uint32(s),
			Rect:  image.Rect(x, y, x+int(binary.BigEndian.Uint16(s[8:])), y+int(binary.BigEndian.Uint16(s[10:]))),
			Flags: binary.BigEndian.Uint32(s[12:]),
		}
	}
	reason, status := r.Min.X, uint16(r.Min.Y)
	if status == 0 {
		c.resize(r.Dx(), r.Dy())
	}
	c.mu.Lock()
	c.features["extended_desktop_size"] = true
	if status == 0 {
		c.screens = screens
	}
	if reason == 1 && c.resizeWait != nil {
		c.resizeWait <- status
		c.resizeWait = nil
	}
	c.mu.Unlock()
	return false, nil
}

// Screens is the last screen layout from the server. It is empty when the
// server does not support ExtendedDesktopSize.
func (c *Conn) Screens() []Screen {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Screen(nil), c.screens...)
}

// SetDesktopSize asks the server to change the screen size to one screen
// of w x h, and waits for the answer. A refusal is a *ResizeError.
func (c *Conn) SetDesktopSize(ctx context.Context, w, h int) error {
	if !c.hasFeature("extended_desktop_size") {
		return ErrUnsupported
	}
	if w <= 0 || h <= 0 || w > 0xffff || h > 0xffff {
		return fmt.Errorf("size %dx%d is not valid", w, h)
	}
	c.resizeMu.Lock()
	defer c.resizeMu.Unlock()
	ch := make(chan uint16, 1)
	c.mu.Lock()
	c.resizeWait = ch
	var first Screen
	if len(c.screens) > 0 {
		first = c.screens[0]
	}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.resizeWait == ch {
			c.resizeWait = nil
		}
		c.mu.Unlock()
	}()
	b := make([]byte, 24)
	b[0] = msgSetDesktopSize
	binary.BigEndian.PutUint16(b[2:], uint16(w))
	binary.BigEndian.PutUint16(b[4:], uint16(h))
	b[6] = 1
	binary.BigEndian.PutUint32(b[8:], first.ID)
	binary.BigEndian.PutUint16(b[16:], uint16(w))
	binary.BigEndian.PutUint16(b[18:], uint16(h))
	binary.BigEndian.PutUint32(b[20:], first.Flags)
	if err := c.write(b); err != nil {
		return err
	}
	select {
	case s := <-ch:
		if s != 0 {
			return &ResizeError{Status: s}
		}
		return nil
	case <-c.done:
		return c.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}
