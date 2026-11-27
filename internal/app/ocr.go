package app

import (
	"fmt"
	"image"
	"strings"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"koma/internal/canvas"
	"koma/internal/glasswork"
	"koma/internal/icons"
	"koma/internal/ocr"
	"koma/internal/redact"
	"koma/internal/winapi"
)

// ocrResultWindow is a lightweight, transient Liquid Glass card presenting
// offline OCR results (PP-OCRv6 or enhanced Windows native OCR) with
// selectable/editable text, one-click engine switch, and one-click copy.
// Closing the window drops all buffers and triggers immediate memory reclaim.
type ocrResultWindow struct {
	mu      sync.Mutex
	app     *App
	win     *mygo.Window
	srcImg  *image.RGBA
	engine  ocr.Engine
	loading bool
	text    string
	errMsg  string
	lines   int
}

// RecognizeOCR runs Koma's configured offline OCR engine (PP-OCRv6 with automatic
// fallback to Windows native OCR) and returns lines plus per-word pixel bounding boxes.
func (a *App) RecognizeOCR(img *image.RGBA) (winapi.OCRResult, ocr.Engine, error) {
	if img == nil {
		return winapi.OCRResult{}, ocr.EngineSystem, fmt.Errorf("图像为空")
	}
	eng := a.OCREngine()
	if eng == ocr.EnginePPOCRv6 && !a.PPOCRInstalled() {
		eng = ocr.EngineSystem
	}
	if eng == ocr.EnginePPOCRv6 && a.ppocr != nil {
		a.ppocr.SetDir(a.OCRDataDir())
		res, err := a.ppocr.Recognize(img)
		if err == nil {
			return res, ocr.EnginePPOCRv6, nil
		}
		if sysRes, sysErr := winapi.RecognizeText(img); sysErr == nil {
			return sysRes, ocr.EngineSystem, nil
		}
		return winapi.OCRResult{}, ocr.EnginePPOCRv6, err
	}
	res, err := winapi.RecognizeText(img)
	return res, ocr.EngineSystem, err
}

// buildRedactionAnnotations detects sensitive regions inside words and converts
// them into standard, non-destructive Mosaic or Blur annotations shifted by (offsetX, offsetY).
// When srcImg is non-nil, each box is ink-tightened to crisp glyph bounds with clean padding.
func buildRedactionAnnotations(
	words []winapi.OCRWord,
	imgBounds image.Rectangle,
	srcImg *image.RGBA,
	offsetX, offsetY int,
	customKeywords []string,
	kind canvas.Tool,
	style canvas.MosaicStyle,
	blockSize int,
	nextID *int,
) []canvas.Annotation {
	if kind != canvas.ToolBlur {
		kind = canvas.ToolPixelate
	}
	findings := redact.Detect(words, imgBounds, customKeywords)
	if len(findings) == 0 {
		return nil
	}
	anns := make([]canvas.Annotation, 0, len(findings))
	for _, f := range findings {
		rc := image.Rect(
			f.Rect.Min.X+offsetX,
			f.Rect.Min.Y+offsetY,
			f.Rect.Max.X+offsetX,
			f.Rect.Max.Y+offsetY,
		)
		if srcImg != nil {
			tight := redact.TightenRectToInk(srcImg, rc)
			h := max(1, tight.Dy())
			padX := max(3, h/6)
			padY := max(2, h/8)
			rc = image.Rect(tight.Min.X-padX, tight.Min.Y-padY, tight.Max.X+padX, tight.Max.Y+padY).Intersect(srcImg.Bounds())
			if rc.Dx() < 4 || rc.Dy() < 4 {
				continue
			}
		}
		*nextID++
		anns = append(anns, canvas.Annotation{
			ID:          *nextID,
			Kind:        kind,
			Start:       canvas.PointF{X: float32(rc.Min.X), Y: float32(rc.Min.Y)},
			End:         canvas.PointF{X: float32(rc.Max.X), Y: float32(rc.Max.Y)},
			BlockSize:   blockSize,
			MosaicStyle: style,
		})
	}
	return anns
}

// makeWordRedactionAnnotation builds a tightly fitted Mosaic/Blur annotation
// around a single clicked OCR word (with comfortable glyph padding).
func makeWordRedactionAnnotation(
	word winapi.OCRWord,
	clipBounds image.Rectangle,
	kind canvas.Tool,
	style canvas.MosaicStyle,
	blockSize int,
	id int,
) (canvas.Annotation, bool) {
	if kind != canvas.ToolBlur {
		kind = canvas.ToolPixelate
	}
	h := max(1, word.Rect.Dy())
	padX := max(4, h/5)
	padY := max(2, h/8)
	rc := image.Rect(
		word.Rect.Min.X-padX,
		word.Rect.Min.Y-padY,
		word.Rect.Max.X+padX,
		word.Rect.Max.Y+padY,
	).Intersect(clipBounds)
	if rc.Dx() < 4 || rc.Dy() < 4 {
		return canvas.Annotation{}, false
	}
	return canvas.Annotation{
		ID:          id,
		Kind:        kind,
		Start:       canvas.PointF{X: float32(rc.Min.X), Y: float32(rc.Min.Y)},
		End:         canvas.PointF{X: float32(rc.Max.X), Y: float32(rc.Max.Y)},
		BlockSize:   blockSize,
		MosaicStyle: style,
	}, true
}

// hitRedactionAnnotation finds the top-most mosaic or blur annotation containing
// image pixel (px, py) with a tiny tolerance so clicking an existing redaction
// cleanly toggles it off without stealing clicks from adjacent words.
func hitRedactionAnnotation(anns []canvas.Annotation, px, py float32) int {
	const tol float32 = 2.0
	for i := len(anns) - 1; i >= 0; i-- {
		a := anns[i]
		if a.Kind != canvas.ToolPixelate && a.Kind != canvas.ToolBlur {
			continue
		}
		minX, minY, maxX, maxY := a.Bounds()
		if px >= minX-tol && px <= maxX+tol && py >= minY-tol && py <= maxY+tol {
			return i
		}
	}
	return -1
}

// drawRedactionRemoveHint paints a subtle crimson outline and corner close badge
// over a hovered mosaic/blur block to signal one-click removal.
func drawRedactionRemoveHint(pt *ui.Painter, r ui.Rect) {
	if r.W < 2 || r.H < 2 {
		return
	}
	rad := min(float32(10), min(r.W, r.H)*0.28)
	if rad < 3 {
		rad = 3
	}
	pt.Fill(r, ui.Hex("#FF3B30").Alpha(0.14), rad)
	pt.StrokeDashed(r, ui.Hex("#FF453A").Alpha(0.85), rad, 1.4)

	const badge float32 = 14
	bx := r.X + r.W - badge*0.55
	by := r.Y - badge*0.45
	br := ui.Rect{X: bx, Y: by, W: badge, H: badge}
	pt.Shadow(br, badge/2, 0, 1, 4, 0, ui.RGBA(0, 0, 0, 0.30))
	pt.Fill(br, ui.Hex("#FF3B30"), badge/2)
	cx, cy := bx+badge*0.5, by+badge*0.5
	const arm float32 = 3.0
	pt.Line(cx-arm, cy-arm, cx+arm, cy+arm, 1.5, ui.Hex("#FFFFFF"))
	pt.Line(cx+arm, cy-arm, cx-arm, cy+arm, 1.5, ui.Hex("#FFFFFF"))
}

// drawWordRedactHint paints the hover preview box for one-click word or Alt+whole-line redaction.
func drawWordRedactHint(pt *ui.Painter, r ui.Rect, wholeLine bool) {
	if r.W < 2 || r.H < 2 {
		return
	}
	rad := min(float32(8), min(r.W, r.H)*0.26)
	if rad < 3 {
		rad = 3
	}
	if wholeLine {
		pt.Fill(r, ui.Hex("#6366F1").Alpha(0.16), rad)
		pt.StrokeDashed(r, ui.Hex("#818CF8").Alpha(0.88), rad, 1.4)
		return
	}
	pt.Fill(r, ui.Hex("#0A84FF").Alpha(0.14), rad)
	pt.StrokeDashed(r, ui.Hex("#0A84FF").Alpha(0.78), rad, 1.25)
}

// RecognizeTextFromImage runs offline OCR (PP-OCRv6 when selected & installed,
// otherwise enhanced Windows native OCR) asynchronously and presents the result
// in a lightweight Liquid Glass card.
func (a *App) RecognizeTextFromImage(img *image.RGBA) {
	if img == nil {
		return
	}
	eng := a.OCREngine()
	if eng == ocr.EnginePPOCRv6 && !a.PPOCRInstalled() {
		eng = ocr.EngineSystem
	}
	ow := a.openOCRWindow()
	ow.runRecognition(img, eng)
}

func (o *ocrResultWindow) runRecognition(img *image.RGBA, eng ocr.Engine) {
	o.mu.Lock()
	o.srcImg = img
	o.engine = eng
	o.loading = true
	o.text = ""
	o.errMsg = ""
	o.lines = 0
	w := o.win
	o.mu.Unlock()
	if w != nil {
		w.Update(func() {})
	}

	go func() {
		var (
			res winapi.OCRResult
			err error
		)
		used := eng
		if eng == ocr.EnginePPOCRv6 {
			o.app.ppocr.SetDir(o.app.OCRDataDir())
			res, err = o.app.ppocr.Recognize(img)
			if err != nil {
				// Graceful fallback to Windows native OCR if PP-OCRv6 fails to load/infer.
				if sysRes, sysErr := winapi.RecognizeText(img); sysErr == nil {
					res = sysRes
					err = nil
					used = ocr.EngineSystem
				}
			}
		} else {
			res, err = winapi.RecognizeText(img)
		}

		o.mu.Lock()
		o.engine = used
		o.loading = false
		if err != nil {
			o.errMsg = err.Error()
		} else if strings.TrimSpace(res.Text) == "" {
			o.errMsg = "未在选区中识别到可提取的文字"
		} else {
			o.text = res.Text
			o.lines = len(res.Lines)
		}
		win := o.win
		o.mu.Unlock()
		if win != nil {
			win.Update(func() {})
		}
	}()
}

func (a *App) openOCRWindow() *ocrResultWindow {
	a.mu.Lock()
	if a.ocrWin != nil && a.ocrWin.win != nil {
		ow := a.ocrWin
		a.mu.Unlock()
		ow.win.Show()
		ow.win.Focus()
		return ow
	}
	a.mu.Unlock()

	ow := &ocrResultWindow{
		app:     a,
		loading: true,
	}

	if !mygo.App.IsReady() {
		a.mu.Lock()
		a.ocrWin = ow
		a.mu.Unlock()
		return ow
	}

	const (
		winW = 480
		winH = 380
	)
	work := mygo.Screen.PrimaryDisplay().WorkArea
	x := work.X + max(24, (work.Width-winW)/2)
	y := work.Y + max(24, (work.Height-winH)/2)

	w := mygo.NewWindow(mygo.WindowOptions{
		Title:           "Koma · 文字识别",
		X:               x,
		Y:               y,
		Width:           winW,
		Height:          winH,
		MinWidth:        360,
		MinHeight:       240,
		Frameless:       true,
		AlwaysOnTop:     true,
		SkipTaskbar:     true,
		DisableMinimize: true,
		DisableMaximize: true,
		CornerRadius:    12,
		BackgroundColor: "light-dark(#F7F9FC, #1B1E26)",
		Content:         ui.View(ow.view),
	})
	ow.win = w

	a.mu.Lock()
	a.ocrWin = ow
	a.mu.Unlock()

	w.OnClosed(func() {
		a.mu.Lock()
		if a.ocrWin == ow {
			a.ocrWin = nil
		}
		a.mu.Unlock()
		ow.mu.Lock()
		ow.srcImg = nil
		ow.text = ""
		ow.errMsg = ""
		ow.win = nil
		ow.mu.Unlock()
		reclaimMemoryAsync()
		a.scheduleTrayTrim()
	})
	w.Focus()
	return ow
}

func (o *ocrResultWindow) Close() {
	o.mu.Lock()
	w := o.win
	o.mu.Unlock()
	if w != nil {
		w.Close()
	}
}

func (o *ocrResultWindow) copyAllAndClose() {
	o.mu.Lock()
	txt := o.text
	o.mu.Unlock()
	if strings.TrimSpace(txt) == "" {
		return
	}
	mygo.Clipboard.WriteText(txt)
	o.app.NotifyToast(fmt.Sprintf("已复制 %d 字到剪贴板", len([]rune(txt))))
	o.Close()
}

func (o *ocrResultWindow) view(c *ui.Context) {
	dark := o.app.IsDarkMode()
	if dark {
		c.SetTheme(ui.DarkTheme())
	} else {
		c.SetTheme(ui.LightTheme())
	}

	if c.Shortcut(0, ui.KeyEscape) {
		o.Close()
		return
	}
	if c.Shortcut(ui.Ctrl, ui.KeyEnter) {
		o.copyAllAndClose()
		return
	}

	o.mu.Lock()
	loading := o.loading
	errMsg := o.errMsg
	lines := o.lines
	eng := o.engine
	srcImg := o.srcImg
	textLen := len([]rune(o.text))
	o.mu.Unlock()
	if eng == "" {
		eng = o.app.OCREngine()
	}

	kind := glasswork.KindLight
	cardBg := ui.Hex("#F7F9FC")
	textPrimary := ui.Hex("#0F172A")
	textSecondary := ui.Hex("#475569")
	fieldBg := ui.Hex("#FFFFFF")
	secBtnBg := ui.Hex("#FFFFFF")
	hairline := ui.RGBA(15, 23, 42, 0.11)
	if dark {
		kind = glasswork.KindDark
		cardBg = ui.Hex("#1B1E26")
		textPrimary = ui.Hex("#F8FAFC")
		textSecondary = ui.Hex("#CBD5E1")
		fieldBg = ui.Hex("#13161D")
		secBtnBg = ui.Hex("#252A35")
		hairline = ui.RGBA(255, 255, 255, 0.12)
	}
	c.Root().Background(cardBg)
	inks := kind.Inks()

	// Make the whole card surface a native DragWindow handle so dragging from the
	// top margin, header, side padding, or footer moves the popup smoothly.
	// Interactive children (buttons, TextArea) appear earlier in MyGo's hit chain
	// and keep full click/edit behavior. Never call Clicked/RightClicked on card.
	// Windows 11 clips this opaque frameless CornerRadius window natively with its
	// own rounded corners, border, and drop shadow, avoiding inner/outer corner gaps.
	card := ui.Column(c.Key("ocr-card")).
		Fill().Padding(16, 18, 16, 18).Gap(12).
		Background(cardBg).DragWindow()
	_ = card.WindowDoubleClicked()
	card.Children(func() {
		// Header bar
		ui.Row(c.Key("ocr-header")).AlignItems(ui.Center).Gap(8).Shrink(0).Children(func() {
			ui.Icon(c, icons.OCR.Idle).Size(19, 19).TextColor(inks.Accent).Shrink(0)
			ui.Text(c, "文字识别").FontSize(15).FontWeight(700).TextColor(textPrimary).SingleLine()

			// Interactive engine pill: switch engines immediately if PP-OCRv6 is installed,
			// or jump straight to Settings to download PP-OCRv6 on demand.
			ppInstalled := o.app.PPOCRInstalled()
			engTip := "点击前往「设置 → 文字识别」按需下载高精度 PP-OCRv6"
			if ppInstalled {
				if eng == ocr.EnginePPOCRv6 {
					engTip = "当前使用 PP-OCRv6，点击切换为系统原生重识别"
				} else {
					engTip = "当前使用系统原生，点击切换为 PP-OCRv6 重识别"
				}
			}
			engPill := ui.Row(c.Key("ocr-engine-pill")).
				Padding(2, 8, 2, 8).Radius(8).AlignItems(ui.Center).Shrink(0).
				Background(inks.Hover).Border(1, hairline).
				Role(ui.RoleButton).Label("切换识别引擎").
				Cursor(ui.CursorPointer).Tooltip(engTip)
			if eng == ocr.EnginePPOCRv6 {
				engPill.Background(inks.AccentFill).Border(1, inks.AccentBorder)
			}
			engCol := textSecondary
			if eng == ocr.EnginePPOCRv6 {
				engCol = inks.Accent
			}
			engPill.Children(func() {
				ui.Text(c, eng.DisplayName()).FontSize(11).FontWeight(600).TextColor(engCol).SingleLine()
			})
			if engPill.Clicked() && !loading {
				if ppInstalled && srcImg != nil {
					next := ocr.EnginePPOCRv6
					if eng == ocr.EnginePPOCRv6 {
						next = ocr.EngineSystem
					}
					o.app.SetOCREngine(next)
					o.runRecognition(srcImg, next)
				} else {
					o.app.ShowSettings()
				}
			}

			if !loading && errMsg == "" && textLen > 0 {
				ui.Box(c).Padding(2, 8, 2, 8).Radius(8).
					Background(inks.AccentFill).Border(1, inks.AccentBorder).Children(func() {
					ui.Textf(c, "%d 行 · %d 字", max(1, lines), textLen).
						FontSize(11).FontWeight(600).TextColor(inks.Accent).SingleLine()
				})
			}
			ui.Box(c).Grow(1)
			closeBtn := ui.Box(c.Key("ocr-close")).
				Size(26, 26).Center().Radius(13).
				Role(ui.RoleButton).Label("关闭").
				Cursor(ui.CursorPointer).Tooltip("关闭 (Esc)")
			if closeBtn.Hovered() {
				closeBtn.Background(inks.Hover)
			}
			glasswork.ApplyLiquidButton(closeBtn, glasswork.CompactButtonOpts())
			closeBtn.Children(func() {
				ui.Icon(c, icons.Close.For(closeBtn.Hovered() || closeBtn.Pressed())).Size(15, 15).TextColor(textSecondary)
			})
			if closeBtn.Clicked() {
				o.Close()
			}
		})

		// Body
		if loading {
			ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
				ui.Icon(c, icons.OCR.Idle).Size(28, 28).TextColor(inks.Accent)
				ui.Text(c, "正在识别文字…").FontSize(13).FontWeight(600).TextColor(textSecondary)
			})
		} else if errMsg != "" {
			ui.Column(c).Grow(1).Center().Padding(12).Gap(8).Children(func() {
				ui.Text(c, errMsg).FontSize(13).FontWeight(500).TextColor(textSecondary)
			})
		} else {
			ui.TextArea(c.Key("ocr-textarea"), &o.text).
				Grow(1).Padding(10, 12, 10, 12).Radius(12).
				Background(fieldBg).Border(1, hairline).
				FontSize(13).TextColor(textPrimary)
		}

		// Footer actions
		ui.Row(c.Key("ocr-footer")).AlignItems(ui.Center).Justify(ui.End).Gap(8).Shrink(0).Children(func() {
			if !loading && errMsg == "" && textLen > 0 {
				copyOnly := ui.Row(c.Key("ocr-copy-keep")).
					Height(32).Padding(0, 12, 0, 10).Gap(5).Radius(16).
					AlignItems(ui.Center).Role(ui.RoleButton).Label("仅复制").
					Background(secBtnBg).Border(1, hairline).
					Cursor(ui.CursorPointer).Tooltip("复制全部文本并保持窗口打开")
				glasswork.ApplyLiquidButton(copyOnly, glasswork.CompactButtonOpts())
				copyOnly.Children(func() {
					ui.Icon(c, icons.Copy.Idle).Size(15, 15).TextColor(textPrimary)
					ui.Text(c, "复制").FontSize(12).FontWeight(600).TextColor(textPrimary).SingleLine()
				})
				if copyOnly.Clicked() {
					o.mu.Lock()
					txt := o.text
					o.mu.Unlock()
					mygo.Clipboard.WriteText(txt)
					o.app.NotifyToast(fmt.Sprintf("已复制 %d 字到剪贴板", len([]rune(txt))))
				}

				copyClose := ui.Row(c.Key("ocr-copy-close")).
					Height(32).Padding(0, 14, 0, 12).Gap(5).Radius(16).
					AlignItems(ui.Center).Role(ui.RoleButton).Label("复制并关闭").
					Material(glasswork.Tinted(ui.Hex("#0A84FF"))).
					Cursor(ui.CursorPointer).Tooltip("复制全部文本并关闭 (Ctrl+Enter)")
				glasswork.ApplyLiquidButton(copyClose, glasswork.PrimaryButtonOpts())
				copyClose.Children(func() {
					ui.Icon(c, icons.Check.Active).Size(15, 15).TextColor(ui.Hex("#FFFFFF")).Shrink(0)
					ui.Text(c, "复制并关闭").FontSize(12).FontWeight(600).TextColor(ui.Hex("#FFFFFF")).SingleLine().Shrink(0)
				})
				if copyClose.Clicked() {
					o.copyAllAndClose()
				}
			}
		})
	})
}

// NewTestOCRView builds a headless-testable OCR result view for automated UI testing.
func NewTestOCRView(a *App, text string, lines int) func(*ui.Context) {
	ow := &ocrResultWindow{
		app:     a,
		engine:  a.OCREngine(),
		loading: false,
		text:    text,
		lines:   lines,
	}
	return ow.view
}
