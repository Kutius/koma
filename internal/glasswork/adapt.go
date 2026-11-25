package glasswork

import (
	"image"
	"math"
	"sort"

	"github.com/egoist/mygo/ui"
)

// Kind selects Glasswork's two physical materials:
//   - KindLight: crystal clear glass with dark ink (#000000 86%)
//   - KindDark: smoky clear glass with light ink (#FFFFFF 94%)
type Kind uint8

const (
	KindLight Kind = iota
	KindDark
)

// Sample summarizes the wallpaper / desktop region directly under a glass surface.
type Sample struct {
	Luminance float64
	Texture   float64
	Color     [3]float64
}

// Adaptation holds the per-surface parameters computed by Glasswork's adapt.js.
type Adaptation struct {
	Kind    Kind
	Density float32
	Keep    float32
	Floor   float32
}

// InkColors returns Glasswork's exact ink palette for Kind.
type InkColors struct {
	Primary      ui.Color
	Secondary    ui.Color
	Hover        ui.Color
	Active       ui.Color
	Hairline     ui.Color
	Accent       ui.Color // High-contrast active/emphasis ink (never white)
	AccentFill   ui.Color // Soft translucent active pill fill
	AccentBorder ui.Color // Subtle optical hairline around active pill
	Danger       ui.Color // High-contrast destructive hover ink
	DangerFill   ui.Color // Soft translucent destructive hover fill
}

// Inks returns the high-contrast ink tokens defined in Glasswork's glass.css,
// paired with WCAG-verified Tahoe Sapphire accent & coral danger inks so active
// icons never rely on white glyphs over translucent glass.
func (k Kind) Inks() InkColors {
	if k == KindDark {
		return InkColors{
			Primary:      ui.RGBA(255, 255, 255, 0.94),
			Secondary:    ui.RGBA(235, 235, 245, 0.66),
			Hover:        ui.RGBA(255, 255, 255, 0.12),
			Active:       ui.RGBA(255, 255, 255, 0.18),
			Hairline:     ui.RGBA(255, 255, 255, 0.16),
			Accent:       ui.Hex("#58B0FF"),
			AccentFill:   ui.Hex("#0A84FF").Alpha(0.24),
			AccentBorder: ui.Hex("#58B0FF").Alpha(0.42),
			Danger:       ui.Hex("#FF6961"),
			DangerFill:   ui.Hex("#FF453A").Alpha(0.22),
		}
	}
	return InkColors{
		Primary:      ui.RGBA(0, 0, 0, 0.86),
		Secondary:    ui.RGBA(60, 60, 67, 0.72),
		Hover:        ui.RGBA(0, 0, 0, 0.065),
		Active:       ui.RGBA(0, 0, 0, 0.11),
		Hairline:     ui.RGBA(0, 0, 0, 0.12),
		Accent:       ui.Hex("#005AD6"),
		AccentFill:   ui.Hex("#0066E6").Alpha(0.16),
		AccentBorder: ui.Hex("#0066E6").Alpha(0.32),
		Danger:       ui.Hex("#C81E1E"),
		DangerFill:   ui.Hex("#FF3B30").Alpha(0.15),
	}
}

var (
	gridXS = [5]float64{0.18, 0.34, 0.50, 0.66, 0.82}
	gridYS = [3]float64{0.24, 0.50, 0.76}
)

// SampleRegion samples a 5x3 grid under box on img, dropping the 2 darkest and
// 2 brightest points, mirroring Glasswork's adapt.js sampler().
func SampleRegion(img *image.RGBA, box image.Rectangle) (Sample, bool) {
	if img == nil {
		return Sample{}, false
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 || box.Dx() <= 0 || box.Dy() <= 0 {
		return Sample{}, false
	}
	type pt struct {
		rgb [3]float64
		lum float64
	}
	pts := make([]pt, 0, 15)
	for _, fy := range gridYS {
		for _, fx := range gridXS {
			px := clampInt(int(math.Round(float64(box.Min.X)+float64(box.Dx())*fx)), 0, w-1)
			py := clampInt(int(math.Round(float64(box.Min.Y)+float64(box.Dy())*fy)), 0, h-1)
			off := py*img.Stride + px*4
			rgb := [3]float64{float64(img.Pix[off]), float64(img.Pix[off+1]), float64(img.Pix[off+2])}
			pts = append(pts, pt{rgb: rgb, lum: luma(rgb)})
		}
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].lum < pts[j].lum })
	texture := pts[len(pts)-1].lum - pts[0].lum
	kept := pts[2 : len(pts)-2]
	var meanLum float64
	var meanRGB [3]float64
	for _, p := range kept {
		meanLum += p.lum
		meanRGB[0] += p.rgb[0]
		meanRGB[1] += p.rgb[1]
		meanRGB[2] += p.rgb[2]
	}
	n := float64(len(kept))
	return Sample{
		Luminance: meanLum / n,
		Texture:   texture,
		Color:     [3]float64{meanRGB[0] / n, meanRGB[1] / n, meanRGB[2] / n},
	}, true
}

// PickKind chooses KindDark below luminance threshold (0.50) and KindLight above,
// with a hysteresis band (0.05) so surfaces never flicker near the boundary.
func PickKind(luminance float64, previous Kind, hasPrev bool) Kind {
	const threshold = 0.50
	const band = 0.05
	shift := 0.0
	if hasPrev {
		if previous == KindDark {
			shift = band
		} else {
			shift = -band
		}
	}
	if luminance < threshold+shift {
		return KindDark
	}
	return KindLight
}

// Adapt computes Density, Keep (saturation retention), and WCAG 4.5:1 Floor
// directly from Glasswork's adapt.js formulas.
func Adapt(s Sample, kind Kind) Adaptation {
	var near float64
	if kind == KindLight {
		near = smoothstep((0.8 - s.Luminance) / 0.35)
	} else {
		near = smoothstep((s.Luminance - 0.12) / 0.40)
	}
	c := chroma(s.Color)
	density := clampF(near*0.55+s.Texture*0.7+c*0.3, 0, 1)
	keep := clampF(1-c*0.75, 0.45, 1)

	tintLevel := 1.0 // white tint for light glass
	if kind == KindDark {
		tintLevel = 30.0 / 255.0 // #1e1e22 smoky tint for dark glass
	}
	floor := tintFloor(s.Luminance, s.Texture, kind, tintLevel)

	return Adaptation{
		Kind:    kind,
		Density: float32(density),
		Keep:    float32(keep),
		Floor:   float32(floor),
	}
}

func luma(rgb [3]float64) float64 {
	return (rgb[0]*0.299 + rgb[1]*0.587 + rgb[2]*0.114) / 255.0
}

func chroma(rgb [3]float64) float64 {
	mx := math.Max(rgb[0], math.Max(rgb[1], rgb[2]))
	mn := math.Min(rgb[0], math.Min(rgb[1], rgb[2]))
	return (mx - mn) / 255.0
}

func channelLin(v float64) float64 {
	c := v / 255.0
	if c <= 0.03928 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func relLum(rgb [3]float64) float64 {
	return 0.2126*channelLin(rgb[0]) + 0.7152*channelLin(rgb[1]) + 0.0722*channelLin(rgb[2])
}

func contrastRatio(a, b [3]float64) float64 {
	la, lb := relLum(a), relLum(b)
	hi, lo := math.Max(la, lb), math.Min(la, lb)
	return (hi + 0.05) / (lo + 0.05)
}

func tintFloor(luminance, texture float64, kind Kind, tintLevel float64) float64 {
	const targetContrast = 4.5
	const maxFloor = 0.88
	spread := clampF(texture, 0, 1) * 0.5
	mean := clampF(luminance, 0, 1)

	ink := [3]float64{29, 29, 31}
	backdrop := math.Max(0, mean-spread)
	if kind == KindDark {
		ink = [3]float64{245, 245, 247}
		backdrop = math.Min(1, mean+spread)
	}

	reads := func(alpha float64) bool {
		g := clampF(backdrop+(tintLevel-backdrop)*alpha, 0, 1) * 255.0
		return contrastRatio(ink, [3]float64{g, g, g}) >= targetContrast
	}
	if reads(0) {
		return 0
	}
	if !reads(maxFloor) {
		return maxFloor
	}
	lo, hi := 0.0, maxFloor
	for range 16 {
		mid := (lo + hi) * 0.5
		if reads(mid) {
			hi = mid
		} else {
			lo = mid
		}
	}
	return hi
}

func smoothstep(v float64) float64 {
	a := clampF(v, 0, 1)
	return a * a * (3 - 2*a)
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
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
