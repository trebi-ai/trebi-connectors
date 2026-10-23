package vnc_test

import (
	"errors"
	"image"
	"image/color"
	"image/draw"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc"
	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc/vnctest"
)

// waitFeature waits until the server announced a feature.
func waitFeature(t *testing.T, c *vnc.Conn, name string) {
	t.Helper()
	ctx := testCtx(t)
	for c.Facts()[name] != "true" {
		select {
		case <-ctx.Done():
			t.Fatalf("no feature %s: %v", name, c.Facts())
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// eventually polls cond until it is true.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	ctx := testCtx(t)
	for !cond() {
		select {
		case <-ctx.Done():
			t.Fatalf("timeout: %s", what)
		case <-time.After(2 * time.Millisecond):
		}
	}
}

func cursorShape() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 5, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x <= y; x++ {
			img.SetRGBA(x, y, color.RGBA{255, 255, 255, 255})
		}
		img.SetRGBA(4, y, color.RGBA{255, 0, 0, 255})
	}
	return img
}

func TestCursorModes(t *testing.T) {
	for _, mode := range []vnc.CursorMode{"", vnc.CursorLocal, vnc.CursorNone, vnc.CursorServer} {
		t.Run(string(mode), func(t *testing.T) {
			srv := vnctest.New(vnctest.Options{})
			defer srv.Close()
			c := connect(t, srv, vnc.Options{Cursor: mode, UpdateInterval: time.Millisecond})
			if err := c.Refresh(testCtx(t), true); err != nil {
				t.Fatal(err)
			}
			offered := slices.Contains(srv.Encodings(), vnc.EncCursor)
			if offered == (mode == vnc.CursorServer) {
				t.Fatalf("mode %q: cursor offered %v", mode, offered)
			}
			if mode == vnc.CursorServer {
				return
			}
			sub := c.Subscribe()
			defer sub.Close()
			shape := cursorShape()
			srv.SetCursor(shape, image.Pt(1, 1))
			srv.MovePointer(image.Pt(20, 10))
			var cur *vnc.Cursor
			for cur == nil {
				b, err := sub.Next(testCtx(t))
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range b.Events {
					if e.Kind == vnc.EventCursor {
						cur = e.Cursor
					}
				}
			}
			if cur.HotSpot != image.Pt(1, 1) || cur.Hash == "" {
				t.Fatalf("cursor %+v", cur)
			}
			if msg := firstDifference(cur.Image, shape, [3]int{}); msg != "" {
				t.Fatal(msg)
			}
			eventually(t, "pointer at 20,10", func() bool { return c.PointerPosition() == image.Pt(20, 10) })

			got := c.Snapshot(image.Rectangle{})
			want := srv.Screen()
			if mode == vnc.CursorLocal {
				draw.Draw(want, shape.Rect.Add(image.Pt(19, 9)), shape, image.Point{}, draw.Over)
			}
			if msg := firstDifference(got, want, [3]int{}); msg != "" {
				t.Fatal(msg)
			}
			if msg := firstDifference(c.SnapshotCursor(image.Rectangle{}, vnc.CursorNone), srv.Screen(), [3]int{}); msg != "" {
				t.Fatal(msg)
			}
		})
	}
}

func TestXCursor(t *testing.T) {
	srv := vnctest.New(vnctest.Options{})
	defer srv.Close()
	// Without the full colour cursor, vnctest falls back to XCursor.
	c := connect(t, srv, vnc.Options{Cursor: vnc.CursorLocal, Encodings: []int32{vnc.EncRaw}})
	if err := c.Refresh(testCtx(t), true); err != nil {
		t.Fatal(err)
	}
	srv.SendRaw(update(rect(0, 0, 2, 1, vnc.EncXCursor,
		255, 255, 255, 0, 0, 0, // foreground, background
		0x80, // bitmap: foreground, background
		0xc0, // mask: both opaque
	)))
	sub := c.Subscribe()
	defer sub.Close()
	eventually(t, "the XCursor shape", func() bool {
		snap := c.Snapshot(image.Rect(0, 0, 2, 1))
		return snap.RGBAAt(0, 0) == color.RGBA{255, 255, 255, 255}
	})
	if got := c.Snapshot(image.Rect(0, 0, 2, 1)).RGBAAt(1, 0); got != (color.RGBA{0, 0, 0, 255}) {
		t.Fatalf("background pixel %v", got)
	}
}

func TestPointerPosSettle(t *testing.T) {
	srv := vnctest.New(vnctest.Options{})
	defer srv.Close()
	c := connect(t, srv, vnc.Options{UpdateInterval: time.Millisecond})
	if err := c.Refresh(testCtx(t), true); err != nil {
		t.Fatal(err)
	}
	// The server echoes an old position just after our own move.
	if err := c.Pointer(30, 30, 0); err != nil {
		t.Fatal(err)
	}
	srv.MovePointer(image.Pt(5, 5))
	if err := c.Refresh(testCtx(t), false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if p := c.PointerPosition(); p != image.Pt(30, 30) {
		t.Fatalf("pointer at %v, want 30,30", p)
	}
}

func TestQEMUKeys(t *testing.T) {
	srv := vnctest.New(vnctest.Options{Ext: vnctest.Extensions{QEMUKeys: true}})
	defer srv.Close()
	c := connect(t, srv, vnc.Options{UpdateInterval: time.Millisecond})
	waitFeature(t, c, "qemu_keys")
	c.Press(vnc.KeyReturn)
	c.Press('a')
	c.Press(0x20ac) // € has no scan code
	ev, err := srv.WaitEvents(testCtx(t), 6)
	if err != nil {
		t.Fatal(err)
	}
	want := []vnctest.InputEvent{
		{Kind: "key", Keysym: vnc.KeyReturn, Down: true, Scancode: 0x1c},
		{Kind: "key", Keysym: vnc.KeyReturn, Scancode: 0x1c},
		{Kind: "key", Keysym: 'a', Down: true},
		{Kind: "key", Keysym: 'a'},
		{Kind: "key", Keysym: 0x20ac, Down: true},
		{Kind: "key", Keysym: 0x20ac},
	}
	for i := range want {
		if ev[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, ev[i], want[i])
		}
	}
}

func TestExtendedDesktopSize(t *testing.T) {
	srv := vnctest.New(vnctest.Options{Ext: vnctest.Extensions{ExtendedDesktopSize: true}})
	defer srv.Close()
	c := connect(t, srv, vnc.Options{UpdateInterval: time.Millisecond})
	waitFeature(t, c, "extended_desktop_size")
	if s := c.Screens(); len(s) != 1 || s[0].Rect != image.Rect(0, 0, 64, 48) {
		t.Fatalf("screens %+v", s)
	}
	if err := c.SetDesktopSize(testCtx(t), 100, 80); err != nil {
		t.Fatal(err)
	}
	if got := c.Size(); got != image.Pt(100, 80) {
		t.Fatalf("size %v after SetDesktopSize", got)
	}
	srv.Resize(40, 30)
	eventually(t, "size 40x30", func() bool { return c.Size() == image.Pt(40, 30) })
	if s := c.Screens(); len(s) != 1 || s[0].Rect != image.Rect(0, 0, 40, 30) {
		t.Fatalf("screens %+v", s)
	}
	if err := c.SetDesktopSize(testCtx(t), 0, 10); err == nil {
		t.Fatal("size 0x10 accepted")
	}
}

func TestSetDesktopSizeRefused(t *testing.T) {
	srv := vnctest.New(vnctest.Options{Ext: vnctest.Extensions{ExtendedDesktopSize: true, ResizeStatus: vnc.ResizeProhibited}})
	defer srv.Close()
	c := connect(t, srv, vnc.Options{})
	waitFeature(t, c, "extended_desktop_size")
	err := c.SetDesktopSize(testCtx(t), 100, 80)
	var re *vnc.ResizeError
	if !errors.As(err, &re) || re.Status != vnc.ResizeProhibited {
		t.Fatalf("error %v, want a ResizeError with status %d", err, vnc.ResizeProhibited)
	}
	if c.Size() != image.Pt(64, 48) {
		t.Fatalf("size %v after a refusal", c.Size())
	}
}

func TestUnsupportedExtensions(t *testing.T) {
	srv := vnctest.New(vnctest.Options{})
	defer srv.Close()
	c := connect(t, srv, vnc.Options{})
	if err := c.Refresh(testCtx(t), true); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"SetDesktopSize":          c.SetDesktopSize(testCtx(t), 10, 10),
		"Sync":                    c.Sync(testCtx(t)),
		"EnableContinuousUpdates": c.EnableContinuousUpdates(true),
	} {
		if !errors.Is(err, vnc.ErrUnsupported) {
			t.Errorf("%s: %v, want ErrUnsupported", name, err)
		}
	}
}

func TestFence(t *testing.T) {
	srv := vnctest.New(vnctest.Options{Ext: vnctest.Extensions{Fence: true}})
	defer srv.Close()
	c := connect(t, srv, vnc.Options{})
	waitFeature(t, c, "fence")
	c.Pointer(3, 4, 0)
	if err := c.Sync(testCtx(t)); err != nil {
		t.Fatal(err)
	}
	if ev := srv.Events(); len(ev) != 1 || ev[0].X != 3 {
		t.Fatalf("events after Sync: %+v", ev)
	}
}

func TestContinuousUpdates(t *testing.T) {
	srv := vnctest.New(vnctest.Options{Ext: vnctest.Extensions{ContinuousUpdates: true}})
	defer srv.Close()
	// A long interval: only continuous updates come fast.
	c := connect(t, srv, vnc.Options{UpdateInterval: time.Hour})
	waitFeature(t, c, "continuous_updates")
	if err := c.Refresh(testCtx(t), true); err != nil {
		t.Fatal(err)
	}
	if err := c.EnableContinuousUpdates(true); err != nil {
		t.Fatal(err)
	}
	if !c.ContinuousUpdates() {
		t.Fatal("continuous updates are off")
	}
	for i, col := range []color.RGBA{{255, 0, 0, 255}, {0, 255, 0, 255}, {0, 0, 255, 255}} {
		srv.Paint(image.Rect(i*10, 0, i*10+10, 10), col)
		eventually(t, "a continuous update", func() bool {
			return c.Snapshot(image.Rect(i*10, 0, i*10+1, 1)).RGBAAt(0, 0) == col
		})
	}
	if err := c.EnableContinuousUpdates(false); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the end of continuous updates", func() bool { return !c.ContinuousUpdates() })
}

func TestExtendedClipboard(t *testing.T) {
	for _, tc := range []struct {
		name string
		ext  vnctest.Extensions
	}{
		{"provide", vnctest.Extensions{ExtendedClipboard: true}},
		{"notify", vnctest.Extensions{ExtendedClipboard: true, ClipboardNotify: true}},
		{"limit", vnctest.Extensions{ExtendedClipboard: true, ClipboardLimit: 8}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := vnctest.New(vnctest.Options{Ext: tc.ext})
			defer srv.Close()
			c := connect(t, srv, vnc.Options{})
			waitFeature(t, c, "extended_clipboard")
			text := "héllo ☃\nline two"
			if err := c.SetClipboard(text); err != nil {
				t.Fatal(err)
			}
			ev, err := srv.WaitEvents(testCtx(t), 1)
			if err != nil {
				t.Fatal(err)
			}
			if ev[0].Kind != "cut" || ev[0].Text != text {
				t.Fatalf("server got %+v, want %q", ev[0], text)
			}
			back := "ünïcode ✓ " + strings.Repeat("x", 3)
			srv.SetClipboard(back)
			eventually(t, "the server clipboard", func() bool { return c.Clipboard() == back })
		})
	}
}
