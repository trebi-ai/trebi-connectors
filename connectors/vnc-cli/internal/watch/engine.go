// Package watch finds changes in areas of the screen and streams them as
// events. The events and their JSON names are those of glkvm-cli watch,
// plus the events that only VNC has. The CLI watch and serve share it.
package watch

import (
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ErrArea is an area that does not fit the screen; a new connection cannot
// fix it.
var ErrArea = errors.New("invalid area")

// Event kinds.
const (
	KindReady        = "ready"
	KindChange       = "change"
	KindStable       = "stable"
	KindError        = "error"
	KindClipboard    = "clipboard"
	KindBell         = "bell"
	KindResize       = "resize"
	KindCursor       = "cursor"
	KindDisconnected = "disconnected"
	KindConnected    = "connected"
)

// Rect is a rectangle in reference pixels.
type Rect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

func rectOf(r image.Rectangle) Rect { return Rect{r.Min.X, r.Min.Y, r.Dx(), r.Dy()} }

// Area is a named rectangle to watch.
type Area struct {
	Name string `json:"name"`
	Rect Rect   `json:"rect"`
}

// ParseArea parses "[NAME=]X,Y,W,H". Without a name, the area is "area<n>".
func ParseArea(s string, n int) (Area, error) {
	a := Area{Name: fmt.Sprintf("area%d", n)}
	if name, rest, ok := strings.Cut(s, "="); ok {
		a.Name, s = strings.TrimSpace(name), rest
	}
	parts := strings.Split(s, ",")
	var v [4]int
	if len(parts) != 4 || a.Name == "" {
		return a, fmt.Errorf("invalid area %q (want [NAME=]X,Y,W,H)", s)
	}
	for i, p := range parts {
		var err error
		if v[i], err = strconv.Atoi(strings.TrimSpace(p)); err != nil || v[i] < 0 {
			return a, fmt.Errorf("invalid area %q (want [NAME=]X,Y,W,H)", s)
		}
	}
	if v[2] == 0 || v[3] == 0 {
		return a, fmt.Errorf("area %q has no size", a.Name)
	}
	a.Rect = Rect{v[0], v[1], v[2], v[3]}
	return a, nil
}

// ParseSize parses "W" or "WxH". A missing height is 0.
func ParseSize(s string) (image.Point, error) {
	ws, hs, _ := strings.Cut(s, "x")
	w, err := strconv.Atoi(ws)
	if err != nil || w < 2 {
		return image.Point{}, fmt.Errorf("invalid size %q (want W or WxH)", s)
	}
	h := 0
	if hs != "" {
		if h, err = strconv.Atoi(hs); err != nil || h < 2 {
			return image.Point{}, fmt.Errorf("invalid size %q (want W or WxH)", s)
		}
	}
	return image.Pt(w, h), nil
}

// Options are the limits of a change.
type Options struct {
	Threshold  float64       `json:"threshold"`   // percent of the area pixels that must change
	MinPixels  int           `json:"min_pixels"`  // and at least this many pixels
	PixelDelta int           `json:"pixel_delta"` // luma difference (0-255) that counts as a changed pixel
	Settle     time.Duration `json:"settle"`      // no change for this long ends a change
	MaxFPS     float64       `json:"max_fps"`     // most checks per second; 0 is no limit
	SaveDir    string        `json:"save_dir,omitempty"`
}

// Size is a width and a height.
type Size struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Cursor describes a new pointer shape.
type Cursor struct {
	Hash string `json:"hash"`
	X    int    `json:"x"` // hot spot
	Y    int    `json:"y"`
	W    int    `json:"w"`
	H    int    `json:"h"`
}

// Event is one line of the JSONL output.
type Event struct {
	Event      string    `json:"event"`
	Time       time.Time `json:"time"`
	Area       string    `json:"area,omitempty"`
	Rect       *Rect     `json:"rect,omitempty"`
	Changed    float64   `json:"changed_pct,omitempty"`
	Pixels     int       `json:"changed_px,omitempty"`
	BBox       *Rect     `json:"bbox,omitempty"`
	ChangingMS *int64    `json:"changing_ms,omitempty"`
	Image      string    `json:"image,omitempty"`
	Frame      int       `json:"frame,omitempty"`
	Ref        *Size     `json:"ref,omitempty"`
	Picture    *Rect     `json:"picture,omitempty"`
	Areas      []Area    `json:"areas,omitempty"`
	Error      string    `json:"error,omitempty"`

	// VNC only.
	Text   string  `json:"text,omitempty"`
	Size   *Size   `json:"size,omitempty"`
	Cursor *Cursor `json:"cursor,omitempty"`
}

// Engine compares the areas with their last stable state. An area reports
// "change" when it starts to change and "stable" when it has not changed
// for Options.Settle. Only the areas that a changed rectangle touches are
// read; the server sends no rectangle for an area that stays the same.
type Engine struct {
	opt    Options
	ref    image.Point
	given  []Area
	view   image.Rectangle
	areas  []*areaState
	emit   func(Event) error
	frames int
}

type areaState struct {
	Area
	px          image.Rectangle
	base, prev  *image.RGBA
	changing    bool
	since, last time.Time
}

// New makes an engine. Area coordinates are pixels of a reference image of
// size ref (zero: the screen; zero height: keep the aspect ratio). Without
// areas, it watches the whole screen as "screen".
func New(areas []Area, ref image.Point, opt Options, emit func(Event) error) *Engine {
	return &Engine{opt: opt, ref: ref, given: areas, emit: emit}
}

// Ready maps the areas to a screen of the given size, takes their base
// state, and sends "ready". Call it again after a resize: the reference
// size stays, so the areas scale with the screen.
func (e *Engine) Ready(size image.Point, snap func(image.Rectangle) *image.RGBA, now time.Time) error {
	e.view = image.Rectangle{Max: size}
	if e.view.Empty() {
		return fmt.Errorf("%w: the screen has no size", ErrArea)
	}
	if e.ref.X == 0 {
		e.ref = size
	}
	if e.ref.Y == 0 {
		e.ref.Y = e.ref.X * size.Y / size.X
	}
	given := e.given
	if len(given) == 0 {
		given = []Area{{Name: "screen", Rect: Rect{0, 0, e.ref.X, e.ref.Y}}}
	}
	ev := Event{Event: KindReady, Time: now, Ref: &Size{e.ref.X, e.ref.Y}, Picture: &Rect{0, 0, size.X, size.Y}}
	e.areas = e.areas[:0]
	for _, a := range given {
		r := a.Rect
		px := image.Rect(
			r.X*size.X/e.ref.X, r.Y*size.Y/e.ref.Y,
			(r.X+r.W)*size.X/e.ref.X, (r.Y+r.H)*size.Y/e.ref.Y,
		).Intersect(e.view)
		if px.Empty() {
			return fmt.Errorf("%w: %q is outside the %dx%d reference image", ErrArea, a.Name, e.ref.X, e.ref.Y)
		}
		st := &areaState{Area: a, px: px}
		if snap != nil {
			st.base = snap(px)
			st.prev = st.base
		}
		e.areas = append(e.areas, st)
		ev.Areas = append(ev.Areas, a)
	}
	return e.emit(ev)
}

// Frame checks a full frame, as glkvm-cli does with each frame.
func (e *Engine) Frame(img *image.RGBA, now time.Time) error {
	if e.view.Empty() {
		if err := e.Ready(img.Bounds().Size(), nil, now); err != nil {
			return err
		}
	}
	return e.Check(func(r image.Rectangle) *image.RGBA { return crop(img, r) }, []image.Rectangle{img.Bounds()}, now)
}

// Check reads the areas that dirty touches and ends the changes that
// settled.
func (e *Engine) Check(snap func(image.Rectangle) *image.RGBA, dirty []image.Rectangle, now time.Time) error {
	e.frames++
	for _, a := range e.areas {
		touched := false
		for _, r := range dirty {
			if r.Overlaps(a.px) {
				touched = true
				break
			}
		}
		cur := a.prev
		if touched || cur == nil {
			cur = snap(a.px)
		}
		switch {
		case a.base == nil:
			a.base = cur
		case !a.changing:
			if !touched {
				break
			}
			if n, bb := e.diff(a.base, cur); e.over(a, n) {
				a.changing, a.since, a.last = true, now, now
				if err := e.report(a, KindChange, now, cur, n, bb); err != nil {
					return err
				}
			}
		default:
			if n, _ := e.diff(a.prev, cur); touched && e.over(a, n) {
				a.last = now
			} else if now.Sub(a.last) >= e.opt.Settle {
				n, bb := e.diff(a.base, cur)
				a.changing, a.base = false, cur
				if err := e.report(a, KindStable, now, cur, n, bb); err != nil {
					return err
				}
			}
		}
		a.prev = cur
	}
	return nil
}

// Due is the time when a change settles with no more update, or zero.
func (e *Engine) Due() time.Time {
	var t time.Time
	for _, a := range e.areas {
		if d := a.last.Add(e.opt.Settle); a.changing && (t.IsZero() || d.Before(t)) {
			t = d
		}
	}
	return t
}

func (e *Engine) over(a *areaState, n int) bool {
	total := a.px.Dx() * a.px.Dy()
	return n >= e.opt.MinPixels && float64(n)*100 >= e.opt.Threshold*float64(total)
}

// diff counts the pixels whose luma differs by more than PixelDelta, and
// their bounds.
func (e *Engine) diff(a, b *image.RGBA) (int, image.Rectangle) {
	var n int
	var bb image.Rectangle
	for y := 0; y < a.Rect.Dy(); y++ {
		ra, rb := a.Pix[y*a.Stride:], b.Pix[y*b.Stride:]
		for x := 0; x < a.Rect.Dx(); x++ {
			d := luma(ra[x*4:]) - luma(rb[x*4:])
			if d > e.opt.PixelDelta || -d > e.opt.PixelDelta {
				n++
				bb = bb.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return n, bb
}

func luma(p []byte) int { return (int(p[0])*77 + int(p[1])*150 + int(p[2])*29) >> 8 }

func (e *Engine) report(a *areaState, kind string, now time.Time, cur *image.RGBA, n int, bb image.Rectangle) error {
	total := a.px.Dx() * a.px.Dy()
	ev := Event{
		Event: kind, Time: now, Area: a.Name, Rect: &a.Rect, Frame: e.frames,
		Pixels: n, Changed: float64(int(float64(n)*10000/float64(total))) / 100,
	}
	if !bb.Empty() {
		r := e.toRef(bb.Add(a.px.Min))
		ev.BBox = &r
	}
	if kind == KindStable {
		ms := a.last.Sub(a.since).Milliseconds()
		ev.ChangingMS = &ms
	}
	if e.opt.SaveDir != "" {
		ev.Image = filepath.Join(e.opt.SaveDir, fmt.Sprintf("%s-%06d-%s.png", a.Name, e.frames, kind))
		if err := savePNG(ev.Image, cur); err != nil {
			return err
		}
	}
	return e.emit(ev)
}

// toRef maps a rectangle in screen pixels to reference pixels.
func (e *Engine) toRef(r image.Rectangle) Rect {
	r = r.Sub(e.view.Min)
	x0, y0 := r.Min.X*e.ref.X/e.view.Dx(), r.Min.Y*e.ref.Y/e.view.Dy()
	x1, y1 := (r.Max.X*e.ref.X+e.view.Dx()-1)/e.view.Dx(), (r.Max.Y*e.ref.Y+e.view.Dy()-1)/e.view.Dy()
	return rectOf(image.Rect(x0, y0, x1, y1))
}

func crop(img *image.RGBA, r image.Rectangle) *image.RGBA {
	out := image.NewRGBA(image.Rectangle{Max: r.Size()})
	for y := 0; y < r.Dy(); y++ {
		copy(out.Pix[y*out.Stride:], img.Pix[img.PixOffset(r.Min.X, r.Min.Y+y):][:4*r.Dx()])
	}
	return out
}

func savePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
