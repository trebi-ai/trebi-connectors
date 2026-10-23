package vnctest

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"image"
	"io"
	"slices"
	"strings"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc"
)

// Extensions turn on RFB extensions. The server uses one only when the
// client offers it too.
type Extensions struct {
	QEMUKeys            bool
	Fence               bool
	ContinuousUpdates   bool
	ExtendedDesktopSize bool
	// ResizeStatus is the answer to SetDesktopSize. Zero accepts it.
	ResizeStatus uint16
	// ExtendedClipboard sends UTF-8 text. ClipboardNotify announces new
	// text with a notify, so the client must ask for it. ClipboardLimit is
	// the largest text that the server takes (default 1 MiB).
	ExtendedClipboard bool
	ClipboardNotify   bool
	ClipboardLimit    uint32
	// TightGradient sends Tight rectangles of many colours with the
	// gradient filter. TightReset resets the zlib stream of each Tight
	// rectangle.
	TightGradient bool
	TightReset    bool
}

// serverExt is the server state of the extensions, under s.mu.
type serverExt struct {
	clip string
}

// clientExt is the client state of the encoders and the extensions, under
// s.mu.
type clientExt struct {
	zrle   zstream
	tight  [4]zstream
	copies []copyOp

	qemu       bool
	desktop    []desktopRect
	cursor     []byte
	pointer    *image.Point
	continuous bool
	clipCaps   uint32 // from the client; zero without the extended clipboard
}

// desktopRect is an ExtendedDesktopSize rectangle to send.
type desktopRect struct{ reason, status uint16 }

func (x *clientExt) pending() bool {
	return len(x.copies) > 0 || x.qemu || len(x.desktop) > 0 || x.cursor != nil || x.pointer != nil
}

// Message types of the extensions, the same in both directions.
const (
	msgContinuous     = 150
	msgFence          = 248
	msgSetDesktopSize = 251
	msgQEMU           = 255
)

// Extended clipboard flags.
const (
	clipText    = 1 << 0
	clipCaps    = 1 << 24
	clipRequest = 1 << 25
	clipPeek    = 1 << 26
	clipNotify  = 1 << 27
	clipProvide = 1 << 28
)

func (c *client) offered(e int32) bool { return slices.Contains(c.encs, e) }

// encodingsSet announces the extensions that both sides have, after a
// SetEncodings.
func (c *client) encodingsSet() error {
	e := c.s.opts.Ext
	c.s.mu.Lock()
	c.x.qemu = e.QEMUKeys && c.offered(vnc.EncQEMUExtKey)
	if e.ExtendedDesktopSize && c.offered(vnc.EncExtendedDesktopSize) {
		c.x.desktop = append(c.x.desktop, desktopRect{})
	}
	fence := e.Fence && c.offered(vnc.EncFence)
	cu := e.ContinuousUpdates && c.offered(vnc.EncContinuousUpdates)
	clip := e.ExtendedClipboard && c.offered(vnc.EncExtendedClipboard)
	c.s.mu.Unlock()
	c.s.kick()
	if fence {
		if err := c.write(fenceMessage(1<<31|1, []byte("vt"))); err != nil {
			return err
		}
	}
	if cu {
		if err := c.write([]byte{msgContinuous}); err != nil {
			return err
		}
	}
	if clip {
		limit := e.ClipboardLimit
		if limit == 0 {
			limit = 1 << 20
		}
		caps := uint32(clipCaps | clipRequest | clipPeek | clipNotify | clipProvide | clipText)
		return c.write(clipMessage(caps, binary.BigEndian.AppendUint32(nil, limit)))
	}
	return nil
}

func fenceMessage(flags uint32, payload []byte) []byte {
	b := make([]byte, 9, 9+len(payload))
	b[0] = msgFence
	binary.BigEndian.PutUint32(b[4:], flags)
	b[8] = byte(len(payload))
	return append(b, payload...)
}

// clipMessage is a ServerCutText with a negative length.
func clipMessage(flags uint32, payload []byte) []byte {
	b := make([]byte, 12, 12+len(payload))
	b[0] = 3
	binary.BigEndian.PutUint32(b[4:], uint32(-int32(4+len(payload))))
	binary.BigEndian.PutUint32(b[8:], flags)
	return append(b, payload...)
}

func provideMessage(text string) []byte {
	data := append([]byte(strings.ReplaceAll(text, "\n", "\r\n")), 0)
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	zw.Write(binary.BigEndian.AppendUint32(nil, uint32(len(data))))
	zw.Write(data)
	zw.Close()
	return clipMessage(clipProvide|clipText, buf.Bytes())
}

// readExt reads a client message of an extension.
func (c *client) readExt(t byte) error {
	switch t {
	case msgFence:
		b, err := c.read(8)
		if err != nil {
			return err
		}
		flags := binary.BigEndian.Uint32(b[3:])
		payload, err := c.read(int(b[7]))
		if err != nil {
			return err
		}
		if flags&(1<<31) != 0 {
			return c.write(fenceMessage(flags&7, payload))
		}
		return nil
	case msgContinuous:
		b, err := c.read(9)
		if err != nil {
			return err
		}
		c.s.mu.Lock()
		c.x.continuous = b[0] != 0
		c.want = c.want || c.x.continuous
		c.s.mu.Unlock()
		if b[0] == 0 {
			return c.write([]byte{msgContinuous})
		}
		c.s.kick()
		return nil
	case msgSetDesktopSize:
		b, err := c.read(7)
		if err != nil {
			return err
		}
		if _, err := c.read(int(b[5]) * 16); err != nil {
			return err
		}
		w, h := int(binary.BigEndian.Uint16(b[1:])), int(binary.BigEndian.Uint16(b[3:]))
		if st := c.s.opts.Ext.ResizeStatus; st != 0 {
			c.s.mu.Lock()
			c.x.desktop = append(c.x.desktop, desktopRect{1, st})
			c.s.mu.Unlock()
			c.s.kick()
			return nil
		}
		c.s.resize(w, h, c)
		return nil
	case msgQEMU:
		b, err := c.read(11)
		if err != nil {
			return err
		}
		if b[0] != 0 {
			return fmt.Errorf("vnctest: unknown QEMU message %d", b[0])
		}
		c.s.record(InputEvent{Kind: "key", Down: binary.BigEndian.Uint16(b[1:]) != 0,
			Keysym: binary.BigEndian.Uint32(b[3:]), Scancode: binary.BigEndian.Uint32(b[7:])})
		return nil
	}
	return fmt.Errorf("vnctest: unknown client message %d", t)
}

// readExtClipboard reads a ClientCutText with a negative length n.
func (c *client) readExtClipboard(n int) error {
	b, err := c.read(n)
	if err != nil {
		return err
	}
	if n < 4 {
		return fmt.Errorf("vnctest: extended clipboard message of %d bytes", n)
	}
	flags, payload := binary.BigEndian.Uint32(b), b[4:]
	c.s.mu.Lock()
	text := c.s.x.clip
	c.s.mu.Unlock()
	switch {
	case flags&clipCaps != 0:
		c.s.mu.Lock()
		c.x.clipCaps = flags
		c.s.mu.Unlock()
	case flags&clipProvide != 0 && flags&clipText != 0:
		zr, err := zlib.NewReader(bytes.NewReader(payload))
		if err != nil {
			return err
		}
		data, err := io.ReadAll(zr)
		if err != nil || len(data) < 4 {
			return fmt.Errorf("vnctest: bad provide message: %v", err)
		}
		data, _ = bytes.CutSuffix(data[4:4+binary.BigEndian.Uint32(data)], []byte{0})
		c.s.record(InputEvent{Kind: "cut", Text: strings.ReplaceAll(string(data), "\r\n", "\n")})
	case flags&clipNotify != 0 && flags&clipText != 0:
		return c.write(clipMessage(clipRequest|clipText, nil))
	case flags&clipRequest != 0:
		return c.write(provideMessage(text))
	case flags&clipPeek != 0:
		f := uint32(clipNotify)
		if text != "" {
			f |= clipText
		}
		return c.write(clipMessage(f, nil))
	}
	return nil
}

// SetClipboard sets the clipboard of the server and announces it: with
// the extended clipboard when the client has it, else as Latin-1.
func (s *Server) SetClipboard(text string) {
	s.mu.Lock()
	s.x.clip = text
	type out struct {
		c *client
		b []byte
	}
	var outs []out
	for c := range s.clients {
		caps := c.x.clipCaps
		switch {
		case caps == 0:
			var data []byte
			for _, r := range text {
				data = append(data, byte(r))
			}
			b := binary.BigEndian.AppendUint32([]byte{3, 0, 0, 0}, uint32(len(data)))
			outs = append(outs, out{c, append(b, data...)})
		case s.opts.Ext.ClipboardNotify && caps&clipNotify != 0:
			outs = append(outs, out{c, clipMessage(clipNotify|clipText, nil)})
		case caps&clipProvide != 0:
			outs = append(outs, out{c, provideMessage(text)})
		}
	}
	s.mu.Unlock()
	for _, o := range outs {
		o.c.write(o.b)
	}
}

// resize changes the screen size for a SetDesktopSize from one client.
func (s *Server) resize(w, h int, from *client) {
	s.mu.Lock()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	copyScreen(img, s.screen)
	s.screen = img
	for c := range s.clients {
		c.dirty = append(c.dirty[:0], img.Rect)
		switch {
		case c == from:
			c.x.desktop = append(c.x.desktop, desktopRect{1, 0})
		case s.opts.Ext.ExtendedDesktopSize && c.offered(vnc.EncExtendedDesktopSize):
			c.x.desktop = append(c.x.desktop, desktopRect{2, 0})
		default:
			c.resized = true
		}
	}
	s.mu.Unlock()
	s.kick()
}

func copyScreen(dst, src *image.RGBA) {
	r := dst.Rect.Intersect(src.Rect)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		copy(dst.Pix[dst.PixOffset(0, y):][:r.Dx()*4], src.Pix[src.PixOffset(0, y):])
	}
}

// resizeRects encodes the size changes. They come first in an update. It
// runs under s.mu.
func (c *client) resizeRects() [][]byte {
	var out [][]byte
	eds := c.s.opts.Ext.ExtendedDesktopSize && c.offered(vnc.EncExtendedDesktopSize)
	if c.resized {
		switch {
		case eds:
			c.x.desktop = append([]desktopRect{{}}, c.x.desktop...)
		case c.offered(vnc.EncDesktopSize):
			out = append(out, rectHeader(c.s.screen.Rect, vnc.EncDesktopSize))
		}
	}
	for _, d := range c.x.desktop {
		w, h := c.s.screen.Rect.Dx(), c.s.screen.Rect.Dy()
		b := rectHeader(image.Rect(int(d.reason), int(d.status), int(d.reason)+w, int(d.status)+h), vnc.EncExtendedDesktopSize)
		b = append(b, 1, 0, 0, 0)
		b = binary.BigEndian.AppendUint32(b, 1)
		b = binary.BigEndian.AppendUint64(b, uint64(w)<<16|uint64(h))
		out = append(out, binary.BigEndian.AppendUint32(b, 0))
	}
	c.x.desktop = nil
	return out
}

// pseudoRects encodes the QEMU key, cursor, and pointer rectangles. It runs
// under s.mu.
func (c *client) pseudoRects() [][]byte {
	var out [][]byte
	if c.x.qemu {
		out = append(out, rectHeader(image.Rectangle{}, vnc.EncQEMUExtKey))
		c.x.qemu = false
	}
	if c.x.cursor != nil {
		out = append(out, c.x.cursor)
		c.x.cursor = nil
	}
	if p := c.x.pointer; p != nil {
		out = append(out, rectHeader(image.Rectangle{Min: *p, Max: *p}, vnc.EncPointerPos))
		c.x.pointer = nil
	}
	return out
}

// SetCursor sends a pointer shape with its hot spot. Pixels with alpha
// under 128 are clear. A client that offered only XCursor gets the shape
// in two colours.
func (s *Server) SetCursor(img *image.RGBA, hot image.Point) {
	s.mu.Lock()
	r := image.Rectangle{Min: hot, Max: hot.Add(img.Rect.Size())}
	w, h, mrow := img.Rect.Dx(), img.Rect.Dy(), (img.Rect.Dx()+7)/8
	mask := make([]byte, mrow*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if img.RGBAAt(img.Rect.Min.X+x, img.Rect.Min.Y+y).A >= 128 {
				mask[y*mrow+x/8] |= 0x80 >> (x % 8)
			}
		}
	}
	for c := range s.clients {
		switch {
		case c.offered(vnc.EncCursor):
			wr := newWire(c.pf)
			b := rectHeader(r, vnc.EncCursor)
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					b = wr.pixel(b, wr.value(img.RGBAAt(img.Rect.Min.X+x, img.Rect.Min.Y+y)))
				}
			}
			c.x.cursor = append(b, mask...)
		case c.offered(vnc.EncXCursor):
			c.x.cursor = xcursor(img, r, mask)
		}
	}
	s.mu.Unlock()
	s.kick()
}

// xcursor encodes the shape with the first opaque colour as the
// foreground and the next other one as the background.
func xcursor(img *image.RGBA, r image.Rectangle, mask []byte) []byte {
	w, h, mrow := r.Dx(), r.Dy(), (r.Dx()+7)/8
	var cols [][3]uint8
	bitmap := make([]byte, mrow*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			p := img.RGBAAt(img.Rect.Min.X+x, img.Rect.Min.Y+y)
			if p.A < 128 {
				continue
			}
			col := [3]uint8{p.R, p.G, p.B}
			if !slices.Contains(cols, col) {
				cols = append(cols, col)
			}
			if col == cols[0] {
				bitmap[y*mrow+x/8] |= 0x80 >> (x % 8)
			}
		}
	}
	cols = append(cols, [3]uint8{}, [3]uint8{})
	b := rectHeader(r, vnc.EncXCursor)
	b = append(b, cols[0][:]...)
	b = append(b, cols[1][:]...)
	return append(append(b, bitmap...), mask...)
}

// MovePointer tells the clients that offered PointerPos that the pointer
// moved.
func (s *Server) MovePointer(p image.Point) {
	s.mu.Lock()
	for c := range s.clients {
		if c.offered(vnc.EncPointerPos) {
			c.x.pointer = &p
		}
	}
	s.mu.Unlock()
	s.kick()
}
