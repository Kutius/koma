package app

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"koma/internal/canvas"
	"koma/internal/winapi"
)

func TestOverlayHDRToolbarVisibilityAndQuickSaveHDR(t *testing.T) {
	a := New()
	saveDir := t.TempDir()
	a.SetSaveDir(saveDir)
	a.SetQuickSave(true)

	// 1. SDR capture: HDR button must stay absent (zero clutter on SDR monitors).
	sdrImg := GenerateSampleCanvas()
	sdrOv := NewTestOverlayView(a, sdrImg, canvas.ToolSelect)
	ttSDR := ui.NewTesterWith(sdrOv.View, 1280, 720, ui.TesterOptions{
		Preferences: ui.Preferences{ReduceMotion: true, ReduceTransparency: true},
	})
	if ttSDR.HasText("HDR") {
		t.Fatalf("SDR overlay should not show HDR export button; texts=%q", ttSDR.Texts())
	}

	// 2. HDR capture: HDR button appears cleanly and QuickSaveHDR produces a valid 16-bit cICP PQ PNG.
	hdrFrame := &winapi.HDRFrame{
		Width:        sdrImg.Bounds().Dx(),
		Height:       sdrImg.Bounds().Dy(),
		SDRWhiteNits: 240.0,
		Float16:      make([]uint16, sdrImg.Bounds().Dx()*sdrImg.Bounds().Dy()*4),
	}
	one := winapi.Float32ToHalfBits(3.0) // 240 nits
	alpha := winapi.Float32ToHalfBits(1.0)
	for i := 0; i < len(hdrFrame.Float16); i += 4 {
		hdrFrame.Float16[i+0] = one
		hdrFrame.Float16[i+1] = one
		hdrFrame.Float16[i+2] = one
		hdrFrame.Float16[i+3] = alpha
	}

	hdrOv := NewTestOverlayView(a, sdrImg, canvas.ToolSelect)
	hdrOv.pane.capture.HDR = hdrFrame
	ttHDR := ui.NewTesterWith(hdrOv.View, 1280, 720, ui.TesterOptions{
		Preferences: ui.Preferences{ReduceMotion: true, ReduceTransparency: true},
	})
	if !ttHDR.HasText("HDR") {
		t.Fatalf("HDR overlay should display compact HDR export pill; texts=%q", ttHDR.Texts())
	}

	// Test QuickSaveHDR directly into temp directory.
	cropped := hdrFrame.Crop(sdrImg.Bounds().Inset(10))
	savedPath, err := a.QuickSaveHDR(cropped)
	if err != nil {
		t.Fatalf("QuickSaveHDR failed: %v", err)
	}
	if !strings.HasSuffix(filepath.Base(savedPath), "_HDR.png") {
		t.Fatalf("expected _HDR.png filename, got %q", savedPath)
	}
	raw, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatalf("read saved HDR PNG: %v", err)
	}
	if !bytes.Contains(raw[:64], []byte("cICP")) {
		t.Fatalf("saved HDR PNG missing cICP chunk in header")
	}
	decoded, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode saved HDR PNG: %v", err)
	}
	if decoded.Bounds().Dx() != cropped.Width || decoded.Bounds().Dy() != cropped.Height {
		t.Fatalf("decoded HDR PNG size = %v, want %dx%d", decoded.Bounds(), cropped.Width, cropped.Height)
	}
}
