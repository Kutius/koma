package canvas

import (
	"bytes"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
)

// pngBufPool reuses png.EncoderBuffer instances across EncodePNG calls to cut
// per-screenshot zlib compression scratch allocations.
type pngBufPool struct {
	pool sync.Pool
}

func (p *pngBufPool) Get() *png.EncoderBuffer {
	if v := p.pool.Get(); v != nil {
		return v.(*png.EncoderBuffer)
	}
	return nil
}

func (p *pngBufPool) Put(b *png.EncoderBuffer) {
	if b != nil {
		p.pool.Put(b)
	}
}

var sharedPNGBufPool = &pngBufPool{}

// blurScratch holds reusable buffers for applyBlur so repeated blurs avoid
// multi-MB heap churn.
type blurScratch struct {
	bufA   []uint8
	bufB   []uint8
	colSum []int32
}

var blurScratchPool = sync.Pool{
	New: func() any { return &blurScratch{} },
}

func getBlurScratch(pixels, width int) *blurScratch {
	sc := blurScratchPool.Get().(*blurScratch)
	need := pixels * 3
	if cap(sc.bufA) < need {
		sc.bufA = make([]uint8, need)
	} else {
		sc.bufA = sc.bufA[:need]
	}
	if cap(sc.bufB) < need {
		sc.bufB = make([]uint8, need)
	} else {
		sc.bufB = sc.bufB[:need]
	}
	colNeed := width * 3
	if cap(sc.colSum) < colNeed {
		sc.colSum = make([]int32, colNeed)
	} else {
		sc.colSum = sc.colSum[:colNeed]
	}
	return sc
}

func putBlurScratch(sc *blurScratch) {
	// Cap per-buffer retention (6 MiB each -> <= 12 MiB total) so extreme 4K+
	// scratch buffers are released immediately instead of lingering in the pool.
	const maxPooledBytes = 6 << 20
	if cap(sc.bufA) > maxPooledBytes || cap(sc.bufB) > maxPooledBytes {
		sc.bufA = nil
		sc.bufB = nil
		sc.colSum = nil
		return
	}
	blurScratchPool.Put(sc)
}

// CloneRGBA returns an independent copy of src.
func CloneRGBA(src *image.RGBA) *image.RGBA {
	if src == nil {
		return nil
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	if w <= 0 || h <= 0 {
		return dst
	}
	// Note: in Go's image.RGBA, SubImage already advances src.Pix to Rect.Min,
	// so pixel (b.Min.X, b.Min.Y) is always at src.Pix[0].
	if src.Stride == dst.Stride {
		copy(dst.Pix, src.Pix[:h*dst.Stride])
		return dst
	}
	rowBytes := w * 4
	for y := 0; y < h; y++ {
		sOff := y * src.Stride
		dOff := y * dst.Stride
		copy(dst.Pix[dOff:dOff+rowBytes], src.Pix[sOff:sOff+rowBytes])
	}
	return dst
}

// HasRasterAnnotations reports whether anns contains any pixel-modifying effect
// (Pixelate, Blur, Spotlight) that requires baking into a raster bitmap.
func HasRasterAnnotations(anns []Annotation) bool {
	for _, a := range anns {
		if a.Kind == ToolPixelate || a.Kind == ToolBlur || a.Kind == ToolSpotlight {
			return true
		}
	}
	return false
}

// RenderRasterEffects applies all pixel-level annotations (Pixelate, Blur,
// Spotlight) onto a copy of base. Used both for live canvas caching and export.
func RenderRasterEffects(base *image.RGBA, anns []Annotation) *image.RGBA {
	if base == nil {
		return nil
	}
	out := CloneRGBA(base)
	w, h := out.Bounds().Dx(), out.Bounds().Dy()
	var spotlights []image.Rectangle

	for _, a := range anns {
		minX, minY, maxX, maxY := a.Bounds()
		r := image.Rect(int(math.Floor(float64(minX))), int(math.Floor(float64(minY))),
			int(math.Ceil(float64(maxX))), int(math.Ceil(float64(maxY)))).Intersect(image.Rect(0, 0, w, h))
		if r.Empty() {
			continue
		}
		switch a.Kind {
		case ToolPixelate:
			bs := a.BlockSize
			if bs < 4 {
				bs = 12
			}
			applyMosaic(out, r, bs, a.MosaicStyle)
		case ToolBlur:
			rad := a.BlockSize
			if rad < 3 {
				rad = 12
			}
			applyBlur(out, r, rad)
		case ToolSpotlight:
			spotlights = append(spotlights, r)
		}
	}

	if len(spotlights) > 0 {
		applySpotlights(out, spotlights)
	}
	return out
}

// RenderComposite produces the final export-ready *image.RGBA containing raster
// effects and anti-aliased vector annotations (including Unicode/CJK text via MyGo
// SVG rasterizer).
func RenderComposite(base *image.RGBA, anns []Annotation) *image.RGBA {
	if base == nil {
		return nil
	}
	out := RenderRasterEffects(base, anns)
	w, h := out.Bounds().Dx(), out.Bounds().Dy()

	svgData := buildAnnotationsSVG(w, h, anns)
	if len(svgData) > 0 {
		if parsed, err := ui.ParseSVG(svgData); err == nil {
			overlay := parsed.Image(w, h, color.White)
			if overlay != nil {
				blendOver(out, overlay)
			}
		}
	}
	return out
}

// EncodePNG encodes img to PNG bytes using a pooled encoder buffer.
func EncodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if b := img.Bounds(); b.Dx() > 0 && b.Dy() > 0 {
		// Pre-size modestly (~12% of pixel count, capped at 256 KiB) so returned
		// PNG slices retained in history never pin oversized backing arrays.
		buf.Grow(min(b.Dx()*b.Dy()/8, 256<<10))
	}
	enc := png.Encoder{
		CompressionLevel: png.DefaultCompression,
		BufferPool:       sharedPNGBufPool,
	}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// EncodeJPEG encodes img to high-quality JPEG bytes.
func EncodeJPEG(img image.Image, quality int) ([]byte, error) {
	if quality <= 0 || quality > 100 {
		quality = 92
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// MakeThumbnail creates a crisp bilinear-downsampled thumbnail fitting inside maxW x maxH
// using precomputed fixed-point horizontal lookup tables (zero per-pixel float math).
func MakeThumbnail(src *image.RGBA, maxW, maxH int) *image.RGBA {
	if src == nil || maxW <= 0 || maxH <= 0 {
		return nil
	}
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	if sw <= 0 || sh <= 0 {
		return nil
	}
	scale := math.Min(float64(maxW)/float64(sw), float64(maxH)/float64(sh))
	if scale >= 1 {
		return CloneRGBA(src)
	}
	dw := max(1, int(math.Round(float64(sw)*scale)))
	dh := max(1, int(math.Round(float64(sh)*scale)))
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))

	// Precompute X lookup tables once per call. Clamp sx/sy to [0, dim-1] so
	// extreme aspect ratios (where dw or dh clamps to 1) never underflow 256-fx.
	// Note: src.Pix[0] is always (b.Min.X, b.Min.Y), even for SubImages.
	x0Tab := make([]int, dw)
	x1Tab := make([]int, dw)
	fxTab := make([]uint32, dw)
	invScale := 1.0 / scale
	maxSX := float64(sw - 1)
	maxSY := float64(sh - 1)
	for x := 0; x < dw; x++ {
		sx := (float64(x) + 0.5) * invScale
		if sx < 0 {
			sx = 0
		} else if sx > maxSX {
			sx = maxSX
		}
		ix := int(sx)
		ix1 := min(ix+1, sw-1)
		x0Tab[x] = ix * 4
		x1Tab[x] = ix1 * 4
		fxTab[x] = uint32(math.Round((sx - float64(ix)) * 256))
	}

	sPix := src.Pix
	dPix := dst.Pix
	sStride := src.Stride
	dStride := dst.Stride

	for y := 0; y < dh; y++ {
		sy := (float64(y) + 0.5) * invScale
		if sy < 0 {
			sy = 0
		} else if sy > maxSY {
			sy = maxSY
		}
		iy := int(sy)
		iy1 := min(iy+1, sh-1)
		fy := uint32(math.Round((sy - float64(iy)) * 256))
		ify := 256 - fy

		sRow0 := iy * sStride
		sRow1 := iy1 * sStride
		dRow := y * dStride

		for x := 0; x < dw; x++ {
			fx := fxTab[x]
			ifx := 256 - fx
			w00 := ifx * ify
			w10 := fx * ify
			w01 := ifx * fy
			w11 := fx * fy

			o00 := sRow0 + x0Tab[x]
			o10 := sRow0 + x1Tab[x]
			o01 := sRow1 + x0Tab[x]
			o11 := sRow1 + x1Tab[x]
			do := dRow + x*4

			dPix[do+0] = uint8((uint32(sPix[o00+0])*w00 + uint32(sPix[o10+0])*w10 + uint32(sPix[o01+0])*w01 + uint32(sPix[o11+0])*w11 + 32768) >> 16)
			dPix[do+1] = uint8((uint32(sPix[o00+1])*w00 + uint32(sPix[o10+1])*w10 + uint32(sPix[o01+1])*w01 + uint32(sPix[o11+1])*w11 + 32768) >> 16)
			dPix[do+2] = uint8((uint32(sPix[o00+2])*w00 + uint32(sPix[o10+2])*w10 + uint32(sPix[o01+2])*w01 + uint32(sPix[o11+2])*w11 + 32768) >> 16)
			dPix[do+3] = 255
		}
	}
	return dst
}

func adaptiveMosaicRadius(w, h int) float64 {
	short := float64(min(w, h))
	rad := short * 0.28
	if rad > 10.0 {
		rad = 10.0
	}
	if rad < 2.0 && short >= 6 {
		rad = 2.0
	}
	return rad
}

// roundedRectSDF returns the signed distance from pixel center (px, py) to the
// boundary of a rounded rectangle [x0, x1] x [y0, y1] with corner radius rad.
// Negative values are inside the shape, positive values are outside.
func roundedRectSDF(px, py, x0, y0, x1, y1, rad float64) float64 {
	cx := (x0 + x1) * 0.5
	cy := (y0 + y1) * 0.5
	hx := (x1-x0)*0.5 - rad
	hy := (y1-y0)*0.5 - rad
	if hx < 0 {
		hx = 0
	}
	if hy < 0 {
		hy = 0
	}
	dx := math.Abs(px-cx) - hx
	dy := math.Abs(py-cy) - hy
	ox := dx
	if ox < 0 {
		ox = 0
	}
	oy := dy
	if oy < 0 {
		oy = 0
	}
	inside := math.Max(dx, dy)
	if inside > 0 {
		inside = 0
	}
	return math.Hypot(ox, oy) + inside - rad
}

func sdfCoverage(sdf float64) float64 {
	if sdf <= -0.75 {
		return 1.0
	}
	if sdf >= 0.75 {
		return 0.0
	}
	t := (0.75 - sdf) / 1.5
	return t * t * (3.0 - 2.0*t)
}

func clampByteF(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(v + 0.5)
}

func fastHashNoise(x, y int) float64 {
	h := uint32(x)*374761393 + uint32(y)*668265263
	h = (h ^ (h >> 13)) * 1274126177
	h ^= h >> 16
	return (float64(h&0xFF)/255.0 - 0.5) * 2.0 // [-1, 1]
}

func sampleBorderLuminance(img *image.RGBA, r image.Rectangle) float64 {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return 128
	}
	var sum float64
	var cnt int
	add := func(x, y int) {
		if x < 0 {
			x = 0
		} else if x >= w {
			x = w - 1
		}
		if y < 0 {
			y = 0
		} else if y >= h {
			y = h - 1
		}
		off := y*img.Stride + x*4
		sum += 0.2126*float64(img.Pix[off]) + 0.7152*float64(img.Pix[off+1]) + 0.0722*float64(img.Pix[off+2])
		cnt++
	}
	stepX := max(1, r.Dx()/16)
	stepY := max(1, r.Dy()/16)
	for x := r.Min.X; x < r.Max.X; x += stepX {
		add(x, r.Min.Y-2)
		add(x, r.Max.Y+1)
	}
	for y := r.Min.Y; y < r.Max.Y; y += stepY {
		add(r.Min.X-2, y)
		add(r.Max.X+1, y)
	}
	if cnt == 0 {
		return 128
	}
	return sum / float64(cnt)
}

func applyMosaic(img *image.RGBA, r image.Rectangle, blockSize int, style MosaicStyle) {
	switch style {
	case MosaicBlur:
		applyBlur(img, r, blockSize)
	case MosaicPixel:
		applyPixelate(img, r, blockSize)
	case MosaicRedact:
		applyRedactBar(img, r)
	default:
		applyCrystalMosaic(img, r, blockSize)
	}
}

// applyCrystalMosaic renders a modern, Apple Liquid Glass inspired frosted
// prism tile mosaic: rounded anti-aliased silhouette, neighbor-softened glass
// tile colors, subtle convex lens specular gradient per tile, micro-frosted
// grain, and an adaptive 1px hairline rim.
func applyCrystalMosaic(img *image.RGBA, r image.Rectangle, blockSize int) {
	w, h := r.Dx(), r.Dy()
	if w <= 0 || h <= 0 {
		return
	}
	if blockSize < 6 {
		blockSize = 6
	}
	// Keep tile size proportional on narrow single-line text chips so at least ~2 rows of tiles appear.
	tile := blockSize
	if h < tile*2 && h >= 12 {
		tile = max(6, (h+1)/2)
	}

	cols := (w + tile - 1) / tile
	rows := (h + tile - 1) / tile
	type rgbF struct{ r, g, b float64 }
	grid := make([]rgbF, cols*rows)

	pix := img.Pix
	stride := img.Stride
	for gy := 0; gy < rows; gy++ {
		y0 := r.Min.Y + gy*tile
		y1 := min(y0+tile, r.Max.Y)
		for gx := 0; gx < cols; gx++ {
			x0 := r.Min.X + gx*tile
			x1 := min(x0+tile, r.Max.X)
			var sr, sg, sb, cnt int
			for y := y0; y < y1; y++ {
				row := y * stride
				for x := x0; x < x1; x++ {
					off := row + x*4
					sr += int(pix[off])
					sg += int(pix[off+1])
					sb += int(pix[off+2])
					cnt++
				}
			}
			if cnt > 0 {
				inv := 1.0 / float64(cnt)
				grid[gy*cols+gx] = rgbF{float64(sr) * inv, float64(sg) * inv, float64(sb) * inv}
			}
		}
	}

	// Gently cross-blend neighboring tiles so sharp glyph edges dissolve into
	// cohesive frosted glass color fields that are harder to reverse and far cleaner.
	soft := make([]rgbF, len(grid))
	for gy := 0; gy < rows; gy++ {
		for gx := 0; gx < cols; gx++ {
			c := grid[gy*cols+gx]
			wr, wg, wb := c.r*0.64, c.g*0.64, c.b*0.64
			wSum := 0.64
			for _, d := range [4][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
				nx, ny := gx+d[0], gy+d[1]
				if nx >= 0 && nx < cols && ny >= 0 && ny < rows {
					nb := grid[ny*cols+nx]
					wr += nb.r * 0.09
					wg += nb.g * 0.09
					wb += nb.b * 0.09
					wSum += 0.09
				}
			}
			inv := 1.0 / wSum
			// Add a subtle deterministic per-tile color micro-jitter (±3) to further defeat reconstruction.
			j := fastHashNoise(gx*7+r.Min.X, gy*11+r.Min.Y) * 3.0
			soft[gy*cols+gx] = rgbF{wr*inv + j, wg*inv + j, wb*inv + j}
		}
	}

	rad := adaptiveMosaicRadius(w, h)
	radInt := int(math.Ceil(rad)) + 2
	x0F, y0F := float64(r.Min.X), float64(r.Min.Y)
	x1F, y1F := float64(r.Max.X), float64(r.Max.Y)
	bgLum := sampleBorderLuminance(img, r)
	darkBg := bgLum < 128

	invTile := 1.0 / float64(tile)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		ly := y - r.Min.Y
		gy := ly / tile
		v := (float64(ly-gy*tile) + 0.5) * invTile
		row := y * stride
		nearEdgeY := ly < 2 || ly >= h-2
		nearCornerY := ly < radInt || ly >= h-radInt
		rowTileBase := gy * cols
		for x := r.Min.X; x < r.Max.X; x++ {
			lx := x - r.Min.X
			needSDF := nearEdgeY || lx < 2 || lx >= w-2 || (nearCornerY && (lx < radInt || lx >= w-radInt))

			cov := 1.0
			sdf := -10.0
			if needSDF {
				sdf = roundedRectSDF(float64(x)+0.5, float64(y)+0.5, x0F, y0F, x1F, y1F, rad)
				cov = sdfCoverage(sdf)
				if cov <= 0 {
					continue
				}
			}

			gx := lx / tile
			u := (float64(lx-gx*tile) + 0.5) * invTile
			col := soft[rowTileBase+gx]

			// Diagonal frosted prism curvature: gentle top-left specular, soft bottom-right depth.
			sheen := (0.48 - (u*0.58 + v*0.42)) * 20.0
			// Fine frosted bevel at tile edges (fluted/reeded architectural glass feel).
			edge := math.Min(math.Min(u, 1.0-u), math.Min(v, 1.0-v))
			if edge < 0.14 {
				t := 1.0 - edge/0.14
				if u+v < 1.0 {
					sheen += t * 9.0
				} else {
					sheen -= t * 8.0
				}
			}
			// Subtle organic sandblasted frosted grain.
			grain := fastHashNoise(x, y) * 2.8

			cr := col.r + sheen + grain
			cg := col.g + sheen + grain
			cb := col.b + sheen + grain

			// Adaptive 1px inner glass rim along the rounded silhouette.
			if needSDF && sdf > -1.8 && sdf < -0.1 {
				rimT := 1.0 - math.Abs(sdf+0.95)/0.85
				if rimT > 0 {
					if darkBg {
						cr += rimT * 28.0
						cg += rimT * 32.0
						cb += rimT * 38.0
					} else {
						cr -= rimT * 18.0
						cg -= rimT * 18.0
						cb -= rimT * 16.0
					}
				}
			}

			off := row + x*4
			if cov >= 0.999 {
				pix[off] = clampByteF(cr)
				pix[off+1] = clampByteF(cg)
				pix[off+2] = clampByteF(cb)
				pix[off+3] = 255
			} else {
				inv := 1.0 - cov
				pix[off] = clampByteF(cr*cov + float64(pix[off])*inv)
				pix[off+1] = clampByteF(cg*cov + float64(pix[off+1])*inv)
				pix[off+2] = clampByteF(cb*cov + float64(pix[off+2])*inv)
				pix[off+3] = 255
			}
		}
	}
}

// applyRedactBar renders a clean, completely irreversible rounded matte redaction
// capsule with subtle diagonal security striping and adaptive light/dark contrast.
func applyRedactBar(img *image.RGBA, r image.Rectangle) {
	w, h := r.Dx(), r.Dy()
	if w <= 0 || h <= 0 {
		return
	}
	rad := adaptiveMosaicRadius(w, h)
	radInt := int(math.Ceil(rad)) + 2
	x0F, y0F := float64(r.Min.X), float64(r.Min.Y)
	x1F, y1F := float64(r.Max.X), float64(r.Max.Y)
	darkBg := sampleBorderLuminance(img, r) < 128

	// Refined charcoal matte pill on light surfaces; elevated slate pill on dark surfaces.
	var baseR, baseG, baseB float64
	if darkBg {
		baseR, baseG, baseB = 54, 60, 72
	} else {
		baseR, baseG, baseB = 28, 31, 38
	}

	pix := img.Pix
	stride := img.Stride
	invH := 1.0 / float64(max(1, h))
	for y := r.Min.Y; y < r.Max.Y; y++ {
		ly := y - r.Min.Y
		lift := (0.5 - float64(ly)*invH) * 8.0
		row := y * stride
		nearEdgeY := ly < 2 || ly >= h-2
		nearCornerY := ly < radInt || ly >= h-radInt
		for x := r.Min.X; x < r.Max.X; x++ {
			lx := x - r.Min.X
			needSDF := nearEdgeY || lx < 2 || lx >= w-2 || (nearCornerY && (lx < radInt || lx >= w-radInt))
			cov := 1.0
			sdf := -10.0
			if needSDF {
				sdf = roundedRectSDF(float64(x)+0.5, float64(y)+0.5, x0F, y0F, x1F, y1F, rad)
				cov = sdfCoverage(sdf)
				if cov <= 0 {
					continue
				}
			}

			// Very subtle 45° matte diagonal micro-weave + top specular lift.
			stripe := 0.0
			if (x+y)&7 < 2 {
				stripe = 5.5
			}
			grain := fastHashNoise(x, y) * 1.6

			cr := baseR + lift + stripe + grain
			cg := baseG + lift + stripe + grain
			cb := baseB + lift + stripe + grain

			if needSDF && sdf > -1.7 && sdf < -0.1 {
				rimT := 1.0 - math.Abs(sdf+0.9)/0.8
				if rimT > 0 {
					if darkBg {
						cr += rimT * 36.0
						cg += rimT * 40.0
						cb += rimT * 48.0
					} else {
						cr += rimT * 22.0
						cg += rimT * 24.0
						cb += rimT * 28.0
					}
				}
			}

			off := row + x*4
			if cov >= 0.999 {
				pix[off] = clampByteF(cr)
				pix[off+1] = clampByteF(cg)
				pix[off+2] = clampByteF(cb)
				pix[off+3] = 255
			} else {
				inv := 1.0 - cov
				pix[off] = clampByteF(cr*cov + float64(pix[off])*inv)
				pix[off+1] = clampByteF(cg*cov + float64(pix[off+1])*inv)
				pix[off+2] = clampByteF(cb*cov + float64(pix[off+2])*inv)
				pix[off+3] = 255
			}
		}
	}
}

func applyPixelate(img *image.RGBA, r image.Rectangle, blockSize int) {
	w, h := r.Dx(), r.Dy()
	if w <= 0 || h <= 0 {
		return
	}
	rad := adaptiveMosaicRadius(w, h)
	radInt := int(math.Ceil(rad)) + 2
	x0F, y0F := float64(r.Min.X), float64(r.Min.Y)
	x1F, y1F := float64(r.Max.X), float64(r.Max.Y)

	pix := img.Pix
	stride := img.Stride
	for by := r.Min.Y; by < r.Max.Y; by += blockSize {
		yEnd := min(by+blockSize, r.Max.Y)
		for bx := r.Min.X; bx < r.Max.X; bx += blockSize {
			xEnd := min(bx+blockSize, r.Max.X)
			var sr, sg, sb, cnt int
			for y := by; y < yEnd; y++ {
				row := y * stride
				for x := bx; x < xEnd; x++ {
					off := row + x*4
					sr += int(pix[off])
					sg += int(pix[off+1])
					sb += int(pix[off+2])
					cnt++
				}
			}
			if cnt == 0 {
				continue
			}
			arU, agU, abU := uint8(sr/cnt), uint8(sg/cnt), uint8(sb/cnt)
			ar, ag, ab := float64(arU), float64(agU), float64(abU)
			for y := by; y < yEnd; y++ {
				ly := y - r.Min.Y
				nearCornerY := ly < radInt || ly >= h-radInt
				row := y * stride
				for x := bx; x < xEnd; x++ {
					off := row + x*4
					lx := x - r.Min.X
					if nearCornerY && (lx < radInt || lx >= w-radInt) {
						sdf := roundedRectSDF(float64(x)+0.5, float64(y)+0.5, x0F, y0F, x1F, y1F, rad)
						cov := sdfCoverage(sdf)
						if cov <= 0 {
							continue
						}
						if cov < 0.999 {
							inv := 1.0 - cov
							pix[off] = clampByteF(ar*cov + float64(pix[off])*inv)
							pix[off+1] = clampByteF(ag*cov + float64(pix[off+1])*inv)
							pix[off+2] = clampByteF(ab*cov + float64(pix[off+2])*inv)
							pix[off+3] = 255
							continue
						}
					}
					pix[off] = arU
					pix[off+1] = agU
					pix[off+2] = abU
					pix[off+3] = 255
				}
			}
		}
	}
}

// applyBlur runs a cache-friendly, pooled 3-pass separable box blur inside r
// (Gaussian approximation) with fixed-point reciprocal scaling and row-major
// vertical accumulation.
func applyBlur(img *image.RGBA, r image.Rectangle, radius int) {
	w, h := r.Dx(), r.Dy()
	if w <= 1 || h <= 1 || radius <= 0 {
		return
	}
	if radius > 40 {
		radius = 40
	}

	sc := getBlurScratch(w*h, w)
	defer putBlurScratch(sc)
	bufA := sc.bufA
	bufB := sc.bufB
	colSum := sc.colSum

	rowW3 := w * 3
	pix := img.Pix
	stride := img.Stride

	// Pack RGB region into tight contiguous buffer.
	for y := 0; y < h; y++ {
		sRow := (r.Min.Y+y)*stride + r.Min.X*4
		dRow := y * rowW3
		for x := 0; x < w; x++ {
			so := sRow + x*4
			do := dRow + x*3
			bufA[do] = pix[so]
			bufA[do+1] = pix[so+1]
			bufA[do+2] = pix[so+2]
		}
	}

	rad := max(1, radius/2)
	win := rad*2 + 1
	// Fixed-point reciprocal: (sum * invWin + 32768) >> 16 == round(sum / win)
	// Verified overflow-free for all 0 <= sum <= 255*win with radius <= 40 (win <= 41).
	invWin := int32((1<<16 + win/2) / win)

	for pass := 0; pass < 3; pass++ {
		// Horizontal pass: bufA -> bufB (sequential memory access, branchless interior).
		for y := 0; y < h; y++ {
			row := y * rowW3
			srcRow := bufA[row : row+rowW3]
			dstRow := bufB[row : row+rowW3]

			sr := int32(srcRow[0]) * int32(rad+1)
			sg := int32(srcRow[1]) * int32(rad+1)
			sb := int32(srcRow[2]) * int32(rad+1)
			for k := 1; k <= rad; k++ {
				cx := k
				if cx >= w {
					cx = w - 1
				}
				o := cx * 3
				sr += int32(srcRow[o])
				sg += int32(srcRow[o+1])
				sb += int32(srcRow[o+2])
			}

			// Left border (x < rad)
			leftEnd := min(rad, w)
			for x := 0; x < leftEnd; x++ {
				do := x * 3
				dstRow[do] = uint8((sr*invWin + 32768) >> 16)
				dstRow[do+1] = uint8((sg*invWin + 32768) >> 16)
				dstRow[do+2] = uint8((sb*invWin + 32768) >> 16)
				nx := x + rad + 1
				if nx >= w {
					nx = w - 1
				}
				no := nx * 3
				sr += int32(srcRow[no]) - int32(srcRow[0])
				sg += int32(srcRow[no+1]) - int32(srcRow[1])
				sb += int32(srcRow[no+2]) - int32(srcRow[2])
			}

			// Branchless interior (rad <= x < w-rad-1)
			midEnd := w - rad - 1
			for x := leftEnd; x < midEnd; x++ {
				do := x * 3
				dstRow[do] = uint8((sr*invWin + 32768) >> 16)
				dstRow[do+1] = uint8((sg*invWin + 32768) >> 16)
				dstRow[do+2] = uint8((sb*invWin + 32768) >> 16)
				oo := (x - rad) * 3
				no := (x + rad + 1) * 3
				sr += int32(srcRow[no]) - int32(srcRow[oo])
				sg += int32(srcRow[no+1]) - int32(srcRow[oo+1])
				sb += int32(srcRow[no+2]) - int32(srcRow[oo+2])
			}

			// Right border
			rightStart := max(leftEnd, midEnd)
			lastO := (w - 1) * 3
			for x := rightStart; x < w; x++ {
				do := x * 3
				dstRow[do] = uint8((sr*invWin + 32768) >> 16)
				dstRow[do+1] = uint8((sg*invWin + 32768) >> 16)
				dstRow[do+2] = uint8((sb*invWin + 32768) >> 16)
				ox := x - rad
				if ox < 0 {
					ox = 0
				}
				oo := ox * 3
				nx := x + rad + 1
				no := lastO
				if nx < w {
					no = nx * 3
				}
				sr += int32(srcRow[no]) - int32(srcRow[oo])
				sg += int32(srcRow[no+1]) - int32(srcRow[oo+1])
				sb += int32(srcRow[no+2]) - int32(srcRow[oo+2])
			}
		}

		// Vertical pass: bufB -> bufA in row-major order via colSum[] so every
		// read and write streams sequentially across cache lines.
		firstRow := bufB[:rowW3]
		radPlus1 := int32(rad + 1)
		for i := 0; i < rowW3; i++ {
			colSum[i] = int32(firstRow[i]) * radPlus1
		}
		for k := 1; k <= rad; k++ {
			cy := k
			if cy >= h {
				cy = h - 1
			}
			rSlice := bufB[cy*rowW3 : (cy+1)*rowW3]
			for i := 0; i < rowW3; i++ {
				colSum[i] += int32(rSlice[i])
			}
		}

		for y := 0; y < h; y++ {
			dSlice := bufA[y*rowW3 : (y+1)*rowW3]
			oldY := y - rad
			if oldY < 0 {
				oldY = 0
			}
			newY := y + rad + 1
			if newY >= h {
				newY = h - 1
			}
			oldRow := bufB[oldY*rowW3 : (oldY+1)*rowW3]
			newRow := bufB[newY*rowW3 : (newY+1)*rowW3]
			for i := 0; i < rowW3; i++ {
				v := colSum[i]
				dSlice[i] = uint8((v*invWin + 32768) >> 16)
				colSum[i] = v + int32(newRow[i]) - int32(oldRow[i])
			}
		}
	}

	// Unpack back to RGBA image with an anti-aliased rounded silhouette.
	radF := adaptiveMosaicRadius(w, h)
	radInt := int(math.Ceil(radF)) + 2
	x0F, y0F := float64(r.Min.X), float64(r.Min.Y)
	x1F, y1F := float64(r.Max.X), float64(r.Max.Y)
	for y := 0; y < h; y++ {
		dRow := (r.Min.Y+y)*stride + r.Min.X*4
		sRow := y * rowW3
		nearCornerY := y < radInt || y >= h-radInt
		for x := 0; x < w; x++ {
			so := sRow + x*3
			do := dRow + x*4
			if nearCornerY && (x < radInt || x >= w-radInt) {
				sdf := roundedRectSDF(float64(r.Min.X+x)+0.5, float64(r.Min.Y+y)+0.5, x0F, y0F, x1F, y1F, radF)
				cov := sdfCoverage(sdf)
				if cov <= 0 {
					continue
				}
				if cov < 0.999 {
					inv := 1.0 - cov
					pix[do] = clampByteF(float64(bufA[so])*cov + float64(pix[do])*inv)
					pix[do+1] = clampByteF(float64(bufA[so+1])*cov + float64(pix[do+1])*inv)
					pix[do+2] = clampByteF(float64(bufA[so+2])*cov + float64(pix[do+2])*inv)
					continue
				}
			}
			pix[do] = bufA[so]
			pix[do+1] = bufA[so+1]
			pix[do+2] = bufA[so+2]
		}
	}
}

func applySpotlights(img *image.RGBA, rects []image.Rectangle) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	pix := img.Pix
	stride := img.Stride
	// Fixed-point 97/256 ≈ 0.3789 (matches 0.38 dimFactor with zero float conversion).
	const dimNum uint32 = 97

	// Stack buffer for active spotlight X intervals on the current row; spills to heap only if >16.
	var activeBuf [16][2]int
	var active [][2]int
	for y := 0; y < h; y++ {
		row := y * stride
		active = activeBuf[:0]
		for _, r := range rects {
			if y >= r.Min.Y && y < r.Max.Y {
				active = append(active, [2]int{r.Min.X, r.Max.X})
			}
		}
		if len(active) == 0 {
			for x := 0; x < w; x++ {
				off := row + x*4
				pix[off] = uint8((uint32(pix[off]) * dimNum) >> 8)
				pix[off+1] = uint8((uint32(pix[off+1]) * dimNum) >> 8)
				pix[off+2] = uint8((uint32(pix[off+2]) * dimNum) >> 8)
			}
			continue
		}
		for x := 0; x < w; x++ {
			inSpot := false
			for i := range active {
				if x >= active[i][0] && x < active[i][1] {
					inSpot = true
					break
				}
			}
			if !inSpot {
				off := row + x*4
				pix[off] = uint8((uint32(pix[off]) * dimNum) >> 8)
				pix[off+1] = uint8((uint32(pix[off+1]) * dimNum) >> 8)
				pix[off+2] = uint8((uint32(pix[off+2]) * dimNum) >> 8)
			}
		}
	}
}

// blendOver alpha-composites premultiplied src (from ui.SVG.Image) over opaque dst
// using exact division-free /255 arithmetic.
func blendOver(dst, src *image.RGBA) {
	w := min(dst.Bounds().Dx(), src.Bounds().Dx())
	h := min(dst.Bounds().Dy(), src.Bounds().Dy())
	dPix := dst.Pix
	sPix := src.Pix
	dStride := dst.Stride
	sStride := src.Stride
	for y := 0; y < h; y++ {
		dRow := y * dStride
		sRow := y * sStride
		for x := 0; x < w; x++ {
			so := sRow + x*4
			sa := uint32(sPix[so+3])
			if sa == 0 {
				continue
			}
			do := dRow + x*4
			if sa == 255 {
				dPix[do] = sPix[so]
				dPix[do+1] = sPix[so+1]
				dPix[do+2] = sPix[so+2]
				dPix[do+3] = 255
				continue
			}
			inv := 255 - sa
			// Exact /255 for all t in [0, 65025]: (t + 1 + (t >> 8)) >> 8
			tr := uint32(dPix[do]) * inv
			tg := uint32(dPix[do+1]) * inv
			tb := uint32(dPix[do+2]) * inv
			r := uint32(sPix[so]) + ((tr + 1 + (tr >> 8)) >> 8)
			g := uint32(sPix[so+1]) + ((tg + 1 + (tg >> 8)) >> 8)
			b := uint32(sPix[so+2]) + ((tb + 1 + (tb >> 8)) >> 8)
			if r > 255 {
				r = 255
			}
			if g > 255 {
				g = 255
			}
			if b > 255 {
				b = 255
			}
			dPix[do] = uint8(r)
			dPix[do+1] = uint8(g)
			dPix[do+2] = uint8(b)
			dPix[do+3] = 255
		}
	}
}

func hexColor(c ui.Color) string {
	return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
}

func rgbaCSS(c ui.Color) string {
	return fmt.Sprintf("rgba(%d,%d,%d,%.2f)", c.R, c.G, c.B, float64(c.A)/255.0)
}

func buildAnnotationsSVG(w, h int, anns []Annotation) []byte {
	hasVector := false
	for _, a := range anns {
		if a.Kind != ToolPixelate && a.Kind != ToolBlur {
			hasVector = true
			break
		}
	}
	if !hasVector {
		return nil
	}

	var sb strings.Builder
	sb.Grow(len(anns)*160 + 128)
	fmt.Fprintf(&sb, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">`, w, h, w, h)

	for _, a := range anns {
		col := hexColor(a.Color)
		sw := a.StrokeWidth
		if sw <= 0 {
			sw = 3
		}
		switch a.Kind {
		case ToolSpotlight:
			minX, minY, maxX, maxY := a.Bounds()
			fmt.Fprintf(&sb, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" rx="6" fill="none" stroke="rgba(255,255,255,0.85)" stroke-width="2"/>`,
				minX, minY, maxX-minX, maxY-minY)

		case ToolLine:
			if halo, ok := strokeContrastHalo(a.Color); ok {
				fmt.Fprintf(&sb, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="%.1f" stroke-linecap="round"/>`,
					a.Start.X, a.Start.Y, a.End.X, a.End.Y, rgbaCSS(halo), sw+2)
			}
			fmt.Fprintf(&sb, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="%.1f" stroke-linecap="round"/>`,
				a.Start.X, a.Start.Y, a.End.X, a.End.Y, col, sw)

		case ToolArrow:
			if d := taperedArrowSVGPath(a.Start.X, a.Start.Y, a.End.X, a.End.Y, sw); d != "" {
				// Crisp subtle shadow under arrow
				fmt.Fprintf(&sb, `<path d="%s" transform="translate(0,1.5)" fill="rgba(0,0,0,0.28)"/>`, d)
				if halo, ok := strokeContrastHalo(a.Color); ok {
					fmt.Fprintf(&sb, `<path d="%s" fill="%s" stroke="%s" stroke-width="1.8" stroke-linejoin="round" paint-order="stroke fill"/>`, d, col, rgbaCSS(halo))
				} else {
					fmt.Fprintf(&sb, `<path d="%s" fill="%s"/>`, d, col)
				}
			}

		case ToolRect:
			minX, minY, maxX, maxY := a.Bounds()
			rx := float32(0)
			if a.Rounded {
				rx = 8
			}
			fill := "none"
			if a.Filled {
				fill = fmt.Sprintf("rgba(%d,%d,%d,0.20)", a.Color.R, a.Color.G, a.Color.B)
			}
			if halo, ok := strokeContrastHalo(a.Color); ok {
				fmt.Fprintf(&sb, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" rx="%.1f" fill="none" stroke="%s" stroke-width="%.1f"/>`,
					minX, minY, maxX-minX, maxY-minY, rx, rgbaCSS(halo), sw+2)
			}
			fmt.Fprintf(&sb, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" rx="%.1f" fill="%s" stroke="%s" stroke-width="%.1f"/>`,
				minX, minY, maxX-minX, maxY-minY, rx, fill, col, sw)

		case ToolEllipse:
			minX, minY, maxX, maxY := a.Bounds()
			cx, cy := (minX+maxX)/2, (minY+maxY)/2
			rx, ry := (maxX-minX)/2, (maxY-minY)/2
			fill := "none"
			if a.Filled {
				fill = fmt.Sprintf("rgba(%d,%d,%d,0.20)", a.Color.R, a.Color.G, a.Color.B)
			}
			if halo, ok := strokeContrastHalo(a.Color); ok {
				fmt.Fprintf(&sb, `<ellipse cx="%.1f" cy="%.1f" rx="%.1f" ry="%.1f" fill="none" stroke="%s" stroke-width="%.1f"/>`,
					cx, cy, rx, ry, rgbaCSS(halo), sw+2)
			}
			fmt.Fprintf(&sb, `<ellipse cx="%.1f" cy="%.1f" rx="%.1f" ry="%.1f" fill="%s" stroke="%s" stroke-width="%.1f"/>`,
				cx, cy, rx, ry, fill, col, sw)

		case ToolPen:
			if len(a.Points) >= 2 {
				d := smoothPathSVG(a.Points)
				if halo, ok := strokeContrastHalo(a.Color); ok {
					fmt.Fprintf(&sb, `<path d="%s" fill="none" stroke="%s" stroke-width="%.1f" stroke-linecap="round" stroke-linejoin="round"/>`,
						d, rgbaCSS(halo), sw+2)
				}
				fmt.Fprintf(&sb, `<path d="%s" fill="none" stroke="%s" stroke-width="%.1f" stroke-linecap="round" stroke-linejoin="round"/>`,
					d, col, sw)
			}

		case ToolHighlighter:
			if len(a.Points) >= 2 {
				hw := max(float32(14), sw*4.5)
				fmt.Fprintf(&sb, `<path d="%s" fill="none" stroke="%s" stroke-opacity="0.38" stroke-width="%.1f" stroke-linecap="round" stroke-linejoin="round"/>`,
					smoothPathSVG(a.Points), col, hw)
			}

		case ToolCounter:
			r := CounterRadius(a)
			cx, cy := a.Start.X, a.Start.Y
			fmt.Fprintf(&sb, `<circle cx="%.1f" cy="%.1f" r="%.1f" fill="rgba(0,0,0,0.30)"/>`, cx, cy+2, r+0.5)
			ringCol := "#ffffff"
			if RelativeLuminance(a.Color) > 0.72 {
				ringCol = "rgba(15,23,42,0.34)"
			}
			fmt.Fprintf(&sb, `<circle cx="%.1f" cy="%.1f" r="%.1f" fill="%s" stroke="%s" stroke-width="2"/>`, cx, cy, r, col, ringCol)
			txtCol := hexColor(ContrastInk(a.Color))
			fmt.Fprintf(&sb, `<text x="%.1f" y="%.1f" fill="%s" font-family="Segoe UI, sans-serif" font-size="%.1f" font-weight="700" text-anchor="middle" dominant-baseline="central">%d</text>`,
				cx, cy, txtCol, r*1.05, a.CounterNum)

		case ToolText:
			txt := strings.TrimRight(a.Text, "\r\n")
			if strings.TrimSpace(txt) == "" {
				continue
			}
			fs := ClampFontSize(a.FontSize)
			lines := strings.Split(strings.ReplaceAll(txt, "\r\n", "\n"), "\n")
			lineH := fs * 1.36
			if a.Filled {
				var maxLineW float32
				for _, line := range lines {
					if lw := estimateTextWidth(line, fs); lw > maxLineW {
						maxLineW = lw
					}
				}
				padX := max(float32(8), fs*0.42)
				padY := max(float32(5), fs*0.24)
				rad := min(float32(10), max(float32(6), fs*0.30))
				estW := maxLineW + padX*2.1
				estH := float32(len(lines))*lineH + padY*1.8
				txtCol := hexColor(ContrastInk(a.Color))
				borderCSS := rgbaCSS(PillBorderColor(a.Color))
				px, py := a.Start.X-padX*0.65, a.Start.Y-padY
				fmt.Fprintf(&sb, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" rx="%.1f" fill="rgba(0,0,0,0.22)"/>`, px, py+2, estW, estH, rad)
				fmt.Fprintf(&sb, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" rx="%.1f" fill="%s" stroke="%s" stroke-width="1.1"/>`, px, py, estW, estH, rad, col, borderCSS)
				for i, line := range lines {
					if line == "" {
						continue
					}
					fmt.Fprintf(&sb, `<text x="%.1f" y="%.1f" fill="%s" font-family="Segoe UI, Microsoft YaHei UI, sans-serif" font-size="%.1f" font-weight="600" dominant-baseline="hanging">%s</text>`,
						a.Start.X+padX*0.4, a.Start.Y+1+float32(i)*lineH, txtCol, fs, html.EscapeString(line))
				}
			} else {
				haloCSS := rgbaCSS(TextHaloColor(a.Color))
				haloW := min(float32(5.2), max(float32(2.4), fs*0.14))
				for i, line := range lines {
					if line == "" {
						continue
					}
					esc := html.EscapeString(line)
					ly := a.Start.Y + float32(i)*lineH
					fmt.Fprintf(&sb, `<text x="%.1f" y="%.1f" fill="%s" stroke="%s" stroke-width="%.1f" stroke-linejoin="round" paint-order="stroke fill" font-family="Segoe UI, Microsoft YaHei UI, sans-serif" font-size="%.1f" font-weight="700" dominant-baseline="hanging">%s</text>`,
						a.Start.X, ly, col, haloCSS, haloW, fs, esc)
				}
			}

		case ToolMeasure:
			mw := a.MeasRight - a.MeasLeft + 1
			mh := a.MeasBottom - a.MeasTop + 1
			ox, oy := a.Start.X, a.Start.Y
			if halo, ok := strokeContrastHalo(a.Color); ok {
				fmt.Fprintf(&sb, `<line x1="%d" y1="%.1f" x2="%d" y2="%.1f" stroke="%s" stroke-width="3.0"/>`, a.MeasLeft, oy, a.MeasRight, oy, rgbaCSS(halo))
				fmt.Fprintf(&sb, `<line x1="%.1f" y1="%d" x2="%.1f" y2="%d" stroke="%s" stroke-width="3.0"/>`, ox, a.MeasTop, ox, a.MeasBottom, rgbaCSS(halo))
			}
			// Horizontal measurement line & endcaps
			fmt.Fprintf(&sb, `<line x1="%d" y1="%.1f" x2="%d" y2="%.1f" stroke="%s" stroke-width="1.5"/>`, a.MeasLeft, oy, a.MeasRight, oy, col)
			fmt.Fprintf(&sb, `<line x1="%d" y1="%.1f" x2="%d" y2="%.1f" stroke="%s" stroke-width="1.5"/>`, a.MeasLeft, oy-5, a.MeasLeft, oy+5, col)
			fmt.Fprintf(&sb, `<line x1="%d" y1="%.1f" x2="%d" y2="%.1f" stroke="%s" stroke-width="1.5"/>`, a.MeasRight, oy-5, a.MeasRight, oy+5, col)
			// Vertical measurement line & endcaps
			fmt.Fprintf(&sb, `<line x1="%.1f" y1="%d" x2="%.1f" y2="%d" stroke="%s" stroke-width="1.5"/>`, ox, a.MeasTop, ox, a.MeasBottom, col)
			fmt.Fprintf(&sb, `<line x1="%.1f" y1="%d" x2="%.1f" y2="%d" stroke="%s" stroke-width="1.5"/>`, ox-5, a.MeasTop, ox+5, a.MeasTop, col)
			fmt.Fprintf(&sb, `<line x1="%.1f" y1="%d" x2="%.1f" y2="%d" stroke="%s" stroke-width="1.5"/>`, ox-5, a.MeasBottom, ox+5, a.MeasBottom, col)
			// Pill badge with W × H
			label := fmt.Sprintf("%d × %d px", mw, mh)
			bw := float32(len(label))*7.4 + 14
			bx := ox + 8
			by := oy - 26
			if bx+bw > float32(w) {
				bx = ox - bw - 8
			}
			if by < 4 {
				by = oy + 8
			}
			txtCol := hexColor(ContrastInk(a.Color))
			borderCSS := rgbaCSS(PillBorderColor(a.Color))
			fmt.Fprintf(&sb, `<rect x="%.1f" y="%.1f" width="%.1f" height="22" rx="6" fill="%s" stroke="%s" stroke-width="1"/>`, bx, by, bw, col, borderCSS)
			fmt.Fprintf(&sb, `<text x="%.1f" y="%.1f" fill="%s" font-family="Segoe UI, Consolas, monospace" font-size="11.5" font-weight="700" text-anchor="middle" dominant-baseline="central">%s</text>`,
				bx+bw/2, by+11, txtCol, label)
		}
	}

	sb.WriteString(`</svg>`)
	return []byte(sb.String())
}

// taperedArrowPoints builds the 7 vertices of a sleek Shottr-style tapered arrow.
func taperedArrowPoints(x0, y0, x1, y1, sw float32) ([][2]float32, bool) {
	dx := float64(x1 - x0)
	dy := float64(y1 - y0)
	length := math.Hypot(dx, dy)
	if length < 4 {
		return nil, false
	}
	ux, uy := dx/length, dy/length
	px, py := -uy, ux

	headLen := math.Min(length*0.42, math.Max(16.0, float64(sw)*4.6))
	headHalfW := headLen * 0.48
	neckHalfW := math.Max(2.2, float64(sw)*0.68)
	tailHalfW := math.Max(1.1, float64(sw)*0.24)

	neckDist := length - headLen*0.82
	nx := float64(x0) + ux*neckDist
	ny := float64(y0) + uy*neckDist
	baseX := float64(x0) + ux*(length-headLen)
	baseY := float64(y0) + uy*(length-headLen)

	return [][2]float32{
		{float32(float64(x0) + px*tailHalfW), float32(float64(y0) + py*tailHalfW)},
		{float32(nx + px*neckHalfW), float32(ny + py*neckHalfW)},
		{float32(baseX + px*headHalfW), float32(baseY + py*headHalfW)},
		{x1, y1},
		{float32(baseX - px*headHalfW), float32(baseY - py*headHalfW)},
		{float32(nx - px*neckHalfW), float32(ny - py*neckHalfW)},
		{float32(float64(x0) - px*tailHalfW), float32(float64(y0) - py*tailHalfW)},
	}, true
}

func taperedArrowSVGPath(x0, y0, x1, y1, sw float32) string {
	pts, ok := taperedArrowPoints(x0, y0, x1, y1, sw)
	if !ok {
		return ""
	}
	var sb strings.Builder
	sb.Grow(128)
	fmt.Fprintf(&sb, "M%.1f %.1f", pts[0][0], pts[0][1])
	for _, pt := range pts[1:] {
		fmt.Fprintf(&sb, " L%.1f %.1f", pt[0], pt[1])
	}
	sb.WriteString(" Z")
	return sb.String()
}

func smoothPathSVG(pts []PointF) string {
	if len(pts) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.Grow(len(pts) * 24)
	fmt.Fprintf(&sb, "M%.1f %.1f", pts[0].X, pts[0].Y)
	if len(pts) == 2 {
		fmt.Fprintf(&sb, " L%.1f %.1f", pts[1].X, pts[1].Y)
		return sb.String()
	}
	for i := 1; i < len(pts)-1; i++ {
		mx := (pts[i].X + pts[i+1].X) * 0.5
		my := (pts[i].Y + pts[i+1].Y) * 0.5
		fmt.Fprintf(&sb, " Q%.1f %.1f %.1f %.1f", pts[i].X, pts[i].Y, mx, my)
	}
	last := pts[len(pts)-1]
	fmt.Fprintf(&sb, " L%.1f %.1f", last.X, last.Y)
	return sb.String()
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func estimateTextWidth(s string, fontSize float32) float32 {
	var units float32
	for _, r := range s {
		switch {
		case r > 0x2E7F: // CJK / wide glyphs
			units += 1.02
		case r == 'i' || r == 'l' || r == 'I' || r == '!' || r == '.' || r == ',' || r == ':' || r == ';' || r == '|' || r == '1':
			units += 0.30
		case r == ' ' || r == '-' || r == '\'' || r == 't' || r == 'f' || r == 'r' || r == 'j':
			units += 0.38
		case r == 'W' || r == 'M' || r == 'm' || r == 'w' || r == '@' || r == '%':
			units += 0.82
		case r >= 'A' && r <= 'Z':
			units += 0.66
		default:
			units += 0.54
		}
	}
	return units * fontSize
}
