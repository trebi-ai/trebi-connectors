package vnc

import (
	"fmt"
	"image"
)

// Hextile subencoding bits (RFC 6143 §7.7.4).
const (
	hextileRaw        = 1
	hextileBackground = 2
	hextileForeground = 4
	hextileAnySubrect = 8
	hextileColoured   = 16
)

type hextileDecoder struct{}

func (hextileDecoder) decode(c *Conn, r image.Rectangle) (bool, error) {
	if err := c.fits(r); err != nil {
		return false, err
	}
	bpp := c.cv.bypp
	var bg, fg [4]byte
	hasBg, hasFg := false, false
	for ty := r.Min.Y; ty < r.Max.Y; ty += 16 {
		for tx := r.Min.X; tx < r.Max.X; tx += 16 {
			t := image.Rect(tx, ty, min(tx+16, r.Max.X), min(ty+16, r.Max.Y))
			m, err := c.r.u8()
			if err != nil {
				return false, err
			}
			if m&hextileRaw != 0 {
				b, err := c.r.bytes(t.Dx() * t.Dy() * bpp)
				if err != nil {
					return false, err
				}
				c.fb.rows(t, b, c.cv)
				hasBg, hasFg = false, false
				continue
			}
			if m&hextileBackground != 0 {
				if err := c.r.read(bg[:bpp]); err != nil {
					return false, err
				}
				hasBg = true
			}
			if !hasBg {
				return false, fmt.Errorf("tile at %v has no background colour", t.Min)
			}
			c.fb.fill(t, bg[:bpp], c.cv)
			if m&hextileForeground != 0 {
				if err := c.r.read(fg[:bpp]); err != nil {
					return false, err
				}
				hasFg = true
			}
			if m&hextileAnySubrect == 0 {
				continue
			}
			n, err := c.r.u8()
			if err != nil {
				return false, err
			}
			coloured := m&hextileColoured != 0
			if n > 0 && !coloured && !hasFg {
				return false, fmt.Errorf("tile at %v has no foreground colour", t.Min)
			}
			size := 2
			if coloured {
				size += bpp
			}
			b, err := c.r.bytes(int(n) * size)
			if err != nil {
				return false, err
			}
			for i := 0; i < int(n); i++ {
				s, p := b[i*size:], fg[:bpp]
				if coloured {
					p, s = s[:bpp], s[bpp:]
				}
				x, y := int(s[0]>>4), int(s[0]&15)
				sr := image.Rect(x, y, x+int(s[1]>>4)+1, y+int(s[1]&15)+1).Add(t.Min)
				if !sr.In(t) {
					return false, fmt.Errorf("subrectangle %v is outside the tile %v", sr, t)
				}
				c.fb.fill(sr, p, c.cv)
			}
			if coloured {
				hasFg = false
			}
		}
	}
	return true, nil
}
