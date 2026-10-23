package vnc_test

import (
	"context"
	"errors"
	"image"
	"image/color"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc"
	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc/vnctest"
)

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func connect(t *testing.T, srv *vnctest.Server, o vnc.Options) *vnc.Conn {
	t.Helper()
	c, err := vnc.NewConn(testCtx(t), srv.Pipe(), o)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func sameImage(t *testing.T, got, want *image.RGBA, tol int) {
	t.Helper()
	if got.Rect.Size() != want.Rect.Size() {
		t.Fatalf("size %v, want %v", got.Rect.Size(), want.Rect.Size())
	}
	for y := 0; y < want.Rect.Dy(); y++ {
		for x := 0; x < want.Rect.Dx(); x++ {
			g, w := got.RGBAAt(x, y), want.RGBAAt(x+want.Rect.Min.X, y+want.Rect.Min.Y)
			if absDiff(g.R, w.R) > tol || absDiff(g.G, w.G) > tol || absDiff(g.B, w.B) > tol {
				t.Fatalf("pixel %d,%d is %v, want %v", x, y, g, w)
			}
		}
	}
}

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

func TestHandshake(t *testing.T) {
	for _, ver := range []string{"3.3", "3.7", "3.8"} {
		for _, sec := range []uint8{vnc.SecNone, vnc.SecVNC} {
			t.Run(ver+"/"+map[uint8]string{1: "none", 2: "vnc"}[sec], func(t *testing.T) {
				srv := vnctest.New(vnctest.Options{Version: ver, Security: []uint8{sec}, Password: "s3kr1t", Name: "desk"})
				defer srv.Close()
				srv.Paint(image.Rect(10, 5, 30, 20), color.RGBA{200, 100, 50, 255})
				c := connect(t, srv, vnc.Options{Password: "s3kr1t"})
				if c.Name() != "desk" {
					t.Errorf("name %q", c.Name())
				}
				if c.Size() != image.Pt(64, 48) {
					t.Errorf("size %v", c.Size())
				}
				if err := c.Refresh(testCtx(t), true); err != nil {
					t.Fatal(err)
				}
				sameImage(t, c.Snapshot(image.Rectangle{}), srv.Screen(), 0)
				if !srv.Shared() {
					t.Error("ClientInit did not ask for a shared session")
				}
			})
		}
	}
}

func TestExclusive(t *testing.T) {
	srv := vnctest.New(vnctest.Options{})
	defer srv.Close()
	c := connect(t, srv, vnc.Options{Exclusive: true})
	c.Refresh(testCtx(t), true)
	if srv.Shared() {
		t.Error("shared flag set")
	}
}

func TestSecurityChoice(t *testing.T) {
	srv := vnctest.New(vnctest.Options{Security: []uint8{vnc.SecNone, vnc.SecVNC}, Password: "pw"})
	defer srv.Close()
	// With a password, VNC auth wins over None.
	connect(t, srv, vnc.Options{Password: "pw"})

	srv2 := vnctest.New(vnctest.Options{Security: []uint8{16, 5}})
	defer srv2.Close()
	_, err := vnc.NewConn(testCtx(t), srv2.Pipe(), vnc.Options{})
	if err == nil || !strings.Contains(err.Error(), "Tight (16), RSA-AES (5)") {
		t.Fatalf("err = %v", err)
	}
}

func TestWrongPassword(t *testing.T) {
	for _, tc := range []struct{ ver, want string }{
		{"3.8", "Bad password for desk"},
		{"3.7", "wrong password or user"},
		{"3.3", "wrong password or user"},
	} {
		t.Run(tc.ver, func(t *testing.T) {
			srv := vnctest.New(vnctest.Options{Version: tc.ver, Security: []uint8{vnc.SecVNC}, Password: "right", FailReason: "Bad password for desk"})
			defer srv.Close()
			_, err := vnc.NewConn(testCtx(t), srv.Pipe(), vnc.Options{Password: "wrong-pw"})
			var ae *vnc.AuthError
			if !errors.Is(err, vnc.ErrAuth) || !errors.As(err, &ae) || ae.Reason != tc.want {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(err.Error(), "wrong-pw") {
				t.Fatal("error holds the password")
			}
		})
	}
}

func TestNoPassword(t *testing.T) {
	srv := vnctest.New(vnctest.Options{Security: []uint8{vnc.SecVNC}, Password: "x"})
	defer srv.Close()
	_, err := vnc.NewConn(testCtx(t), srv.Pipe(), vnc.Options{})
	if !errors.Is(err, vnc.ErrAuth) {
		t.Fatalf("err = %v", err)
	}
}

func TestHandshakeTimeout(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := vnc.NewConn(ctx, a, vnc.Options{Addr: "x"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("no timeout")
	}
}

func TestNotRFB(t *testing.T) {
	a, b := net.Pipe()
	go func() { b.Write([]byte("SSH-2.0-Open\n")); b.Close() }()
	_, err := vnc.NewConn(testCtx(t), a, vnc.Options{})
	var pe *vnc.ProtocolError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v", err)
	}
}

func TestCloseInTheMiddleOfAMessage(t *testing.T) {
	srv := vnctest.New(vnctest.Options{})
	defer srv.Close()
	c := connect(t, srv, vnc.Options{})
	if err := c.Refresh(testCtx(t), true); err != nil {
		t.Fatal(err)
	}
	// One Raw 10x10 rectangle, with 7 of 400 pixel bytes.
	srv.SendRaw([]byte{0, 0, 0, 1, 0, 0, 0, 0, 0, 10, 0, 10, 0, 0, 0, 0, 1, 2, 3, 4, 5, 6, 7})
	srv.Close()
	select {
	case <-c.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("no end after a cut message")
	}
	if !errors.Is(c.Err(), vnc.ErrClosed) {
		t.Fatalf("err = %v", c.Err())
	}
	if err := c.Refresh(testCtx(t), true); err == nil {
		t.Fatal("Refresh after the end gave no error")
	}
}

func TestBadMessage(t *testing.T) {
	srv := vnctest.New(vnctest.Options{})
	defer srv.Close()
	c := connect(t, srv, vnc.Options{})
	c.Refresh(testCtx(t), true)
	// A Raw rectangle outside the screen.
	srv.SendRaw([]byte{0, 0, 0, 1, 0, 100, 0, 100, 0, 1, 0, 1, 0, 0, 0, 0})
	<-c.Done()
	var pe *vnc.ProtocolError
	if !errors.As(c.Err(), &pe) {
		t.Fatalf("err = %v", c.Err())
	}
}

func TestInput(t *testing.T) {
	srv := vnctest.New(vnctest.Options{})
	defer srv.Close()
	c := connect(t, srv, vnc.Options{})
	keys, _ := vnc.ParseKey("ctrl-alt-del", false)
	if err := c.Press(keys...); err != nil {
		t.Fatal(err)
	}
	c.Pointer(10, 20, vnc.ButtonLeft)
	c.Pointer(500, -3, 0)
	c.SetClipboard("héllo ☃")
	ev, err := srv.WaitEvents(testCtx(t), 9)
	if err != nil {
		t.Fatal(err)
	}
	want := []vnctest.InputEvent{
		{Kind: "key", Keysym: vnc.KeyControlL, Down: true},
		{Kind: "key", Keysym: vnc.KeyAltL, Down: true},
		{Kind: "key", Keysym: vnc.KeyDelete, Down: true},
		{Kind: "key", Keysym: vnc.KeyDelete},
		{Kind: "key", Keysym: vnc.KeyAltL},
		{Kind: "key", Keysym: vnc.KeyControlL},
		{Kind: "pointer", X: 10, Y: 20, Buttons: 1},
		{Kind: "pointer", X: 63, Y: 0},
		{Kind: "cut", Text: "héllo ?"},
	}
	for i := range want {
		if ev[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, ev[i], want[i])
		}
	}
}

func TestSubscribeAndEvents(t *testing.T) {
	srv := vnctest.New(vnctest.Options{})
	defer srv.Close()
	c := connect(t, srv, vnc.Options{UpdateInterval: time.Millisecond})
	ctx := testCtx(t)
	if err := c.Refresh(ctx, true); err != nil {
		t.Fatal(err)
	}
	sub := c.Subscribe()
	defer sub.Close()

	red := color.RGBA{255, 0, 0, 255}
	srv.Paint(image.Rect(4, 4, 8, 8), red)
	for {
		b, err := sub.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(b.Rects) > 0 {
			if b.Rects[0] != image.Rect(4, 4, 8, 8) {
				t.Errorf("rects %v", b.Rects)
			}
			break
		}
	}
	if got := c.Snapshot(image.Rect(4, 4, 5, 5)).RGBAAt(0, 0); got != red {
		t.Errorf("pixel %v", got)
	}

	srv.CutText("copied ü")
	srv.Bell()
	srv.Resize(80, 40)
	var kinds []string
	for len(kinds) < 3 {
		b, err := sub.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range b.Events {
			kinds = append(kinds, e.Kind)
			if e.Kind == vnc.EventResize && (e.Width != 80 || e.Height != 40) {
				t.Errorf("resize %+v", e)
			}
		}
	}
	if strings.Join(kinds, ",") != "clipboard,bell,resize" {
		t.Errorf("events %v", kinds)
	}
	if c.Clipboard() != "copied ü" {
		t.Errorf("clipboard %q", c.Clipboard())
	}
	if c.Size() != image.Pt(80, 40) {
		t.Errorf("size %v", c.Size())
	}
	if err := c.Refresh(ctx, true); err != nil {
		t.Fatal(err)
	}
	sameImage(t, c.Snapshot(image.Rectangle{}), srv.Screen(), 0)

	c.Close()
	for {
		_, err := sub.Next(ctx)
		if err == nil {
			continue // changes from before Close
		}
		if !errors.Is(err, vnc.ErrClosed) {
			t.Fatalf("Next after Close = %v", err)
		}
		break
	}
}

func TestPixelFormats(t *testing.T) {
	rgb565 := vnc.FormatRGB565
	cmap := vnc.PixelFormat{BPP: 8, Depth: 8}
	be := vnc.PixelFormat{32, 24, true, true, 255, 255, 255, 16, 8, 0}
	for _, tc := range []struct {
		name   string
		native vnc.PixelFormat
		force  *vnc.PixelFormat
		want   vnc.PixelFormat
		tol    int
	}{
		{"native bgrx", vnc.FormatBGRX8888, nil, vnc.FormatBGRX8888, 0},
		{"native rgb565 kept", rgb565, nil, rgb565, 8},
		{"native big endian kept", be, nil, be, 0},
		{"colour map replaced", cmap, nil, vnc.FormatBGRX8888, 0},
		{"forced rgbx", vnc.FormatBGRX8888, &vnc.FormatRGBX8888, vnc.FormatRGBX8888, 0},
		{"forced rgb565", vnc.FormatBGRX8888, &rgb565, rgb565, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := vnctest.New(vnctest.Options{Format: &tc.native})
			defer srv.Close()
			srv.Paint(image.Rect(0, 0, 32, 48), color.RGBA{250, 128, 3, 255})
			srv.Paint(image.Rect(32, 0, 64, 24), color.RGBA{1, 64, 255, 255})
			c := connect(t, srv, vnc.Options{PixelFormat: tc.force})
			if err := c.Refresh(testCtx(t), true); err != nil {
				t.Fatal(err)
			}
			if got := srv.ClientFormat(); got != tc.want {
				t.Fatalf("client format %v, want %v", got, tc.want)
			}
			sameImage(t, c.Snapshot(image.Rectangle{}), srv.Screen(), tc.tol)
		})
	}
}

func TestDialTCP(t *testing.T) {
	srv := vnctest.New(vnctest.Options{})
	defer srv.Close()
	addr, err := srv.Listen()
	if err != nil {
		t.Fatal(err)
	}
	c, err := vnc.Dial(testCtx(t), vnc.Options{Addr: addr})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Refresh(testCtx(t), true); err != nil {
		t.Fatal(err)
	}

	_, err = vnc.Dial(testCtx(t), vnc.Options{Addr: "127.0.0.1::1"})
	var de *vnc.DialError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v", err)
	}
}
