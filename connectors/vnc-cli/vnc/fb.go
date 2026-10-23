package vnc

import (
	"image"
	"sync"
)

// framebuffer is the client copy of the remote screen in RGBA.
type framebuffer struct {
	mu  sync.RWMutex
	img *image.RGBA
}

func newFramebuffer(w, h int) *framebuffer {
	return &framebuffer{img: image.NewRGBA(image.Rect(0, 0, w, h))}
}

func (f *framebuffer) bounds() image.Rectangle {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.img.Rect
}

// resize keeps the old content where it overlaps.
func (f *framebuffer) resize(w, h int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.img.Rect.Dx() == w && f.img.Rect.Dy() == h {
		return
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r := img.Rect.Intersect(f.img.Rect)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		copy(img.Pix[img.PixOffset(0, y):img.PixOffset(r.Max.X, y)], f.img.Pix[f.img.PixOffset(0, y):])
	}
	f.img = img
}

// row converts n wire pixels from src into the framebuffer at (x, y).
func (f *framebuffer) row(x, y int, src []byte, n int, cv *converter) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o := f.img.PixOffset(x, y)
	cv.row(f.img.Pix[o:o+n*4], src, n)
}

// rows converts a block of wire pixels, w pixels a row, at r.
func (f *framebuffer) rows(r image.Rectangle, src []byte, cv *converter) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, step := r.Dx(), r.Dx()*cv.bypp
	for y := r.Min.Y; y < r.Max.Y; y++ {
		o := f.img.PixOffset(r.Min.X, y)
		cv.row(f.img.Pix[o:o+w*4], src[(y-r.Min.Y)*step:], w)
	}
}

// fill paints r with one wire pixel.
func (f *framebuffer) fill(r image.Rectangle, pixel []byte, cv *converter) {
	var c [4]byte
	c[0], c[1], c[2] = cv.rgb(pixel)
	c[3] = 0xff
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Empty() {
		return
	}
	first := f.img.Pix[f.img.PixOffset(r.Min.X, r.Min.Y):f.img.PixOffset(r.Max.X, r.Min.Y)]
	for i := 0; i < len(first); i += 4 {
		copy(first[i:i+4], c[:])
	}
	for y := r.Min.Y + 1; y < r.Max.Y; y++ {
		copy(f.img.Pix[f.img.PixOffset(r.Min.X, y):], first)
	}
}

// copyRect copies the area at sp with the size of dst to dst. The areas
// can overlap.
func (f *framebuffer) copyRect(sp image.Point, dst image.Rectangle) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w := dst.Dx() * 4
	if sp.Y < dst.Min.Y {
		for y := dst.Dy() - 1; y >= 0; y-- {
			copy(f.img.Pix[f.img.PixOffset(dst.Min.X, dst.Min.Y+y):][:w], f.img.Pix[f.img.PixOffset(sp.X, sp.Y+y):][:w])
		}
		return
	}
	for y := 0; y < dst.Dy(); y++ {
		copy(f.img.Pix[f.img.PixOffset(dst.Min.X, dst.Min.Y+y):][:w], f.img.Pix[f.img.PixOffset(sp.X, sp.Y+y):][:w])
	}
}

// snapshot copies the area r (clipped to the screen).
func (f *framebuffer) snapshot(r image.Rectangle) *image.RGBA {
	f.mu.RLock()
	defer f.mu.RUnlock()
	r = r.Intersect(f.img.Rect)
	out := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	for y := r.Min.Y; y < r.Max.Y; y++ {
		copy(out.Pix[out.PixOffset(0, y-r.Min.Y):][:r.Dx()*4], f.img.Pix[f.img.PixOffset(r.Min.X, y):])
	}
	return out
}
