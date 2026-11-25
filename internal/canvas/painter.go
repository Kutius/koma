package canvas

import (
	"fmt"
	"math"

	"github.com/egoist/mygo/ui"
)

// DrawAnnotationLive renders a single vector annotation onto a MyGo *ui.Painter
// in viewport coordinates using the given image-to-screen transform (ox, oy, zoom).
// Raster effects (Pixelate/Blur/Spotlight) are already baked into the cached bitmap,
// except for live drag previews or selection outlines.
func DrawAnnotationLive(p *ui.Painter, a Annotation, ox, oy, zoom float32, selected bool, isPreview bool) {
	tx := func(x float32) float32 { return ox + x*zoom }
	ty := func(y float32) float32 { return oy + y*zoom }
	sw := max(float32(1), a.StrokeWidth*zoom)

	switch a.Kind {
	case ToolPixelate, ToolBlur:
		if isPreview || selected {
			minX, minY, maxX, maxY := a.Bounds()
			r := ui.Rect{X: tx(minX), Y: ty(minY), W: (maxX - minX) * zoom, H: (maxY - minY) * zoom}
			if isPreview {
				fill := ui.Hex("#38BDF8").Alpha(0.18)
				if a.Kind == ToolBlur {
					fill = ui.Hex("#A855F7").Alpha(0.20)
				}
				p.Fill(r, fill, 4)
			}
			p.StrokeDashed(r, ui.Hex("#38BDF8"), 4, 1.5)
		}

	case ToolSpotlight:
		minX, minY, maxX, maxY := a.Bounds()
		r := ui.Rect{X: tx(minX), Y: ty(minY), W: (maxX - minX) * zoom, H: (maxY - minY) * zoom}
		p.Stroke(r, ui.RGBA(255, 255, 255, 0.85), 6*zoom, max(1.5, 2*zoom))

	case ToolLine:
		path := (&ui.Path{}).MoveTo(tx(a.Start.X), ty(a.Start.Y)).LineTo(tx(a.End.X), ty(a.End.Y))
		p.StrokePath(path, sw, a.Color)

	case ToolArrow:
		pts, ok := taperedArrowPoints(tx(a.Start.X), ty(a.Start.Y), tx(a.End.X), ty(a.End.Y), sw)
		if ok {
			shadow := &ui.Path{}
			arrow := &ui.Path{}
			for i, pt := range pts {
				if i == 0 {
					shadow.MoveTo(pt[0], pt[1]+1.5)
					arrow.MoveTo(pt[0], pt[1])
				} else {
					shadow.LineTo(pt[0], pt[1]+1.5)
					arrow.LineTo(pt[0], pt[1])
				}
			}
			shadow.Close()
			arrow.Close()
			p.FillPath(shadow, ui.RGBA(0, 0, 0, 0.28))
			p.FillPath(arrow, a.Color)
		}

	case ToolRect:
		minX, minY, maxX, maxY := a.Bounds()
		r := ui.Rect{X: tx(minX), Y: ty(minY), W: (maxX - minX) * zoom, H: (maxY - minY) * zoom}
		rad := float32(0)
		if a.Rounded {
			rad = 8 * zoom
		}
		if a.Filled {
			p.Fill(r, a.Color.Alpha(0.20), rad)
		}
		p.Stroke(r, a.Color, rad, sw)

	case ToolEllipse:
		minX, minY, maxX, maxY := a.Bounds()
		rx := (maxX - minX) * 0.5 * zoom
		ry := (maxY - minY) * 0.5 * zoom
		cx := tx((minX + maxX) * 0.5)
		cy := ty((minY + maxY) * 0.5)
		if rx > 0.5 && ry > 0.5 {
			ep := ellipsePath(cx, cy, rx, ry)
			if a.Filled {
				p.FillPath(ep, a.Color.Alpha(0.20))
			}
			p.StrokePath(ep, sw, a.Color)
		}

	case ToolPen:
		if len(a.Points) >= 2 {
			path := smoothUIPath(a.Points, tx, ty)
			p.StrokePath(path, sw, a.Color)
		}

	case ToolHighlighter:
		if len(a.Points) >= 2 {
			hw := max(float32(14)*zoom, sw*4.5)
			path := smoothUIPath(a.Points, tx, ty)
			p.StrokePath(path, hw, a.Color.Alpha(0.38))
		}

	case ToolCounter:
		r := 14 * zoom
		cx, cy := tx(a.Start.X), ty(a.Start.Y)
		p.Shadow(ui.Rect{X: cx - r, Y: cy - r, W: r * 2, H: r * 2}, r, 0, 2*zoom, 6*zoom, 0, ui.RGBA(0, 0, 0, 0.35))
		p.Fill(ui.Rect{X: cx - r, Y: cy - r, W: r * 2, H: r * 2}, a.Color, r)
		p.Stroke(ui.Rect{X: cx - r, Y: cy - r, W: r * 2, H: r * 2}, ui.Hex("#FFFFFF"), r, max(1.5, 2*zoom))
		txtCol := ui.Hex("#FFFFFF")
		if isLightColor(a.Color) {
			txtCol = ui.Hex("#0F172A")
		}
		span := ui.Span{Text: fmt.Sprintf("%d", a.CounterNum), Size: 14 * zoom, Weight: 700, Color: txtCol}
		tw, th := p.MeasureText(0, span)
		p.RichText(cx-tw/2, cy-th/2, 0, span)

	case ToolText:
		txt := a.Text
		if txt == "" {
			txt = "Text"
		}
		fs := a.FontSize
		if fs <= 0 {
			fs = 20
		}
		sx, sy := tx(a.Start.X), ty(a.Start.Y)
		if a.Filled {
			txtCol := ui.Hex("#FFFFFF")
			if isLightColor(a.Color) {
				txtCol = ui.Hex("#0F172A")
			}
			span := ui.Span{Text: txt, Size: fs * zoom, Weight: 600, Color: txtCol}
			tw, th := p.MeasureText(0, span)
			padX, padY := 10*zoom, 5*zoom
			pill := ui.Rect{X: sx - padX*0.6, Y: sy - padY, W: tw + padX*2, H: th + padY*2}
			p.Shadow(pill, 7*zoom, 0, 2*zoom, 8*zoom, 0, ui.RGBA(0, 0, 0, 0.28))
			p.Fill(pill, a.Color, 7*zoom)
			p.RichText(sx+padX*0.4, sy, 0, span)
		} else {
			shadowSpan := ui.Span{Text: txt, Size: fs * zoom, Weight: 700, Color: ui.RGBA(0, 0, 0, 0.55)}
			p.RichText(sx+1.5*zoom, sy+1.5*zoom, 0, shadowSpan)
			mainSpan := ui.Span{Text: txt, Size: fs * zoom, Weight: 700, Color: a.Color}
			p.RichText(sx, sy, 0, mainSpan)
		}

	case ToolMeasure:
		lx, rx := tx(float32(a.MeasLeft)), tx(float32(a.MeasRight))
		tyTop, tyBot := ty(float32(a.MeasTop)), ty(float32(a.MeasBottom))
		cx, cy := tx(a.Start.X), ty(a.Start.Y)
		c := a.Color
		p.Line(lx, cy, rx, cy, 1.5, c)
		p.Line(lx, cy-5, lx, cy+5, 1.5, c)
		p.Line(rx, cy-5, rx, cy+5, 1.5, c)
		p.Line(cx, tyTop, cx, tyBot, 1.5, c)
		p.Line(cx-5, tyTop, cx+5, tyTop, 1.5, c)
		p.Line(cx-5, tyBot, cx+5, tyBot, 1.5, c)

		mw := a.MeasRight - a.MeasLeft + 1
		mh := a.MeasBottom - a.MeasTop + 1
		label := fmt.Sprintf("%d × %d px", mw, mh)
		span := ui.Span{Text: label, Size: 11.5, Weight: 700, Color: ui.Hex("#FFFFFF")}
		tw, th := p.MeasureText(0, span)
		pill := ui.Rect{X: cx + 8, Y: cy - th - 12, W: tw + 14, H: th + 8}
		p.Shadow(pill, 6, 0, 2, 6, 0, ui.RGBA(0, 0, 0, 0.35))
		p.Fill(pill, c, 6)
		p.RichText(pill.X+7, pill.Y+4, 0, span)
	}

	if selected {
		minX, minY, maxX, maxY := a.Bounds()
		sr := ui.Rect{
			X: tx(minX) - 5,
			Y: ty(minY) - 5,
			W: (maxX-minX)*zoom + 10,
			H: (maxY-minY)*zoom + 10,
		}
		p.StrokeDashed(sr, ui.Hex("#38BDF8"), 4, 1.5)
		// Corner selection grips
		corners := [4][2]float32{
			{sr.X, sr.Y},
			{sr.X + sr.W, sr.Y},
			{sr.X, sr.Y + sr.H},
			{sr.X + sr.W, sr.Y + sr.H},
		}
		for _, pt := range corners {
			hRect := ui.Rect{X: pt[0] - 4, Y: pt[1] - 4, W: 8, H: 8}
			p.Fill(hRect, ui.Hex("#FFFFFF"), 4)
			p.Stroke(hRect, ui.Hex("#0EA5E9"), 4, 1.5)
		}
	}
}

func smoothUIPath(pts []PointF, tx, ty func(float32) float32) *ui.Path {
	p := &ui.Path{}
	p.MoveTo(tx(pts[0].X), ty(pts[0].Y))
	if len(pts) == 2 {
		p.LineTo(tx(pts[1].X), ty(pts[1].Y))
		return p
	}
	for i := 1; i < len(pts)-1; i++ {
		mx := (pts[i].X + pts[i+1].X) * 0.5
		my := (pts[i].Y + pts[i+1].Y) * 0.5
		p.QuadTo(tx(pts[i].X), ty(pts[i].Y), tx(mx), ty(my))
	}
	last := pts[len(pts)-1]
	p.LineTo(tx(last.X), ty(last.Y))
	return p
}

func ellipsePath(cx, cy, rx, ry float32) *ui.Path {
	const k = 0.5522847498
	kx, ky := rx*k, ry*k
	p := &ui.Path{}
	p.MoveTo(cx+rx, cy)
	p.CubeTo(cx+rx, cy+ky, cx+kx, cy+ry, cx, cy+ry)
	p.CubeTo(cx-kx, cy+ry, cx-rx, cy+ky, cx-rx, cy)
	p.CubeTo(cx-rx, cy-ky, cx-kx, cy-ry, cx, cy-ry)
	p.CubeTo(cx+kx, cy-ry, cx+rx, cy-ky, cx+rx, cy)
	p.Close()
	return p
}

// HitTestAnnotation returns the top-most annotation index containing (ix, iy) in
// image pixel coordinates, or -1 if none.
func HitTestAnnotation(anns []Annotation, ix, iy, tol float32) int {
	for i := len(anns) - 1; i >= 0; i-- {
		a := anns[i]
		minX, minY, maxX, maxY := a.Bounds()
		if ix >= minX-tol && ix <= maxX+tol && iy >= minY-tol && iy <= maxY+tol {
			if a.Kind == ToolLine || a.Kind == ToolArrow {
				if distToSegment(ix, iy, a.Start.X, a.Start.Y, a.End.X, a.End.Y) <= max(tol, a.StrokeWidth*2) {
					return i
				}
				continue
			}
			return i
		}
	}
	return -1
}

func distToSegment(px, py, x1, y1, x2, y2 float32) float32 {
	dx, dy := x2-x1, y2-y1
	l2 := dx*dx + dy*dy
	if l2 == 0 {
		return float32(math.Hypot(float64(px-x1), float64(py-y1)))
	}
	t := ((px-x1)*dx + (py-y1)*dy) / l2
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	projX, projY := x1+t*dx, y1+t*dy
	return float32(math.Hypot(float64(px-projX), float64(py-projY)))
}
