package vnc

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"image"
	"io"
)

// Encoding numbers (RFC 6143 §7.7, rfbproto).
const (
	EncRaw      int32 = 0
	EncCopyRect int32 = 1
	EncRRE      int32 = 2
	EncCoRRE    int32 = 4
	EncHextile  int32 = 5
	EncTight    int32 = 7
	EncZRLE     int32 = 16

	EncJPEGQuality0        int32 = -32 // -32 (low) to -23 (high)
	EncCompressLevel0      int32 = -256
	EncDesktopSize         int32 = -223
	EncLastRect            int32 = -224
	EncPointerPos          int32 = -232
	EncCursor              int32 = -239
	EncXCursor             int32 = -240
	EncQEMUExtKey          int32 = -258
	EncExtendedDesktopSize int32 = -308
	EncFence               int32 = -312
	EncContinuousUpdates   int32 = -313
	EncExtendedClipboard   int32 = -1063131698 // 0xC0A1E5CE
)

// EncodingNames maps the --encodings names to numbers.
var EncodingNames = map[string]int32{
	"raw": EncRaw, "copyrect": EncCopyRect, "rre": EncRRE, "corre": EncCoRRE,
	"hextile": EncHextile, "tight": EncTight, "zrle": EncZRLE,
}

// defaultEncodings is the preference order (vncdotool
// specs/default-encodings.md, plus CopyRect for moved areas).
var defaultEncodings = []int32{EncTight, EncCopyRect, EncZRLE, EncHextile, EncRaw}

// decoder decodes one rectangle. It returns true when the framebuffer
// changed. A decoder keeps state for the whole connection (zlib streams).
type decoder interface {
	decode(c *Conn, r image.Rectangle) (bool, error)
}

var decoderFactories = map[int32]func() decoder{
	EncRaw:         func() decoder { return rawDecoder{} },
	EncCopyRect:    func() decoder { return copyRectDecoder{} },
	EncDesktopSize: func() decoder { return desktopSizeDecoder{} },
}

func newDecoders() map[int32]decoder {
	m := make(map[int32]decoder, len(decoderFactories))
	for e, f := range decoderFactories {
		m[e] = f()
	}
	return m
}

// setEncodings sends the encoding list: the pixel encodings, then the
// pseudo-encodings that the client handles.
func (c *Conn) setEncodings() error {
	encs, explicit := c.opts.Encodings, true
	if encs == nil {
		encs, explicit = defaultEncodings, false
	}
	list := make([]int32, 0, len(encs)+16)
	for _, e := range encs {
		if _, ok := decoderFactories[e]; !ok {
			if explicit {
				return fmt.Errorf("encoding %d is not supported", e)
			}
			continue
		}
		list = append(list, e)
	}
	list = append(list, c.pseudoEncodings()...)
	b := make([]byte, 4+4*len(list))
	b[0] = msgSetEncodings
	binary.BigEndian.PutUint16(b[2:], uint16(len(list)))
	for i, e := range list {
		binary.BigEndian.PutUint32(b[4+4*i:], uint32(e))
	}
	c.mu.Lock()
	c.encs = list
	c.mu.Unlock()
	return c.write(b)
}

// pseudoEncodings lists the pseudo-encodings with a decoder, in a fixed order.
func (c *Conn) pseudoEncodings() []int32 {
	var out []int32
	for _, e := range []int32{EncDesktopSize, EncExtendedDesktopSize, EncLastRect, EncFence,
		EncContinuousUpdates, EncQEMUExtKey, EncExtendedClipboard} {
		if _, ok := decoderFactories[e]; ok {
			out = append(out, e)
		}
	}
	return out
}

// offered is true when the client sent encoding e.
func (c *Conn) offered(e int32) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, x := range c.encs {
		if x == e {
			return true
		}
	}
	return false
}

// fits checks that a pixel rectangle is inside the framebuffer.
func (c *Conn) fits(r image.Rectangle) error {
	if b := c.fb.bounds(); !r.In(b) && !r.Empty() {
		return fmt.Errorf("rectangle %v is outside the %dx%d screen", r, b.Dx(), b.Dy())
	}
	return nil
}

// maxRead bounds one read from the server.
const maxRead = 64 << 20

// reader reads the server stream. bytes returns a buffer that is valid
// until the next call.
type reader struct {
	br  *bufio.Reader
	buf []byte
}

func newReader(br *bufio.Reader) *reader { return &reader{br: br} }

func (r *reader) bytes(n int) ([]byte, error) {
	if n < 0 || n > maxRead {
		return nil, fmt.Errorf("read of %d bytes is too large", n)
	}
	if cap(r.buf) < n {
		r.buf = make([]byte, n)
	}
	b := r.buf[:n]
	_, err := io.ReadFull(r.br, b)
	return b, err
}

// read reads into p, which the caller owns.
func (r *reader) read(p []byte) error {
	_, err := io.ReadFull(r.br, p)
	return err
}

func (r *reader) u8() (uint8, error) { return r.br.ReadByte() }

func (r *reader) u16() (uint16, error) {
	b, err := r.bytes(2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(b), nil
}

func (r *reader) u32() (uint32, error) {
	b, err := r.bytes(4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b), nil
}

func (r *reader) discard(n int) error {
	_, err := r.br.Discard(n)
	return err
}

type rawDecoder struct{}

func (rawDecoder) decode(c *Conn, r image.Rectangle) (bool, error) {
	if err := c.fits(r); err != nil {
		return false, err
	}
	w := r.Dx() * c.cv.bypp
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row, err := c.r.bytes(w)
		if err != nil {
			return false, err
		}
		c.fb.row(r.Min.X, y, row, r.Dx(), c.cv)
	}
	return true, nil
}

type copyRectDecoder struct{}

func (copyRectDecoder) decode(c *Conn, r image.Rectangle) (bool, error) {
	b, err := c.r.bytes(4)
	if err != nil {
		return false, err
	}
	sp := image.Pt(int(binary.BigEndian.Uint16(b)), int(binary.BigEndian.Uint16(b[2:])))
	if err := c.fits(r); err != nil {
		return false, err
	}
	if err := c.fits(r.Sub(r.Min).Add(sp)); err != nil {
		return false, fmt.Errorf("copy source: %w", err)
	}
	c.fb.copyRect(sp, r)
	return true, nil
}

type desktopSizeDecoder struct{}

func (desktopSizeDecoder) decode(c *Conn, r image.Rectangle) (bool, error) {
	c.resize(r.Dx(), r.Dy())
	return false, nil
}
