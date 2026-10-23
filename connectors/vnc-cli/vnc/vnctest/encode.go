package vnctest

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"maps"
	"math/bits"
	"slices"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc"
)

// wire writes pixel values in the client pixel format.
type wire struct {
	pf     vnc.PixelFormat
	bpp    int
	cshift int // the shift of the 3 CPIXEL bytes, or -1 for a full PIXEL
	tpixel bool
}

func newWire(pf vnc.PixelFormat) wire {
	w := wire{pf: pf, bpp: int(pf.BPP) / 8, cshift: -1}
	if !pf.TrueColor || pf.BPP != 32 {
		return w
	}
	width := func(m uint16) int { return bits.Len16(m) }
	if pf.Depth <= 24 {
		switch {
		case int(pf.RedShift)+width(pf.RedMax) <= 24 && int(pf.GreenShift)+width(pf.GreenMax) <= 24 && int(pf.BlueShift)+width(pf.BlueMax) <= 24:
			w.cshift = 0
		case pf.RedShift >= 8 && pf.GreenShift >= 8 && pf.BlueShift >= 8:
			w.cshift = 8
		}
	}
	w.tpixel = pf.Depth == 24 && pf.RedMax == 255 && pf.GreenMax == 255 && pf.BlueMax == 255
	return w
}

func (w wire) value(col color.RGBA) uint32 {
	scale := func(v uint8, max uint16) uint32 { return (uint32(v)*uint32(max) + 127) / 255 }
	return scale(col.R, w.pf.RedMax)<<w.pf.RedShift | scale(col.G, w.pf.GreenMax)<<w.pf.GreenShift |
		scale(col.B, w.pf.BlueMax)<<w.pf.BlueShift
}

// bytes appends the n low bytes of v in the byte order of the format.
func (w wire) bytes(b []byte, v uint32, n int) []byte {
	for i := 0; i < n; i++ {
		shift := 8 * i
		if w.pf.BigEndian {
			shift = 8 * (n - 1 - i)
		}
		b = append(b, byte(v>>shift))
	}
	return b
}

func (w wire) pixel(b []byte, v uint32) []byte { return w.bytes(b, v, w.bpp) }

func (w wire) cpixel(b []byte, v uint32) []byte {
	if w.cshift < 0 {
		return w.pixel(b, v)
	}
	return w.bytes(b, v>>w.cshift, 3)
}

func (w wire) cpixelSize() int {
	if w.cshift < 0 {
		return w.bpp
	}
	return 3
}

func (w wire) tpixelOf(b []byte, v uint32) []byte {
	if !w.tpixel {
		return w.pixel(b, v)
	}
	return append(b, byte(v>>w.pf.RedShift), byte(v>>w.pf.GreenShift), byte(v>>w.pf.BlueShift))
}

// values reads the screen area r as pixel values.
func (c *client) values(w wire, r image.Rectangle) []uint32 {
	out := make([]uint32, 0, r.Dx()*r.Dy())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			out = append(out, w.value(c.s.screen.RGBAAt(x, y)))
		}
	}
	return out
}

// encoding is the first encoding in the client list that vnctest sends.
func (c *client) encoding() int32 {
	for _, e := range c.encs {
		switch e {
		case vnc.EncRaw, vnc.EncRRE, vnc.EncCoRRE, vnc.EncHextile, vnc.EncZRLE, vnc.EncTight:
			return e
		}
	}
	return vnc.EncRaw
}

// encodeRect encodes the screen area r as rectangles with headers. It runs
// under s.mu.
func (c *client) encodeRect(r image.Rectangle) [][]byte {
	w := newWire(c.pf)
	switch e := c.encoding(); e {
	case vnc.EncRRE:
		return [][]byte{append(rectHeader(r, e), c.rre(w, r, false)...)}
	case vnc.EncCoRRE:
		return c.split(r, 255, 255, func(t image.Rectangle) []byte {
			return append(rectHeader(t, e), c.rre(w, t, true)...)
		})
	case vnc.EncHextile:
		return [][]byte{append(rectHeader(r, e), c.hextile(w, r)...)}
	case vnc.EncZRLE:
		return [][]byte{append(rectHeader(r, e), c.zrle(w, r)...)}
	case vnc.EncTight:
		return c.split(r, 2048, r.Dy(), func(t image.Rectangle) []byte {
			return append(rectHeader(t, e), c.tight(w, t)...)
		})
	}
	return [][]byte{append(rectHeader(r, vnc.EncRaw), c.pixels(r)...)}
}

// split cuts r into tiles of at most tw x th.
func (c *client) split(r image.Rectangle, tw, th int, f func(image.Rectangle) []byte) [][]byte {
	var out [][]byte
	for y := r.Min.Y; y < r.Max.Y; y += th {
		for x := r.Min.X; x < r.Max.X; x += tw {
			out = append(out, f(image.Rect(x, y, min(x+tw, r.Max.X), min(y+th, r.Max.Y))))
		}
	}
	return out
}

// mostCommon is the value that occurs most often.
func mostCommon(vals []uint32) uint32 {
	n := map[uint32]int{}
	best := vals[0]
	for _, v := range vals {
		n[v]++
		if n[v] > n[best] {
			best = v
		}
	}
	return best
}

// runs are the horizontal runs of one value that is not bg, as rectangles
// relative to the area of w x h.
type run struct {
	v uint32
	r image.Rectangle
}

func runs(vals []uint32, w, h int, bg uint32) []run {
	var out []run
	for y := 0; y < h; y++ {
		for x := 0; x < w; {
			v, x0 := vals[y*w+x], x
			for x < w && vals[y*w+x] == v {
				x++
			}
			if v != bg {
				out = append(out, run{v, image.Rect(x0, y, x, y+1)})
			}
		}
	}
	return out
}

func (c *client) rre(w wire, r image.Rectangle, compact bool) []byte {
	vals := c.values(w, r)
	bg := uint32(0)
	if len(vals) > 0 {
		bg = mostCommon(vals)
	}
	subs := runs(vals, r.Dx(), r.Dy(), bg)
	out := binary.BigEndian.AppendUint32(nil, uint32(len(subs)))
	out = w.pixel(out, bg)
	for _, s := range subs {
		out = w.pixel(out, s.v)
		if compact {
			out = append(out, byte(s.r.Min.X), byte(s.r.Min.Y), byte(s.r.Dx()), byte(s.r.Dy()))
			continue
		}
		for _, n := range []int{s.r.Min.X, s.r.Min.Y, s.r.Dx(), s.r.Dy()} {
			out = binary.BigEndian.AppendUint16(out, uint16(n))
		}
	}
	return out
}

// hextile encodes 16x16 tiles. It keeps the background and the foreground
// from tile to tile, as the protocol allows.
func (c *client) hextile(w wire, r image.Rectangle) []byte {
	var out []byte
	var bg, fg uint32
	bgOK, fgOK := false, false
	for _, t := range tiles(r, 16) {
		vals := c.values(w, t)
		colours := map[uint32]bool{}
		for _, v := range vals {
			colours[v] = true
		}
		tbg := mostCommon(vals)
		subs := runs(vals, t.Dx(), t.Dy(), tbg)
		coloured := len(colours) > 2
		size := 2
		if coloured {
			size += w.bpp
		}
		if len(subs) > 255 || len(subs)*size+2*w.bpp+1 > t.Dx()*t.Dy()*w.bpp {
			out = append(out, 1)
			out = append(out, c.pixels(t)...)
			bgOK, fgOK = false, false
			continue
		}
		var m byte
		mi := len(out)
		out = append(out, 0)
		if !bgOK || bg != tbg {
			m |= 2
			out = w.pixel(out, tbg)
			bg, bgOK = tbg, true
		}
		if len(subs) == 0 {
			out[mi] = m
			continue
		}
		m |= 8
		if coloured {
			m |= 16
		} else if v := subs[0].v; !fgOK || fg != v {
			m |= 4
			out = w.pixel(out, v)
			fg, fgOK = v, true
		}
		out[mi] = m
		out = append(out, byte(len(subs)))
		for _, s := range subs {
			if coloured {
				out = w.pixel(out, s.v)
			}
			out = append(out, byte(s.r.Min.X<<4|s.r.Min.Y), byte((s.r.Dx()-1)<<4|(s.r.Dy()-1)))
		}
		if coloured {
			fgOK = false
		}
	}
	return out
}

func tiles(r image.Rectangle, size int) []image.Rectangle {
	var out []image.Rectangle
	for y := r.Min.Y; y < r.Max.Y; y += size {
		for x := r.Min.X; x < r.Max.X; x += size {
			out = append(out, image.Rect(x, y, min(x+size, r.Max.X), min(y+size, r.Max.Y)))
		}
	}
	return out
}

// zstream is a zlib stream that lasts for the session. Each rectangle ends
// with a sync flush.
type zstream struct {
	buf bytes.Buffer
	zw  *zlib.Writer
}

func (z *zstream) compress(b []byte) []byte {
	if z.zw == nil {
		z.zw = zlib.NewWriter(&z.buf)
	}
	z.zw.Write(b)
	z.zw.Flush()
	out := bytes.Clone(z.buf.Bytes())
	z.buf.Reset()
	return out
}

// zrle encodes 64x64 tiles. Each tile takes the smallest subencoding.
func (c *client) zrle(w wire, r image.Rectangle) []byte {
	var data []byte
	cb := w.cpixelSize()
	for _, t := range tiles(r, 64) {
		vals := c.values(w, t)
		pal := palette(vals, 127)
		type rl struct {
			v uint32
			n int
		}
		var rls []rl
		for _, v := range vals {
			if len(rls) > 0 && rls[len(rls)-1].v == v {
				rls[len(rls)-1].n++
			} else {
				rls = append(rls, rl{v, 1})
			}
		}
		runBytes := func(n int) int { return (n-1)/255 + 1 }
		appendRun := func(b []byte, n int) []byte {
			for n--; n >= 255; n -= 255 {
				b = append(b, 255)
			}
			return append(b, byte(n))
		}
		if len(pal) == 1 {
			data = w.cpixel(append(data, 1), vals[0])
			continue
		}
		rawSize := len(vals) * cb
		plainSize := 0
		for _, x := range rls {
			plainSize += cb + runBytes(x.n)
		}
		best, sub := rawSize, 0
		if plainSize < best {
			best, sub = plainSize, 128
		}
		bitsPer := 0
		if pal != nil {
			palRLE := len(pal) * cb
			for _, x := range rls {
				palRLE++
				if x.n > 1 {
					palRLE += runBytes(x.n)
				}
			}
			if palRLE < best {
				best, sub = palRLE, 128+len(pal)
			}
			if len(pal) <= 16 {
				bitsPer = 4
				if len(pal) == 2 {
					bitsPer = 1
				} else if len(pal) <= 4 {
					bitsPer = 2
				}
				if packed := len(pal)*cb + (t.Dx()*bitsPer+7)/8*t.Dy(); packed < best {
					sub = len(pal)
				}
			}
		}
		data = append(data, byte(sub))
		index := map[uint32]int{}
		for i, v := range pal {
			index[v] = i
			if sub > 1 && sub != 128 {
				data = w.cpixel(data, v)
			}
		}
		switch {
		case sub == 0:
			for _, v := range vals {
				data = w.cpixel(data, v)
			}
		case sub <= 16:
			for y := 0; y < t.Dy(); y++ {
				var acc, nb int
				for x := 0; x < t.Dx(); x++ {
					acc, nb = acc<<bitsPer|index[vals[y*t.Dx()+x]], nb+bitsPer
					if nb == 8 {
						data, acc, nb = append(data, byte(acc)), 0, 0
					}
				}
				if nb > 0 {
					data = append(data, byte(acc<<(8-nb)))
				}
			}
		case sub == 128:
			for _, x := range rls {
				data = appendRun(w.cpixel(data, x.v), x.n)
			}
		default:
			for _, x := range rls {
				if x.n == 1 {
					data = append(data, byte(index[x.v]))
					continue
				}
				data = appendRun(append(data, byte(index[x.v])|128), x.n)
			}
		}
	}
	z := c.x.zrle.compress(data)
	return append(binary.BigEndian.AppendUint32(nil, uint32(len(z))), z...)
}

// palette lists the distinct values in a fixed order, or nil when there
// are more than limit.
func palette(vals []uint32, limit int) []uint32 {
	seen := map[uint32]bool{}
	for _, v := range vals {
		if !seen[v] {
			if len(seen) == limit {
				return nil
			}
			seen[v] = true
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// tight encodes one rectangle: Fill for one colour, the palette filter for
// up to 16, else JPEG when the client asked for it, else the gradient
// filter (Extensions.TightGradient) or the copy filter.
func (c *client) tight(w wire, r image.Rectangle) []byte {
	vals := c.values(w, r)
	pal := palette(vals, 16)
	if len(pal) == 1 {
		return w.tpixelOf([]byte{0x80}, vals[0])
	}
	if q, ok := c.jpegQuality(); ok && pal == nil {
		var buf bytes.Buffer
		img := c.s.screen.SubImage(r)
		jpeg.Encode(&buf, img, &jpeg.Options{Quality: 10 + q*10})
		return append(tightLength([]byte{0x90}, buf.Len()), buf.Bytes()...)
	}
	var stream byte
	var head, data []byte
	switch {
	case pal != nil:
		stream = 1
		if len(pal) > 2 {
			stream = 2
		}
		head = []byte{1, byte(len(pal) - 1)}
		index := map[uint32]int{}
		for i, v := range pal {
			index[v] = i
			head = w.tpixelOf(head, v)
		}
		for y := 0; y < r.Dy(); y++ {
			if len(pal) == 2 {
				row := make([]byte, (r.Dx()+7)/8)
				for x := 0; x < r.Dx(); x++ {
					row[x/8] |= byte(index[vals[y*r.Dx()+x]]) << (7 - x%8)
				}
				data = append(data, row...)
				continue
			}
			for x := 0; x < r.Dx(); x++ {
				data = append(data, byte(index[vals[y*r.Dx()+x]]))
			}
		}
	case c.s.opts.Ext.TightGradient && (w.bpp == 2 || w.bpp == 4):
		stream, head = 3, []byte{2}
		data = c.gradient(w, vals, r.Dx(), r.Dy())
	default:
		for _, v := range vals {
			data = w.tpixelOf(data, v)
		}
	}
	ctl := stream << 4
	if head != nil {
		ctl |= 0x40
	}
	if len(data) < 12 {
		return append(append([]byte{ctl}, head...), data...)
	}
	z := &c.x.tight[stream]
	if c.s.opts.Ext.TightReset {
		*z = zstream{}
		ctl |= 1 << stream
	}
	cz := z.compress(data)
	return append(tightLength(append([]byte{ctl}, head...), len(cz)), cz...)
}

func (c *client) jpegQuality() (int, bool) {
	for _, e := range c.encs {
		if e >= vnc.EncJPEGQuality0 && e <= vnc.EncJPEGQuality0+9 {
			return int(e - vnc.EncJPEGQuality0), true
		}
	}
	return 0, false
}

// gradient applies the gradient filter: each channel minus a prediction
// from the left, upper, and upper-left pixels.
func (c *client) gradient(w wire, vals []uint32, width, height int) []byte {
	pf := w.pf
	maxes := [3]int{int(pf.RedMax), int(pf.GreenMax), int(pf.BlueMax)}
	shifts := [3]uint8{pf.RedShift, pf.GreenShift, pf.BlueShift}
	ch := func(x, y, k int) int {
		if x < 0 || y < 0 {
			return 0
		}
		return int(vals[y*width+x]>>shifts[k]) & maxes[k]
	}
	var out []byte
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			var v uint32
			for k := 0; k < 3; k++ {
				e := min(max(ch(x-1, y, k)+ch(x, y-1, k)-ch(x-1, y-1, k), 0), maxes[k])
				v |= uint32((ch(x, y, k)-e)&maxes[k]) << shifts[k]
			}
			out = w.tpixelOf(out, v)
		}
	}
	return out
}

func tightLength(b []byte, n int) []byte {
	if n < 0x80 {
		return append(b, byte(n))
	}
	if n < 0x4000 {
		return append(b, byte(n)|0x80, byte(n>>7))
	}
	return append(b, byte(n)|0x80, byte(n>>7)|0x80, byte(n>>14))
}

// Copy copies the screen area src to dst. A client that offered CopyRect
// gets a CopyRect; the others get the pixels.
func (s *Server) Copy(src image.Rectangle, dst image.Point) {
	s.mu.Lock()
	tmp := image.NewRGBA(src)
	for y := src.Min.Y; y < src.Max.Y; y++ {
		copy(tmp.Pix[tmp.PixOffset(src.Min.X, y):][:src.Dx()*4], s.screen.Pix[s.screen.PixOffset(src.Min.X, y):])
	}
	d := src.Sub(src.Min).Add(dst)
	for y := src.Min.Y; y < src.Max.Y; y++ {
		copy(s.screen.Pix[s.screen.PixOffset(d.Min.X, d.Min.Y+y-src.Min.Y):][:src.Dx()*4], tmp.Pix[tmp.PixOffset(src.Min.X, y):])
	}
	for c := range s.clients {
		stale := false
		for _, r := range c.dirty {
			stale = stale || r.Overlaps(src)
		}
		if stale || !slices.Contains(c.encs, vnc.EncCopyRect) {
			c.dirty = append(c.dirty, d)
			continue
		}
		c.x.copies = append(c.x.copies, copyOp{src.Min, d})
	}
	s.mu.Unlock()
	s.kick()
}

type copyOp struct {
	src image.Point
	dst image.Rectangle
}

// copyRects encodes the pending copies, in order. It runs under s.mu.
func (c *client) copyRects() [][]byte {
	var out [][]byte
	for _, op := range c.x.copies {
		b := rectHeader(op.dst, vnc.EncCopyRect)
		b = binary.BigEndian.AppendUint16(b, uint16(op.src.X))
		out = append(out, binary.BigEndian.AppendUint16(b, uint16(op.src.Y)))
	}
	c.x.copies = nil
	return out
}
