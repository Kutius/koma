package winapi

import (
	"image"
	"image/color"
	"testing"
)

func TestMeasureEdges(t *testing.T) {
	// 200x100 dark canvas with a 80x40 bright button at [50..130, 30..70)
	img := image.NewRGBA(image.Rect(0, 0, 200, 100))
	bg := color.RGBA{20, 24, 32, 255}
	btn := color.RGBA{59, 130, 246, 255}
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			if x >= 50 && x < 130 && y >= 30 && y < 70 {
				img.SetRGBA(x, y, btn)
			} else {
				img.SetRGBA(x, y, bg)
			}
		}
	}

	// Probe inside the button
	m := MeasureEdges(img, 80, 50, 28)
	if !m.Valid {
		t.Fatal("expected valid measurement")
	}
	if m.Left != 50 || m.Right != 129 || m.Width != 80 {
		t.Errorf("horizontal measure = [%d..%d] w=%d, want [50..129] w=80", m.Left, m.Right, m.Width)
	}
	if m.Top != 30 || m.Bottom != 69 || m.Height != 40 {
		t.Errorf("vertical measure = [%d..%d] h=%d, want [30..69] h=40", m.Top, m.Bottom, m.Height)
	}

	// Probe the left margin gap between x=0 and button x=50
	gap := MeasureEdges(img, 25, 50, 28)
	if gap.Left != 0 || gap.Right != 49 || gap.Width != 50 {
		t.Errorf("gap measure = [%d..%d] w=%d, want [0..49] w=50", gap.Left, gap.Right, gap.Width)
	}
}

func TestCropRGBA(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 100, 80))
	img.SetRGBA(15, 25, color.RGBA{255, 10, 20, 255})
	c := CropRGBA(img, image.Rect(10, 20, 40, 50))
	if c == nil || c.Bounds().Dx() != 30 || c.Bounds().Dy() != 30 {
		t.Fatalf("unexpected crop bounds: %v", c)
	}
	r, g, b, ok := ColorAt(c, 5, 5)
	if !ok || r != 255 || g != 10 || b != 20 {
		t.Errorf("got (%d,%d,%d,%v), want (255,10,20,true)", r, g, b, ok)
	}
}
