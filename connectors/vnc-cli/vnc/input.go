package vnc

import (
	"encoding/binary"
	"image"
	"strings"
	"time"
)

// Pointer buttons as bits of the button mask.
const (
	ButtonLeft       uint8 = 1 << 0
	ButtonMiddle     uint8 = 1 << 1
	ButtonRight      uint8 = 1 << 2
	ButtonWheelUp    uint8 = 1 << 3
	ButtonWheelDown  uint8 = 1 << 4
	ButtonWheelLeft  uint8 = 1 << 5
	ButtonWheelRight uint8 = 1 << 6
)

// Key sends one key event. When the server takes QEMU key events and the
// key has a scan code that does not depend on the layout, the event also
// carries the scan code.
func (c *Conn) Key(keysym uint32, down bool) error {
	if b := c.qemuKey(keysym, down); b != nil {
		return c.write(b)
	}
	b := make([]byte, 8)
	b[0] = msgKeyEvent
	if down {
		b[1] = 1
	}
	binary.BigEndian.PutUint32(b[4:], keysym)
	return c.write(b)
}

// Press presses the keys in order and releases them in reverse order.
func (c *Conn) Press(keysyms ...uint32) error {
	for _, k := range keysyms {
		if err := c.Key(k, true); err != nil {
			return err
		}
	}
	for i := len(keysyms) - 1; i >= 0; i-- {
		if err := c.Key(keysyms[i], false); err != nil {
			return err
		}
	}
	return nil
}

// Pointer moves the pointer to (x, y) with the buttons in the mask down.
// Coordinates outside the screen are clamped.
func (c *Conn) Pointer(x, y int, buttons uint8) error {
	sz := c.Size()
	x = min(max(x, 0), max(sz.X-1, 0))
	y = min(max(y, 0), max(sz.Y-1, 0))
	c.mu.Lock()
	c.pointer, c.movedAt = image.Pt(x, y), time.Now()
	c.mu.Unlock()
	b := make([]byte, 6)
	b[0] = msgPointerEvent
	b[1] = buttons
	binary.BigEndian.PutUint16(b[2:], uint16(x))
	binary.BigEndian.PutUint16(b[4:], uint16(y))
	return c.write(b)
}

// SetClipboard sends text to the clipboard of the server. With the
// extended clipboard, the text is UTF-8. Basic RFB carries Latin-1 only;
// other characters become "?".
func (c *Conn) SetClipboard(text string) error {
	if c.hasFeature("extended_clipboard") {
		return c.setExtClipboard(text)
	}
	data := latin1(text)
	b := make([]byte, 8+len(data))
	b[0] = msgClientCutText
	binary.BigEndian.PutUint32(b[4:], uint32(len(data)))
	copy(b[8:], data)
	return c.write(b)
}

// Clipboard is the last clipboard text from the server.
func (c *Conn) Clipboard() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.clip
}

func (c *Conn) setClipboard(text string) {
	c.mu.Lock()
	c.clip = text
	c.mu.Unlock()
	c.publish(Event{Kind: EventClipboard, Text: text})
}

// maxClipboard bounds the clipboard text that the client accepts.
const maxClipboard = 16 << 20

func (c *Conn) readCutText() error {
	b, err := c.r.bytes(7)
	if err != nil {
		return err
	}
	n := int32(binary.BigEndian.Uint32(b[3:]))
	if n < 0 {
		if n < -maxClipboard {
			return protoErr("clipboard", "extended clipboard message of %d bytes is too long", -int64(n))
		}
		return c.readExtClipboard(int(-n))
	}
	if n > maxClipboard {
		return protoErr("clipboard", "text of %d bytes is too long", n)
	}
	data, err := c.r.bytes(int(n))
	if err != nil {
		return err
	}
	c.setClipboard(fromLatin1(data))
	return nil
}

func latin1(s string) []byte {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		if r > 0xff {
			r = '?'
		}
		out = append(out, byte(r))
	}
	return out
}

func fromLatin1(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for _, c := range b {
		sb.WriteRune(rune(c))
	}
	return sb.String()
}
