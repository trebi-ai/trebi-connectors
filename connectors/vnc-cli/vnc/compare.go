package vnc

import (
	"image"
	"image/draw"
	"math"
)

// The screen compare of vncdotool imagematch.py (specs/expect-matching.md):
// the per-pixel YIQ delta of pixelmatch, on a scale of 0 to 255. Two images
// match when no pixel is more than fuzz from the other.

var (
	yiqY       = [3]float64{0.29889531, 0.58662247, 0.11448223}
	yiqI       = [3]float64{0.59597799, -0.27417610, -0.32180189}
	yiqQ       = [3]float64{0.11147015, -0.52248202, 0.41101187}
	yiqWeights = [3]float64{0.5053, 0.299, 0.1957}
)

const yiqMaxDelta = 35215.0

func yiqScale(d float64) float64 { return 255 * math.Sqrt(d/yiqMaxDelta) }

// channelBound is the largest YIQ delta that a difference of up to dr, dg,
// db in the channels can give.
func channelBound(dr, dg, db int) int {
	var d float64
	for i, axis := range [3][3]float64{yiqY, yiqI, yiqQ} {
		v := math.Abs(axis[0])*float64(dr) + math.Abs(axis[1])*float64(dg) + math.Abs(axis[2])*float64(db)
		d += yiqWeights[i] * v * v
	}
	return int(math.Ceil(yiqScale(d)))
}

// DefaultFuzz is the fuzz that the quantization of the format makes
// necessary: 0 for 8 bits per channel, 8 for rgb565.
func (pf PixelFormat) DefaultFuzz() int {
	t := pf.ChannelTolerance()
	return channelBound(t[0], t[1], t[2])
}

// Match tells if a and b are the same screen within fuzz (0 is exact),
// after a box blur of radius blur on both. A different size never matches.
func Match(a, b image.Image, fuzz, blur int) bool {
	if a.Bounds().Size() != b.Bounds().Size() {
		return false
	}
	ra, rb := toRGBA(a), toRGBA(b)
	if channelBound(maxChannelDiff(ra, rb)) <= fuzz {
		return true
	}
	return WorstDelta(ra, rb, blur) <= float64(fuzz)
}

// WorstDelta is the largest per-pixel YIQ delta between a and b, from 0 to
// 255. The images must have the same size.
func WorstDelta(a, b image.Image, blur int) float64 {
	ra, rb := boxBlur(toRGBA(a), blur), boxBlur(toRGBA(b), blur)
	w, h := ra.Rect.Dx(), ra.Rect.Dy()
	var worst float64
	for y := 0; y < h; y++ {
		pa := ra.Pix[y*ra.Stride : y*ra.Stride+w*4]
		pb := rb.Pix[y*rb.Stride : y*rb.Stride+w*4]
		for x := 0; x < w*4; x += 4 {
			dr := float64(pa[x]) - float64(pb[x])
			dg := float64(pa[x+1]) - float64(pb[x+1])
			db := float64(pa[x+2]) - float64(pb[x+2])
			dy := yiqY[0]*dr + yiqY[1]*dg + yiqY[2]*db
			di := yiqI[0]*dr + yiqI[1]*dg + yiqI[2]*db
			dq := yiqQ[0]*dr + yiqQ[1]*dg + yiqQ[2]*db
			if d := yiqWeights[0]*dy*dy + yiqWeights[1]*di*di + yiqWeights[2]*dq*dq; d > worst {
				worst = d
			}
		}
	}
	return yiqScale(worst)
}

func maxChannelDiff(a, b *image.RGBA) (dr, dg, db int) {
	w, h := a.Rect.Dx(), a.Rect.Dy()
	for y := 0; y < h; y++ {
		pa := a.Pix[y*a.Stride : y*a.Stride+w*4]
		pb := b.Pix[y*b.Stride : y*b.Stride+w*4]
		for x := 0; x < w*4; x += 4 {
			dr = max(dr, absDiff(pa[x], pb[x]))
			dg = max(dg, absDiff(pa[x+1], pb[x+1]))
			db = max(db, absDiff(pa[x+2], pb[x+2]))
		}
	}
	return dr, dg, db
}

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

func toRGBA(img image.Image) *image.RGBA {
	if r, ok := img.(*image.RGBA); ok {
		return r
	}
	b := img.Bounds()
	r := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(r, r.Rect, img, b.Min, draw.Src)
	return r
}

// boxBlur is the Pillow BoxBlur: a (2r+1) square mean with the edge pixels
// extended, in two passes, rounded to 8 bits.
func boxBlur(src *image.RGBA, r int) *image.RGBA {
	if r <= 0 {
		return src
	}
	w, h := src.Rect.Dx(), src.Rect.Dy()
	tmp := image.NewRGBA(image.Rect(0, 0, w, h))
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	n := 2*r + 1
	at := func(img *image.RGBA, x, y, c int) int {
		x, y = min(max(x, 0), w-1), min(max(y, 0), h-1)
		return int(img.Pix[y*img.Stride+x*4+c])
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			for c := 0; c < 3; c++ {
				s := 0
				for k := -r; k <= r; k++ {
					s += at(src, x+k, y, c)
				}
				tmp.Pix[y*tmp.Stride+x*4+c] = uint8((s + n/2) / n)
			}
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			for c := 0; c < 3; c++ {
				s := 0
				for k := -r; k <= r; k++ {
					s += at(tmp, x, y+k, c)
				}
				dst.Pix[y*dst.Stride+x*4+c] = uint8((s + n/2) / n)
			}
			dst.Pix[y*dst.Stride+x*4+3] = 0xff
		}
	}
	return dst
}
