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
