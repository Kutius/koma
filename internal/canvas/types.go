package canvas

import (
	"image"
	"image/color"
	"math"
	"strings"

	"github.com/egoist/mygo/ui"
)

// Tool identifies the active interactive tool in the Koma editor.
type Tool int

const (
	ToolSelect Tool = iota
	ToolArrow
	ToolLine
	ToolRect
	ToolEllipse
	ToolPen
	ToolHighlighter
	ToolText
	ToolCounter
	ToolPixelate
	ToolBlur
	ToolSpotlight
	ToolMeasure
	ToolCrop
)

func (t Tool) Name() string {
	switch t {
	case ToolSelect:
		return "选择"
	case ToolArrow:
		return "箭头"
	case ToolLine:
		return "直线"
	case ToolRect:
		return "矩形"
	case ToolEllipse:
		return "椭圆"
	case ToolPen:
		return "画笔"
	case ToolHighlighter:
		return "荧光笔"
	case ToolText:
		return "文字"
	case ToolCounter:
		return "序号"
	case ToolPixelate:
		return "马赛克"
	case ToolBlur:
		return "模糊"
	case ToolSpotlight:
		return "聚光灯"
	case ToolMeasure:
		return "测距"
	case ToolCrop:
		return "裁剪"
	default:
		return "工具"
	}
}

func (t Tool) Shortcut() string {
	switch t {
	case ToolSelect:
		return "V"
	case ToolArrow:
		return "A"
	case ToolLine:
		return "L"
	case ToolRect:
		return "R"
	case ToolEllipse:
		return "O"
	case ToolPen:
		return "P"
	case ToolHighlighter:
		return "H"
	case ToolText:
		return "T"
	case ToolCounter:
		return "N"
	case ToolPixelate:
		return "X"
	case ToolBlur:
		return "B"
	case ToolSpotlight:
		return "S"
	case ToolMeasure:
		return "M"
	case ToolCrop:
		return "C"
	default:
		return ""
	}
}

// PointF is a 2D point in image pixel coordinates.
type PointF struct {
	X, Y float32
}

// MosaicStyle selects the visual finish of ToolPixelate redaction regions.
type MosaicStyle int

const (
	MosaicCrystal MosaicStyle = iota // 晶格：现代琉璃压花磨砂马赛克（默认）
	MosaicBlur                       // 模糊：液态圆角高斯模糊
	MosaicPixel                      // 像素：经典圆角像素马赛克
	MosaicRedact                     // 遮盖：不可逆净色磨砂脱敏胶囊
)

func (s MosaicStyle) Name() string {
	switch s {
	case MosaicBlur:
		return "模糊"
	case MosaicPixel:
		return "像素"
	case MosaicRedact:
		return "遮盖"
	default:
		return "晶格"
	}
}

// Annotation represents a single vector or raster-region annotation in image coordinates.
type Annotation struct {
	ID          int
	Kind        Tool
	Start       PointF
	End         PointF
	Points      []PointF // used by ToolPen, ToolHighlighter
	Color       ui.Color
	StrokeWidth float32
	Filled      bool        // Rect/Ellipse filled translucent; Text pill background
	Rounded     bool        // Rect rounded corners
	Text        string      // ToolText
	FontSize    float32     // ToolText
	CounterNum  int         // ToolCounter
	BlockSize   int         // ToolPixelate / ToolBlur intensity (px)
	MosaicStyle MosaicStyle // ToolPixelate visual finish
	// Measure spans (in image pixels)
	MeasLeft   int
	MeasRight  int
	MeasTop    int
	MeasBottom int
}

// Bounds returns the normalized bounding box of the annotation in image pixel coordinates.
func (a Annotation) Bounds() (minX, minY, maxX, maxY float32) {
	switch a.Kind {
	case ToolPen, ToolHighlighter:
		if len(a.Points) == 0 {
			return a.Start.X, a.Start.Y, a.End.X, a.End.Y
		}
		minX, maxX = a.Points[0].X, a.Points[0].X
		minY, maxY = a.Points[0].Y, a.Points[0].Y
		for _, pt := range a.Points[1:] {
			if pt.X < minX {
				minX = pt.X
			}
			if pt.X > maxX {
				maxX = pt.X
			}
			if pt.Y < minY {
				minY = pt.Y
			}
			if pt.Y > maxY {
				maxY = pt.Y
			}
		}
		pad := a.StrokeWidth * 0.6
		return minX - pad, minY - pad, maxX + pad, maxY + pad
	case ToolCounter:
		r := CounterRadius(a) + 1.5
		return a.Start.X - r, a.Start.Y - r, a.Start.X + r, a.Start.Y + r
	case ToolText:
		fs := ClampFontSize(a.FontSize)
		lines := strings.Split(strings.ReplaceAll(a.Text, "\r\n", "\n"), "\n")
		var maxW float32
		for _, line := range lines {
			if lw := estimateTextWidth(line, fs); lw > maxW {
				maxW = lw
			}
		}
		if maxW < fs*1.6 {
			maxW = fs * 1.6
		}
		lineH := fs * 1.36
		padX := max(float32(8), fs*0.42)
		padY := max(float32(5), fs*0.24)
		h := float32(len(lines))*lineH + padY*2
		return a.Start.X - padX*0.65, a.Start.Y - padY, a.Start.X + maxW + padX*1.45, a.Start.Y + h
	case ToolMeasure:
		return float32(a.MeasLeft), float32(a.MeasTop), float32(a.MeasRight), float32(a.MeasBottom)
	default:
		minX, maxX = a.Start.X, a.End.X
		if minX > maxX {
			minX, maxX = maxX, minX
		}
		minY, maxY = a.Start.Y, a.End.Y
		if minY > maxY {
			minY, maxY = maxY, minY
		}
		return minX, minY, maxX, maxY
	}
}

// Translate moves an annotation by (dx, dy) in image pixel coordinates.
func (a *Annotation) Translate(dx, dy float32) {
	a.Start.X += dx
	a.Start.Y += dy
	a.End.X += dx
	a.End.Y += dy
	for i := range a.Points {
		a.Points[i].X += dx
		a.Points[i].Y += dy
	}
	if a.Kind == ToolMeasure {
		a.MeasLeft += int(dx)
		a.MeasRight += int(dx)
		a.MeasTop += int(dy)
		a.MeasBottom += int(dy)
	}
}

// Clone deep-copies an annotation.
func (a Annotation) Clone() Annotation {
	out := a
	if len(a.Points) > 0 {
		out.Points = make([]PointF, len(a.Points))
		copy(out.Points, a.Points)
	}
	return out
}

// PaletteSwatch is a curated color swatch for annotation tools.
type PaletteSwatch struct {
	Name  string
	Color ui.Color
}

// FontSizeSteps defines clean optical typographic stops for quick stepping (-/+ and [/]).
var FontSizeSteps = []float32{12, 14, 16, 18, 20, 22, 26, 30, 36, 44, 54, 64, 72}

// StrokeWidthSteps defines 5 balanced stroke widths from crisp 1.5px hairline to 12px bold callout.
var StrokeWidthSteps = []struct {
	Dot   float32
	Width float32
	Label string
}{
	{4, 1.5, "极细"},
	{6, 3, "细"},
	{8, 5, "中"},
	{10, 8, "粗"},
	{12, 12, "特粗"},
}

// CounterSizeSteps defines 3 optical counter badge radii (stored in Annotation.FontSize).
var CounterSizeSteps = []struct {
	Radius float32
	Label  string
}{
	{11, "小"},
	{14, "中"},
	{19, "大"},
}

const (
	MinFontSize float32 = 12
	MaxFontSize float32 = 72
)

// ClampFontSize normalizes a text annotation font size to [MinFontSize, MaxFontSize].
func ClampFontSize(fs float32) float32 {
	if fs <= 0 {
		return 22
	}
	if fs < MinFontSize {
		return MinFontSize
	}
	if fs > MaxFontSize {
		return MaxFontSize
	}
	return fs
}

// StepFontSize returns the next larger (delta > 0) or smaller (delta < 0) typographic stop.
func StepFontSize(cur float32, delta int) float32 {
	cur = ClampFontSize(cur)
	if delta > 0 {
		for _, s := range FontSizeSteps {
			if s > cur+0.5 {
				return s
			}
		}
		return FontSizeSteps[len(FontSizeSteps)-1]
	}
	for i := len(FontSizeSteps) - 1; i >= 0; i-- {
		if FontSizeSteps[i] < cur-0.5 {
			return FontSizeSteps[i]
		}
	}
	return FontSizeSteps[0]
}

// StepStrokeWidth returns the next thicker (delta > 0) or thinner (delta < 0) stroke stop.
func StepStrokeWidth(cur float32, delta int) float32 {
	if delta > 0 {
		for _, s := range StrokeWidthSteps {
			if s.Width > cur+0.25 {
				return s.Width
			}
		}
		return StrokeWidthSteps[len(StrokeWidthSteps)-1].Width
	}
	for i := len(StrokeWidthSteps) - 1; i >= 0; i-- {
		if StrokeWidthSteps[i].Width < cur-0.25 {
			return StrokeWidthSteps[i].Width
		}
	}
	return StrokeWidthSteps[0].Width
}

// CounterRadius returns the normalized pixel radius of a ToolCounter badge.
func CounterRadius(a Annotation) float32 {
	r := a.FontSize
	if r <= 0 {
		return 14
	}
	if r < 10 {
		return 10
	}
	if r > 26 {
		return 26
	}
	return r
}

// RelativeLuminance returns WCAG 2.1 relative luminance in [0, 1] for an sRGB color.
func RelativeLuminance(c ui.Color) float64 {
	lin := func(u uint8) float64 {
		v := float64(u) / 255.0
		if v <= 0.03928 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c.R) + 0.7152*lin(c.G) + 0.0722*lin(c.B)
}

// ContrastRatio computes the WCAG 2.1 contrast ratio in [1, 21] between a and b.
func ContrastRatio(a, b ui.Color) float64 {
	la, lb := RelativeLuminance(a), RelativeLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// IsLightColor reports whether c is bright enough (WCAG relative luminance > 0.34,
// covering Amber #FF9500, Yellow #FFCC00, Emerald #34C759, White #FFFFFF) that dark
// ink is required for crisp legibility.
func IsLightColor(c ui.Color) bool {
	return RelativeLuminance(c) > 0.34
}

// ContrastInk returns the highest-legibility foreground ink (#0F172A or #FFFFFF)
// for text and badges rendered over fill color bg.
func ContrastInk(bg ui.Color) ui.Color {
	if IsLightColor(bg) {
		return ui.Hex("#0F172A")
	}
	return ui.Hex("#FFFFFF")
}

// TextHaloColor returns an adaptive optical outline color that keeps unfilled text
// sharp across pure white, pure black, and busy screenshots without muddy shadows.
func TextHaloColor(fg ui.Color) ui.Color {
	if IsLightColor(fg) {
		return ui.RGBA(15, 23, 42, 0.86)
	}
	return ui.RGBA(255, 255, 255, 0.88)
}

// PillBorderColor returns an adaptive hairline border color for filled badges/pills
// so white pills stay defined on light screenshots and charcoal pills on dark screens.
func PillBorderColor(bg ui.Color) ui.Color {
	lum := RelativeLuminance(bg)
	switch {
	case lum > 0.72:
		return ui.RGBA(15, 23, 42, 0.24)
	case lum < 0.08:
		return ui.RGBA(255, 255, 255, 0.26)
	case lum > 0.34:
		return ui.RGBA(15, 23, 42, 0.14)
	default:
		return ui.RGBA(255, 255, 255, 0.20)
	}
}

// InlineEditorBackdrop picks a translucent backdrop behind the inline text editor
// that guarantees crisp readability while typing for both light and dark ink colors.
func InlineEditorBackdrop(fg ui.Color) ui.Color {
	if IsLightColor(fg) {
		return ui.RGBA(15, 23, 42, 0.82)
	}
	return ui.RGBA(255, 255, 255, 0.90)
}

// DefaultPalette provides Apple HIG system colors tuned for crisp annotation legibility.
var DefaultPalette = []PaletteSwatch{
	{Name: "绯红", Color: ui.Hex("#FF3B30")},
	{Name: "琥珀", Color: ui.Hex("#FF9500")},
	{Name: "明黄", Color: ui.Hex("#FFCC00")},
	{Name: "翡翠", Color: ui.Hex("#34C759")},
	{Name: "晴空", Color: ui.Hex("#007AFF")},
	{Name: "鸢尾", Color: ui.Hex("#AF52DE")},
	{Name: "玫瑰", Color: ui.Hex("#FF2D55")},
	{Name: "纯白", Color: ui.Hex("#FFFFFF")},
	{Name: "石墨", Color: ui.Hex("#1C1C1E")},
}

// ToRGBA converts a ui.Color to standard library color.RGBA.
func ToRGBA(c ui.Color) color.RGBA {
	return color.RGBA{R: c.R, G: c.G, B: c.B, A: c.A}
}

// Snapshot stores an undoable state of the document.
type Snapshot struct {
	Image       *image.RGBA
	Annotations []Annotation
	NextCounter int
}

// CloneAnnotations returns a deep copy of a slice of annotations.
func CloneAnnotations(in []Annotation) []Annotation {
	if len(in) == 0 {
		return nil
	}
	out := make([]Annotation, len(in))
	for i, a := range in {
		out[i] = a.Clone()
	}
	return out
}
