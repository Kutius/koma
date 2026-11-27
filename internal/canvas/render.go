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

	"github.com/egoist/mygo/ui"
)

// CloneRGBA returns an independent copy of src.
func CloneRGBA(src *image.RGBA) *image.RGBA {
	if src == nil {
		return nil
	}
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		sOff := y * src.Stride
		dOff := y * dst.Stride
		copy(dst.Pix[dOff:dOff+b.Dx()*4], src.Pix[sOff:sOff+b.Dx()*4])
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

// EncodePNG encodes img to PNG bytes.
func EncodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.DefaultCompression}
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

// MakeThumbnail creates a crisp bilinear-downsampled thumbnail fitting inside maxW x maxH.
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
	for y := 0; y < dh; y++ {
		sy := (float64(y) + 0.5) / scale
		y0 := clampInt(int(math.Floor(sy)), 0, sh-1)
		y1 := clampInt(y0+1, 0, sh-1)
		fy := sy - float64(y0)
		for x := 0; x < dw; x++ {
			sx := (float64(x) + 0.5) / scale
			x0 := clampInt(int(math.Floor(sx)), 0, sw-1)
			x1 := clampInt(x0+1, 0, sw-1)
			fx := sx - float64(x0)

			o00 := y0*src.Stride + x0*4
			o10 := y0*src.Stride + x1*4
			o01 := y1*src.Stride + x0*4
			o11 := y1*src.Stride + x1*4
			do := y*dst.Stride + x*4
			for c := 0; c < 3; c++ {
				top := float64(src.Pix[o00+c])*(1-fx) + float64(src.Pix[o10+c])*fx
				bot := float64(src.Pix[o01+c])*(1-fx) + float64(src.Pix[o11+c])*fx
				dst.Pix[do+c] = uint8(math.Round(top*(1-fy) + bot*fy))
			}
			dst.Pix[do+3] = 255
		}
	}
	return dst
}

func applyPixelate(img *image.RGBA, r image.Rectangle, blockSize int) {
	for by := r.Min.Y; by < r.Max.Y; by += blockSize {
		yEnd := min(by+blockSize, r.Max.Y)
		for bx := r.Min.X; bx < r.Max.X; bx += blockSize {
			xEnd := min(bx+blockSize, r.Max.X)
			var sr, sg, sb, cnt int
			for y := by; y < yEnd; y++ {
				row := y * img.Stride
				for x := bx; x < xEnd; x++ {
					off := row + x*4
					sr += int(img.Pix[off])
					sg += int(img.Pix[off+1])
					sb += int(img.Pix[off+2])
					cnt++
				}
			}
			if cnt == 0 {
				continue
			}
			ar, ag, ab := uint8(sr/cnt), uint8(sg/cnt), uint8(sb/cnt)
			for y := by; y < yEnd; y++ {
				row := y * img.Stride
				for x := bx; x < xEnd; x++ {
					off := row + x*4
					img.Pix[off] = ar
					img.Pix[off+1] = ag
					img.Pix[off+2] = ab
					img.Pix[off+3] = 255
				}
			}
		}
	}
}

// applyBlur runs a fast 3-pass separable box blur inside r (Gaussian approximation).
func applyBlur(img *image.RGBA, r image.Rectangle, radius int) {
	w, h := r.Dx(), r.Dy()
	if w <= 1 || h <= 1 || radius <= 0 {
		return
	}
	if radius > 40 {
		radius = 40
	}
	// Work in a tight temporary buffer for cache locality.
	bufA := make([]uint8, w*h*3)
	bufB := make([]uint8, w*h*3)
	for y := 0; y < h; y++ {
		sRow := (r.Min.Y + y) * img.Stride
		dRow := y * w * 3
		for x := 0; x < w; x++ {
			so := sRow + (r.Min.X+x)*4
			do := dRow + x*3
			bufA[do] = img.Pix[so]
			bufA[do+1] = img.Pix[so+1]
			bufA[do+2] = img.Pix[so+2]
		}
	}

	rad := max(1, radius/2)
	for pass := 0; pass < 3; pass++ {
		// Horizontal pass: bufA -> bufB
		for y := 0; y < h; y++ {
			row := y * w * 3
			var sr, sg, sb int
			win := rad*2 + 1
			for k := -rad; k <= rad; k++ {
				cx := clampInt(k, 0, w-1)
				o := row + cx*3
				sr += int(bufA[o])
				sg += int(bufA[o+1])
				sb += int(bufA[o+2])
			}
			for x := 0; x < w; x++ {
				do := row + x*3
				bufB[do] = uint8(sr / win)
				bufB[do+1] = uint8(sg / win)
				bufB[do+2] = uint8(sb / win)

				oldX := clampInt(x-rad, 0, w-1)
				newX := clampInt(x+rad+1, 0, w-1)
				oo := row + oldX*3
				no := row + newX*3
				sr += int(bufA[no]) - int(bufA[oo])
				sg += int(bufA[no+1]) - int(bufA[oo+1])
				sb += int(bufA[no+2]) - int(bufA[oo+2])
			}
		}
		// Vertical pass: bufB -> bufA
		for x := 0; x < w; x++ {
			var sr, sg, sb int
			win := rad*2 + 1
			for k := -rad; k <= rad; k++ {
				cy := clampInt(k, 0, h-1)
				o := (cy*w + x) * 3
				sr += int(bufB[o])
				sg += int(bufB[o+1])
				sb += int(bufB[o+2])
			}
			for y := 0; y < h; y++ {
				do := (y*w + x) * 3
				bufA[do] = uint8(sr / win)
				bufA[do+1] = uint8(sg / win)
				bufA[do+2] = uint8(sb / win)

				oldY := clampInt(y-rad, 0, h-1)
				newY := clampInt(y+rad+1, 0, h-1)
				oo := (oldY*w + x) * 3
				no := (newY*w + x) * 3
				sr += int(bufB[no]) - int(bufB[oo])
				sg += int(bufB[no+1]) - int(bufB[oo+1])
				sb += int(bufB[no+2]) - int(bufB[oo+2])
			}
		}
	}

	for y := 0; y < h; y++ {
		dRow := (r.Min.Y + y) * img.Stride
		sRow := y * w * 3
		for x := 0; x < w; x++ {
			so := sRow + x*3
			do := dRow + (r.Min.X+x)*4
			img.Pix[do] = bufA[so]
			img.Pix[do+1] = bufA[so+1]
			img.Pix[do+2] = bufA[so+2]
		}
	}
}

func applySpotlights(img *image.RGBA, rects []image.Rectangle) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	const dimFactor = 0.38
	for y := 0; y < h; y++ {
		row := y * img.Stride
		for x := 0; x < w; x++ {
			inSpot := false
			for _, r := range rects {
				if x >= r.Min.X && x < r.Max.X && y >= r.Min.Y && y < r.Max.Y {
					inSpot = true
					break
				}
			}
			if !inSpot {
				off := row + x*4
				img.Pix[off] = uint8(float64(img.Pix[off]) * dimFactor)
				img.Pix[off+1] = uint8(float64(img.Pix[off+1]) * dimFactor)
				img.Pix[off+2] = uint8(float64(img.Pix[off+2]) * dimFactor)
			}
		}
	}
}

// blendOver alpha-composites premultiplied src (from ui.SVG.Image) over opaque dst.
func blendOver(dst, src *image.RGBA) {
	w := min(dst.Bounds().Dx(), src.Bounds().Dx())
	h := min(dst.Bounds().Dy(), src.Bounds().Dy())
	for y := 0; y < h; y++ {
		dRow := y * dst.Stride
		sRow := y * src.Stride
		for x := 0; x < w; x++ {
			so := sRow + x*4
			sa := uint32(src.Pix[so+3])
			if sa == 0 {
				continue
			}
			do := dRow + x*4
			if sa == 255 {
				dst.Pix[do] = src.Pix[so]
				dst.Pix[do+1] = src.Pix[so+1]
				dst.Pix[do+2] = src.Pix[so+2]
				dst.Pix[do+3] = 255
				continue
			}
			inv := 255 - sa
			// src from SVG.Image is premultiplied RGBA
			r := uint32(src.Pix[so]) + (uint32(dst.Pix[do])*inv)/255
			g := uint32(src.Pix[so+1]) + (uint32(dst.Pix[do+1])*inv)/255
			b := uint32(src.Pix[so+2]) + (uint32(dst.Pix[do+2])*inv)/255
			if r > 255 {
				r = 255
			}
			if g > 255 {
				g = 255
			}
			if b > 255 {
				b = 255
			}
			dst.Pix[do] = uint8(r)
			dst.Pix[do+1] = uint8(g)
			dst.Pix[do+2] = uint8(b)
			dst.Pix[do+3] = 255
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
