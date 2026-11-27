package main

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"koma/internal/app"
	"koma/internal/canvas"
	"koma/internal/icons"
)

func TestFullscreenToolbarPlacementAlwaysVisible(t *testing.T) {
	cases := []struct {
		name           string
		x0, y0, x1, y1 float32
		viewW, viewH   float32
		showSub        bool
	}{
		{"normal_mid", 140, 70, 740, 410, 860, 520, false},
		{"normal_mid_with_sub", 140, 70, 740, 410, 860, 520, true},
		{"near_fullscreen_no_sub", 2, 2, 1918, 1078, 1920, 1080, false},
		{"near_fullscreen_with_sub", 2, 2, 1918, 1078, 1920, 1080, true},
		{"exact_fullscreen_no_sub", 0, 0, 1920, 1080, 1920, 1080, false},
		{"exact_fullscreen_with_sub", 0, 0, 1920, 1080, 1920, 1080, true},
		{"top_edge_selection", 100, 0, 700, 500, 860, 520, false},
	}

	const mainW, subW float32 = 740, 440
	for _, tc := range cases {
		barX, barY, subX, subY, _ := app.ComputeToolbarLayout(tc.x0, tc.y0, tc.x1, tc.y1, tc.viewW, tc.viewH, mainW, subW, tc.showSub)
		if barX < 16 || barX+mainW > tc.viewW-15 {
			t.Errorf("%s: main bar X=[%.1f, %.1f] outside viewport [16, %.1f]", tc.name, barX, barX+mainW, tc.viewW-16)
		}
		if barY < 16 || barY+44 > tc.viewH-15 {
			t.Errorf("%s: barY=%.1f (bottom=%.1f) outside viewport [16, %.1f]", tc.name, barY, barY+44, tc.viewH-16)
		}
		if tc.showSub {
			if subX < 16 || subX+subW > tc.viewW-15 {
				t.Errorf("%s: sub bar X=[%.1f, %.1f] outside viewport [16, %.1f]", tc.name, subX, subX+subW, tc.viewW-16)
			}
			if subY < 16 || subY+40 > tc.viewH-15 {
				t.Errorf("%s: subY=%.1f (bottom=%.1f) outside viewport [16, %.1f]", tc.name, subY, subY+40, tc.viewH-16)
			}
		}
	}
}

func TestScrollOverlayPlacementAlwaysVisible(t *testing.T) {
	cases := []struct {
		name           string
		x0, y0, x1, y1 float32
		viewW, viewH   float32
	}{
		{"mid_screen", 140, 72, 720, 532, 1120, 680},
		{"right_edge", 520, 72, 1100, 532, 1120, 680},
		{"left_edge", 12, 72, 620, 532, 1120, 680},
		{"near_fullscreen", 4, 4, 1916, 1076, 1920, 1080},
		{"exact_fullscreen", 0, 0, 1920, 1080, 1920, 1080},
	}

	const barW float32 = 376
	for _, tc := range cases {
		barR, prevR, showPrev := app.ComputeScrollOverlayLayout(tc.x0, tc.y0, tc.x1, tc.y1, tc.viewW, tc.viewH, barW)
		if barR.X < 16 || barR.X+barR.W > tc.viewW-15 {
			t.Errorf("%s: bar X=[%.1f, %.1f] outside [16, %.1f]", tc.name, barR.X, barR.X+barR.W, tc.viewW-16)
		}
		if barR.Y < 16 || barR.Y+barR.H > tc.viewH-15 {
			t.Errorf("%s: bar Y=[%.1f, %.1f] outside [16, %.1f]", tc.name, barR.Y, barR.Y+barR.H, tc.viewH-16)
		}
		if !showPrev {
			t.Errorf("%s: expected live preview card to be visible", tc.name)
			continue
		}
		if prevR.X < 16 || prevR.X+prevR.W > tc.viewW-15 {
			t.Errorf("%s: preview X=[%.1f, %.1f] outside [16, %.1f]", tc.name, prevR.X, prevR.X+prevR.W, tc.viewW-16)
		}
		if prevR.Y < 16 || prevR.Y+prevR.H > tc.viewH-15 {
			t.Errorf("%s: preview Y=[%.1f, %.1f] outside [16, %.1f]", tc.name, prevR.Y, prevR.Y+prevR.H, tc.viewH-16)
		}
		overlapX := prevR.X < barR.X+barR.W && prevR.X+prevR.W > barR.X
		overlapY := prevR.Y < barR.Y+barR.H && prevR.Y+prevR.H > barR.Y
		if overlapX && overlapY {
			t.Errorf("%s: preview %+v overlaps control bar %+v", tc.name, prevR, barR)
		}
	}
}

func TestKomaGlassworkAndToolbarClicks(t *testing.T) {
	dumpDir := os.Getenv("KOMA_PREVIEW_DIR")

	a := app.New()
	ed := app.NewEditorState(a)

	// 1. Minimalist High-Contrast Liquid Glass Home Stage
	ttHome := ui.NewTester(ed.View, 1220, 780)
	if !ttHome.HasText("区域截图") {
		t.Fatalf("expected primary Chinese capture CTA, got texts: %q", ttHome.Texts())
	}
	if !ttHome.HasText("长截图") {
		t.Fatalf("expected 长截图 pill on Home stage, got texts: %q", ttHome.Texts())
	}
	savePreview(t, dumpDir, "v3_01_home_glasswork.png", ttHome.Image())

	// 2. QQ-style In-Place Overlay: verify EVERY toolbar button click actually triggers!
	sample := app.GenerateSampleCanvas()
	ov := app.NewTestOverlayView(a, sample, canvas.ToolSelect)
	ttOv := ui.NewTester(ov.View, 860, 520)

	if _, ok := ttOv.Find("长截图"); !ok {
		t.Fatalf("expected 长截图 button on QQ overlay toolbar")
	}
	if _, ok := ttOv.Find("提取文字"); !ok {
		t.Fatalf("expected 提取文字 (OCR) button on QQ overlay toolbar")
	}
	if _, ok := ttOv.Find("编辑"); !ok {
		t.Fatalf("expected 编辑 button on QQ overlay toolbar")
	}

	if err := ttOv.Click("箭头"); err != nil {
		t.Fatalf("overlay click 箭头 failed: %v", err)
	}
	if !ov.HasSelection() || ov.ActiveTool() != canvas.ToolArrow {
		t.Fatalf("after clicking 箭头: hasSelection=%v tool=%v, want true / ToolArrow", ov.HasSelection(), ov.ActiveTool())
	}
	savePreview(t, dumpDir, "v3_02_qq_overlay_glasswork.png", ttOv.Image())

	if err := ttOv.Click("琥珀"); err != nil {
		t.Fatalf("overlay click color 琥珀 failed: %v", err)
	}
	if !ov.HasSelection() || ov.ColorIdx() != 1 {
		t.Fatalf("after clicking 琥珀: hasSelection=%v colorIdx=%d, want true / 1", ov.HasSelection(), ov.ColorIdx())
	}

	if err := ttOv.Click("马赛克"); err != nil {
		t.Fatalf("overlay click 马赛克 failed: %v", err)
	}
	if !ov.HasSelection() || ov.ActiveTool() != canvas.ToolPixelate {
		t.Fatalf("after clicking 马赛克: hasSelection=%v tool=%v, want true / ToolPixelate", ov.HasSelection(), ov.ActiveTool())
	}

	// Verify 100% fullscreen selection keeps the ENTIRE toolbar (including rightmost 完成) strictly inside viewport!
	ov.SetSelectionBounds(0, 0, 860, 520)
	ttFull := ui.NewTester(ov.View, 860, 520)
	if err := ttFull.Click("矩形"); err != nil {
		t.Fatalf("fullscreen overlay click 矩形 failed: %v", err)
	}
	if !ov.HasSelection() || ov.ActiveTool() != canvas.ToolRect {
		t.Fatalf("after fullscreen clicking 矩形: hasSelection=%v tool=%v", ov.HasSelection(), ov.ActiveTool())
	}
	savePreview(t, dumpDir, "v3_04_qq_fullscreen_overlay.png", ttFull.Image())

	// Clicking active tool again toggles back to ToolSelect (classic QQ截图 behavior)
	if err := ttFull.Click("矩形"); err != nil {
		t.Fatalf("overlay re-click 矩形 failed: %v", err)
	}
	if !ov.HasSelection() || ov.ActiveTool() != canvas.ToolSelect {
		t.Fatalf("after toggling 矩形 off: hasSelection=%v tool=%v, want true / ToolSelect", ov.HasSelection(), ov.ActiveTool())
	}

	// 3. Studio Editor: verify bottom dock clicks all trigger cleanly
	a.AddHistoryEntry(sample, "示例画布")
	ed.LoadImage(sample, "示例画布")
	ed.AddDemoAnnotations()

	ttStudio := ui.NewTester(ed.View, 1220, 780)
	if err := ttStudio.Click("马赛克"); err != nil {
		t.Fatalf("studio dock click 马赛克 failed: %v", err)
	}
	if ed.ActiveTool() != canvas.ToolPixelate {
		t.Fatalf("studio tool after clicking 马赛克 = %v, want ToolPixelate", ed.ActiveTool())
	}

	if err := ttStudio.Click("测距"); err != nil {
		t.Fatalf("studio dock click 测距 failed: %v", err)
	}
	if ed.ActiveTool() != canvas.ToolMeasure {
		t.Fatalf("studio tool after clicking 测距 = %v, want ToolMeasure", ed.ActiveTool())
	}
	savePreview(t, dumpDir, "v3_03_studio_glasswork_beautify.png", ttStudio.Image())

	// 4. Dark-mode Studio & Light-background QQ Overlay previews for contrast verification
	if dumpDir != "" {
		a.ToggleDarkMode()
		ttStudioDark := ui.NewTester(ed.View, 1220, 780)
		savePreview(t, dumpDir, "v3_05_studio_dark_glasswork.png", ttStudioDark.Image())
		a.ToggleDarkMode()

		brightBg := image.NewRGBA(image.Rect(0, 0, 860, 520))
		for i := 0; i < len(brightBg.Pix); i += 4 {
			brightBg.Pix[i] = 242
			brightBg.Pix[i+1] = 245
			brightBg.Pix[i+2] = 250
			brightBg.Pix[i+3] = 255
		}
		ovLight := app.NewTestOverlayView(a, brightBg, canvas.ToolRect)
		ttOvLight := ui.NewTester(ovLight.View, 860, 520)
		savePreview(t, dumpDir, "v3_06_qq_overlay_light_bg.png", ttOvLight.Image())
	}

	// 5. Settings sheet: open via corner gear button on Studio AND Home, toggle launch at startup,
	// record & clear hotkeys, and reset defaults!
	if err := ttStudio.Click("设置"); err != nil {
		t.Fatalf("studio click 设置 button failed: %v", err)
	}
	if !ed.SettingsOpen() {
		t.Fatalf("expected settings sheet to open after clicking 设置 on studio")
	}
	ed.CloseSettings()

	ed.ClearDocument()
	ttSettings := ui.NewTester(ed.View, 1220, 780)
	if err := ttSettings.Click("设置"); err != nil {
		t.Fatalf("home click 设置 button failed: %v", err)
	}
	if !ed.SettingsOpen() {
		t.Fatalf("expected settings sheet to open after clicking 设置 on home")
	}
	rowBeforeToast, ok := ttSettings.Find("开机自启")
	if !ok {
		t.Fatalf("expected 开机自启 row in settings sheet")
	}
	if err := ttSettings.Click("开机自启"); err != nil {
		t.Fatalf("click 开机自启 switch failed: %v", err)
	}
	if !a.OpenAtLogin() {
		t.Fatalf("expected OpenAtLogin() == true after toggling switch")
	}
	// Verify popping up Toast does NOT shift the Settings sheet or scrim vertically!
	ttSettings.Frame()
	rowWithToast, ok := ttSettings.Find("开机自启")
	if !ok {
		t.Fatalf("expected 开机自启 row while toast visible")
	}
	if diff := rowWithToast.Y - rowBeforeToast.Y; diff < -1 || diff > 1 {
		t.Fatalf("toast shifted settings sheet vertically: before=%.1f withToast=%.1f", rowBeforeToast.Y, rowWithToast.Y)
	}
	toastRect, ok := ttSettings.Find("已开启开机自启（后台静默驻留托盘）")
	if !ok || toastRect.Y < 40 || toastRect.Y > 120 {
		t.Fatalf("expected floating toast near top (Y~64), got ok=%v rect=%+v", ok, toastRect)
	}
	// Record Alt+Q for 区域截图
	if err := ttSettings.Click("修改区域截图快捷键"); err != nil {
		t.Fatalf("click 修改区域截图快捷键 failed: %v", err)
	}
	if ed.RecordingHotkey() != app.HotkeyArea {
		t.Fatalf("expected RecordingHotkey == HotkeyArea, got %q", ed.RecordingHotkey())
	}
	ttSettings.Key(ui.Alt, ui.KeyQ)
	if got := a.Hotkey(app.HotkeyArea); got != "Alt+Q" {
		t.Fatalf("after recording Alt+Q, HotkeyArea = %q, want Alt+Q", got)
	}
	// Cancel 全屏截图 hotkey via one-click clear button
	if err := ttSettings.Click("取消全屏截图快捷键"); err != nil {
		t.Fatalf("click 取消全屏截图快捷键 failed: %v", err)
	}
	if got := a.Hotkey(app.HotkeyFull); got != "" {
		t.Fatalf("after canceling 全屏截图 hotkey, got %q, want empty", got)
	}
	// Capture preview while the confirmation Toast is floating above Settings
	ttSettings.Advance(450 * time.Millisecond)
	savePreview(t, dumpDir, "v3_07_settings_light.png", ttSettings.Image())

	if dumpDir != "" {
		// Dump Settings with Liquid Glass Select open (both mid-stretch neck & settled open) in Light & Dark modes
		_ = ttSettings.Click("选择外观")
		ttSettings.Advance(45 * time.Millisecond)
		savePreview(t, dumpDir, "v3_07b_settings_select_stretch_light.png", ttSettings.Image())
		ttSettings.Advance(450 * time.Millisecond)
		savePreview(t, dumpDir, "v3_07c_settings_select_open_light.png", ttSettings.Image())
		ttSettings.Key(0, ui.KeyEscape)
		ttSettings.Advance(450 * time.Millisecond)

		a.SetDarkMode(true)
		ttSettingsDark := ui.NewTester(ed.View, 1220, 780)
		savePreview(t, dumpDir, "v3_08_settings_dark.png", ttSettingsDark.Image())
		_ = ttSettingsDark.Click("选择识别引擎")
		ttSettingsDark.Advance(450 * time.Millisecond)
		savePreview(t, dumpDir, "v3_08b_settings_select_open_dark.png", ttSettingsDark.Image())
		ttSettingsDark.Key(0, ui.KeyEscape)
		ttSettingsDark.Advance(450 * time.Millisecond)
		a.SetDarkMode(false)
	}

	// Reset defaults restores all shortcuts
	if err := ttSettings.Click("恢复默认"); err != nil {
		t.Fatalf("click 恢复默认 failed: %v", err)
	}
	if a.Hotkey(app.HotkeyArea) != app.DefaultHotkeyArea || a.Hotkey(app.HotkeyFull) != app.DefaultHotkeyFull {
		t.Fatalf("expected default hotkeys after 恢复默认, got area=%q full=%q", a.Hotkey(app.HotkeyArea), a.Hotkey(app.HotkeyFull))
	}

	// Verify software update controls inside Settings sheet
	updateChecks := 0
	a.SetTestUpdateState("", func() { updateChecks++ })
	if err := ttSettings.Click("检查更新"); err != nil {
		t.Fatalf("click 检查更新 failed: %v", err)
	}
	if updateChecks != 1 {
		t.Fatalf("expected CheckForUpdates count = 1, got %d", updateChecks)
	}
	ed.CloseSettings()

	// 6. Live Scrolling Capture Overlay: verify live preview stitching, auto-scroll toggle, and visual layout
	scrollFrames := generateTestScrollFrames(580, 460)
	sc := app.NewTestScrollView(a, scrollFrames)
	if sc.StitchedHeight() <= 460 {
		t.Fatalf("expected stitched height > 460 after feeding scrolled frames, got %d", sc.StitchedHeight())
	}
	ttScroll := ui.NewTester(sc.View, 1120, 680)
	if !ttScroll.HasText("自动滚动") || !ttScroll.HasText("完成") {
		t.Fatalf("expected 自动滚动 & 完成 on scroll control bar, got texts: %q", ttScroll.Texts())
	}
	if err := ttScroll.Click("自动滚动"); err != nil {
		t.Fatalf("scroll overlay click 自动滚动 failed: %v", err)
	}
	if !sc.AutoScrolling() {
		t.Fatalf("expected AutoScrolling() == true after clicking 自动滚动")
	}
	savePreview(t, dumpDir, "v3_09_scroll_overlay.png", ttScroll.Image())
	// Capture area interior MUST remain fully transparent (A==0) so the live desktop shows through!
	if px := ttScroll.Image().RGBAAt(400, 300); px.A != 0 {
		t.Fatalf("expected transparent capture interior (A=0), got %+v", px)
	}
	if err := ttScroll.Click("暂停滚动"); err != nil {
		t.Fatalf("scroll overlay click 暂停滚动 failed: %v", err)
	}
	if sc.AutoScrolling() {
		t.Fatalf("expected AutoScrolling() == false after clicking 暂停")
	}
	if err := ttScroll.Click("取消"); err != nil {
		t.Fatalf("scroll overlay click 取消 failed: %v", err)
	}
	if !sc.Closed() {
		t.Fatalf("expected scroll session Closed() == true after clicking 取消")
	}

	// 7. Native OCR Result Card Preview
	ocrView := app.NewTestOCRView(a, "Koma — 一帧清光，恰如其分。\nFrame the moment, effortlessly.", 2)
	ttOCR := ui.NewTester(ocrView, 480, 380)
	if !ttOCR.HasText("文字识别") || !ttOCR.HasText("复制并关闭") {
		t.Fatalf("expected OCR result view controls, got texts: %q", ttOCR.Texts())
	}
	savePreview(t, dumpDir, "v3_10_ocr_result.png", ttOCR.Image())

	if os.Getenv("KOMA_GEN_ICON") == "1" {
		if err := os.WriteFile(filepath.Join("resources", "icon.png"), icons.AppIconPNG(512), 0o644); err != nil {
			t.Fatalf("write icon.png: %v", err)
		}
	}
}

func generateTestScrollFrames(w, h int) []*image.RGBA {
	const totalH = 1280
	page := image.NewRGBA(image.Rect(0, 0, w, totalH))
	for y := 0; y < totalH; y++ {
		cardIdx := y / 100
		localY := y % 100
		for x := 0; x < w; x++ {
			c := color.RGBA{R: 242, G: 245, B: 252, A: 255}
			if x >= 18 && x < w-18 && localY >= 10 && localY < 88 {
				c = color.RGBA{R: 255, G: 255, B: 255, A: 255}
				if localY < 32 {
					c = color.RGBA{
						R: uint8(35 + (cardIdx*47)%170),
						G: uint8(95 + (cardIdx*73)%130),
						B: uint8(190 + (cardIdx*29)%60),
						A: 255,
					}
				} else if localY%16 >= 4 && localY%16 <= 9 && x > 36 && x < w-50-((cardIdx*37+localY*11)%140) {
					if ((x/6 + cardIdx*13 + localY*7) % 5) != 0 {
						c = color.RGBA{R: 30, G: 41, B: 59, A: 255}
					}
				}
			}
			page.SetRGBA(x, y, c)
		}
	}
	offsets := []int{0, 180, 380, 580, 780}
	frames := make([]*image.RGBA, 0, len(offsets))
	for _, off := range offsets {
		f := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			copy(f.Pix[y*f.Stride:(y+1)*f.Stride], page.Pix[(off+y)*page.Stride:(off+y)*page.Stride+w*4])
		}
		frames = append(frames, f)
	}
	return frames
}

func savePreview(t *testing.T, dir, name string, img *image.RGBA) {
	t.Helper()
	if dir == "" || img == nil {
		return
	}
	_ = os.MkdirAll(dir, 0o755)
	b, err := canvas.EncodePNG(img)
	if err != nil {
		t.Fatalf("encode %s: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
