package vnc_test

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc"
	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc/vnctest"
)

// testFormats add a big-endian format and a format with the colour in the
// high bytes, for the CPIXEL and TPIXEL rules.
var testFormats = map[string]vnc.PixelFormat{
	"bgrx8888":  vnc.FormatBGRX8888,
	"rgbx8888":  vnc.FormatRGBX8888,
	"rgb565":    vnc.FormatRGB565,
	"xrgb-be":   {BPP: 32, Depth: 24, BigEndian: true, TrueColor: true, RedMax: 255, GreenMax: 255, BlueMax: 255, RedShift: 16, GreenShift: 8, BlueShift: 0},
	"rgbx-high": {BPP: 32, Depth: 24, TrueColor: true, RedMax: 255, GreenMax: 255, BlueMax: 255, RedShift: 24, GreenShift: 16, BlueShift: 8},
}

var sceneNames = []string{"0", "s", "d", "x", "g", "p", "c", "f"}

// waitScreen waits until the client screen matches want within tol.
func waitScreen(t *testing.T, c *vnc.Conn, want *image.RGBA, tol [3]int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		msg := firstDifference(c.Snapshot(image.Rectangle{}), want, tol)
		if msg == "" {
			return
		}
		if time.Now().After(deadline) || c.Err() != nil {
			t.Fatalf("%s (connection: %v)", msg, c.Err())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestEncodings(t *testing.T) {
	variants := []struct {
		name string
		enc  int32
		ext  vnctest.Extensions
	}{
		{"raw", vnc.EncRaw, vnctest.Extensions{}},
		{"rre", vnc.EncRRE, vnctest.Extensions{}},
		{"corre", vnc.EncCoRRE, vnctest.Extensions{}},
		{"hextile", vnc.EncHextile, vnctest.Extensions{}},
		{"zrle", vnc.EncZRLE, vnctest.Extensions{}},
		{"tight", vnc.EncTight, vnctest.Extensions{}},
		{"tight-gradient", vnc.EncTight, vnctest.Extensions{TightGradient: true}},
		{"tight-reset", vnc.EncTight, vnctest.Extensions{TightReset: true}},
	}
	for _, v := range variants {
		for name, pf := range testFormats {
			t.Run(v.name+"/"+name, func(t *testing.T) {
				t.Parallel()
				srv := vnctest.New(vnctest.Options{Width: 256, Height: 192, Ext: v.ext})
				defer srv.Close()
				c := connect(t, srv, vnc.Options{Encodings: []int32{v.enc}, PixelFormat: &pf, UpdateInterval: time.Millisecond})
				for _, s := range sceneNames {
					srv.Draw(image.Point{}, sceneImage(t, s))
					waitScreen(t, c, srv.Screen(), pf.ChannelTolerance())
				}
			})
		}
	}
}

func TestTightJPEG(t *testing.T) {
	srv := vnctest.New(vnctest.Options{Width: 256, Height: 192})
	defer srv.Close()
	c := connect(t, srv, vnc.Options{Encodings: []int32{vnc.EncTight}, JPEG: true, JPEGQuality: 8, UpdateInterval: time.Millisecond})
	for _, s := range sceneNames {
		want := sceneImage(t, s)
		srv.Draw(image.Point{}, want)
		deadline := time.Now().Add(5 * time.Second)
		for {
			d := jpegDelta(c.Snapshot(image.Rectangle{}), want, 2)
			if d <= 19 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("scene %s: %.1f from the scene", s, d)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func TestCopyRect(t *testing.T) {
	srv := vnctest.New(vnctest.Options{Width: 256, Height: 192})
	defer srv.Close()
	c := connect(t, srv, vnc.Options{Encodings: []int32{vnc.EncCopyRect, vnc.EncZRLE}, UpdateInterval: time.Millisecond})
	srv.Draw(image.Point{}, sceneImage(t, "d"))
	waitScreen(t, c, srv.Screen(), [3]int{})
	// Overlapping copies, down and up.
	srv.Copy(image.Rect(10, 10, 120, 90), image.Pt(30, 40))
	srv.Copy(image.Rect(100, 100, 200, 180), image.Pt(90, 60))
	waitScreen(t, c, srv.Screen(), [3]int{})
}

// update builds a FramebufferUpdate from rectangles with headers.
func update(rects ...[]byte) []byte {
	b := binary.BigEndian.AppendUint16([]byte{0, 0}, uint16(len(rects)))
	for _, r := range rects {
		b = append(b, r...)
	}
	return b
}

func rect(x, y, w, h int, enc int32, payload ...byte) []byte {
	b := make([]byte, 12, 12+len(payload))
	binary.BigEndian.PutUint16(b, uint16(x))
	binary.BigEndian.PutUint16(b[2:], uint16(y))
	binary.BigEndian.PutUint16(b[4:], uint16(w))
	binary.BigEndian.PutUint16(b[6:], uint16(h))
	binary.BigEndian.PutUint32(b[8:], uint32(enc))
	return append(b, payload...)
}

func solid(n int, col color.RGBA) []byte {
	var b []byte
	for i := 0; i < n; i++ {
		b = append(b, col.B, col.G, col.R, 0)
	}
	return b
}

func TestResizeInTheMiddleOfAnUpdate(t *testing.T) {
	srv := vnctest.New(vnctest.Options{})
	defer srv.Close()
	c := connect(t, srv, vnc.Options{Encodings: []int32{vnc.EncRaw}})
	if err := c.Refresh(testCtx(t), true); err != nil {
		t.Fatal(err)
	}
	red, blue := color.RGBA{255, 0, 0, 255}, color.RGBA{0, 0, 255, 255}
	// The second rectangle fits only the new size.
	sub := c.Subscribe()
	defer sub.Close()
	srv.SendRaw(update(
		rect(0, 0, 4, 4, vnc.EncRaw, solid(16, red)...),
		rect(0, 0, 80, 60, vnc.EncDesktopSize),
		rect(70, 50, 4, 4, vnc.EncRaw, solid(16, blue)...),
	))
	for c.Size() != image.Pt(80, 60) || c.Snapshot(image.Rect(70, 50, 71, 51)).RGBAAt(0, 0) != blue {
		if _, err := sub.Next(testCtx(t)); err != nil {
			t.Fatalf("%v (connection: %v)", err, c.Err())
		}
	}
	if got := c.Snapshot(image.Rect(0, 0, 1, 1)).RGBAAt(0, 0); got != red {
		t.Fatalf("pixel 0,0 is %v after the resize, want %v", got, red)
	}
}

func TestCopyRectFromRemovedArea(t *testing.T) {
	srv := vnctest.New(vnctest.Options{})
	defer srv.Close()
	c := connect(t, srv, vnc.Options{Encodings: []int32{vnc.EncCopyRect, vnc.EncRaw}})
	if err := c.Refresh(testCtx(t), true); err != nil {
		t.Fatal(err)
	}
	srv.SendRaw(update(
		rect(0, 0, 32, 24, vnc.EncDesktopSize),
		rect(0, 0, 4, 4, vnc.EncCopyRect, 0, 40, 0, 30),
	))
	select {
	case <-c.Done():
	case <-testCtx(t).Done():
		t.Fatal("the client took a copy from outside the screen")
	}
	var pe *vnc.ProtocolError
	if !errors.As(c.Err(), &pe) {
		t.Fatalf("error %v, want a ProtocolError", c.Err())
	}
}

// TestZlibStreamAcrossUpdates sends many small updates, so each zlib
// stream carries data from one update into the next.
func TestZlibStreamAcrossUpdates(t *testing.T) {
	for _, enc := range []int32{vnc.EncZRLE, vnc.EncTight} {
		t.Run(fmt.Sprint(enc), func(t *testing.T) {
			srv := vnctest.New(vnctest.Options{Width: 256, Height: 192})
			defer srv.Close()
			c := connect(t, srv, vnc.Options{Encodings: []int32{enc}, UpdateInterval: time.Millisecond})
			scene := sceneImage(t, "d")
			for i := 0; i < 12; i++ {
				r := image.Rect(i*20, i*15, i*20+40, i*15+30)
				srv.Draw(r.Min, scene.SubImage(r))
				waitScreen(t, c, srv.Screen(), [3]int{})
			}
		})
	}
}
