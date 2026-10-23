//go:build fleet

// The fleet tests dial the real servers of fleet/docker-compose.yml:
//
//	docker compose -f fleet/docker-compose.yml up -d --build --wait
//	go test -tags fleet ./vnc/
package vnc_test

import (
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc"
)

func fleetAddr(port string) string {
	host := os.Getenv("VNC_FLEET_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	return host + "::" + port
}

func loadScene(t *testing.T, name string) image.Image {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "fleet", "goldens", "scenes", name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// diff counts the pixels whose colour differs by more than tol in a channel.
func diff(a, b image.Image, tol int) int {
	if a.Bounds().Size() != b.Bounds().Size() {
		return -1
	}
	n := 0
	ab, bb := a.Bounds(), b.Bounds()
	for y := 0; y < ab.Dy(); y++ {
		for x := 0; x < ab.Dx(); x++ {
			r1, g1, b1, _ := a.At(ab.Min.X+x, ab.Min.Y+y).RGBA()
			r2, g2, b2, _ := b.At(bb.Min.X+x, bb.Min.Y+y).RGBA()
			for _, d := range []int{int(r1>>8) - int(r2>>8), int(g1>>8) - int(g2>>8), int(b1>>8) - int(b2>>8)} {
				if d > tol || -d > tol {
					n++
					break
				}
			}
		}
	}
	return n
}

// waitScene refreshes until the screen matches the scene, or the time ends.
func waitScene(t *testing.T, ctx context.Context, c *vnc.Conn, name string, tol int) *image.RGBA {
	t.Helper()
	want := loadScene(t, name)
	var got *image.RGBA
	for {
		if err := c.Refresh(ctx, true); err != nil {
			t.Fatalf("refresh: %v", err)
		}
		got = c.Snapshot(image.Rectangle{})
		n := diff(got, want, tol)
		if n == 0 {
			return got
		}
		select {
		case <-ctx.Done():
			t.Fatalf("scene %s: %d pixels differ (size %v, want %v)", name, n, got.Bounds().Size(), want.Bounds().Size())
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func TestFleetTigerVNCRaw(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := vnc.Dial(ctx, vnc.Options{Addr: fleetAddr("5931"), Encodings: []int32{vnc.EncRaw}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	got := waitScene(t, ctx, c, "0", 0)

	out := filepath.Join(t.TempDir(), "screen.png")
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, got); err != nil {
		t.Fatal(err)
	}
	f.Close()
	f, err = os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	back, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	if n := diff(back, loadScene(t, "0"), 0); n != 0 {
		t.Fatalf("written PNG: %d pixels differ", n)
	}
	t.Logf("server %q, format %v, facts %v", c.Name(), c.PixelFormat(), c.Facts())
}

func TestFleetTigerVNCPixelFormats(t *testing.T) {
	for name, pf := range vnc.PixelFormats {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			c, err := vnc.Dial(ctx, vnc.Options{Addr: fleetAddr("5931"), Encodings: []int32{vnc.EncRaw}, PixelFormat: &pf})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			tol := pf.ChannelTolerance()
			waitScene(t, ctx, c, "0", max(tol[0], tol[1], tol[2]))
		})
	}
}

func TestFleetTigerVNCAuth(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := vnc.Dial(ctx, vnc.Options{Addr: fleetAddr("5932"), Password: "vncdotool", Encodings: []int32{vnc.EncRaw}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	waitScene(t, ctx, c, "0", 0)
}

func TestFleetTigerVNCEncodings(t *testing.T) {
	for _, tc := range []struct {
		name string
		encs []int32
	}{
		{"tight", []int32{vnc.EncTight}},
		{"zrle", []int32{vnc.EncZRLE}},
		{"hextile", []int32{vnc.EncHextile}},
		{"rre", []int32{vnc.EncRRE}},
		{"copyrect+raw", []int32{vnc.EncCopyRect, vnc.EncRaw}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			c, err := vnc.Dial(ctx, vnc.Options{Addr: fleetAddr("5931"), Encodings: tc.encs})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			waitScene(t, ctx, c, "0", 0)
		})
	}
}

func TestFleetTigerVNCJPEG(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := vnc.Dial(ctx, vnc.Options{Addr: fleetAddr("5931"), Encodings: []int32{vnc.EncTight}, JPEG: true, JPEGQuality: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	want := sceneImage(t, "0")
	for {
		if err := c.Refresh(ctx, true); err != nil {
			t.Fatalf("refresh: %v", err)
		}
		d := jpegDelta(c.Snapshot(image.Rectangle{}), want, 2)
		if d <= 19 {
			t.Logf("delta %.1f", d)
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("scene 0: delta %.1f", d)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// TestFleetTigerVNCExtensions does not resize: the other tests share the
// screen.
func TestFleetTigerVNCExtensions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := vnc.Dial(ctx, vnc.Options{Addr: fleetAddr("5931")})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	waitScene(t, ctx, c, "0", 0)
	if err := c.Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if s := c.Screens(); len(s) != 1 || s[0].Rect.Size() != image.Pt(256, 192) {
		t.Fatalf("screens %+v", s)
	}
	if err := c.EnableContinuousUpdates(true); err != nil {
		t.Fatal(err)
	}
	if err := c.EnableContinuousUpdates(false); err != nil {
		t.Fatal(err)
	}
	for c.ContinuousUpdates() {
		select {
		case <-ctx.Done():
			t.Fatal("continuous updates did not end")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := c.Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
}

// No test sends a wrong or no password: Xvnc blacklists the client address
// after a few connections that close before auth, and the other fleet tests
// then fail.
