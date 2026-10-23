package watch

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/draw"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

func frame(boxes ...image.Rectangle) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 200, 100))
	draw.Draw(img, img.Rect, image.NewUniform(color.Black), image.Point{}, draw.Src)
	for i, b := range boxes {
		c := color.RGBA{255, uint8(80 * i), 255, 255}
		draw.Draw(img, b, image.NewUniform(c), image.Point{}, draw.Src)
	}
	return img
}

func collect(evs *[]Event) func(Event) error {
	return func(ev Event) error { *evs = append(*evs, ev); return nil }
}

var opt = Options{Threshold: 0.5, MinPixels: 16, PixelDelta: 24, Settle: 500 * time.Millisecond}

// testdata/glkvm.jsonl is the output of glkvm-cli watch for the same frames.
func TestGlkvmParity(t *testing.T) {
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	w := New([]Area{{Name: "a", Rect: Rect{0, 0, 200, 200}}, {Name: "b", Rect: Rect{200, 0, 200, 200}}},
		image.Pt(400, 200), opt, func(ev Event) error { return enc.Encode(ev) })
	box, box2, tiny := image.Rect(10, 10, 30, 20), image.Rect(120, 50, 160, 90), image.Rect(150, 10, 152, 12)
	steps := []struct {
		img *image.RGBA
		ms  int
	}{
		{frame(), 0},
		{frame(), 250},
		{frame(box), 500},
		{frame(box), 750},
		{frame(box), 1250},
		{frame(box, box2), 1500},
		{frame(box, box2, box2.Add(image.Pt(-10, 0))), 1800},
		{frame(box, box2.Add(image.Pt(-10, 0))), 2000},
		{frame(box, box2.Add(image.Pt(-10, 0))), 2600},
		{frame(box, box2.Add(image.Pt(-10, 0)), tiny), 2800},
		{frame(), 3000},
		{frame(), 3600},
	}
	for _, s := range steps {
		if err := w.Frame(s.img, t0.Add(time.Duration(s.ms)*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("testdata/glkvm.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != string(want) {
		t.Errorf("got:\n%s\nwant:\n%s", out.String(), want)
	}
}

// screen is a screen that the test paints and the engine reads.
type screen struct{ img *image.RGBA }

func (s *screen) snap(r image.Rectangle) *image.RGBA { return crop(s.img, r) }

func (s *screen) paint(r image.Rectangle, c color.RGBA) image.Rectangle {
	draw.Draw(s.img, r, image.NewUniform(c), image.Point{}, draw.Src)
	return r
}

func TestPush(t *testing.T) {
	s := &screen{frame()}
	var evs []Event
	e := New([]Area{{Name: "left", Rect: Rect{0, 0, 100, 100}}, {Name: "right", Rect: Rect{100, 0, 100, 100}}}, image.Point{}, opt, collect(&evs))
	if err := e.Ready(image.Pt(200, 100), s.snap, t0); err != nil {
		t.Fatal(err)
	}
	if evs[0].Event != KindReady || *evs[0].Ref != (Size{200, 100}) || len(evs[0].Areas) != 2 {
		t.Fatalf("ready = %+v", evs[0])
	}
	if !e.Due().IsZero() {
		t.Fatal("due with no change")
	}

	// A paint in the right area, reported as a dirty rect in the left area
	// only: the engine reads only the left area, so it sees no change.
	s.paint(image.Rect(150, 10, 170, 30), color.RGBA{255, 255, 255, 255})
	if err := e.Check(s.snap, []image.Rectangle{image.Rect(0, 0, 10, 10)}, t0.Add(100*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("event for an untouched area: %+v", evs[1:])
	}

	// The same paint with its own rect.
	if err := e.Check(s.snap, []image.Rectangle{image.Rect(150, 10, 170, 30)}, t0.Add(200*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[1].Event != KindChange || evs[1].Area != "right" || *evs[1].BBox != (Rect{150, 10, 20, 20}) || evs[1].Pixels != 400 {
		t.Fatalf("change = %+v", evs[1:])
	}
	if got := e.Due(); !got.Equal(t0.Add(700 * time.Millisecond)) {
		t.Fatalf("due = %v", got)
	}

	// The area changes again before it settles: the settle time restarts.
	s.paint(image.Rect(150, 40, 170, 60), color.RGBA{255, 255, 255, 255})
	if err := e.Check(s.snap, []image.Rectangle{image.Rect(150, 40, 170, 60)}, t0.Add(600*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if err := e.Check(s.snap, nil, t0.Add(1000*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("stable too early: %+v", evs[2:])
	}
	if err := e.Check(s.snap, nil, t0.Add(1100*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	st := evs[len(evs)-1]
	if len(evs) != 3 || st.Event != KindStable || st.Area != "right" || *st.ChangingMS != 400 || st.Pixels != 800 {
		t.Fatalf("stable = %+v", evs[2:])
	}
	if !e.Due().IsZero() {
		t.Fatal("due after stable")
	}
}

func TestLimits(t *testing.T) {
	cases := []struct {
		name string
		opt  Options
		box  image.Rectangle
		col  color.RGBA
		want bool
	}{
		{"over all", opt, image.Rect(0, 0, 10, 10), color.RGBA{255, 255, 255, 255}, true},
		{"under min pixels", opt, image.Rect(0, 0, 3, 3), color.RGBA{255, 255, 255, 255}, false},
		{"under threshold", Options{Threshold: 2, MinPixels: 1, PixelDelta: 24}, image.Rect(0, 0, 10, 10), color.RGBA{255, 255, 255, 255}, false},
		{"under pixel delta", opt, image.Rect(0, 0, 10, 10), color.RGBA{20, 20, 20, 255}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &screen{frame()}
			var evs []Event
			e := New([]Area{{Name: "a", Rect: Rect{0, 0, 100, 100}}}, image.Point{}, c.opt, collect(&evs))
			if err := e.Ready(image.Pt(200, 100), s.snap, t0); err != nil {
				t.Fatal(err)
			}
			if err := e.Check(s.snap, []image.Rectangle{s.paint(c.box, c.col)}, t0); err != nil {
				t.Fatal(err)
			}
			if got := len(evs) == 2 && evs[1].Event == KindChange; got != c.want {
				t.Fatalf("change = %v, want %v: %+v", got, c.want, evs)
			}
		})
	}
}

func TestResizeScales(t *testing.T) {
	var evs []Event
	e := New([]Area{{Name: "a", Rect: Rect{50, 0, 50, 50}}}, image.Pt(100, 0), opt, collect(&evs))
	if err := e.Ready(image.Pt(200, 100), nil, t0); err != nil {
		t.Fatal(err)
	}
	if got := e.areas[0].px; got != image.Rect(100, 0, 200, 100) {
		t.Fatalf("px = %v", got)
	}
	if err := e.Ready(image.Pt(400, 200), nil, t0); err != nil {
		t.Fatal(err)
	}
	if got := e.areas[0].px; got != image.Rect(200, 0, 400, 200) {
		t.Fatalf("px after resize = %v", got)
	}
	if *evs[1].Ref != (Size{100, 50}) {
		t.Fatalf("ref = %+v", evs[1].Ref)
	}
}

func TestAreaErrors(t *testing.T) {
	e := New([]Area{{Name: "out", Rect: Rect{500, 0, 10, 10}}}, image.Point{}, opt, func(Event) error { return nil })
	if err := e.Ready(image.Pt(200, 100), nil, t0); err == nil {
		t.Fatal("no error for an area outside the screen")
	}
	for _, s := range []string{"1,2,3", "a=1,2,3,x", "=1,2,3,4", "1,2,0,4", "-1,2,3,4"} {
		if _, err := ParseArea(s, 1); err == nil {
			t.Errorf("ParseArea(%q) gave no error", s)
		}
	}
	if a, err := ParseArea("clock=10, 20, 30, 40", 1); err != nil || a != (Area{"clock", Rect{10, 20, 30, 40}}) {
		t.Errorf("ParseArea = %+v, %v", a, err)
	}
	if a, _ := ParseArea("1,2,3,4", 3); a.Name != "area3" {
		t.Errorf("name = %q", a.Name)
	}
	if p, err := ParseSize("1920"); err != nil || p != image.Pt(1920, 0) {
		t.Errorf("ParseSize = %v, %v", p, err)
	}
	if _, err := ParseSize("1x2x3"); err == nil {
		t.Error("ParseSize(1x2x3) gave no error")
	}
}

func TestSave(t *testing.T) {
	dir := t.TempDir()
	s := &screen{frame()}
	var evs []Event
	o := opt
	o.SaveDir = dir
	e := New(nil, image.Point{}, o, collect(&evs))
	if err := e.Ready(image.Pt(200, 100), s.snap, t0); err != nil {
		t.Fatal(err)
	}
	if err := e.Check(s.snap, []image.Rectangle{s.paint(image.Rect(0, 0, 20, 20), color.RGBA{255, 255, 255, 255})}, t0); err != nil {
		t.Fatal(err)
	}
	if evs[1].Area != "screen" || evs[1].Image != filepath.Join(dir, "screen-000001-change.png") {
		t.Fatalf("change = %+v", evs[1])
	}
	if _, err := os.Stat(evs[1].Image); err != nil {
		t.Fatal(err)
	}
}
