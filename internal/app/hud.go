package app

import (
	"fmt"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"koma/internal/glasswork"
	"koma/internal/icons"
)

// globalToastHUD manages a transient, non-activating, click-through Liquid Glass
// toast window shown when Studio is closed to the tray so actions like color
// picking, copying, quick-saving, and OCR always give crisp visual feedback.
type globalToastHUD struct {
	mu    sync.Mutex
	app   *App
	win   *mygo.Window
	msg   string
	timer *time.Timer
}

func newGlobalToastHUD(a *App) *globalToastHUD {
	return &globalToastHUD{app: a}
}

func (h *globalToastHUD) Show(msg string) {
	if msg == "" || !mygo.App.IsReady() {
		return
	}
	const (
		winW    = 380
		winH    = 68
		showDur = 2100 * time.Millisecond
	)

	h.mu.Lock()
	h.msg = msg
	w := h.win
	if h.timer != nil {
		h.timer.Stop()
	}
	h.timer = time.AfterFunc(showDur, func() {
		h.Close()
	})
	h.mu.Unlock()

	if w != nil {
		w.Update(func() {})
		return
	}

	work := mygo.Screen.PrimaryDisplay().WorkArea
	x := work.X + max(16, (work.Width-winW)/2)
	y := work.Y + max(24, work.Height-winH-28)

	nw := mygo.NewWindow(mygo.WindowOptions{
		Title:             "Koma 提示",
		X:                 x,
		Y:                 y,
		Width:             winW,
		Height:            winH,
		Frameless:         true,
		Transparent:       true,
		AlwaysOnTop:       true,
		SkipTaskbar:       true,
		DisableShadow:     true,
		DisableResize:     true,
		DisableMove:       true,
		IgnoreMouseEvents: true,
		Hidden:            true,
		Content:           ui.View(h.view),
	})
	nw.SetContentProtection(true)
	nw.OnClosed(func() {
		h.mu.Lock()
		if h.win == nw {
			h.win = nil
		}
		if h.timer != nil {
			h.timer.Stop()
			h.timer = nil
		}
		h.mu.Unlock()
		reclaimMemoryAsync()
		h.app.scheduleTrayTrim()
	})

	h.mu.Lock()
	h.win = nw
	h.mu.Unlock()
	nw.ShowInactive()
}

func (h *globalToastHUD) Close() {
	h.mu.Lock()
	w := h.win
	h.win = nil
	if h.timer != nil {
		h.timer.Stop()
		h.timer = nil
	}
	h.mu.Unlock()
	if w != nil {
		w.Close()
	}
}

func (h *globalToastHUD) Active() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.win != nil
}

func (h *globalToastHUD) view(c *ui.Context) {
	dark := h.app.IsDarkMode()
	if dark {
		c.SetTheme(ui.DarkTheme())
	} else {
		c.SetTheme(ui.LightTheme())
	}
	// Transparent windows MUST clear root background immediately after SetTheme.
	c.Root().Background(ui.Transparent)

	h.mu.Lock()
	msg := h.msg
	h.mu.Unlock()

	surf := glasswork.Dark()
	fg := ui.Hex("#FFFFFF")
	if !dark {
		surf = glasswork.Light()
		fg = ui.Hex("#0F172A")
	}

	ui.Box(c).Fill().Center().Padding(8).Children(func() {
		pill := ui.Row(c.Key("global-toast-pill")).
			Height(38).Padding(0, 16, 0, 14).Gap(8).
			Radius(19).Material(surf).AlignItems(ui.Center).
			Transition(glasswork.ToolbarAppearTransition(8))
		pill.Children(func() {
			ui.Icon(c, icons.Check.Active).Size(15, 15).TextColor(ui.Hex("#34C759")).Shrink(0)
			ui.Text(c, msg).FontSize(12.5).FontWeight(600).TextColor(fg).SingleLine()
		})
	})
}

// delayCountdownHUD displays a compact floating Liquid Glass countdown capsule
// on screen during delayed capture even when Studio is closed in the tray.
type delayCountdownHUD struct {
	mu       sync.Mutex
	app      *App
	win      *mygo.Window
	seconds  int
	onCancel func()
}

func newDelayCountdownHUD(a *App) *delayCountdownHUD {
	return &delayCountdownHUD{app: a}
}

func (d *delayCountdownHUD) Show(seconds int, onCancel func()) {
	if !mygo.App.IsReady() {
		return
	}
	const (
		winW = 196
		winH = 66
	)
	d.mu.Lock()
	d.seconds = seconds
	d.onCancel = onCancel
	w := d.win
	d.mu.Unlock()

	if w != nil {
		w.Update(func() {})
		return
	}

	work := mygo.Screen.PrimaryDisplay().WorkArea
	x := work.X + max(16, work.Width-winW-24)
	y := work.Y + 24

	nw := mygo.NewWindow(mygo.WindowOptions{
		Title:         "Koma 延时截屏",
		X:             x,
		Y:             y,
		Width:         winW,
		Height:        winH,
		Frameless:     true,
		Transparent:   true,
		AlwaysOnTop:   true,
		SkipTaskbar:   true,
		DisableShadow: true,
		DisableResize: true,
		DisableMove:   true,
		Hidden:        true,
		Content:       ui.View(d.view),
	})
	nw.SetContentProtection(true)
	nw.OnClosed(func() {
		d.mu.Lock()
		if d.win == nw {
			d.win = nil
		}
		d.mu.Unlock()
		reclaimMemoryAsync()
		d.app.scheduleTrayTrim()
	})

	d.mu.Lock()
	d.win = nw
	d.mu.Unlock()
	nw.ShowInactive()
}

func (d *delayCountdownHUD) Close() {
	d.mu.Lock()
	w := d.win
	d.win = nil
	d.mu.Unlock()
	if w != nil {
		w.Close()
	}
}

func (d *delayCountdownHUD) view(c *ui.Context) {
	c.SetTheme(ui.DarkTheme())
	c.Root().Background(ui.Transparent)

	if c.Shortcut(0, ui.KeyEscape) {
		d.mu.Lock()
		cancel := d.onCancel
		d.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return
	}

	d.mu.Lock()
	sec := d.seconds
	cancel := d.onCancel
	d.mu.Unlock()

	ui.Box(c).Fill().Center().Padding(8).Children(func() {
		pill := ui.Row(c.Key("delay-hud-pill")).
			Height(42).Padding(0, 10, 0, 14).Gap(9).
			Radius(21).Material(glasswork.Dark()).AlignItems(ui.Center).
			Transition(glasswork.ToolbarAppearTransition(-6))
		pill.Clicked()
		pill.Children(func() {
			ui.Icon(c, icons.CaptureDelay.Active).Size(17, 17).TextColor(ui.Hex("#FF9500")).Shrink(0)
			ui.Text(c, fmt.Sprintf("%d 秒后截屏", sec)).
				FontSize(12.5).FontWeight(700).TextColor(ui.Hex("#FFFFFF")).SingleLine().
				ContentTransition(glasswork.ContentNumericDown)

			closeBtn := ui.Box(c.Key("delay-hud-cancel")).
				Size(24, 24).Radius(12).Center().
				Role(ui.RoleButton).Label("取消延时截图").
				Cursor(ui.CursorPointer).Tooltip("取消延时截图 (Esc)")
			if closeBtn.Hovered() {
				closeBtn.Background(ui.RGBA(255, 69, 58, 0.28))
			}
			glasswork.ApplyLiquidButton(closeBtn, glasswork.CompactButtonOpts())
			closeBtn.Children(func() {
				ui.Icon(c, icons.Close.For(closeBtn.Hovered() || closeBtn.Pressed())).
					Size(14, 14).TextColor(ui.RGBA(255, 255, 255, 0.85))
			})
			if closeBtn.Clicked() && cancel != nil {
				cancel()
			}
		})
	})
}
