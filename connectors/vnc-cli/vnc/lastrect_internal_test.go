package vnc

import (
	"encoding/binary"
	"image/color"
	"testing"
)

// TestLastRect reads an update with an unknown count (0xffff) that LastRect
// ends, then the next update.
func TestLastRect(t *testing.T) {
	rect := func(x, y, w, h int, enc int32) []byte {
		b := make([]byte, 12)
		binary.BigEndian.PutUint16(b, uint16(x))
		binary.BigEndian.PutUint16(b[2:], uint16(y))
		binary.BigEndian.PutUint16(b[4:], uint16(w))
		binary.BigEndian.PutUint16(b[6:], uint16(h))
		binary.BigEndian.PutUint32(b[8:], uint32(enc))
		return b
	}
	var data []byte
	data = append(data, msgFramebufferUpdate, 0, 0xff, 0xff)
	data = append(data, rect(2, 3, 1, 1, EncRaw)...)
	data = append(data, 0, 0, 255, 0) // red in bgrx8888
	data = append(data, rect(0, 0, 0, 0, EncLastRect)...)
	data = append(data, msgFramebufferUpdate, 0, 0, 1)
	data = append(data, rect(4, 3, 1, 1, EncRaw)...)
	data = append(data, 255, 0, 0, 0) // blue

	c := readConn(data)
	if err := c.setFormat(FormatBGRX8888); err != nil {
		t.Fatal(err)
	}
	c.decoders = map[int32]decoder{EncRaw: decoderFactories[EncRaw]()}
	for range 2 {
		if typ, err := c.r.u8(); err != nil || typ != msgFramebufferUpdate {
			t.Fatalf("message %d: %v", typ, err)
		}
		if err := c.readUpdate(); err != nil {
			t.Fatal(err)
		}
	}
	if got := c.fb.img.RGBAAt(2, 3); got != (color.RGBA{255, 0, 0, 255}) {
		t.Fatalf("first update: %v", got)
	}
	if got := c.fb.img.RGBAAt(4, 3); got != (color.RGBA{0, 0, 255, 255}) {
		t.Fatalf("second update: %v", got)
	}
}
