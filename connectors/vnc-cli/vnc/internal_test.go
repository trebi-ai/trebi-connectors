package vnc

import (
	"bytes"
	"encoding/hex"
	"math/bits"
	"testing"
)

// The FIPS SP 800-17 DES vector. The password is the key with the bits of
// each byte reversed, so that the client reverses them back.
func TestVNCAuthFIPSVector(t *testing.T) {
	key, _ := hex.DecodeString("0123456789ABCDEF")
	pw := make([]byte, 8)
	for i, k := range key {
		pw[i] = bits.Reverse8(k)
	}
	plain, _ := hex.DecodeString("4E6F7720697320744E6F772069732074")
	got, err := vncAuthResponse(string(pw), plain)
	if err != nil {
		t.Fatal(err)
	}
	if want := "3fa40e8a984d48153fa40e8a984d4815"; hex.EncodeToString(got) != want {
		t.Fatalf("got %x, want %s", got, want)
	}
}

// Computed with openssl des-ecb and the key cea6c64ea62e0000 ("secret",
// padded, bits reversed).
func TestVNCAuthPasswordVector(t *testing.T) {
	challenge, _ := hex.DecodeString("000102030405060708090a0b0c0d0e0f")
	got, _ := vncAuthResponse("secret", challenge)
	if want := "ee22539f33a5983ec12f9c2edbc995dd"; hex.EncodeToString(got) != want {
		t.Fatalf("got %x, want %s", got, want)
	}
	// Only the first 8 bytes of a password count.
	long, _ := vncAuthResponse("secret\x00\x00tail", challenge)
	if !bytes.Equal(long, got) {
		t.Fatal("bytes after the eighth changed the response")
	}
}

func pack(pf PixelFormat, r, g, b uint32) []byte {
	v := r<<pf.RedShift | g<<pf.GreenShift | b<<pf.BlueShift
	n := pf.BytesPerPixel()
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		s := 8 * i
		if pf.BigEndian {
			s = 8 * (n - 1 - i)
		}
		out[i] = byte(v >> s)
	}
	return out
}

func permutations(s [3]uint8) [][3]uint8 {
	return [][3]uint8{{s[0], s[1], s[2]}, {s[0], s[2], s[1]}, {s[1], s[0], s[2]}, {s[1], s[2], s[0]}, {s[2], s[0], s[1]}, {s[2], s[1], s[0]}}
}

// Every channel layout of vncdotool tests/unit/test_pixelformat.py decodes
// each primary within 1 of value*255/max.
func TestConverterEveryLayout(t *testing.T) {
	type layout struct {
		bpp, depth uint8
		max        [3]uint16
		slots      []uint8
	}
	n := 0
	for _, l := range []layout{
		{32, 24, [3]uint16{255, 255, 255}, []uint8{0, 8, 16, 24}},
		{32, 32, [3]uint16{255, 255, 255}, []uint8{0, 8, 16, 24}},
		{24, 24, [3]uint16{255, 255, 255}, []uint8{0, 8, 16}},
		{16, 16, [3]uint16{31, 63, 31}, []uint8{0, 5, 11}},
		{16, 15, [3]uint16{31, 31, 31}, []uint8{0, 5, 10}},
		{16, 12, [3]uint16{15, 15, 15}, []uint8{0, 4, 8}},
	} {
		var triples [][3]uint8
		for i := range l.slots {
			for j := range l.slots {
				for k := range l.slots {
					if i != j && j != k && i != k {
						triples = append(triples, [3]uint8{l.slots[i], l.slots[j], l.slots[k]})
					}
				}
			}
		}
		for _, sh := range triples {
			for _, be := range []bool{false, true} {
				pf := PixelFormat{l.bpp, l.depth, be, true, l.max[0], l.max[1], l.max[2], sh[0], sh[1], sh[2]}
				if pf.Validate() != nil {
					continue // the 16-bit shifts with a wide channel on top do not fit
				}
				cv, err := newConverter(pf, nil)
				if err != nil {
					t.Fatal(err)
				}
				n++
				for _, c := range colours(l.max) {
					r, g, b := cv.rgb(pack(pf, c[0], c[1], c[2]))
					for i, v := range []uint8{r, g, b} {
						want := float64(c[i]) * 255 / float64(l.max[i])
						if d := float64(v) - want; d > 1 || d < -1 {
							t.Fatalf("%v colour %v: channel %d = %d, want %.1f", pf, c, i, v, want)
						}
					}
					// row agrees with rgb
					dst := make([]byte, 4)
					cv.row(dst, pack(pf, c[0], c[1], c[2]), 1)
					if dst[0] != r || dst[1] != g || dst[2] != b || dst[3] != 255 {
						t.Fatalf("%v: row %v, rgb %d %d %d", pf, dst, r, g, b)
					}
				}
			}
		}
	}
	if n < 100 {
		t.Fatalf("only %d layouts ran", n)
	}
}

func colours(max [3]uint16) [][3]uint32 {
	out := [][3]uint32{{0, 0, 0}, {uint32(max[0]), uint32(max[1]), uint32(max[2])}}
	for ch := range 3 {
		for _, v := range []uint32{uint32(max[ch]), uint32(max[ch]) / 2, 1} {
			c := [3]uint32{}
			c[ch] = v
			out = append(out, c)
		}
	}
	return out
}

func TestRGB565RedIsHigh(t *testing.T) {
	cv, _ := newConverter(FormatRGB565, nil)
	r, g, b := cv.rgb([]byte{0x00, 0xf8})
	if r != 255 || g != 0 || b != 0 {
		t.Fatalf("got %d %d %d", r, g, b)
	}
}

func TestColourMap(t *testing.T) {
	var cm [256][3]uint8
	cm[7] = [3]uint8{1, 2, 3}
	cv, err := newConverter(PixelFormat{BPP: 8, Depth: 8}, &cm)
	if err != nil {
		t.Fatal(err)
	}
	if r, g, b := cv.rgb([]byte{7}); r != 1 || g != 2 || b != 3 {
		t.Fatalf("got %d %d %d", r, g, b)
	}
}

func TestValidate(t *testing.T) {
	for _, pf := range []PixelFormat{
		{BPP: 12, TrueColor: true, RedMax: 15, GreenMax: 15, BlueMax: 15},
		{BPP: 16, Depth: 16, TrueColor: false},
		{BPP: 32, Depth: 24, TrueColor: true, RedMax: 200, GreenMax: 255, BlueMax: 255},
		{BPP: 16, Depth: 16, TrueColor: true, RedMax: 255, GreenMax: 255, BlueMax: 255, RedShift: 16},
	} {
		if pf.Validate() == nil {
			t.Errorf("%v: no error", pf)
		}
	}
	deep := PixelFormat{32, 30, false, true, 1023, 1023, 1023, 20, 10, 0}
	cv, err := newConverter(deep, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r, g, b := cv.rgb(pack(deep, 1023, 512, 0)); r != 255 || g != 128 || b != 0 {
		t.Fatalf("10-bit: %d %d %d", r, g, b)
	}
}

func TestCPixel(t *testing.T) {
	high := PixelFormat{32, 24, false, true, 255, 255, 255, 24, 16, 8}
	tie := PixelFormat{32, 16, false, true, 31, 63, 31, 19, 13, 8}
	beLow := PixelFormat{32, 24, true, true, 255, 255, 255, 16, 8, 0}
	beHigh := PixelFormat{32, 24, true, true, 255, 255, 255, 24, 16, 8}
	for _, tc := range []struct {
		pf        PixelFormat
		size, off int
	}{
		{FormatBGRX8888, 3, 0}, {high, 3, 1}, {tie, 3, 0}, {beLow, 3, 1}, {beHigh, 3, 0}, {FormatRGB565, 2, 0},
	} {
		if s, o := tc.pf.cpixelBytes(), tc.pf.cpixelOffset(); s != tc.size || o != tc.off {
			t.Errorf("%v: cpixel %d at %d, want %d at %d", tc.pf, s, o, tc.size, tc.off)
		}
	}
	if FormatBGRX8888.tpixelBytes() != 3 || (PixelFormat{32, 32, false, true, 255, 255, 255, 16, 8, 0}).tpixelBytes() != 4 {
		t.Error("tpixel")
	}
}

func TestChannelTolerance(t *testing.T) {
	if got := FormatRGB565.ChannelTolerance(); got != [3]int{7, 3, 7} {
		t.Fatal(got)
	}
	if got := FormatBGRX8888.ChannelTolerance(); got != [3]int{0, 0, 0} {
		t.Fatal(got)
	}
}

func TestFormatBytesRoundTrip(t *testing.T) {
	for _, pf := range PixelFormats {
		if got := ParsePixelFormat(pf.Bytes()); got != pf {
			t.Errorf("%v -> %v", pf, got)
		}
	}
}
