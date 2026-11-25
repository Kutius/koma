package winapi

import (
	"image"
	"math"
)

// MeasureResult holds pixel distances from a probe point to the nearest
// contrasting edges in all four cardinal directions (Shottr-style pixel ruler).
type MeasureResult struct {
	OriginX int
	OriginY int
	Left    int // X coordinate of left edge (inclusive of span)
	Right   int // X coordinate of right edge (inclusive of span)
	Top     int // Y coordinate of top edge (inclusive of span)
	Bottom  int // Y coordinate of bottom edge (inclusive of span)
	Width   int // Right - Left + 1
	Height  int // Bottom - Top + 1
	Valid   bool
}

// MeasureEdges casts four rays from (x, y) on img and finds the nearest
// contrasting pixel boundary in each direction using weighted perceptual color
// distance. sensitivity is in [5..120], where lower values detect subtler edges
// (default ~28 works well for UI elements).
func MeasureEdges(img *image.RGBA, x, y int, sensitivity float64) MeasureResult {
	if img == nil {
		return MeasureResult{}
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 || x < 0 || y < 0 || x >= w || y >= h {
		return MeasureResult{}
	}
	if sensitivity <= 0 {
		sensitivity = 28
	}

	pxAt := func(px, py int) (uint8, uint8, uint8) {
		off := (py-b.Min.Y)*img.Stride + (px-b.Min.X)*4
		return img.Pix[off], img.Pix[off+1], img.Pix[off+2]
	}

	r0, g0, b0 := pxAt(x, y)
	threshSq := sensitivity * sensitivity

	// colorDiffSq computes a fast redmean-weighted squared perceptual color distance.
	colorDiffSq := func(r1, g1, b1, r2, g2, b2 uint8) float64 {
		rmean := 0.5 * (float64(r1) + float64(r2))
		dr := float64(r1) - float64(r2)
		dg := float64(g1) - float64(g2)
		db := float64(b1) - float64(b2)
		return (2+rmean/256.0)*dr*dr + 4*dg*dg + (2+(255-rmean)/256.0)*db*db
	}
	// Scale threshold to match weighted formula (max ~9 * 255^2, so ~3x Euclidean).
	weightedThresh := threshSq * 3.0

	isEdge := func(px, py, prevX, prevY int) bool {
		r, g, bl := pxAt(px, py)
		if colorDiffSq(r0, g0, b0, r, g, bl) > weightedThresh {
			return true
		}
		pr, pg, pb := pxAt(prevX, prevY)
		if colorDiffSq(pr, pg, pb, r, g, bl) > weightedThresh*0.85 {
			return true
		}
		return false
	}

	left := x
	for cx := x - 1; cx >= 0; cx-- {
		if isEdge(cx, y, cx+1, y) {
			break
		}
		left = cx
	}

	right := x
	for cx := x + 1; cx < w; cx++ {
		if isEdge(cx, y, cx-1, y) {
			break
		}
		right = cx
	}

	top := y
	for cy := y - 1; cy >= 0; cy-- {
		if isEdge(x, cy, x, cy+1) {
			break
		}
		top = cy
	}

	bottom := y
	for cy := y + 1; cy < h; cy++ {
		if isEdge(x, cy, x, cy-1) {
			break
		}
		bottom = cy
	}

	return MeasureResult{
		OriginX: x,
		OriginY: y,
		Left:    left,
		Right:   right,
		Top:     top,
		Bottom:  bottom,
		Width:   right - left + 1,
		Height:  bottom - top + 1,
		Valid:   true,
	}
}

// ColorAt returns the sRGB values and formatted hex string of pixel (x, y).
func ColorAt(img *image.RGBA, x, y int) (r, g, b uint8, ok bool) {
	if img == nil {
		return 0, 0, 0, false
	}
	bounds := img.Bounds()
	if x < 0 || y < 0 || x >= bounds.Dx() || y >= bounds.Dy() {
		return 0, 0, 0, false
	}
	off := y*img.Stride + x*4
	return img.Pix[off], img.Pix[off+1], img.Pix[off+2], true
}

// CropRGBA extracts a clamped copy of rect from src.
func CropRGBA(src *image.RGBA, rect image.Rectangle) *image.RGBA {
	if src == nil {
		return nil
	}
	clamped := rect.Canon().Intersect(src.Bounds())
	if clamped.Empty() {
		return nil
	}
	w, h := clamped.Dx(), clamped.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for row := 0; row < h; row++ {
		sOff := (clamped.Min.Y+row-src.Bounds().Min.Y)*src.Stride + (clamped.Min.X-src.Bounds().Min.X)*4
		dOff := row * dst.Stride
		copy(dst.Pix[dOff:dOff+w*4], src.Pix[sOff:sOff+w*4])
	}
	return dst
}

// ContrastTextLight reports whether light (white) text has better contrast on (r,g,b).
func ContrastTextLight(r, g, b uint8) bool {
	lum := 0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(b)
	return lum < 145
}

// ClampInt clamps v to [lo, hi].
func ClampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Distance2D returns Euclidean distance between two points.
func Distance2D(x1, y1, x2, y2 float64) float64 {
	return math.Hypot(x2-x1, y2-y1)
}
