package canvas

import (
	"image"
	"image/color"
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
		r := float32(15)
		return a.Start.X - r, a.Start.Y - r, a.Start.X + r, a.Start.Y + r
	case ToolText:
		fs := a.FontSize
		if fs <= 0 {
			fs = 20
		}
		lines := strings.Split(a.Text, "\n")
		var maxW float32
		for _, line := range lines {
			if lw := estimateTextWidth(line, fs); lw > maxW {
				maxW = lw
			}
		}
		if maxW < fs*2 {
			maxW = fs * 2
		}
		lineH := fs * 1.38
		h := float32(len(lines))*lineH + 10
		return a.Start.X - 6, a.Start.Y - 6, a.Start.X + maxW + 20, a.Start.Y + h
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

// DefaultPalette provides Apple HIG system colors tuned for crisp annotation legibility.
var DefaultPalette = []PaletteSwatch{
	{Name: "绯红", Color: ui.Hex("#FF3B30")},
	{Name: "琥珀", Color: ui.Hex("#FF9500")},
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
