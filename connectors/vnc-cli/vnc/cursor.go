package vnc

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"image"
	"image/draw"
	"time"
)

// pointerSettle is how long the client ignores PointerPos after its own
// Pointer call. The server can still report an older position.
const pointerSettle = time.Second

// maxCursorSide bounds the width and the height of a pointer shape.
const maxCursorSide = 1024

// cursorDecoder reads the Cursor pseudo-encoding: x,y is the hot spot, then
// w*h pixels and a bit mask with each row padded to a byte.
type cursorDecoder struct{}

func (cursorDecoder) decode(c *Conn, r image.Rectangle) (bool, error) {
	w, h := r.Dx(), r.Dy()
	if w > maxCursorSide || h > maxCursorSide {
		return false, fmt.Errorf("a pointer shape of %dx%d is too large", w, h)
	}
	bpp, mrow := c.cv.bypp, (w+7)/8
	b, err := c.r.bytes(w*h*bpp + mrow*h)
	if err != nil {
		return false, err
	}
	if w == 0 || h == 0 {
		c.setCursor(nil)
		return false, nil
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		c.cv.row(img.Pix[y*img.Stride:], b[y*w*bpp:], w)
	}
	maskCursor(img, b[w*h*bpp:])
	c.setCursor(&Cursor{HotSpot: r.Min, Image: img})
	return false, nil
}

// xcursorDecoder reads the XCursor pseudo-encoding: the foreground and the
// background colour, then a bitmap and a bit mask.
type xcursorDecoder struct{}

func (xcursorDecoder) decode(c *Conn, r image.Rectangle) (bool, error) {
	w, h := r.Dx(), r.Dy()
	if w == 0 || h == 0 {
		c.setCursor(nil)
		return false, nil
	}
	if w > maxCursorSide || h > maxCursorSide {
		return false, fmt.Errorf("a pointer shape of %dx%d is too large", w, h)
	}
	mrow := (w + 7) / 8
	b, err := c.r.bytes(6 + 2*mrow*h)
	if err != nil {
		return false, err
	}
	fg, bg, bitmap := b[0:3], b[3:6], b[6:6+mrow*h]
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			col := bg
			if bitmap[y*mrow+x/8]&(0x80>>(x%8)) != 0 {
				col = fg
			}
			o := y*img.Stride + x*4
			img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = col[0], col[1], col[2], 0xff
		}
	}
	maskCursor(img, b[6+mrow*h:])
	c.setCursor(&Cursor{HotSpot: r.Min, Image: img})
	return false, nil
}

// maskCursor clears the pixels whose mask bit is 0.
func maskCursor(img *image.RGBA, mask []byte) {
	w, h, mrow := img.Rect.Dx(), img.Rect.Dy(), (img.Rect.Dx()+7)/8
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if mask[y*mrow+x/8]&(0x80>>(x%8)) == 0 {
				clear(img.Pix[y*img.Stride+x*4:][:4])
			}
		}
	}
}

// pointerPosDecoder reads PointerPos: x,y is the pointer, with no payload.
type pointerPosDecoder struct{}

func (pointerPosDecoder) decode(c *Conn, r image.Rectangle) (bool, error) {
	c.mu.Lock()
	if time.Since(c.movedAt) >= pointerSettle {
		c.pointer = r.Min
	}
	c.mu.Unlock()
	return false, nil
}

// setCursor stores a new pointer shape, or no shape when cur is nil.
func (c *Conn) setCursor(cur *Cursor) {
	if cur != nil {
		h := sha256.New()
		var hdr [16]byte
		binary.BigEndian.PutUint32(hdr[0:], uint32(cur.HotSpot.X))
		binary.BigEndian.PutUint32(hdr[4:], uint32(cur.HotSpot.Y))
		binary.BigEndian.PutUint32(hdr[8:], uint32(cur.Image.Rect.Dx()))
		binary.BigEndian.PutUint32(hdr[12:], uint32(cur.Image.Rect.Dy()))
		h.Write(hdr[:])
		h.Write(cur.Image.Pix)
		cur.Hash = hex.EncodeToString(h.Sum(nil)[:8])
	}
	c.mu.Lock()
	c.cursor = cur
	c.mu.Unlock()
	c.publish(Event{Kind: EventCursor, Cursor: cur})
}

// PointerPosition is the last pointer position: from Pointer, or from the
// server when it moved the pointer.
func (c *Conn) PointerPosition() image.Point {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pointer
}

// SnapshotCursor copies the area r of the screen, as Snapshot does. With
// CursorLocal, it draws the last pointer shape from the server at the
// pointer position. The other modes add nothing.
func (c *Conn) SnapshotCursor(r image.Rectangle, mode CursorMode) *image.RGBA {
	b := c.fb.bounds()
	if r.Empty() {
		r = b
	}
	img := c.fb.snapshot(r)
	if mode != CursorLocal {
		return img
	}
	c.mu.Lock()
	cur, at := c.cursor, c.pointer
	c.mu.Unlock()
	if cur == nil {
		return img
	}
	dst := cur.Image.Rect.Add(at.Sub(cur.HotSpot)).Sub(r.Intersect(b).Min)
	draw.Draw(img, dst, cur.Image, image.Point{}, draw.Over)
	return img
}
