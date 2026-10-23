package vnc

import (
	"fmt"
	"image"
)

const zrleTile = 64

// zrleDecoder decodes ZRLE (RFC 6143 §7.7.6). One zlib stream lasts for
// the whole connection.
type zrleDecoder struct {
	z    zstream
	tile []byte // the wire pixels of one tile
	raw  []byte
	pal  [127][4]byte
	cp   [4]byte
}

func (d *zrleDecoder) decode(c *Conn, r image.Rectangle) (bool, error) {
	if err := c.fits(r); err != nil {
		return false, err
	}
	n, err := c.r.u32()
	if err != nil {
		return false, err
	}
	cb := int64(c.pf.cpixelBytes())
	tiles := int64((r.Dx()+zrleTile-1)/zrleTile) * int64((r.Dy()+zrleTile-1)/zrleTile)
	maxData := tiles*(1+127*cb) + int64(r.Dx())*int64(r.Dy())*(cb+1)
	if int64(n) > maxData+maxData>>12+maxData>>14+64 {
		return false, fmt.Errorf("%d compressed bytes is more than the rectangle can need", n)
	}
	b, err := c.r.bytes(int(n))
	if err != nil {
		return false, err
	}
	if err := d.z.feed(b); err != nil {
		return false, err
	}
	if d.tile == nil {
		d.tile = make([]byte, zrleTile*zrleTile*4)
		d.raw = make([]byte, zrleTile*zrleTile*4)
	}
	for ty := r.Min.Y; ty < r.Max.Y; ty += zrleTile {
		for tx := r.Min.X; tx < r.Max.X; tx += zrleTile {
			t := image.Rect(tx, ty, min(tx+zrleTile, r.Max.X), min(ty+zrleTile, r.Max.Y))
			if err := d.decodeTile(c, t); err != nil {
				return false, fmt.Errorf("tile at %v: %w", t.Min, err)
			}
		}
	}
	return true, nil
}

func (d *zrleDecoder) decodeTile(c *Conn, t image.Rectangle) error {
	cb, bpp, off := c.pf.cpixelBytes(), c.cv.bypp, c.pf.cpixelOffset()
	n := t.Dx() * t.Dy()
	px := d.tile[:n*bpp]
	sub, err := d.z.u8()
	if err != nil {
		return err
	}
	psize := int(sub & 127)
	if sub&128 == 0 && psize > 16 {
		return fmt.Errorf("subencoding %d is not defined", sub)
	}
	for i := 0; i < psize; i++ {
		if err := d.cpixel(d.pal[i][:], cb, off); err != nil {
			return err
		}
	}
	switch {
	case sub == 0:
		if cb == bpp {
			if err := d.z.read(px); err != nil {
				return err
			}
			break
		}
		raw := d.raw[:n*cb]
		if err := d.z.read(raw); err != nil {
			return err
		}
		for i := 0; i < n; i++ {
			copy(px[i*bpp+off:], raw[i*cb:i*cb+cb])
		}
	case sub == 1:
		c.fb.fill(t, d.pal[0][:bpp], c.cv)
		return nil
	case sub <= 16:
		bits := 4
		if psize == 2 {
			bits = 1
		} else if psize <= 4 {
			bits = 2
		}
		rowLen := (t.Dx()*bits + 7) / 8
		packed := d.raw[:rowLen*t.Dy()]
		if err := d.z.read(packed); err != nil {
			return err
		}
		i := 0
		for y := 0; y < t.Dy(); y++ {
			row := packed[y*rowLen:]
			for x := 0; x < t.Dx(); x++ {
				b := x * bits
				idx := int(row[b/8]>>(8-bits-b%8)) & (1<<bits - 1)
				if idx >= psize {
					return fmt.Errorf("palette index %d is outside a palette of %d colours", idx, psize)
				}
				copy(px[i*bpp:], d.pal[idx][:bpp])
				i++
			}
		}
	default:
		for i := 0; i < n; {
			p, run := d.cp[:bpp], 1
			if psize == 0 {
				if err := d.cpixel(d.cp[:], cb, off); err != nil {
					return err
				}
				if run, err = d.run(n - i); err != nil {
					return err
				}
			} else {
				idx, err := d.z.u8()
				if err != nil {
					return err
				}
				if int(idx&127) >= psize {
					return fmt.Errorf("palette index %d is outside a palette of %d colours", idx&127, psize)
				}
				p = d.pal[idx&127][:bpp]
				if idx&128 != 0 {
					if run, err = d.run(n - i); err != nil {
						return err
					}
				}
			}
			for ; run > 0; run-- {
				copy(px[i*bpp:], p)
				i++
			}
		}
	}
	c.fb.rows(t, px, c.cv)
	return nil
}

// cpixel reads one CPIXEL into a PIXEL at dst.
func (d *zrleDecoder) cpixel(dst []byte, cb, off int) error {
	clear(dst)
	return d.z.read(dst[off : off+cb])
}

// run reads a run length: bytes that add up to the length minus one, where
// 255 means that one more byte follows.
func (d *zrleDecoder) run(left int) (int, error) {
	n := 1
	for {
		b, err := d.z.u8()
		if err != nil {
			return 0, err
		}
		n += int(b)
		if n > left {
			return 0, fmt.Errorf("a run of %d pixels or more is longer than the %d pixels left", n, left)
		}
		if b != 255 {
			return n, nil
		}
	}
}
