package stitch

import (
	"image"
	"image/color"
	"testing"
)

// makeSyntheticPage generates a rich, non-repeating tall page with varied color
// cards, horizontal text-like bars, and unique per-row patterns so any stitching
// misalignment is immediately detectable pixel-for-pixel.
func makeSyntheticPage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		// Soft gradient background
		bgR := uint8(244 - (y*20)/max(1, h))
		bgG := uint8(246 - (y*16)/max(1, h))
		bgB := uint8(252 - (y*10)/max(1, h))
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{R: bgR, G: bgG, B: bgB, A: 255})
		}

		// Unique cards every 96px with distinct colored headers and pseudo-text glyphs
		cardIdx := y / 96
		localY := y % 96
		if localY >= 8 && localY < 86 {
			cardR := uint8(40 + (cardIdx*53)%180)
			cardG := uint8(60 + (cardIdx*97)%160)
			cardB := uint8(90 + (cardIdx*131)%150)
			for x := 24; x < w-36; x++ {
				if localY < 26 {
					// Colored card header bar with unique width pattern
					if x < 24+((cardIdx*47+130)%(w-100))+40 {
						img.SetRGBA(x, y, color.RGBA{R: cardR, G: cardG, B: cardB, A: 255})
					} else {
						img.SetRGBA(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
					}
				} else {
					// White card body with pseudo-text strokes varying by cardIdx and localY
					if (localY%14) >= 4 && (localY%14) <= 9 {
						hash := ((x/5 + cardIdx*17 + localY*31) * 2654435761) >> 24
						if hash&3 != 0 && x < w-60-((cardIdx*29+localY*7)%120) {
							img.SetRGBA(x, y, color.RGBA{R: 30, G: 41, B: 59, A: 255})
						} else {
							img.SetRGBA(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
						}
					} else {
						img.SetRGBA(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
					}
				}
			}
		}
	}
	return img
}

func sliceFrame(page *image.RGBA, y0, h int) *image.RGBA {
	w := page.Bounds().Dx()
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		sOff := (y0 + y) * page.Stride
		dOff := y * out.Stride
		copy(out.Pix[dOff:dOff+w*4], page.Pix[sOff:sOff+w*4])
	}
	return out
}

func overlayStickyBars(frame *image.RGBA, headerH, footerH, scrollThumbY int) {
	w, h := frame.Bounds().Dx(), frame.Bounds().Dy()
	// Sticky top header with logo + nav dots
	for y := 0; y < headerH; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{R: 28, G: 32, B: 42, A: 255}
			if y == headerH-1 {
				c = color.RGBA{R: 80, G: 130, B: 255, A: 255}
			} else if y >= 14 && y <= 34 && x >= 20 && x <= 140 && (x/8)%2 == 0 {
				c = color.RGBA{R: 240, G: 246, B: 255, A: 255}
			}
			frame.SetRGBA(x, y, c)
		}
	}
	// Sticky bottom footer bar
	for y := h - footerH; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{R: 24, G: 26, B: 32, A: 255}
			if y == h-footerH {
				c = color.RGBA{R: 120, G: 130, B: 145, A: 255}
			} else if y >= h-footerH+12 && y <= h-12 && x >= 40 && x <= w-80 && (x/12)%2 == 0 {
				c = color.RGBA{R: 200, G: 210, B: 225, A: 255}
			}
			frame.SetRGBA(x, y, c)
		}
	}
	// Moving right-hand scrollbar inside rightmost 14px (must be ignored by matcher!)
	for y := headerH; y < h-footerH; y++ {
		for x := w - 14; x < w; x++ {
			c := color.RGBA{R: 230, G: 234, B: 240, A: 255}
			if y >= scrollThumbY && y < scrollThumbY+48 {
				c = color.RGBA{R: 120, G: 130, B: 145, A: 255}
			}
			frame.SetRGBA(x, y, c)
		}
	}
}

func TestStitcherExactIncrementalScroll(t *testing.T) {
	const w, viewH, totalH = 520, 420, 1400
	page := makeSyntheticPage(w, totalH)

	st := New(sliceFrame(page, 0, viewH), DefaultMaxHeight)
	offsets := []int{0, 45, 165, 340, 560, 740, 980}
	for i := 1; i < len(offsets); i++ {
		res := st.Feed(sliceFrame(page, offsets[i], viewH))
		wantShift := offsets[i] - offsets[i-1]
		if res.Status != StatusStitched {
			t.Fatalf("step %d (offset %d): got status %v, want StatusStitched", i, offsets[i], res.Status)
		}
		if res.ShiftY != wantShift {
			t.Fatalf("step %d: got ShiftY=%d, want %d", i, res.ShiftY, wantShift)
		}
	}

	got := st.Compose()
	if got.Bounds().Dx() != w || got.Bounds().Dy() != totalH {
		t.Fatalf("composed size = %dx%d, want %dx%d", got.Bounds().Dx(), got.Bounds().Dy(), w, totalH)
	}

	// Verify exact pixel fidelity across the entire stitched image!
	var mismatch int
	for i := 0; i < len(got.Pix); i++ {
		if got.Pix[i] != page.Pix[i] {
			mismatch++
		}
	}
	if mismatch != 0 {
		t.Fatalf("expected zero pixel mismatches against ground truth, got %d differing bytes", mismatch)
	}
}

func TestStitcherStickyHeaderFooterAndScrollbar(t *testing.T) {
	const w, viewH = 540, 440
	const headerH, footerH = 52, 44
	const scrollH = viewH - headerH - footerH
	content := makeSyntheticPage(w, 1200)

	makeFrame := func(contentOff, thumbY int) *image.RGBA {
		f := image.NewRGBA(image.Rect(0, 0, w, viewH))
		// Fill scrollable content window into [headerH .. viewH-footerH)
		for y := 0; y < scrollH; y++ {
			sOff := (contentOff + y) * content.Stride
			dOff := (headerH + y) * f.Stride
			copy(f.Pix[dOff:dOff+w*4], content.Pix[sOff:sOff+w*4])
		}
		overlayStickyBars(f, headerH, footerH, thumbY)
		return f
	}

	st := New(makeFrame(0, 64), DefaultMaxHeight)
	steps := []struct {
		off   int
		thumb int
	}{
		{110, 120},
		{250, 180},
		{390, 240},
		{520, 300},
	}
	prevOff := 0
	for i, s := range steps {
		res := st.Feed(makeFrame(s.off, s.thumb))
		if res.Status != StatusStitched {
			t.Fatalf("sticky step %d: status=%v, want StatusStitched", i, res.Status)
		}
		if res.ShiftY != s.off-prevOff {
			t.Fatalf("sticky step %d: ShiftY=%d, want %d", i, res.ShiftY, s.off-prevOff)
		}
		prevOff = s.off
	}

	wantTotalH := viewH + 520
	if st.TotalHeight() != wantTotalH {
		t.Fatalf("TotalHeight=%d, want %d (header once + content + footer once)", st.TotalHeight(), wantTotalH)
	}

	comp := st.Compose()
	// Verify sticky header is at top (y=10) and NOT duplicated at y=headerH+110
	topPx := comp.RGBAAt(60, 10)
	if topPx.R != 28 || topPx.G != 32 || topPx.B != 42 {
		t.Fatalf("expected sticky header at top, got %+v", topPx)
	}
	// Verify sticky footer is at very bottom (y=wantTotalH-10)
	botPx := comp.RGBAAt(10, wantTotalH-10)
	if botPx.R != 24 || botPx.G != 26 || botPx.B != 32 {
		t.Fatalf("expected sticky footer at bottom, got %+v", botPx)
	}
	// Verify middle stitched content at y=headerH+450 matches content row 450 cleanly
	for x := 30; x < w-40; x += 7 {
		g := comp.RGBAAt(x, headerH+450)
		e := content.RGBAAt(x, 450)
		if g != e {
			t.Fatalf("content mismatch at (%d, %d): got %+v want %+v", x, headerH+450, g, e)
		}
	}
}

func TestStitcherUnchangedUpwardTooFastAndPreview(t *testing.T) {
	const w, viewH = 480, 400
	page := makeSyntheticPage(w, 1600)

	f0 := sliceFrame(page, 200, viewH)
	st := New(f0, 900)

	// Unchanged frame
	if res := st.Feed(sliceFrame(page, 200, viewH)); res.Status != StatusUnchanged {
		t.Fatalf("identical frame: got %v, want StatusUnchanged", res.Status)
	}

	// Tiny blinking cursor (3x16 pixels changed) must still be treated as StatusUnchanged
	fBlink := sliceFrame(page, 200, viewH)
	for y := 100; y < 116; y++ {
		for x := 120; x < 123; x++ {
			fBlink.SetRGBA(x, y, color.RGBA{R: 0, G: 0, B: 0, A: 255})
		}
	}
	if res := st.Feed(fBlink); res.Status != StatusUnchanged {
		t.Fatalf("blinking cursor frame: got %v, want StatusUnchanged", res.Status)
	}

	// Upward scroll jitter must be detected and ignored without corrupting height
	if res := st.Feed(sliceFrame(page, 120, viewH)); res.Status != StatusScrolledUp {
		t.Fatalf("upward scroll: got %v, want StatusScrolledUp", res.Status)
	}
	if st.TotalHeight() != viewH {
		t.Fatalf("upward scroll modified height: got %d, want %d", st.TotalHeight(), viewH)
	}

	// Too-fast jump (zero overlap) must return StatusTooFast and not append garbage
	if res := st.Feed(sliceFrame(page, 900, viewH)); res.Status != StatusTooFast {
		t.Fatalf("too-fast jump: got %v, want StatusTooFast", res.Status)
	}
	if st.TotalHeight() != viewH {
		t.Fatalf("too-fast jump modified height: got %d, want %d", st.TotalHeight(), viewH)
	}

	// Valid downward scrolls up to maxHeight=900
	if res := st.Feed(sliceFrame(page, 420, viewH)); res.Status != StatusStitched || res.ShiftY != 220 {
		t.Fatalf("valid scroll 1: got %+v", res)
	}
	if res := st.Feed(sliceFrame(page, 640, viewH)); res.Status != StatusStitched || res.ShiftY != 220 {
		t.Fatalf("valid scroll 2: got %+v", res)
	}
	// Next 220px scroll exceeds maxHeight=900 (current 840), clamps cleanly to 900
	if res := st.Feed(sliceFrame(page, 860, viewH)); res.Status != StatusMaxHeight || res.TotalHeight != 900 {
		t.Fatalf("max height clamp: got %+v, want StatusMaxHeight & TotalHeight=900", res)
	}

	// RenderPreview into reusable destination must succeed and populate opaque pixels
	dst := image.NewRGBA(image.Rect(0, 0, 200, 320))
	scaledH, dh := st.RenderPreview(dst, 1.0)
	if scaledH <= 320 || dh != 320 {
		t.Fatalf("RenderPreview metrics: scaledH=%d dh=%d", scaledH, dh)
	}
	if dst.RGBAAt(100, 160).A != 255 {
		t.Fatalf("expected opaque preview pixels, got alpha=%d", dst.RGBAAt(100, 160).A)
	}

	st.Release()
	if st.TotalHeight() != 0 || st.Compose() != nil {
		t.Fatalf("expected Release to clear stitcher state")
	}
}

func TestStitcherDarkModeWithBrightChrome(t *testing.T) {
	// Dark IDE body (#1E1E1E background with muted low-contrast text) combined with
	// a bright high-contrast top titlebar and bottom status bar. adaptiveTexThreshold
	// must stay in dark-mode floor despite the high-contrast chrome bars.
	const w, viewH, contentH = 640, 420, 1400
	const headerH, footerH = 36, 28
	const scrollH = viewH - headerH - footerH

	content := image.NewRGBA(image.Rect(0, 0, w, contentH))
	for y := 0; y < contentH; y++ {
		band := (y / 22) * 29
		for x := 0; x < w; x++ {
			r, g, b := uint8(30), uint8(30), uint8(32)
			if y%22 >= 6 && y%22 <= 14 && x >= 24 && x < w-30 && ((x+band)%13 < 6) {
				r = uint8(58 + (band+x)%24)
				g = uint8(62 + (band+y)%24)
				b = uint8(70 + (band+x+y)%24)
			}
			content.SetRGBA(x, y, color.RGBA{r, g, b, 255})
		}
	}

	makeFrame := func(off int) *image.RGBA {
		f := image.NewRGBA(image.Rect(0, 0, w, viewH))
		for y := 0; y < scrollH; y++ {
			copy(f.Pix[(headerH+y)*f.Stride:(headerH+y+1)*f.Stride], content.Pix[(off+y)*content.Stride:(off+y+1)*content.Stride])
		}
		// High-contrast bright top titlebar + bottom status bar (energy >> defaultFloor*2)
		for y := 0; y < headerH; y++ {
			for x := 0; x < w; x++ {
				if (x/6)%2 == 0 {
					f.SetRGBA(x, y, color.RGBA{250, 250, 252, 255})
				} else {
					f.SetRGBA(x, y, color.RGBA{20, 24, 30, 255})
				}
			}
		}
		for y := viewH - footerH; y < viewH; y++ {
			for x := 0; x < w; x++ {
				if (x/8)%2 == 0 {
					f.SetRGBA(x, y, color.RGBA{0, 122, 255, 255})
				} else {
					f.SetRGBA(x, y, color.RGBA{245, 248, 255, 255})
				}
			}
		}
		return f
	}

	st := New(makeFrame(0), DefaultMaxHeight)
	if got := st.adaptiveTexThreshold(); got != sigCols {
		t.Fatalf("expected dark-mode floor (%d) despite bright header/footer chrome, got %d", sigCols, got)
	}
	for _, off := range []int{110, 220, 330, 440} {
		res := st.Feed(makeFrame(off))
		if res.Status != StatusStitched || res.ShiftY != 110 {
			t.Fatalf("dark-mode frame off=%d failed: %+v", off, res)
		}
	}
	if st.TotalHeight() != viewH+440 {
		t.Fatalf("dark-mode TotalHeight = %d, want %d", st.TotalHeight(), viewH+440)
	}
}

func TestStitcherSubImageFrames(t *testing.T) {
	// Feeding non-zero-origin SubImage views must not panic or misalign pixels.
	page := makeSyntheticPage(600, 1200)
	subFrame := func(y0 int) *image.RGBA {
		return page.SubImage(image.Rect(40, y0, 520, y0+360)).(*image.RGBA)
	}
	st := New(subFrame(40), DefaultMaxHeight)
	for _, y0 := range []int{140, 260, 400} {
		res := st.Feed(subFrame(y0))
		if res.Status != StatusStitched {
			t.Fatalf("SubImage feed y0=%d failed: %+v", y0, res)
		}
	}
	wantH := 360 + (400 - 40)
	if st.TotalHeight() != wantH {
		t.Fatalf("SubImage TotalHeight = %d, want %d", st.TotalHeight(), wantH)
	}
}
