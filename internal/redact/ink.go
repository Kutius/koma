package redact

import (
	"image"
)

// TightenRectToInk trims excess background margin around an OCR bounding box
// by measuring foreground ink contrast against the box's surrounding border pixels.
// It is microsecond-fast, safe on both light and dark UIs, and strictly bounded
// so faint text or low-contrast glyphs are never over-cropped.
func TightenRectToInk(img *image.RGBA, rc image.Rectangle) image.Rectangle {
	if img == nil || rc.Empty() {
		return rc
	}
	bounds := img.Bounds()
	rc = rc.Intersect(bounds)
	w, h := rc.Dx(), rc.Dy()
	if w < 6 || h < 8 {
		return rc
	}

	bgR, bgG, bgB := sampleRectBorderRGB(img, rc)
	pix := img.Pix
	stride := img.Stride

	// Manhattan RGB color delta threshold separating glyph ink from background.
	const inkDelta = 42

	minRowInk := max(1, w/64)
	top := rc.Min.Y
	maxTrimY := max(0, (h-8)/2)
	if capTrim := h * 24 / 100; maxTrimY > capTrim {
		maxTrimY = capTrim
	}

	for trim := 0; trim < maxTrimY; trim++ {
		y := rc.Min.Y + trim
		row := y * stride
		cnt := 0
		for x := rc.Min.X; x < rc.Max.X; x++ {
			o := row + x*4
			if absInt(int(pix[o])-bgR)+absInt(int(pix[o+1])-bgG)+absInt(int(pix[o+2])-bgB) >= inkDelta {
				cnt++
				if cnt >= minRowInk {
					break
				}
			}
		}
		if cnt >= minRowInk {
			break
		}
		top = y + 1
	}

	bot := rc.Max.Y
	for trim := 0; trim < maxTrimY; trim++ {
		y := rc.Max.Y - 1 - trim
		if y <= top+7 {
			break
		}
		row := y * stride
		cnt := 0
		for x := rc.Min.X; x < rc.Max.X; x++ {
			o := row + x*4
			if absInt(int(pix[o])-bgR)+absInt(int(pix[o+1])-bgG)+absInt(int(pix[o+2])-bgB) >= inkDelta {
				cnt++
				if cnt >= minRowInk {
					break
				}
			}
		}
		if cnt >= minRowInk {
			break
		}
		bot = y
	}

	// Trim only modest empty horizontal padding on left/right edges.
	maxTrimX := min(min(18, h), max(0, (w-6)/3))
	left := rc.Min.X
	for trim := 0; trim < maxTrimX; trim++ {
		x := rc.Min.X + trim
		hasInk := false
		for y := top; y < bot; y++ {
			o := y*stride + x*4
			if absInt(int(pix[o])-bgR)+absInt(int(pix[o+1])-bgG)+absInt(int(pix[o+2])-bgB) >= inkDelta {
				hasInk = true
				break
			}
		}
		if hasInk {
			break
		}
		left = x + 1
	}

	right := rc.Max.X
	for trim := 0; trim < maxTrimX; trim++ {
		x := rc.Max.X - 1 - trim
		if x <= left+5 {
			break
		}
		hasInk := false
		for y := top; y < bot; y++ {
			o := y*stride + x*4
			if absInt(int(pix[o])-bgR)+absInt(int(pix[o+1])-bgG)+absInt(int(pix[o+2])-bgB) >= inkDelta {
				hasInk = true
				break
			}
		}
		if hasInk {
			break
		}
		right = x
	}

	refined := image.Rect(left, top, right, bot).Intersect(bounds)
	if refined.Dx() < 4 || refined.Dy() < 6 {
		return rc
	}
	return refined
}

func sampleRectBorderRGB(img *image.RGBA, rc image.Rectangle) (int, int, int) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	var sr, sg, sb, cnt int
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
		o := y*img.Stride + x*4
		sr += int(img.Pix[o])
		sg += int(img.Pix[o+1])
		sb += int(img.Pix[o+2])
		cnt++
	}
	stepX := max(1, rc.Dx()/12)
	stepY := max(1, rc.Dy()/6)
	for x := rc.Min.X; x < rc.Max.X; x += stepX {
		add(x, rc.Min.Y-1)
		add(x, rc.Max.Y)
	}
	for y := rc.Min.Y; y < rc.Max.Y; y += stepY {
		add(rc.Min.X-1, y)
		add(rc.Max.X, y)
	}
	if cnt == 0 {
		return 255, 255, 255
	}
	return sr / cnt, sg / cnt, sb / cnt
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
