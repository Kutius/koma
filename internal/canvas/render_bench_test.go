package canvas

import (
	"image"
	"testing"

	"github.com/egoist/mygo/ui"
)

func makeBenchImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		row := y * img.Stride
		for x := 0; x < w; x++ {
			off := row + x*4
			img.Pix[off+0] = uint8((x*3 + y) & 0xFF)
			img.Pix[off+1] = uint8((x + y*5) & 0xFF)
			img.Pix[off+2] = uint8((x*7 ^ y*3) & 0xFF)
			img.Pix[off+3] = 255
		}
	}
	return img
}

func BenchmarkApplyBlur_1080pRegion(b *testing.B) {
	base := makeBenchImage(1920, 1080)
	region := image.Rect(200, 150, 1160, 690) // 960x540 blur region
	work := CloneRGBA(base)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		applyBlur(work, region, 18)
	}
}

func BenchmarkApplySpotlights_1080p(b *testing.B) {
	base := makeBenchImage(1920, 1080)
	spots := []image.Rectangle{
		image.Rect(120, 80, 520, 320),
		image.Rect(900, 400, 1500, 820),
	}
	work := CloneRGBA(base)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		applySpotlights(work, spots)
	}
}

func BenchmarkMakeThumbnail_1080p(b *testing.B) {
	base := makeBenchImage(1920, 1080)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = MakeThumbnail(base, 240, 150)
	}
}

func BenchmarkBlendOver_1080p(b *testing.B) {
	dst := makeBenchImage(1920, 1080)
	src := makeBenchImage(1920, 1080)
	for y := 0; y < 1080; y++ {
		row := y * src.Stride
		for x := 0; x < 1920; x++ {
			off := row + x*4
			if (x+y)%7 == 0 {
				src.Pix[off+3] = 160
			} else {
				src.Pix[off+3] = 0
			}
		}
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		blendOver(dst, src)
	}
}

func BenchmarkEncodePNG_1080p(b *testing.B) {
	base := makeBenchImage(1920, 1080)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := EncodePNG(base); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRenderRasterEffects_Mixed(b *testing.B) {
	base := makeBenchImage(1280, 720)
	anns := []Annotation{
		{ID: 1, Kind: ToolBlur, Start: PointF{X: 80, Y: 60}, End: PointF{X: 480, Y: 300}, BlockSize: 16},
		{ID: 2, Kind: ToolPixelate, Start: PointF{X: 600, Y: 100}, End: PointF{X: 980, Y: 360}, BlockSize: 12},
		{ID: 3, Kind: ToolSpotlight, Start: PointF{X: 200, Y: 400}, End: PointF{X: 700, Y: 650}, Color: ui.Hex("#FFFFFF")},
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = RenderRasterEffects(base, anns)
	}
}
