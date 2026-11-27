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
			applyPixelate(out, r, bs)
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

func applyPixelate(img *image.RGBA, r image.Rectangle, blockSize int) {
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
			ar, ag, ab := uint8(sr/cnt), uint8(sg/cnt), uint8(sb/cnt)
			for y := by; y < yEnd; y++ {
				row := y * stride
				for x := bx; x < xEnd; x++ {
					off := row + x*4
					pix[off] = ar
					pix[off+1] = ag
					pix[off+2] = ab
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

	// Unpack back to RGBA image.
	for y := 0; y < h; y++ {
		dRow := (r.Min.Y+y)*stride + r.Min.X*4
		sRow := y * rowW3
		for x := 0; x < w; x++ {
			so := sRow + x*3
			do := dRow + x*4
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
			fmt.Fprintf(&sb, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="%.1f" stroke-linecap="round"/>`,
				a.Start.X, a.Start.Y, a.End.X, a.End.Y, col, sw)

		case ToolArrow:
			if d := taperedArrowSVGPath(a.Start.X, a.Start.Y, a.End.X, a.End.Y, sw); d != "" {
				// Crisp subtle shadow under arrow
				fmt.Fprintf(&sb, `<path d="%s" transform="translate(0,1.5)" fill="rgba(0,0,0,0.28)"/>`, d)
				fmt.Fprintf(&sb, `<path d="%s" fill="%s"/>`, d, col)
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
			fmt.Fprintf(&sb, `<ellipse cx="%.1f" cy="%.1f" rx="%.1f" ry="%.1f" fill="%s" stroke="%s" stroke-width="%.1f"/>`,
				cx, cy, rx, ry, fill, col, sw)

		case ToolPen:
			if len(a.Points) >= 2 {
				fmt.Fprintf(&sb, `<path d="%s" fill="none" stroke="%s" stroke-width="%.1f" stroke-linecap="round" stroke-linejoin="round"/>`,
					smoothPathSVG(a.Points), col, sw)
			}

		case ToolHighlighter:
			if len(a.Points) >= 2 {
				hw := max(float32(14), sw*4.5)
				fmt.Fprintf(&sb, `<path d="%s" fill="none" stroke="%s" stroke-opacity="0.38" stroke-width="%.1f" stroke-linecap="round" stroke-linejoin="round"/>`,
					smoothPathSVG(a.Points), col, hw)
			}

		case ToolCounter:
			r := float32(14)
			cx, cy := a.Start.X, a.Start.Y
			fmt.Fprintf(&sb, `<circle cx="%.1f" cy="%.1f" r="%.1f" fill="rgba(0,0,0,0.32)"/>`, cx, cy+2, r+0.5)
			fmt.Fprintf(&sb, `<circle cx="%.1f" cy="%.1f" r="%.1f" fill="%s" stroke="#ffffff" stroke-width="2"/>`, cx, cy, r, col)
			txtCol := "#ffffff"
			if isLightColor(a.Color) {
				txtCol = "#0f172a"
			}
			fmt.Fprintf(&sb, `<text x="%.1f" y="%.1f" fill="%s" font-family="Segoe UI, sans-serif" font-size="15" font-weight="700" text-anchor="middle" dominant-baseline="central">%d</text>`,
				cx, cy, txtCol, a.CounterNum)

		case ToolText:
			txt := strings.TrimRight(a.Text, "\r\n")
			if strings.TrimSpace(txt) == "" {
				continue
			}
			fs := a.FontSize
			if fs <= 0 {
				fs = 20
			}
			lines := strings.Split(strings.ReplaceAll(txt, "\r\n", "\n"), "\n")
			lineH := fs * 1.38
			if a.Filled {
				var maxLineW float32
				for _, line := range lines {
					if lw := estimateTextWidth(line, fs); lw > maxLineW {
						maxLineW = lw
					}
				}
				estW := maxLineW + 20
				estH := float32(len(lines))*lineH + 8
				txtCol := "#ffffff"
				if isLightColor(a.Color) {
					txtCol = "#0f172a"
				}
				fmt.Fprintf(&sb, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" rx="7" fill="rgba(0,0,0,0.28)"/>`, a.Start.X-6, a.Start.Y-4, estW, estH)
				fmt.Fprintf(&sb, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" rx="7" fill="%s"/>`, a.Start.X-6, a.Start.Y-6, estW, estH, col)
				for i, line := range lines {
					if line == "" {
						continue
					}
					fmt.Fprintf(&sb, `<text x="%.1f" y="%.1f" fill="%s" font-family="Segoe UI, Microsoft YaHei UI, sans-serif" font-size="%.1f" font-weight="600" dominant-baseline="hanging">%s</text>`,
						a.Start.X+4, a.Start.Y+1+float32(i)*lineH, txtCol, fs, html.EscapeString(line))
				}
			} else {
				for i, line := range lines {
					if line == "" {
						continue
					}
					esc := html.EscapeString(line)
					ly := a.Start.Y + float32(i)*lineH
					fmt.Fprintf(&sb, `<text x="%.1f" y="%.1f" fill="rgba(0,0,0,0.55)" font-family="Segoe UI, Microsoft YaHei UI, sans-serif" font-size="%.1f" font-weight="700" dominant-baseline="hanging">%s</text>`,
						a.Start.X+1.5, ly+1.5, fs, esc)
					fmt.Fprintf(&sb, `<text x="%.1f" y="%.1f" fill="%s" font-family="Segoe UI, Microsoft YaHei UI, sans-serif" font-size="%.1f" font-weight="700" dominant-baseline="hanging">%s</text>`,
						a.Start.X, ly, col, fs, esc)
				}
			}

		case ToolMeasure:
			mw := a.MeasRight - a.MeasLeft + 1
			mh := a.MeasBottom - a.MeasTop + 1
			ox, oy := a.Start.X, a.Start.Y
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
			fmt.Fprintf(&sb, `<rect x="%.1f" y="%.1f" width="%.1f" height="22" rx="6" fill="%s"/>`, bx, by, bw, col)
			fmt.Fprintf(&sb, `<text x="%.1f" y="%.1f" fill="#ffffff" font-family="Segoe UI, Consolas, monospace" font-size="11.5" font-weight="700" text-anchor="middle" dominant-baseline="central">%s</text>`,
				bx+bw/2, by+11, label)
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

func isLightColor(c ui.Color) bool {
	lum := 0.2126*float64(c.R) + 0.7152*float64(c.G) + 0.0722*float64(c.B)
	return lum > 175
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
