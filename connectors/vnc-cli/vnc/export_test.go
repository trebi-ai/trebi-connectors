package vnc

import (
	"bufio"
	"bytes"
	"image"
	"image/color"
	"log/slog"
	"runtime"
	"testing"
)

// FuzzFormats are the pixel formats of the fuzz tests: the named formats,
// a colour map, and formats with other byte orders.
var FuzzFormats = []PixelFormat{
	FormatBGRX8888, FormatRGBX8888, FormatRGB565,
	{8, 8, false, false, 0, 0, 0, 0, 0, 0},
	{8, 8, false, true, 7, 7, 3, 0, 3, 6},
	{16, 15, true, true, 31, 31, 31, 10, 5, 0},
	{32, 24, true, true, 255, 255, 255, 16, 8, 0},
	{32, 24, false, true, 255, 255, 255, 24, 16, 8},
}

// FuzzSize is the screen size of the fuzz tests.
var FuzzSize = image.Pt(24, 20)

// CheckUpdates decodes data as the messages from the server after
// ServerInit, with only the decoder of enc. It stops at the first message
// that is not a FramebufferUpdate, or at the first error. Each rectangle
// must change no pixel outside itself, and must read and allocate no more
// than its size allows.
func CheckUpdates(t *testing.T, enc int32, pf PixelFormat, data []byte) {
	c := readConn(data)
	for y := 0; y < FuzzSize.Y; y++ {
		for x := 0; x < FuzzSize.X; x++ {
			c.fb.img.SetRGBA(x, y, color.RGBA{uint8(x * 10), uint8(y * 12), uint8(x ^ y), 255})
		}
	}
	if err := c.setFormat(pf); err != nil {
		t.Skip(err)
	}
	c.decoders = map[int32]decoder{enc: checkDecoder{t, decoderFactories[enc]()}}
	for {
		typ, err := c.r.u8()
		if err != nil || typ != msgFramebufferUpdate {
			return
		}
		if err := c.readUpdate(); err != nil {
			return
		}
	}
}

// readConn is a closed connection of FuzzSize that reads the messages of
// the server from data. A closed connection sends no update requests.
func readConn(data []byte) *Conn {
	c := &Conn{
		opts:     Options{JPEG: true},
		log:      slog.New(slog.DiscardHandler),
		fb:       newFramebuffer(FuzzSize.X, FuzzSize.Y),
		r:        newReader(bufio.NewReader(bytes.NewReader(data))),
		subs:     map[*Subscription]struct{}{},
		waiters:  map[*waiter]struct{}{},
		features: map[string]bool{},
		fences:   map[uint32]chan struct{}{},
		replies:  make(chan []byte, 64),
		done:     make(chan struct{}),
		err:      ErrClosed,
	}
	close(c.done)
	return c
}

// checkDecoder checks the limits of each rectangle that d decodes.
type checkDecoder struct {
	t *testing.T
	d decoder
}

func (k checkDecoder) decode(c *Conn, r image.Rectangle) (bool, error) {
	before := c.fb.snapshot(c.fb.bounds())
	c.r.peak = 0
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	dirty, err := k.d.decode(c, r)
	runtime.ReadMemStats(&m1)
	after := c.fb.snapshot(c.fb.bounds())

	padded := ((r.Dx() + 15) &^ 15) * ((r.Dy() + 15) &^ 15)
	if limit := 12*padded + 4096; c.r.peak > limit {
		k.t.Fatalf("rectangle %v: read of %d bytes, limit %d", r, c.r.peak, limit)
	}
	if n, limit := m1.TotalAlloc-m0.TotalAlloc, uint64(64*padded+1<<20); n > limit {
		k.t.Fatalf("rectangle %v: %d bytes allocated, limit %d", r, n, limit)
	}
	// A decoder that fails can leave a part of r written.
	if !dirty && err == nil {
		r = image.Rectangle{}
	}
	for y := before.Rect.Min.Y; y < before.Rect.Max.Y; y++ {
		for x := before.Rect.Min.X; x < before.Rect.Max.X; x++ {
			if !image.Pt(x, y).In(r) && before.RGBAAt(x, y) != after.RGBAAt(x, y) {
				k.t.Fatalf("rectangle %v (dirty %v): pixel %d,%d changed", r, dirty, x, y)
			}
		}
	}
	return dirty, err
}
