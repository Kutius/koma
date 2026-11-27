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

	// 1b. Dragging the inline text editor's left grip moves the text position while still typing!
	// Inline wrap left is dipX - gripW(16) - 6 = 298, top is dipY - 6 = 214.
	tt.Press(306, 228)
	tt.Move(356, 258)
	tt.Release(356, 258)
	if !ov.TextEditing() {
		t.Fatalf("dragging inline text grip should keep inline text editor open")
	}

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
	if last.Start.X < 365 || last.Start.Y < 245 {
		t.Fatalf("expected inline drag to move text before commit (from 320,220 by +50,+30), got Start=%+v", last.Start)
	}

	// 1c. While still in ToolText mode, dragging the committed text annotation moves it directly!
	beforeX, beforeY := last.Start.X, last.Start.Y
	dragX, dragY := beforeX+20, beforeY+12
	tt.Press(dragX, dragY)
	tt.Move(dragX+40, dragY+25)
	tt.Release(dragX+40, dragY+25)
	if ov.TextEditing() {
		t.Fatalf("dragging committed text in ToolText mode should move it, not open inline editor")
	}
	anns = ov.Annotations()
	moved := anns[len(anns)-1]
	if moved.Start.X < beforeX+35 || moved.Start.Y < beforeY+20 {
		t.Fatalf("expected committed text drag in ToolText to move annotation by ~(+40,+25): before=(%.1f,%.1f) after=(%.1f,%.1f)",
			beforeX, beforeY, moved.Start.X, moved.Start.Y)
	}

	// 1d. Clicking already-selected text without dragging re-opens inline text edit; clicking empty canvas commits without spawning a new box
	tt.ClickAt(moved.Start.X+20, moved.Start.Y+12)
	if !ov.TextEditing() {
		t.Fatalf("expected single click on selected text without dragging to re-open inline editor")
	}
	tt.ClickAt(200, 350)
	if ov.TextEditing() {
		t.Fatalf("clicking empty canvas while editing should commit cleanly without immediately opening a second text box")
	}

	// 1e. Bracket keys ] / [ step font size cleanly across wide typographic range (12..72px) and live-update selected text
	beforeFS := ov.Annotations()[len(ov.Annotations())-1].FontSize
	tt.Key(0, ui.KeyBracketRight)
	tt.Key(0, ui.KeyBracketRight)
	afterFS := ov.Annotations()[len(ov.Annotations())-1].FontSize
	if afterFS <= beforeFS+4 {
		t.Fatalf("expected ] key to enlarge selected text font size: before=%.1f after=%.1f", beforeFS, afterFS)
	}
	tt.Key(0, ui.KeyBracketLeft)
	steppedBack := ov.Annotations()[len(ov.Annotations())-1].FontSize
	if steppedBack >= afterFS {
		t.Fatalf("expected [ key to shrink selected text font size: before=%.1f after=%.1f", afterFS, steppedBack)
	}

	// 2. Delete removes the currently selected annotation
	tt.Key(0, ui.KeyDelete)
	if got := len(ov.Annotations()); got != initCount {
		t.Fatalf("expected Delete to remove selected text annotation: got %d, want %d", got, initCount)
	}

	// 2b. Counter tool: clicking existing counter drags it instead of stamping a duplicate!
	if err := tt.Click("序号"); err != nil {
		t.Fatalf("overlay click 序号 failed: %v", err)
	}
	beforeCounters := len(ov.Annotations())
	// Existing demo counter #1 is at (242, 84)
	tt.Press(242, 84)
	tt.Move(282, 114)
	tt.Release(282, 114)
	afterCounterAnns := ov.Annotations()
	if len(afterCounterAnns) != beforeCounters {
		t.Fatalf("dragging existing counter in ToolCounter should move it without creating duplicate: got %d want %d", len(afterCounterAnns), beforeCounters)
	}
	if afterCounterAnns[3].Start.X < 275 || afterCounterAnns[3].Start.Y < 108 {
		t.Fatalf("expected dragged counter to move to ~(282,114), got %+v", afterCounterAnns[3].Start)
	}
	// Reset selection back to none for selection box nudge test below
	tt.Click("序号")
	tt.ClickAt(500, 300)

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

func TestStudioTextAnnotationDragMove(t *testing.T) {
	a := New()
	ed := NewEditorState(a)
	ed.LoadImage(GenerateSampleCanvas(), "文本移动测试")
	tt := ui.NewTester(ed.View, 1220, 780)

	if err := tt.Click("文字"); err != nil {
		t.Fatalf("studio click 文字 failed: %v", err)
	}
	if ed.ActiveTool() != canvas.ToolText {
		t.Fatalf("expected ToolText active, got %v", ed.ActiveTool())
	}

	// Clicking canvas in Studio ToolText opens inline WYSIWYG text editor (matching overlay UX!)
	tt.ClickAt(600, 360)
	if !ed.TextEditing() {
		t.Fatalf("expected Studio inline text editor to open on canvas click with ToolText")
	}
	tt.Type("Studio 文字")
	tt.Key(0, ui.KeyEnter)
	if ed.TextEditing() {
		t.Fatalf("expected Enter to commit Studio inline text editor")
	}

	anns := ed.Annotations()
	if len(anns) != 1 || anns[0].Kind != canvas.ToolText || anns[0].Text != "Studio 文字" {
		t.Fatalf("expected 1 committed ToolText annotation, got %+v", anns)
	}
	origX, origY := anns[0].Start.X, anns[0].Start.Y

	// Dragging the placed text while STILL in ToolText mode moves it instead of opening a duplicate!
	tt.Press(620, 372)
	tt.Move(680, 412)
	tt.Release(680, 412)

	anns = ed.Annotations()
	if len(anns) != 1 || ed.TextEditing() {
		t.Fatalf("dragging existing text in Studio ToolText should move it cleanly: count=%d editing=%v", len(anns), ed.TextEditing())
	}
	if anns[0].Start.X <= origX+30 || anns[0].Start.Y <= origY+20 {
		t.Fatalf("expected Studio text annotation to move on drag: before=(%.1f,%.1f) after=(%.1f,%.1f)",
			origX, origY, anns[0].Start.X, anns[0].Start.Y)
	}

	// Arrow key nudges the selected text annotation by 1px, and ] steps font size up
	nudgedBaseX := anns[0].Start.X
	baseFS := anns[0].FontSize
	tt.Key(0, ui.KeyRight)
	tt.Key(0, ui.KeyBracketRight)
	anns = ed.Annotations()
	if anns[0].Start.X != nudgedBaseX+1 {
		t.Fatalf("expected Right arrow to nudge Studio text by +1px: before=%.1f after=%.1f", nudgedBaseX, anns[0].Start.X)
	}
	if anns[0].FontSize <= baseFS {
		t.Fatalf("expected ] key to enlarge Studio text font size: before=%.1f after=%.1f", baseFS, anns[0].FontSize)
	}
}

func TestAnnotationWCAGContrastInks(t *testing.T) {
	// Bright fills (Amber, Yellow, Emerald, White) MUST choose dark ink (#0F172A) for >= 7:1 contrast!
	brightFills := []ui.Color{
		ui.Hex("#FF9500"), // 琥珀
		ui.Hex("#FFCC00"), // 明黄
		ui.Hex("#34C759"), // 翡翠
		ui.Hex("#FFFFFF"), // 纯白
	}
	for _, bg := range brightFills {
		ink := canvas.ContrastInk(bg)
		if ink != ui.Hex("#0F172A") {
			t.Fatalf("bright fill %+v picked ink %+v, want dark ink #0F172A", bg, ink)
		}
		if cr := canvas.ContrastRatio(bg, ink); cr < 7.0 {
			t.Fatalf("contrast ratio for bright fill %+v = %.2f, want >= 7.0", bg, cr)
		}
	}
	// Rich/dark fills MUST choose white ink (#FFFFFF)
	richFills := []ui.Color{
		ui.Hex("#FF3B30"), // 绯红
		ui.Hex("#007AFF"), // 晴空
		ui.Hex("#AF52DE"), // 鸢尾
		ui.Hex("#1C1C1E"), // 石墨
	}
	for _, bg := range richFills {
		ink := canvas.ContrastInk(bg)
		if ink != ui.Hex("#FFFFFF") {
			t.Fatalf("rich fill %+v picked ink %+v, want white ink #FFFFFF", bg, ink)
		}
	}
	// Font size stepping covers full 12..72px range smoothly
	fs := canvas.MinFontSize
	steps := 0
	for fs < canvas.MaxFontSize && steps < 30 {
		next := canvas.StepFontSize(fs, 1)
		if next <= fs {
			t.Fatalf("StepFontSize(%v, +1) did not advance: got %v", fs, next)
		}
		fs = next
		steps++
	}
	if fs != canvas.MaxFontSize {
		t.Fatalf("expected StepFontSize to reach %v, ended at %v", canvas.MaxFontSize, fs)
	}
}

func TestHitTestTextInsideHollowRect(t *testing.T) {
	anns := []canvas.Annotation{
		{ID: 1, Kind: canvas.ToolText, Start: canvas.PointF{X: 200, Y: 200}, FontSize: 20, Text: "标注文字"},
		{ID: 2, Kind: canvas.ToolRect, Start: canvas.PointF{X: 100, Y: 100}, End: canvas.PointF{X: 400, Y: 350}, StrokeWidth: 3, Filled: false},
	}
	// Clicking text inside an unfilled rect drawn on top must still hit the text!
	if got := canvas.HitTestAnnotation(anns, 220, 212, 8); got != 0 {
		t.Fatalf("expected HitTestAnnotation inside hollow rect to hit text (0), got %d", got)
	}
	// Clicking the hollow rect's border still hits the rect!
	if got := canvas.HitTestAnnotation(anns, 100, 200, 8); got != 1 {
		t.Fatalf("expected HitTestAnnotation on hollow rect border to hit rect (1), got %d", got)
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
	if _, ok := tt.Find("关闭"); !ok {
		t.Fatalf("expected 关闭 button in OCR result window")
	}
	// Clicking inside the TextArea must remain editable even though the outer
	// ocr-card is a DragWindow handle (rune count badge increases from 20 to 21).
	if !tt.HasText("2 行 · 20 字") {
		t.Fatalf("expected initial OCR rune count badge '2 行 · 20 字', got texts: %q", tt.Texts())
	}
	tt.ClickAt(200, 140)
	tt.Type("!")
	if !tt.HasText("2 行 · 21 字") {
		t.Fatalf("expected OCR textarea to stay editable inside DragWindow card (21 chars), got texts: %q", tt.Texts())
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
