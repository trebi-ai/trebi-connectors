package vnc_test

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc"
)

// The goldens are server bytes that vncdotool captured from real servers
// (tests/unit/fixtures/goldens). Each step draws a scene of
// ../fleet/goldens/scenes.
type golden struct {
	Encoding      string `json:"encoding"`
	PixelFormat   string `json:"pixel_format"`
	ToleranceKind string `json:"tolerance_kind"`
	Tolerance     [3]int `json:"tolerance"`
	Fuzz          float64
	Blur          int
	JPEGQuality   *int `json:"jpeg_quality"`
}

func TestGoldens(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join("testdata", "goldens", "*"))
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no goldens: %v", err)
	}
	for _, dir := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) { replayGolden(t, dir) })
	}
}

func replayGolden(t *testing.T, dir string) {
	var g golden
	b, err := os.ReadFile(filepath.Join(dir, "conditions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	pf, ok := vnc.PixelFormats[g.PixelFormat]
	if !ok {
		t.Fatalf("pixel format %q", g.PixelFormat)
	}
	o := vnc.Options{Encodings: []int32{vnc.EncodingNames[g.Encoding]}, PixelFormat: &pf}
	if g.JPEGQuality != nil {
		o.JPEG, o.JPEGQuality = true, *g.JPEGQuality
	}

	cn, sn := net.Pipe()
	go io.Copy(io.Discard, sn)
	init := gunzip(t, filepath.Join(dir, "init.bin.gz"))
	go sn.Write(init)
	c, err := vnc.NewConn(testCtx(t), cn, o)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	sub := c.Subscribe()
	defer sub.Close()

	steps, _ := filepath.Glob(filepath.Join(dir, "step-*.bin.gz"))
	for _, step := range steps {
		// A Bell after the step tells when the client has decoded it.
		if _, err := sn.Write(append(gunzip(t, step), 2)); err != nil {
			t.Fatalf("%s: %v (%v)", filepath.Base(step), err, c.Err())
		}
		waitBell(t, sub)
		scene := strings.TrimSuffix(filepath.Base(step), ".bin.gz")[8:]
		got, want := c.Snapshot(image.Rectangle{}), sceneImage(t, scene)
		var msg string
		if g.ToleranceKind == "jpeg-lossy" {
			d := jpegDelta(got, want, g.Blur)
			t.Logf("%s: %.1f from the scene", scene, d)
			if d > g.Fuzz {
				msg = fmt.Sprintf("%.1f from the scene, more than %v at blur %d", d, g.Fuzz, g.Blur)
			}
		} else {
			msg = firstDifference(got, want, g.Tolerance)
		}
		if msg != "" {
			t.Fatalf("%s: %s", filepath.Base(step), msg)
		}
	}
}

func waitBell(t *testing.T, sub *vnc.Subscription) {
	t.Helper()
	for {
		b, err := sub.Next(testCtx(t))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range b.Events {
			if e.Kind == vnc.EventBell {
				return
			}
		}
	}
}

func gunzip(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sceneImage(t testing.TB, name string) *image.RGBA {
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
	out := image.NewRGBA(image.Rect(0, 0, img.Bounds().Dx(), img.Bounds().Dy()))
	draw.Draw(out, out.Rect, img, img.Bounds().Min, draw.Src)
	return out
}

// firstDifference describes the first pixel that differs by more than tol
// in a channel, or returns "".
func firstDifference(got, want *image.RGBA, tol [3]int) string {
	if got.Rect.Size() != want.Rect.Size() {
		return fmt.Sprintf("size %v, want %v", got.Rect.Size(), want.Rect.Size())
	}
	for i := 0; i < len(want.Pix); i += 4 {
		for ch := 0; ch < 3; ch++ {
			if d := int(got.Pix[i+ch]) - int(want.Pix[i+ch]); d > tol[ch] || -d > tol[ch] {
				p := i / 4
				return fmt.Sprintf("pixel (%d,%d) is %v, want %v", p%want.Rect.Dx(), p/want.Rect.Dx(),
					got.Pix[i:i+3], want.Pix[i:i+3])
			}
		}
	}
	return ""
}

// jpegDelta is the largest perceived difference after a box blur, scaled
// to 0..255 (vncdotool imagematch: YIQ from Kotsarenko and Ramos).
func jpegDelta(a, b *image.RGBA, blur int) float64 {
	if a.Rect.Size() != b.Rect.Size() {
		return math.Inf(1)
	}
	a, b = boxBlur(a, blur), boxBlur(b, blur)
	yiq := [3][3]float64{
		{0.29889531, 0.58662247, 0.11448223},
		{0.59597799, -0.27417610, -0.32180189},
		{0.11147015, -0.52248202, 0.41101187},
	}
	weights := [3]float64{0.5053, 0.299, 0.1957}
	worst := 0.0
	for i := 0; i < len(a.Pix); i += 4 {
		delta := 0.0
		for k, row := range yiq {
			d := 0.0
			for ch := 0; ch < 3; ch++ {
				d += row[ch] * (float64(a.Pix[i+ch]) - float64(b.Pix[i+ch]))
			}
			delta += weights[k] * d * d
		}
		worst = max(worst, 255*math.Sqrt(delta/35215))
	}
	return worst
}

// boxBlur averages each pixel over a square of side 2r+1, with the edge
// pixels repeated, as Pillow BoxBlur does: one pass per axis, each rounded.
func boxBlur(img *image.RGBA, r int) *image.RGBA {
	if r == 0 {
		return img
	}
	w, h := img.Rect.Dx(), img.Rect.Dy()
	pass := func(src *image.RGBA, dx, dy int) *image.RGBA {
		dst := image.NewRGBA(src.Rect)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				var sum [3]int
				for k := -r; k <= r; k++ {
					sx, sy := min(max(x+k*dx, 0), w-1), min(max(y+k*dy, 0), h-1)
					o := sy*src.Stride + sx*4
					for ch := 0; ch < 3; ch++ {
						sum[ch] += int(src.Pix[o+ch])
					}
				}
				o := y*dst.Stride + x*4
				for ch := 0; ch < 3; ch++ {
					dst.Pix[o+ch] = uint8((sum[ch] + r) / (2*r + 1))
				}
				dst.Pix[o+3] = 0xff
			}
		}
		return dst
	}
	return pass(pass(img, 1, 0), 0, 1)
}
