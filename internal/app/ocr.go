package app

import (
	"fmt"
	"image"
	"strings"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"koma/internal/glasswork"
	"koma/internal/icons"
	"koma/internal/winapi"
)

// ocrResultWindow is a lightweight, transient Liquid Glass card presenting
// offline Windows OCR results with selectable/editable text and one-click copy.
// Closing the window drops all buffers and triggers immediate memory reclaim.
type ocrResultWindow struct {
	mu      sync.Mutex
	app     *App
	win     *mygo.Window
	loading bool
	text    string
	errMsg  string
	lines   int
}

// RecognizeTextFromImage runs Windows native offline OCR on img asynchronously
// and presents the recognized text in a lightweight Liquid Glass result card.
func (a *App) RecognizeTextFromImage(img *image.RGBA) {
	if img == nil {
		return
	}

	ow := a.openOCRWindow()
	ow.mu.Lock()
	ow.loading = true
	ow.text = ""
	ow.errMsg = ""
	ow.lines = 0
	w := ow.win
	ow.mu.Unlock()
	if w != nil {
		w.Update(func() {})
	}

	go func() {
		res, err := winapi.RecognizeText(img)
		ow.mu.Lock()
		ow.loading = false
		if err != nil {
			ow.errMsg = err.Error()
		} else if strings.TrimSpace(res.Text) == "" {
			ow.errMsg = "未在选区中识别到可提取的文字"
		} else {
			ow.text = res.Text
			ow.lines = len(res.Lines)
		}
		win := ow.win
		ow.mu.Unlock()
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
		Title:         "Koma · 文字识别",
		X:             x,
		Y:             y,
		Width:         winW,
		Height:        winH,
		MinWidth:      360,
		MinHeight:     240,
		Frameless:     true,
		Transparent:   true,
		AlwaysOnTop:   true,
		SkipTaskbar:   true,
		DisableShadow: true,
		Content:       ui.View(ow.view),
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
	c.Root().Background(ui.Transparent)

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
	textLen := len([]rune(o.text))
	o.mu.Unlock()

	kind := glasswork.KindLight
	textPrimary := ui.Hex("#0F172A")
	textSecondary := ui.Hex("#475569")
	fieldBg := ui.RGBA(255, 255, 255, 0.78)
	secBtnBg := ui.RGBA(255, 255, 255, 0.68)
	hairline := ui.RGBA(15, 23, 42, 0.12)
	if dark {
		kind = glasswork.KindDark
		textPrimary = ui.Hex("#F8FAFC")
		textSecondary = ui.Hex("#CBD5E1")
		fieldBg = ui.RGBA(15, 23, 42, 0.56)
		secBtnBg = ui.RGBA(255, 255, 255, 0.12)
		hairline = ui.RGBA(255, 255, 255, 0.14)
	}
	inks := kind.Inks()

	ui.Box(c).Fill().Padding(10).Children(func() {
		card := ui.Column(c.Key("ocr-card")).
			Fill().Padding(18).Gap(12).Radius(24).
			Material(glasswork.Card(kind))
		card.Children(func() {
			// Draggable header bar
			ui.Row(c).AlignItems(ui.Center).Gap(8).DragWindow().Children(func() {
				ui.Icon(c, icons.OCR.Idle).Size(19, 19).TextColor(inks.Accent).Shrink(0)
				ui.Text(c, "文字识别").FontSize(15).FontWeight(700).TextColor(textPrimary).SingleLine()
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
					Grow(1).Fill().Padding(10, 12, 10, 12).Radius(14).
					Background(fieldBg).Border(1, hairline).
					FontSize(13).TextColor(textPrimary)
			}

			// Footer actions
			ui.Row(c).AlignItems(ui.Center).Justify(ui.End).Gap(8).Children(func() {
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
						ui.Icon(c, icons.Check.Active).Size(15, 15).TextColor(ui.Hex("#FFFFFF"))
						ui.Text(c, "复制并关闭").FontSize(12).FontWeight(600).TextColor(ui.Hex("#FFFFFF")).SingleLine()
					})
					if copyClose.Clicked() {
						o.copyAllAndClose()
					}
				}
			})
		})
	})
}

// NewTestOCRView builds a headless-testable OCR result view for automated UI testing.
func NewTestOCRView(a *App, text string, lines int) func(*ui.Context) {
	ow := &ocrResultWindow{
		app:     a,
		loading: false,
		text:    text,
		lines:   lines,
	}
	return ow.view
}
