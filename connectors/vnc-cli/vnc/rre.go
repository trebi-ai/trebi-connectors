package vnc

import (
	"encoding/binary"
	"fmt"
	"image"
)

// rreDecoder decodes RRE, and CoRRE when compact (RFC 6143 §7.7.3).
type rreDecoder struct{ compact bool }

func (d rreDecoder) decode(c *Conn, r image.Rectangle) (bool, error) {
	if err := c.fits(r); err != nil {
		return false, err
	}
	bpp := c.cv.bypp
	h, err := c.r.bytes(4 + bpp)
	if err != nil {
		return false, err
	}
	n := int64(binary.BigEndian.Uint32(h))
	if n > int64(r.Dx())*int64(r.Dy()) {
		return false, fmt.Errorf("%d subrectangles do not fit", n)
	}
	c.fb.fill(r, h[4:], c.cv)
	size := bpp + 8
	if d.compact {
		size = bpp + 4
	}
	for i := int64(0); i < n; i++ {
		b, err := c.r.bytes(size)
		if err != nil {
			return false, err
		}
		g := b[bpp:]
		var s image.Rectangle
		if d.compact {
			s = image.Rect(int(g[0]), int(g[1]), int(g[0])+int(g[2]), int(g[1])+int(g[3]))
		} else {
			x, y := int(binary.BigEndian.Uint16(g)), int(binary.BigEndian.Uint16(g[2:]))
			s = image.Rect(x, y, x+int(binary.BigEndian.Uint16(g[4:])), y+int(binary.BigEndian.Uint16(g[6:])))
		}
		s = s.Add(r.Min)
		if !s.In(r) && !s.Empty() {
			return false, fmt.Errorf("subrectangle %v is outside the rectangle", s)
		}
		c.fb.fill(s, b[:bpp], c.cv)
	}
	return true, nil
}
