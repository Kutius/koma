package stitch

import (
	"image"
	"testing"
)

func makeBenchScrollPage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		row := y * img.Stride
		band := (y / 28) * 37
		for x := 0; x < w; x++ {
			off := row + x*4
			r := uint8(245)
			g := uint8(246)
			b := uint8(248)
			if y%28 >= 8 && y%28 <= 18 && x >= 32 && x < w-40 && ((x+band)%17 < 9) {
				r = uint8((band*3 + x) & 0x7F)
				g = uint8((band*5 + y) & 0x7F)
				b = uint8((band*7 + x + y) & 0x7F)
			}
			img.Pix[off+0] = r
			img.Pix[off+1] = g
			img.Pix[off+2] = b
			img.Pix[off+3] = 255
		}
	}
	return img
}

func sliceBenchFrame(page *image.RGBA, y0, h int) *image.RGBA {
	w := page.Bounds().Dx()
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		copy(out.Pix[y*out.Stride:(y+1)*out.Stride], page.Pix[(y0+y)*page.Stride:(y0+y+1)*page.Stride])
	}
	return out
}

func BenchmarkStitcherFeed_1080p(b *testing.B) {
	page := makeBenchScrollPage(1024, 3600)
	frameH := 720
	frames := make([]*image.RGBA, 12)
	for i := range frames {
		frames[i] = sliceBenchFrame(page, i*180, frameH)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		st := New(frames[0], DefaultMaxHeight)
		for k := 1; k < len(frames); k++ {
			res := st.Feed(frames[k])
			if res.Status != StatusStitched {
				b.Fatalf("frame %d failed: %+v", k, res)
			}
		}
		st.Release()
	}
}
