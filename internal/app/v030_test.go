package app

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"koma/internal/canvas"
	"koma/internal/ocr"
)

func TestFormatPickedColorFormats(t *testing.T) {
	if got := FormatPickedColor(255, 59, 48, ColorFormatHEX); got != "#FF3B30" {
		t.Fatalf("HEX = %q, want #FF3B30", got)
	}
	if got := FormatPickedColor(255, 59, 48, ColorFormatRGB); got != "rgb(255, 59, 48)" {
		t.Fatalf("RGB = %q, want rgb(255, 59, 48)", got)
	}
	if got := FormatPickedColor(255, 59, 48, ColorFormatHSL); got != "hsl(3, 100%, 59%)" {
		t.Fatalf("HSL = %q, want hsl(3, 100%%, 59%%)", got)
	}
}

func TestOverlayInlineTextSelectionMoveAndNudge(t *testing.T) {
	a := New()
	img := GenerateSampleCanvas()
	ov := NewTestOverlayView(a, img, canvas.ToolText)
	tt := ui.NewTester(ov.View, 860, 520)

	initCount := len(ov.Annotations())

	// 1. Clicking inside selection with ToolText opens inline text editor at canvas point
	tt.ClickAt(320, 220)
	if !ov.TextEditing() {
		t.Fatalf("expected inline text editor to open after clicking canvas with ToolText")
	}
	tt.Type("Hello Koma")
	tt.Key(0, ui.KeyEnter)
	if ov.TextEditing() {
		t.Fatalf("expected Enter to commit inline text editor")
	}
	anns := ov.Annotations()
	if len(anns) != initCount+1 {
		t.Fatalf("expected %d annotations after inline text commit, got %d", initCount+1, len(anns))
	}
	last := anns[len(anns)-1]
	if last.Kind != canvas.ToolText || last.Text != "Hello Koma" {
		t.Fatalf("unexpected committed text annotation: %+v", last)
	}

	// 2. Delete removes the currently selected annotation
	tt.Key(0, ui.KeyDelete)
	if got := len(ov.Annotations()); got != initCount {
		t.Fatalf("expected Delete to remove selected text annotation: got %d, want %d", got, initCount)
	}

	// 3. Keyboard 1px selection nudge & Shift resize
	x0, y0, x1, y1 := ov.SelectionBounds()
	tt.Key(0, ui.KeyRight)
	nx0, ny0, nx1, ny1 := ov.SelectionBounds()
	if nx0 <= x0 || nx1 <= x1 || ny0 != y0 || ny1 != y1 {
		t.Fatalf("expected Right arrow to move selection right: before=(%.1f,%.1f,%.1f,%.1f) after=(%.1f,%.1f,%.1f,%.1f)",
			x0, y0, x1, y1, nx0, ny0, nx1, ny1)
	}
	tt.Key(ui.Shift, ui.KeyDown)
	sx0, sy0, sx1, sy1 := ov.SelectionBounds()
	if sx0 != nx0 || sy0 != ny0 || sx1 != nx1 || sy1 <= ny1 {
		t.Fatalf("expected Shift+Down to expand selection bottom: before=(%.1f,%.1f,%.1f,%.1f) after=(%.1f,%.1f,%.1f,%.1f)",
			nx0, ny0, nx1, ny1, sx0, sy0, sx1, sy1)
	}
}

func TestQuickSaveAndSettingsSavePreferences(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "settings.json")
	saveDir := filepath.Join(tmp, "captures")

	a := New()
	a.SetSettingsPath(cfgPath)
	a.SetSaveDir(saveDir)
	a.SetSaveFormat(SaveFormatJPEG)
	a.SetQuickSave(true)
	a.SetOCREngine(ocr.EnginePPOCRv6)
	if err := a.SetHotkey(HotkeyLong, "Alt+L"); err != nil {
		t.Fatalf("SetHotkey(HotkeyLong) failed: %v", err)
	}

	// Verify persistence across reload
	b := New()
	b.SetSettingsPath(cfgPath)
	if err := b.LoadSettings(); err != nil {
		t.Fatalf("LoadSettings failed: %v", err)
	}
	if b.SaveDir() != saveDir || b.SaveFormat() != SaveFormatJPEG || !b.QuickSave() || b.Hotkey(HotkeyLong) != "Alt+L" || b.OCREngine() != ocr.EnginePPOCRv6 {
		t.Fatalf("reloaded settings mismatch: dir=%q fmt=%q quick=%v long=%q ocr=%q",
			b.SaveDir(), b.SaveFormat(), b.QuickSave(), b.Hotkey(HotkeyLong), b.OCREngine())
	}

	// QuickSaveImage writes a valid JPEG file into saveDir without any dialog
	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 10, 132, 255, 255
	}
	outPath, err := b.QuickSaveImage(img, nil)
	if err != nil {
		t.Fatalf("QuickSaveImage failed: %v", err)
	}
	if filepath.Dir(outPath) != saveDir || !strings.HasSuffix(outPath, ".jpg") {
		t.Fatalf("unexpected quick save path: %q", outPath)
	}
	info, err := os.Stat(outPath)
	if err != nil || info.Size() == 0 {
		t.Fatalf("expected non-empty saved JPEG at %q, err=%v", outPath, err)
	}
}

func TestPinImageTransforms(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 3, 2))
	src.SetRGBA(0, 0, color.RGBA{255, 0, 0, 255}) // top-left = red
	src.SetRGBA(2, 0, color.RGBA{0, 255, 0, 255}) // top-right = green

	rot := rotateRGBA90CW(src)
	if rot.Bounds().Dx() != 2 || rot.Bounds().Dy() != 3 {
		t.Fatalf("rotated size = %dx%d, want 2x3", rot.Bounds().Dx(), rot.Bounds().Dy())
	}
	// Top-left (0,0) rotated 90 CW moves to top-right (1,0)
	if got := rot.RGBAAt(1, 0); got.R != 255 || got.G != 0 {
		t.Fatalf("rotated pixel at (1,0) = %+v, want red", got)
	}

	flip := flipRGBAHorizontal(src)
	if got := flip.RGBAAt(0, 0); got.G != 255 || got.R != 0 {
		t.Fatalf("flipped pixel at (0,0) = %+v, want green", got)
	}
}

func TestOCRResultWindowHeadless(t *testing.T) {
	a := New()
	a.SetOCRDataDir(t.TempDir())
	view := NewTestOCRView(a, "Koma 截图工具\n一帧清光，恰如其分。", 2)
	tt := ui.NewTester(view, 480, 380)
	if !tt.HasText("文字识别") || !tt.HasText("复制并关闭") {
		t.Fatalf("expected OCR card header and CTA, got texts: %q", tt.Texts())
	}
	if !tt.HasText("系统原生") {
		t.Fatalf("expected OCR engine badge in result header, got texts: %q", tt.Texts())
	}
	if _, ok := tt.Find("仅复制"); !ok {
		t.Fatalf("expected 仅复制 button in OCR result window")
	}
}

func TestSettingsOCRControlsHeadless(t *testing.T) {
	a := New()
	a.SetSettingsPath(filepath.Join(t.TempDir(), "settings.json"))
	a.SetOCRDataDir(t.TempDir())
	a.editor.OpenSettings()
	tt := ui.NewTester(a.editor.View, 1180, 760)
	for _, want := range []string{"文字识别", "识别引擎", "系统原生", "PP-OCRv6", "下载模型"} {
		if !tt.HasText(want) {
			t.Fatalf("expected Settings sheet to display %q, got texts: %q", want, tt.Texts())
		}
	}
}

func TestPinWindowRightClickAndDragIsolation(t *testing.T) {
	a := New()
	img := image.NewRGBA(image.Rect(0, 0, 240, 160))
	pngBytes, err := canvas.EncodePNG(img)
	if err != nil {
		t.Fatalf("EncodePNG: %v", err)
	}
	menuPopups := 0
	pw := &PinWindow{
		app:           a,
		pngBytes:      pngBytes,
		bmp:           ui.NewBitmap(img),
		baseW:         240,
		baseH:         160,
		imgAspect:     160.0 / 240.0,
		scale:         1.0,
		opacity:       1.0,
		onContextMenu: func() { menuPopups++ },
	}
	tt := ui.NewTester(pw.view, 240, 160)

	// Right-click opens context menu via HandleInput (without setting flagClickable on DragWindow)
	tt.RightClickAt(120, 80)
	if menuPopups != 1 {
		t.Fatalf("expected 1 context menu popup on right-click, got %d", menuPopups)
	}

	// Wheel zooms cleanly without stale element bounds panic
	tt.Wheel(120, 80, 0, -120)
	if pw.scale <= 1.0 {
		t.Fatalf("expected scroll up to zoom in, got scale=%.2f", pw.scale)
	}
}
