package vnc

import (
	"image"
	"image/color"
	"testing"
)

func solid(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return img
}

func TestDefaultFuzz(t *testing.T) {
	for pf, want := range map[PixelFormat]int{FormatBGRX8888: 0, FormatRGBX8888: 0, FormatRGB565: 8} {
		if got := pf.DefaultFuzz(); got != want {
			t.Errorf("%v: fuzz %d, want %d", pf, got, want)
		}
	}
}

func TestMatch(t *testing.T) {
	a := solid(16, 16, color.RGBA{100, 100, 100, 255})
	b := solid(16, 16, color.RGBA{100, 100, 101, 255})
	if !Match(a, a, 0, 0) {
		t.Fatal("an image does not match itself")
	}
	// One blue step scores about 0.36: fuzz 0 is exact.
	if d := WorstDelta(a, b, 0); d < 0.3 || d > 0.4 {
		t.Fatalf("one blue step: delta %.2f", d)
	}
	if Match(a, b, 0, 0) || !Match(a, b, 1, 0) {
		t.Fatal("fuzz 0 must be exact, fuzz 1 must accept one blue step")
	}
	if Match(a, solid(16, 8, color.RGBA{100, 100, 100, 255}), 255, 0) {
		t.Fatal("a different size matches")
	}
	// A blur makes a one-pixel spot less different.
	c := solid(16, 16, color.RGBA{100, 100, 100, 255})
	c.SetRGBA(8, 8, color.RGBA{255, 255, 255, 255})
	if WorstDelta(a, c, 2) >= WorstDelta(a, c, 0)/4 {
		t.Fatalf("blur 2: %.1f, no blur: %.1f", WorstDelta(a, c, 2), WorstDelta(a, c, 0))
	}
}
