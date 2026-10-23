package vnc_test

import (
	"encoding/binary"
	"errors"
	"image"
	"io"
	"net"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc"
	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc/vnctest"
)

// Each fuzz test decodes the server messages after ServerInit with one
// decoder. The seeds come from vnctest: two updates of scenes for each
// pixel format that vnctest can encode.
//
//	go test -run=^$ -fuzz=FuzzTight -fuzztime=60s ./vnc/

// recordUpdates logs in to srv with a raw client and returns the bytes
// from the server after ServerInit. The client asks for a full update,
// then calls next and asks for an incremental update.
func recordUpdates(tb testing.TB, srv *vnctest.Server, pf vnc.PixelFormat, encs []int32, next func()) []byte {
	tb.Helper()
	nc := srv.Pipe()
	defer nc.Close()
	nc.SetDeadline(time.Now().Add(5 * time.Second))
	read := func(n int) []byte {
		b := make([]byte, n)
		if _, err := io.ReadFull(nc, b); err != nil {
			tb.Fatal(err)
		}
		return b
	}
	write := func(b []byte) {
		if _, err := nc.Write(b); err != nil {
			tb.Fatal(err)
		}
	}
	read(12)
	write([]byte("RFB 003.008\n"))
	read(int(read(1)[0]))
	write([]byte{vnc.SecNone})
	read(4)
	write([]byte{1})
	init := read(24)
	read(int(binary.BigEndian.Uint32(init[20:])))

	write(append([]byte{0, 0, 0, 0}, pf.Bytes()...))
	b := binary.BigEndian.AppendUint16([]byte{2, 0}, uint16(len(encs)))
	for _, e := range encs {
		b = binary.BigEndian.AppendUint32(b, uint32(e))
	}
	write(b)
	request := func(incremental byte) {
		write([]byte{3, incremental, 0, 0, 0, 0, 0, byte(vnc.FuzzSize.X), 0, byte(vnc.FuzzSize.Y)})
	}

	var out []byte
	drain := func() {
		buf := make([]byte, 64<<10)
		for {
			nc.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			n, err := nc.Read(buf)
			out = append(out, buf[:n]...)
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return
			}
			if err != nil {
				tb.Fatal(err)
			}
		}
	}
	request(0)
	drain()
	if next != nil {
		next()
		request(1)
		drain()
	}
	return out
}

// seedEncoding adds the seeds of one encoding.
func seedEncoding(f *testing.F, encs []int32, ext vnctest.Extensions) {
	a, b := sceneImage(f, "g"), sceneImage(f, "d")
	for i, pf := range vnc.FuzzFormats {
		if !pf.TrueColor {
			continue
		}
		srv := vnctest.New(vnctest.Options{Width: vnc.FuzzSize.X, Height: vnc.FuzzSize.Y, Ext: ext})
		srv.Draw(image.Point{}, a.SubImage(image.Rect(100, 80, 124, 100)))
		data := recordUpdates(f, srv, pf, encs, func() {
			srv.Draw(image.Pt(4, 3), b.SubImage(image.Rect(40, 40, 56, 52)))
		})
		srv.Close()
		f.Add(uint8(i), data)
	}
}

func fuzzFormat(i uint8) vnc.PixelFormat {
	return vnc.FuzzFormats[int(i)%len(vnc.FuzzFormats)]
}

func FuzzRaw(f *testing.F) {
	seedEncoding(f, []int32{vnc.EncRaw}, vnctest.Extensions{})
	f.Fuzz(func(t *testing.T, pfi uint8, data []byte) { vnc.CheckUpdates(t, vnc.EncRaw, fuzzFormat(pfi), data) })
}

func FuzzCopyRect(f *testing.F) {
	f.Add(uint8(0), update(rect(2, 2, 10, 8, vnc.EncCopyRect, 0, 5, 0, 4)))
	f.Add(uint8(0), update(rect(14, 12, 10, 8, vnc.EncCopyRect, 0, 0, 0, 0), rect(0, 0, 24, 20, vnc.EncCopyRect, 0, 0, 0, 0)))
	f.Fuzz(func(t *testing.T, pfi uint8, data []byte) {
		vnc.CheckUpdates(t, vnc.EncCopyRect, fuzzFormat(pfi), data)
	})
}

func FuzzRRE(f *testing.F) {
	seedEncoding(f, []int32{vnc.EncRRE}, vnctest.Extensions{})
	f.Fuzz(func(t *testing.T, pfi uint8, data []byte) { vnc.CheckUpdates(t, vnc.EncRRE, fuzzFormat(pfi), data) })
}

func FuzzCoRRE(f *testing.F) {
	seedEncoding(f, []int32{vnc.EncCoRRE}, vnctest.Extensions{})
	f.Fuzz(func(t *testing.T, pfi uint8, data []byte) { vnc.CheckUpdates(t, vnc.EncCoRRE, fuzzFormat(pfi), data) })
}

func FuzzHextile(f *testing.F) {
	seedEncoding(f, []int32{vnc.EncHextile}, vnctest.Extensions{})
	f.Fuzz(func(t *testing.T, pfi uint8, data []byte) { vnc.CheckUpdates(t, vnc.EncHextile, fuzzFormat(pfi), data) })
}

func FuzzZRLE(f *testing.F) {
	seedEncoding(f, []int32{vnc.EncZRLE}, vnctest.Extensions{})
	f.Fuzz(func(t *testing.T, pfi uint8, data []byte) { vnc.CheckUpdates(t, vnc.EncZRLE, fuzzFormat(pfi), data) })
}

func FuzzTight(f *testing.F) {
	seedEncoding(f, []int32{vnc.EncTight}, vnctest.Extensions{})
	seedEncoding(f, []int32{vnc.EncTight}, vnctest.Extensions{TightGradient: true, TightReset: true})
	seedEncoding(f, []int32{vnc.EncTight, vnc.EncJPEGQuality0 + 6}, vnctest.Extensions{})
	f.Fuzz(func(t *testing.T, pfi uint8, data []byte) { vnc.CheckUpdates(t, vnc.EncTight, fuzzFormat(pfi), data) })
}

func FuzzCursor(f *testing.F) {
	px := make([]byte, 3*2*4)
	f.Add(uint8(0), update(rect(1, 1, 3, 2, vnc.EncCursor, append(px, 0xa0, 0x40)...)))
	f.Add(uint8(2), update(rect(0, 0, 2, 2, vnc.EncCursor, make([]byte, 2*2*2+2)...)))
	f.Fuzz(func(t *testing.T, pfi uint8, data []byte) { vnc.CheckUpdates(t, vnc.EncCursor, fuzzFormat(pfi), data) })
}

func FuzzXCursor(f *testing.F) {
	f.Add(uint8(0), update(rect(0, 0, 2, 1, vnc.EncXCursor, 255, 255, 255, 0, 0, 0, 0x80, 0xc0)))
	f.Add(uint8(0), update(rect(4, 4, 9, 2, vnc.EncXCursor, 1, 2, 3, 4, 5, 6, 0xff, 0x80, 0x0f, 0, 0xff, 0x80, 0xf0, 0)))
	f.Fuzz(func(t *testing.T, pfi uint8, data []byte) { vnc.CheckUpdates(t, vnc.EncXCursor, fuzzFormat(pfi), data) })
}
