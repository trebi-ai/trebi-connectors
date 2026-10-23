package vnc

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
)

// Tight compression control and filters (rfbproto, vncdotool
// specs/tight-wire.md).
const (
	tightFill           = 0x8
	tightJPEG           = 0x9
	tightExplicitFilter = 0x4

	tightFilterCopy     = 0
	tightFilterPalette  = 1
	tightFilterGradient = 2

	// Below this many bytes, the data is not compressed.
	tightMinToCompress = 12
	// TigerVNC before 1.16.0 sends wider Fill rectangles, so Fill is exempt.
	tightMaxWidth = 2048
)

// tpixelConverter reads TPIXELs, which are R,G,B.
var tpixelConverter, _ = newConverter(tpixelFormat, nil)

// tightDecoder decodes Tight. The four zlib streams last for the whole
// connection, until the server resets one.
type tightDecoder struct {
	z    [4]zstream
	buf  []byte
	out  []byte
	pal  []byte
	grad [2][]int32
}

func (d *tightDecoder) decode(c *Conn, r image.Rectangle) (bool, error) {
	if err := c.fits(r); err != nil {
		return false, err
	}
	ctl, err := c.r.u8()
	if err != nil {
		return false, err
	}
	// The reset bits apply to Fill and JPEG too.
	for i := range d.z {
		if ctl&(1<<i) != 0 {
			d.z[i].reset()
		}
	}
	ctl >>= 4
	cv := c.cv
	if c.pf.tpixelBytes() == 3 {
		cv = tpixelConverter
	}
	switch {
	case ctl == tightFill:
		p, err := c.r.bytes(cv.bypp)
		if err != nil {
			return false, err
		}
		c.fb.fill(r, p, cv)
		return true, nil
	case r.Dx() > tightMaxWidth:
		return false, fmt.Errorf("the rectangle is wider than %d pixels", tightMaxWidth)
	case ctl == tightJPEG:
		return true, d.jpeg(c, r)
	case ctl == 0xA, ctl == 0xE:
		return false, errors.New("BasicCompression without zlib, which the client did not ask for")
	case ctl&0x8 != 0:
		return false, fmt.Errorf("compression control 0x%x is not defined", ctl)
	}
	return true, d.basic(c, r, ctl, cv)
}

func (d *tightDecoder) basic(c *Conn, r image.Rectangle, ctl uint8, cv *converter) error {
	filter := uint8(tightFilterCopy)
	if ctl&tightExplicitFilter != 0 {
		var err error
		if filter, err = c.r.u8(); err != nil {
			return err
		}
	}
	tb, w, h := cv.bypp, r.Dx(), r.Dy()
	psize, rowLen := 0, w*tb
	switch filter {
	case tightFilterCopy:
	case tightFilterPalette:
		n, err := c.r.u8()
		if err != nil {
			return err
		}
		psize = int(n) + 1
		p, err := c.r.bytes(psize * tb)
		if err != nil {
			return err
		}
		d.pal = append(d.pal[:0], p...)
		rowLen = w
		if psize == 2 {
			rowLen = (w + 7) / 8
		}
	case tightFilterGradient:
		if c.pf.BPP != 16 && c.pf.BPP != 32 {
			return fmt.Errorf("the gradient filter needs 16 or 32 bits per pixel, not %d", c.pf.BPP)
		}
	default:
		return fmt.Errorf("filter %d is not defined", filter)
	}
	data, err := d.data(c, &d.z[ctl&3], rowLen*h)
	if err != nil {
		return err
	}
	switch filter {
	case tightFilterPalette:
		if data, err = d.unpalette(data, psize, tb, w, h, rowLen); err != nil {
			return err
		}
	case tightFilterGradient:
		d.gradient(data, w, h, cv)
	}
	c.fb.rows(r, data, cv)
	return nil
}

// data reads n bytes of filtered data: plain when short, else compressed.
func (d *tightDecoder) data(c *Conn, z *zstream, n int) ([]byte, error) {
	if n < tightMinToCompress {
		return c.r.bytes(n)
	}
	l, err := tightLength(c.r)
	if err != nil {
		return nil, err
	}
	if l > n+n>>8+64 {
		return nil, fmt.Errorf("%d compressed bytes is more than %d bytes can need", l, n)
	}
	b, err := c.r.bytes(l)
	if err != nil {
		return nil, err
	}
	if err := z.feed(b); err != nil {
		return nil, err
	}
	if cap(d.buf) < n {
		d.buf = make([]byte, n)
	}
	return d.buf[:n], z.read(d.buf[:n])
}

func (d *tightDecoder) unpalette(data []byte, psize, tb, w, h, rowLen int) ([]byte, error) {
	if cap(d.out) < w*h*tb {
		d.out = make([]byte, w*h*tb)
	}
	out := d.out[:w*h*tb]
	i := 0
	for y := 0; y < h; y++ {
		row := data[y*rowLen:]
		for x := 0; x < w; x++ {
			var idx int
			if psize == 2 {
				idx = int(row[x/8]>>(7-x%8)) & 1
			} else if idx = int(row[x]); idx >= psize {
				return nil, fmt.Errorf("palette index %d is outside a palette of %d colours", idx, psize)
			}
			copy(out[i*tb:i*tb+tb], d.pal[idx*tb:])
			i++
		}
	}
	return out, nil
}

// gradient undoes the gradient filter in place. Each channel is the data
// plus a prediction from the left, upper, and upper-left pixels, clamped to
// the channel range, modulo the range.
func (d *tightDecoder) gradient(px []byte, w, h int, cv *converter) {
	maxes := [3]int32{int32(cv.pf.RedMax), int32(cv.pf.GreenMax), int32(cv.pf.BlueMax)}
	shifts := [3]uint8{cv.pf.RedShift, cv.pf.GreenShift, cv.pf.BlueShift}
	for i := range d.grad {
		if cap(d.grad[i]) < (w+1)*3 {
			d.grad[i] = make([]int32, (w+1)*3)
		}
		d.grad[i] = d.grad[i][:(w+1)*3]
	}
	prev, cur := d.grad[0], d.grad[1]
	clear(prev)
	tb := cv.bypp
	for y := 0; y < h; y++ {
		clear(cur[:3])
		for x := 0; x < w; x++ {
			p := px[(y*w+x)*tb:]
			v := cv.value(p)
			var out uint32
			for ch := 0; ch < 3; ch++ {
				// Index x+1 holds pixel x; index 0 is the zero left edge.
				e := cur[x*3+ch] + prev[(x+1)*3+ch] - prev[x*3+ch]
				e = min(max(e, 0), maxes[ch])
				n := (int32(v>>shifts[ch]) + e) & maxes[ch]
				cur[(x+1)*3+ch] = n
				out |= uint32(n) << shifts[ch]
			}
			cv.put(p, out)
		}
		prev, cur = cur, prev
	}
}

func (d *tightDecoder) jpeg(c *Conn, r image.Rectangle) error {
	if !c.opts.JPEG {
		return errors.New("a JPEG rectangle came, but the client did not ask for JPEG")
	}
	n, err := tightLength(c.r)
	if err != nil {
		return err
	}
	// A JPEG codes 16x16 blocks. At the top quality, noise can take about
	// 10 bytes a pixel.
	if n > (r.Dx()+15)/16*(r.Dy()+15)/16*256*12+4096 {
		return fmt.Errorf("a JPEG of %d bytes is too large for the rectangle", n)
	}
	b, err := c.r.bytes(n)
	if err != nil {
		return err
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("jpeg: %w", err)
	}
	if cfg.Width != r.Dx() || cfg.Height != r.Dy() {
		return fmt.Errorf("the JPEG is %dx%d, not %dx%d", cfg.Width, cfg.Height, r.Dx(), r.Dy())
	}
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("jpeg: %w", err)
	}
	c.fb.draw(r, img)
	return nil
}

// tightLength reads a compact length: 7 bits in each of the first two
// bytes while the top bit is set, then 8 bits.
func tightLength(r *reader) (int, error) {
	n := 0
	for i := 0; i < 3; i++ {
		b, err := r.u8()
		if err != nil {
			return 0, err
		}
		if i == 2 {
			return n | int(b)<<14, nil
		}
		n |= int(b&0x7f) << (7 * i)
		if b&0x80 == 0 {
			break
		}
	}
	return n, nil
}
