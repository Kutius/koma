package glasswork

import (
	"image"
	"image/color"
	"testing"
)

func TestAdaptLightAndDarkBackgrounds(t *testing.T) {
	// 1. Bright white desktop patch -> KindLight (clear glass + dark ink)
	bright := image.NewRGBA(image.Rect(0, 0, 120, 60))
	for y := 0; y < 60; y++ {
		for x := 0; x < 120; x++ {
			bright.SetRGBA(x, y, color.RGBA{242, 245, 250, 255})
		}
	}
	sBright, ok := SampleRegion(bright, image.Rect(10, 10, 110, 50))
	if !ok {
		t.Fatal("expected valid sample")
	}
	if k := PickKind(sBright.Luminance, KindDark, false); k != KindLight {
		t.Fatalf("expected KindLight on bright patch, got %v (lum=%.2f)", k, sBright.Luminance)
	}

	// 2. Dark IDE patch -> KindDark (smoky glass + light ink)
	dark := image.NewRGBA(image.Rect(0, 0, 120, 60))
	for y := 0; y < 60; y++ {
		for x := 0; x < 120; x++ {
			dark.SetRGBA(x, y, color.RGBA{18, 22, 34, 255})
		}
	}
	sDark, ok := SampleRegion(dark, image.Rect(10, 10, 110, 50))
	if !ok {
		t.Fatal("expected valid dark sample")
	}
	if k := PickKind(sDark.Luminance, KindLight, false); k != KindDark {
		t.Fatalf("expected KindDark on dark patch, got %v (lum=%.2f)", k, sDark.Luminance)
	}
	a := Adapt(sDark, KindDark)
	if a.Keep < 0.45 || a.Keep > 1.0 {
		t.Fatalf("unexpected Keep: %v", a.Keep)
	}
}

func TestAccentInksNeverWhiteAndHighContrast(t *testing.T) {
	light := KindLight.Inks()
	dark := KindDark.Inks()

	// Ensure neither light nor dark accent icon ink is white/achromatic.
	for name, col := range map[string][3]float64{
		"light.Accent": {float64(light.Accent.R), float64(light.Accent.G), float64(light.Accent.B)},
		"dark.Accent":  {float64(dark.Accent.R), float64(dark.Accent.G), float64(dark.Accent.B)},
	} {
		if chroma(col) < 0.35 {
			t.Errorf("%s should be a saturated non-white accent color, got rgb(%.0f,%.0f,%.0f)", name, col[0], col[1], col[2])
		}
	}

	lightRGB := [3]float64{float64(light.Accent.R), float64(light.Accent.G), float64(light.Accent.B)}
	if cr := contrastRatio(lightRGB, [3]float64{232, 238, 246}); cr < 4.5 {
		t.Errorf("light accent contrast on light glass = %.2f, want >= 4.5", cr)
	}

	darkRGB := [3]float64{float64(dark.Accent.R), float64(dark.Accent.G), float64(dark.Accent.B)}
	if cr := contrastRatio(darkRGB, [3]float64{34, 36, 44}); cr < 4.5 {
		t.Errorf("dark accent contrast on dark glass = %.2f, want >= 4.5", cr)
	}
}
