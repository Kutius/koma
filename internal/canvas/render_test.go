package canvas

import (
	"image"
	"image/color"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestRenderComposite(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 320, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 320; x++ {
			base.SetRGBA(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), 140, 255})
		}
	}

	anns := []Annotation{
		{ID: 1, Kind: ToolPixelate, Start: PointF{10, 10}, End: PointF{80, 60}, BlockSize: 10},
		{ID: 2, Kind: ToolBlur, Start: PointF{90, 10}, End: PointF{160, 60}, BlockSize: 8},
		{ID: 3, Kind: ToolArrow, Start: PointF{20, 120}, End: PointF{140, 160}, Color: ui.Hex("#EF4444"), StrokeWidth: 4},
		{ID: 4, Kind: ToolCounter, Start: PointF{200, 100}, Color: ui.Hex("#0EA5E9"), CounterNum: 1},
		{ID: 5, Kind: ToolText, Start: PointF{180, 140}, Color: ui.Hex("#10B981"), Text: "Koma 截图测试", FontSize: 18, Filled: true},
		{ID: 6, Kind: ToolMeasure, Start: PointF{250, 50}, Color: ui.Hex("#F59E0B"), MeasLeft: 210, MeasRight: 290, MeasTop: 20, MeasBottom: 80},
	}

	raw := RenderComposite(base, anns)
	if raw == nil || raw.Bounds().Dx() != 320 || raw.Bounds().Dy() != 200 {
		t.Fatalf("unexpected composite size: %v", raw)
	}

	pngBytes, err := EncodePNG(raw)
	if err != nil || len(pngBytes) < 500 {
		t.Fatalf("EncodePNG failed: err=%v len=%d", err, len(pngBytes))
	}
}

func TestOptimizedBlurAccuracyAndEdges(t *testing.T) {
	// Uniform mid-tone and pure white at max radius (40) must not drift or wrap uint8.
	for _, tc := range []struct {
		name   string
		col    color.RGBA
		radius int
	}{
		{"midtone", color.RGBA{120, 180, 60, 255}, 16},
		{"white_max_radius", color.RGBA{255, 255, 255, 255}, 40},
		{"black_max_radius", color.RGBA{0, 0, 0, 255}, 40},
	} {
		img := image.NewRGBA(image.Rect(0, 0, 128, 96))
		for i := 0; i < len(img.Pix); i += 4 {
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = tc.col.R, tc.col.G, tc.col.B, 255
		}
		applyBlur(img, image.Rect(10, 10, 118, 86), tc.radius)
		px := img.RGBAAt(64, 48)
		if absDiffU8(px.R, tc.col.R) > 1 || absDiffU8(px.G, tc.col.G) > 1 || absDiffU8(px.B, tc.col.B) > 1 {
			t.Fatalf("%s: blur drifted to %+v, want ~%+v", tc.name, px, tc.col)
		}
	}

	// Narrow 2x2 region with radius larger than width/height must not panic.
	tiny := image.NewRGBA(image.Rect(0, 0, 16, 16))
	applyBlur(tiny, image.Rect(4, 4, 6, 6), 40)
}

func TestMakeThumbnailExtremeAspectAndSubImage(t *testing.T) {
	// Extreme narrow/tall strip where dw clamps to 1 (sw*scale < 0.5) must not
	// underflow fixed-point weight (256 - fx).
	for _, dims := range [][2]int{{50, 16000}, {16000, 50}} {
		src := image.NewRGBA(image.Rect(0, 0, dims[0], dims[1]))
		for i := 0; i < len(src.Pix); i += 4 {
			src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = 40, 140, 220, 255
		}
		th := MakeThumbnail(src, 240, 150)
		if th == nil {
			t.Fatalf("MakeThumbnail(%dx%d) returned nil", dims[0], dims[1])
		}
		px := th.RGBAAt(0, 0)
		if px.R != 40 || px.G != 140 || px.B != 220 || px.A != 255 {
			t.Fatalf("MakeThumbnail(%dx%d) corrupted uniform pixel: got %+v, want (40,140,220,255)", dims[0], dims[1], px)
		}
	}

	// SubImage with non-zero Rect.Min must work cleanly in CloneRGBA and MakeThumbnail.
	parent := image.NewRGBA(image.Rect(0, 0, 200, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 200; x++ {
			c := color.RGBA{10, 20, 30, 255}
			if x >= 60 && x < 180 && y >= 50 && y < 170 {
				c = color.RGBA{200, 100, 50, 255}
			}
			parent.SetRGBA(x, y, c)
		}
	}
	sub := parent.SubImage(image.Rect(60, 50, 180, 170)).(*image.RGBA)
	cloned := CloneRGBA(sub)
	if cloned.Bounds().Dx() != 120 || cloned.Bounds().Dy() != 120 {
		t.Fatalf("CloneRGBA(sub) bounds = %v", cloned.Bounds())
	}
	if px := cloned.RGBAAt(0, 0); px != (color.RGBA{200, 100, 50, 255}) {
		t.Fatalf("CloneRGBA(sub) pixel (0,0) = %+v, want (200,100,50,255)", px)
	}
	th := MakeThumbnail(sub, 40, 40)
	if px := th.RGBAAt(20, 20); px != (color.RGBA{200, 100, 50, 255}) {
		t.Fatalf("MakeThumbnail(sub) pixel = %+v, want (200,100,50,255)", px)
	}
}

func TestApplySpotlightsOver16Intervals(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 420, 40))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 200, 200, 200, 255
	}
	// Place 20 distinct spotlights across the same scanline (exceeds 16-slot stack fast path).
	var rects []image.Rectangle
	for i := 0; i < 20; i++ {
		x0 := 5 + i*20
		rects = append(rects, image.Rect(x0, 5, x0+10, 35))
	}
	applySpotlights(img, rects)

	// Spotlight #19 (index 18, x=365..375) must remain undimmed!
	if px := img.RGBAAt(370, 20); px.R != 200 {
		t.Fatalf("spotlight #19 was unexpectedly dimmed: got %+v, want R=200", px)
	}
	// Outside region must be dimmed (~75).
	if px := img.RGBAAt(0, 20); px.R < 70 || px.R > 82 {
		t.Fatalf("exterior dim unexpected: got %+v, want R~75", px)
	}
}

func TestBlendOverExactDiv255(t *testing.T) {
	dst := image.NewRGBA(image.Rect(0, 0, 256, 1))
	src := image.NewRGBA(image.Rect(0, 0, 256, 1))
	for a := 0; a < 256; a++ {
		dst.SetRGBA(a, 0, color.RGBA{180, 120, 60, 255})
		src.SetRGBA(a, 0, color.RGBA{uint8(a / 2), uint8(a / 3), uint8(a / 4), uint8(a)})
	}
	blendOver(dst, src)
	for a := 1; a < 255; a++ {
		inv := uint32(255 - a)
		wantR := min(255, uint32(a/2)+(180*inv)/255)
		wantG := min(255, uint32(a/3)+(120*inv)/255)
		wantB := min(255, uint32(a/4)+(60*inv)/255)
		got := dst.RGBAAt(a, 0)
		if uint32(got.R) != wantR || uint32(got.G) != wantG || uint32(got.B) != wantB {
			t.Fatalf("alpha=%d: got %+v, want (%d,%d,%d)", a, got, wantR, wantG, wantB)
		}
	}
}

func TestModernMosaicStyles(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 260, 120))
	for y := 0; y < 120; y++ {
		for x := 0; x < 260; x++ {
			// High-contrast stripe pattern simulating crisp text on light UI.
			if (x/3+y/5)%2 == 0 {
				base.SetRGBA(x, y, color.RGBA{245, 247, 250, 255})
			} else {
				base.SetRGBA(x, y, color.RGBA{25, 45, 95, 255})
			}
		}
	}

	anns := []Annotation{
		{ID: 1, Kind: ToolPixelate, MosaicStyle: MosaicCrystal, Start: PointF{10, 14}, End: PointF{84, 52}, BlockSize: 12},
		{ID: 2, Kind: ToolPixelate, MosaicStyle: MosaicPixel, Start: PointF{94, 14}, End: PointF{168, 52}, BlockSize: 12},
		{ID: 3, Kind: ToolPixelate, MosaicStyle: MosaicRedact, Start: PointF{178, 14}, End: PointF{250, 52}, BlockSize: 12},
	}

	out := RenderRasterEffects(base, anns)
	if out == nil {
		t.Fatalf("RenderRasterEffects returned nil")
	}

	// Verify corner anti-aliasing keeps the very corner outside the rounded capsule untouched.
	cornerOrig := base.RGBAAt(178, 14)
	cornerGot := out.RGBAAt(178, 14)
	if cornerGot != cornerOrig {
		t.Fatalf("expected rounded redact corner (178,14) to stay untouched, got %+v vs orig %+v", cornerGot, cornerOrig)
	}

	// Verify redact center is deeply obscured matte charcoal (low luminance, completely destroys pattern).
	centerRedact := out.RGBAAt(214, 33)
	if centerRedact.R > 70 || centerRedact.G > 75 || centerRedact.B > 85 {
		t.Fatalf("expected dark matte redact center on light bg, got %+v", centerRedact)
	}

	// Verify crystal mosaic actually modifies pixels inside its center.
	diffCount := 0
	for y := 20; y < 46; y++ {
		for x := 16; x < 78; x++ {
			if out.RGBAAt(x, y) != base.RGBAAt(x, y) {
				diffCount++
			}
		}
	}
	if diffCount < 200 {
		t.Fatalf("expected crystal mosaic to visibly transform pixels, diffCount=%d", diffCount)
	}
}

func absDiffU8(a, b uint8) uint8 {
	if a >= b {
		return a - b
	}
	return b - a
}
