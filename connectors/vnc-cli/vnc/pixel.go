package vnc

import (
	"encoding/binary"
	"fmt"
	"math/bits"
)

// PixelFormat is the RFC 6143 §7.4 pixel format.
type PixelFormat struct {
	BPP        uint8
	Depth      uint8
	BigEndian  bool
	TrueColor  bool
	RedMax     uint16
	GreenMax   uint16
	BlueMax    uint16
	RedShift   uint8
	GreenShift uint8
	BlueShift  uint8
}

// Named formats, as vncdotool names them (byte order on the wire).
var (
	FormatBGRX8888 = PixelFormat{32, 24, false, true, 255, 255, 255, 16, 8, 0}
	FormatRGBX8888 = PixelFormat{32, 24, false, true, 255, 255, 255, 0, 8, 16}
	FormatRGB565   = PixelFormat{16, 16, false, true, 31, 63, 31, 11, 5, 0}
)

// PixelFormats maps the --pixel-format names to formats.
var PixelFormats = map[string]PixelFormat{
	"bgrx8888": FormatBGRX8888,
	"rgbx8888": FormatRGBX8888,
	"rgb565":   FormatRGB565,
}

// tpixelFormat describes the three R,G,B bytes of a Tight TPIXEL or a JPEG.
var tpixelFormat = PixelFormat{24, 24, false, true, 255, 255, 255, 0, 8, 16}

// ParsePixelFormat reads the 16-byte wire form.
func ParsePixelFormat(b []byte) PixelFormat {
	return PixelFormat{
		BPP: b[0], Depth: b[1], BigEndian: b[2] != 0, TrueColor: b[3] != 0,
		RedMax:   binary.BigEndian.Uint16(b[4:]),
		GreenMax: binary.BigEndian.Uint16(b[6:]),
		BlueMax:  binary.BigEndian.Uint16(b[8:]),
		RedShift: b[10], GreenShift: b[11], BlueShift: b[12],
	}
}

// Bytes is the 16-byte wire form.
func (pf PixelFormat) Bytes() []byte {
	b := make([]byte, 16)
	b[0], b[1] = pf.BPP, pf.Depth
	if pf.BigEndian {
		b[2] = 1
	}
	if pf.TrueColor {
		b[3] = 1
	}
	binary.BigEndian.PutUint16(b[4:], pf.RedMax)
	binary.BigEndian.PutUint16(b[6:], pf.GreenMax)
	binary.BigEndian.PutUint16(b[8:], pf.BlueMax)
	b[10], b[11], b[12] = pf.RedShift, pf.GreenShift, pf.BlueShift
	return b
}

func (pf PixelFormat) String() string {
	end := "le"
	if pf.BigEndian {
		end = "be"
	}
	if !pf.TrueColor {
		return fmt.Sprintf("%dbpp depth %d colour map", pf.BPP, pf.Depth)
	}
	return fmt.Sprintf("%dbpp depth %d %s max %d/%d/%d shift %d/%d/%d", pf.BPP, pf.Depth, end,
		pf.RedMax, pf.GreenMax, pf.BlueMax, pf.RedShift, pf.GreenShift, pf.BlueShift)
}

// BytesPerPixel is the size of one PIXEL on the wire.
func (pf PixelFormat) BytesPerPixel() int { return (int(pf.BPP) + 7) / 8 }

func (pf PixelFormat) widths() (r, g, b int) {
	return bits.Len16(pf.RedMax), bits.Len16(pf.GreenMax), bits.Len16(pf.BlueMax)
}

// Validate refuses a format that the converter cannot read. The converter
// reads any true colour layout and an 8-bit colour map; depth is ignored,
// because servers misreport it (vncdotool specs/pixel-format.md).
func (pf PixelFormat) Validate() error {
	switch pf.BPP {
	case 8, 16, 24, 32:
	default:
		return fmt.Errorf("pixel format: %d bits per pixel is not supported", pf.BPP)
	}
	if !pf.TrueColor {
		if pf.BPP != 8 {
			return fmt.Errorf("pixel format: a colour map needs 8 bits per pixel, not %d", pf.BPP)
		}
		return nil
	}
	var used uint64
	for _, c := range []struct {
		max   uint16
		shift uint8
	}{{pf.RedMax, pf.RedShift}, {pf.GreenMax, pf.GreenShift}, {pf.BlueMax, pf.BlueShift}} {
		if c.max == 0 || c.max&(c.max+1) != 0 {
			return fmt.Errorf("pixel format: channel maximum %d is not 2^n-1", c.max)
		}
		if int(c.shift)+bits.Len16(c.max) > int(pf.BPP) {
			return fmt.Errorf("pixel format: channel shift %d does not fit %d bits", c.shift, pf.BPP)
		}
		m := uint64(c.max) << c.shift
		if used&m != 0 {
			return fmt.Errorf("pixel format: two channels share bits")
		}
		used |= m
	}
	return nil
}

// ChannelTolerance is the 8-bit error that the quantization of each channel
// can cause: 255 >> width.
func (pf PixelFormat) ChannelTolerance() [3]int {
	r, g, b := pf.widths()
	return [3]int{255 >> r, 255 >> g, 255 >> b}
}

// cpixelPlacement is "low", "high", or "" (rfbproto ZRLE CPIXEL).
func (pf PixelFormat) cpixelPlacement() string {
	wr, wg, wb := pf.widths()
	if int(pf.RedShift)+wr <= 24 && int(pf.GreenShift)+wg <= 24 && int(pf.BlueShift)+wb <= 24 {
		return "low"
	}
	if pf.RedShift >= 8 && pf.GreenShift >= 8 && pf.BlueShift >= 8 {
		return "high"
	}
	return ""
}

// cpixelBytes is 3 for a format that ZRLE sends as a CPIXEL, else the PIXEL
// size. It ignores depth (vncdotool specs/cpixel-depth-quirk.md).
func (pf PixelFormat) cpixelBytes() int {
	if pf.TrueColor && pf.BPP == 32 && pf.cpixelPlacement() != "" {
		return 3
	}
	return pf.BytesPerPixel()
}

// cpixelOffset is the byte offset of the 3 CPIXEL bytes in a PIXEL.
// Big-endian flips it, because placement is in value space.
func (pf PixelFormat) cpixelOffset() int {
	if pf.cpixelBytes() != 3 {
		return 0
	}
	if (pf.cpixelPlacement() == "low") != pf.BigEndian {
		return 0
	}
	return pf.BytesPerPixel() - 3
}

// tpixelBytes is 3 for a format that Tight sends as a TPIXEL (R,G,B), else
// the PIXEL size. Narrower than CPIXEL: depth must be 24.
func (pf PixelFormat) tpixelBytes() int {
	if pf.TrueColor && pf.BPP == 32 && pf.Depth == 24 {
		if r, g, b := pf.widths(); r == 8 && g == 8 && b == 8 {
			return 3
		}
	}
	return pf.BytesPerPixel()
}

// converter turns pixels in one wire format into RGBA.
type converter struct {
	pf     PixelFormat
	bypp   int
	fast   int // 1 = B,G,R,X; 2 = R,G,B,X; 3 = R,G,B
	scale  [3][]uint8
	shift  [3]uint8
	colors *[256][3]uint8
}

func newConverter(pf PixelFormat, colors *[256][3]uint8) (*converter, error) {
	if err := pf.Validate(); err != nil {
		return nil, err
	}
	cv := &converter{pf: pf, bypp: pf.BytesPerPixel(), colors: colors}
	if !pf.TrueColor {
		return cv, nil
	}
	switch {
	case pf == tpixelFormat:
		cv.fast = 3
	case pf.BPP == 32 && pf.RedMax == 255 && pf.GreenMax == 255 && pf.BlueMax == 255 && !pf.BigEndian:
		switch [3]uint8{pf.RedShift, pf.GreenShift, pf.BlueShift} {
		case [3]uint8{16, 8, 0}:
			cv.fast = 1
		case [3]uint8{0, 8, 16}:
			cv.fast = 2
		}
	}
	for i, m := range []uint16{pf.RedMax, pf.GreenMax, pf.BlueMax} {
		cv.scale[i] = channelScale(m)
	}
	cv.shift = [3]uint8{pf.RedShift, pf.GreenShift, pf.BlueShift}
	return cv, nil
}

// channelScale maps 0..max to 0..255 with rounding. A wide channel keeps
// only its top 8 bits.
func channelScale(max uint16) []uint8 {
	if max > 255 {
		s := make([]uint8, int(max)+1)
		w := bits.Len16(max)
		for v := range s {
			s[v] = uint8(v >> (w - 8))
		}
		return s
	}
	s := make([]uint8, int(max)+1)
	for v := range s {
		s[v] = uint8((v*255 + int(max)/2) / int(max))
	}
	return s
}

func (cv *converter) value(p []byte) uint32 {
	switch cv.bypp {
	case 1:
		return uint32(p[0])
	case 2:
		if cv.pf.BigEndian {
			return uint32(binary.BigEndian.Uint16(p))
		}
		return uint32(binary.LittleEndian.Uint16(p))
	case 3:
		if cv.pf.BigEndian {
			return uint32(p[0])<<16 | uint32(p[1])<<8 | uint32(p[2])
		}
		return uint32(p[2])<<16 | uint32(p[1])<<8 | uint32(p[0])
	default:
		if cv.pf.BigEndian {
			return binary.BigEndian.Uint32(p)
		}
		return binary.LittleEndian.Uint32(p)
	}
}

// put writes the pixel value v in wire bytes. It is the inverse of value.
func (cv *converter) put(p []byte, v uint32) {
	switch cv.bypp {
	case 1:
		p[0] = uint8(v)
	case 2:
		if cv.pf.BigEndian {
			binary.BigEndian.PutUint16(p, uint16(v))
		} else {
			binary.LittleEndian.PutUint16(p, uint16(v))
		}
	case 3:
		if cv.pf.BigEndian {
			p[0], p[1], p[2] = uint8(v>>16), uint8(v>>8), uint8(v)
		} else {
			p[0], p[1], p[2] = uint8(v), uint8(v>>8), uint8(v>>16)
		}
	default:
		if cv.pf.BigEndian {
			binary.BigEndian.PutUint32(p, v)
		} else {
			binary.LittleEndian.PutUint32(p, v)
		}
	}
}

// rgb returns the colour of one pixel in wire bytes.
func (cv *converter) rgb(p []byte) (r, g, b uint8) {
	switch cv.fast {
	case 1:
		return p[2], p[1], p[0]
	case 2, 3:
		return p[0], p[1], p[2]
	}
	v := cv.value(p)
	if !cv.pf.TrueColor {
		c := cv.colors[v&0xff]
		return c[0], c[1], c[2]
	}
	return cv.scale[0][(v>>cv.shift[0])&uint32(cv.pf.RedMax)],
		cv.scale[1][(v>>cv.shift[1])&uint32(cv.pf.GreenMax)],
		cv.scale[2][(v>>cv.shift[2])&uint32(cv.pf.BlueMax)]
}

// row converts n pixels from src into RGBA at dst.
func (cv *converter) row(dst, src []byte, n int) {
	switch cv.fast {
	case 1:
		for i := 0; i < n; i++ {
			s, d := src[i*4:i*4+4:i*4+4], dst[i*4:i*4+4:i*4+4]
			d[0], d[1], d[2], d[3] = s[2], s[1], s[0], 0xff
		}
		return
	case 2:
		for i := 0; i < n; i++ {
			s, d := src[i*4:i*4+3:i*4+3], dst[i*4:i*4+4:i*4+4]
			d[0], d[1], d[2], d[3] = s[0], s[1], s[2], 0xff
		}
		return
	}
	for i := 0; i < n; i++ {
		d := dst[i*4 : i*4+4 : i*4+4]
		d[0], d[1], d[2] = cv.rgb(src[i*cv.bypp:])
		d[3] = 0xff
	}
}
