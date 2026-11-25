package app

import (
	"fmt"
	"image"
	"math"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"koma/internal/canvas"
	"koma/internal/glasswork"
	"koma/internal/icons"
)

// PinWindow represents a frameless, always-on-top floating screenshot card pinned on the desktop.
// It retains only compressed PNG bytes on the CPU and releases decoded CPU pixels after the GPU
// uploads its texture, cutting per-pin RAM from 15-35 MB to ~200-500 KB.
type PinWindow struct {
	app            *App
	win            *mygo.Window
	pngBytes       []byte
	bmp            *ui.Bitmap
	drawnFrames    int
	pixelsReleased bool
	baseW          float64
	baseH          float64
	imgAspect      float64
	tallScroll     bool
	scrollY        float32
	scale          float64
	opacity        float64
	hudText        string
	hudUntil       time.Time
}

// OpenPin spawns a frameless, always-on-top floating screenshot card at (x, y) in DIPs.
func (a *App) OpenPin(img *image.RGBA, x, y int, scaleFactor float64) {
	a.OpenPinWithPNG(img, nil, x, y, scaleFactor)
}

// OpenPinWithPNG spawns a floating pin window reusing pre-encoded PNG bytes when available.
func (a *App) OpenPinWithPNG(img *image.RGBA, pngBytes []byte, x, y int, scaleFactor float64) {
	if img == nil {
		return
	}
	if len(pngBytes) == 0 {
		var err error
		pngBytes, err = canvas.EncodePNG(img)
		if err != nil {
			return
		}
	}
	if scaleFactor <= 0 {
		scaleFactor = 1.0
		if d := mygo.Screen.PrimaryDisplay(); d.ScaleFactor > 0 {
			scaleFactor = d.ScaleFactor
		}
	}

	iw, ih := img.Bounds().Dx(), img.Bounds().Dy()
	dipW := math.Max(96, float64(iw)/scaleFactor)
	dipH := math.Max(64, float64(ih)/scaleFactor)
	imgAspect := float64(ih) / math.Max(1, float64(iw))
	tallScroll := false

	work := mygo.Screen.PrimaryDisplay().WorkArea
	if work.Width > 0 && work.Height > 0 {
		maxW := float64(work.Width) * 0.78
		maxH := float64(work.Height) * 0.78
		if ih > iw*2 && dipH > maxH {
			// Tall long screenshots stay readable at width and scroll vertically inside the pin card.
			tallScroll = true
			if dipW > maxW*0.62 {
				dipW = maxW * 0.62
			}
			dipW = math.Max(220, dipW)
			dipH = maxH
		} else {
			fit := math.Min(1.0, math.Min(maxW/dipW, maxH/dipH))
			dipW *= fit
			dipH *= fit
		}
	}

	bmp, err := ui.DecodeBitmap(pngBytes)
	if err != nil || bmp == nil {
		bmp = ui.NewBitmap(img)
	}

	pw := &PinWindow{
		app:        a,
		pngBytes:   pngBytes,
		bmp:        bmp,
		baseW:      dipW,
		baseH:      dipH,
		imgAspect:  imgAspect,
		tallScroll: tallScroll,
		scale:      1.0,
		opacity:    1.0,
	}

	w := mygo.NewWindow(mygo.WindowOptions{
		Title:        "Koma 贴图",
		X:            x,
		Y:            y,
		Width:        int(math.Round(dipW)),
		Height:       int(math.Round(dipH)),
		Frameless:    true,
		AlwaysOnTop:  true,
		SkipTaskbar:  true,
		CornerRadius: 12,
		Content:      ui.View(pw.view),
	})
	pw.win = w

	a.mu.Lock()
	a.pins = append(a.pins, pw)
	a.mu.Unlock()

	w.OnClosed(func() {
		a.mu.Lock()
		for i, p := range a.pins {
			if p == pw {
				a.pins = append(a.pins[:i], a.pins[i+1:]...)
				break
			}
		}
		a.mu.Unlock()
		pw.pngBytes = nil
		pw.bmp = nil
		pw.win = nil
		reclaimMemoryAsync()
		a.scheduleTrayTrim()
	})
}

func (p *PinWindow) flashHUD(text string) {
	p.hudText = text
	p.hudUntil = time.Now().Add(1300 * time.Millisecond)
	if p.win != nil {
		p.win.Update(func() {})
	}
}

func (p *PinWindow) applyZoom(factor float64) {
	next := p.scale * factor
	if next < 0.25 {
		next = 0.25
	} else if next > 4.0 {
		next = 4.0
	}
	if math.Abs(next-p.scale) < 0.005 {
		return
	}
	p.scale = next
	if p.win != nil {
		b := p.win.Bounds()
		nw := max(80, int(math.Round(p.baseW*p.scale)))
		nh := max(54, int(math.Round(p.baseH*p.scale)))
		cx := b.X + b.Width/2
		cy := b.Y + b.Height/2
		p.win.SetBounds(mygo.Rectangle{
			X:      cx - nw/2,
			Y:      cy - nh/2,
			Width:  nw,
			Height: nh,
		})
	}
	p.flashHUD(fmt.Sprintf("%d%%", int(math.Round(p.scale*100))))
}

func (p *PinWindow) applyOpacityDelta(delta float64) {
	next := p.opacity + delta
	if next < 0.2 {
		next = 0.2
	} else if next > 1.0 {
		next = 1.0
	}
	p.opacity = next
	if p.win != nil {
		p.win.SetOpacity(p.opacity)
	}
	p.flashHUD(fmt.Sprintf("不透明度 %d%%", int(math.Round(p.opacity*100))))
}

func (p *PinWindow) copyImage() {
	if len(p.pngBytes) > 0 {
		_ = mygo.Clipboard.WriteImage(p.pngBytes)
		p.flashHUD("已复制到剪贴板")
	}
}

func (p *PinWindow) view(c *ui.Context) {
	c.SetTheme(ui.DarkTheme())

	if c.Shortcut(0, ui.KeyEscape) {
		if p.win != nil {
			p.win.Close()
		}
		return
	}
	if c.Shortcut(ui.Ctrl, ui.KeyC) {
		p.copyImage()
	}
	if c.Shortcut(0, ui.Key0) || c.Shortcut(ui.Ctrl, ui.Key0) {
		p.scale = 1.0
		p.opacity = 1.0
		if p.win != nil {
			p.win.SetOpacity(1.0)
			b := p.win.Bounds()
			p.win.SetBounds(mygo.Rectangle{X: b.X, Y: b.Y, Width: int(math.Round(p.baseW)), Height: int(math.Round(p.baseH))})
		}
		p.flashHUD("重置 100%")
	}

	outer := ui.Box(c).Fill()
	hovered := outer.Hovered()

	outer.Children(func() {
		card := ui.Box(c).Absolute().Fill().Background(ui.Hex("#1C1C1E")).DragWindow().Cursor(ui.CursorMove)

		if card.WindowDoubleClicked() {
			p.copyImage()
		}
		if card.RightClicked() {
			if p.win != nil {
				p.win.Close()
			}
			return
		}

		card.HandleInput(func(ev ui.InputEvent) bool {
			switch ev.Kind {
			case ui.InputScroll:
				if ev.Mods&ui.Alt != 0 {
					if ev.DY < 0 {
						p.applyOpacityDelta(0.08)
					} else if ev.DY > 0 {
						p.applyOpacityDelta(-0.08)
					}
				} else if p.tallScroll && ev.Mods&ui.Ctrl == 0 {
					b := card.Bounds()
					contentH := b.W * float32(p.imgAspect)
					maxScroll := max(float32(0), contentH-b.H)
					p.scrollY = clamp32(p.scrollY+ev.DY, 0, maxScroll)
				} else if ev.Mods&ui.Ctrl != 0 && !p.tallScroll {
					if ev.DY < 0 {
						p.applyOpacityDelta(0.08)
					} else if ev.DY > 0 {
						p.applyOpacityDelta(-0.08)
					}
				} else {
					if ev.DY < 0 {
						p.applyZoom(1.1)
					} else if ev.DY > 0 {
						p.applyZoom(1.0 / 1.1)
					}
				}
				return true
			case ui.InputZoom:
				if ev.Scale > 0 {
					p.applyZoom(float64(ev.Scale))
				}
				return true
			}
			return false
		})

		card.Draw(func(pt *ui.Painter, r ui.Rect) {
			if p.bmp != nil {
				if p.tallScroll {
					contentH := r.W * float32(p.imgAspect)
					maxScroll := max(float32(0), contentH-r.H)
					sy := clamp32(p.scrollY, 0, maxScroll)
					pt.Clip(r, 11, func() {
						pt.Image(p.bmp, ui.Rect{X: r.X, Y: r.Y - sy, W: r.W, H: contentH}, ui.FillBox)
						if maxScroll > 4 && r.H > 40 {
							ratio := clamp32(r.H/contentH, 0.08, 1.0)
							thumbH := max(24, r.H*ratio)
							thumbY := r.Y + 6 + (r.H-thumbH-12)*(sy/maxScroll)
							thumbR := ui.Rect{X: r.X + r.W - 8, Y: thumbY, W: 4.5, H: thumbH}
							pt.Fill(ui.Rect{X: thumbR.X - 1, Y: thumbR.Y - 1, W: thumbR.W + 2, H: thumbR.H + 2}, ui.RGBA(0, 0, 0, 0.34), 3.2)
							pt.Fill(thumbR, ui.RGBA(255, 255, 255, 0.86), 2.25)
						}
					})
				} else {
					pt.Image(p.bmp, r, ui.FillBox)
				}
				if pt.Accelerated() && !p.pixelsReleased {
					if p.drawnFrames >= 1 {
						p.bmp.ReleasePixels()
						p.pixelsReleased = true
					} else {
						p.drawnFrames++
					}
				}
			}
			rimCol := ui.RGBA(0, 0, 0, 0.22)
			if hovered {
				rimCol = ui.Hex("#0A84FF").Alpha(0.9)
			}
			pt.Stroke(r, rimCol, 11, 1.5)
			pt.Stroke(ui.Rect{X: r.X + 1, Y: r.Y + 1, W: r.W - 2, H: r.H - 2}, ui.RGBA(255, 255, 255, 0.30), 10, 1)

			if p.hudText != "" && time.Now().Before(p.hudUntil) {
				span := ui.Span{Text: p.hudText, Size: 12, Weight: 600, Color: ui.Hex("#FFFFFF")}
				tw, th := pt.MeasureText(0, span)
				pill := ui.Rect{
					X: r.X + (r.W-(tw+24))/2,
					Y: r.Y + r.H - th - 22,
					W: tw + 24,
					H: th + 12,
				}
				glasswork.Paint(pt, pill, pill.H/2, glasswork.Dark())
				pt.RichText(pill.X+12, pill.Y+6, 0, span)
				pt.After(200 * time.Millisecond)
			}
		})

		// Sibling floating Glasswork action pill (isolated from DragWindow card!)
		if hovered {
			bar := ui.Row(c.Key("pin-action-bar")).
				Absolute().Top(10).Right(10).
				Padding(4, 6, 4, 6).Gap(3).
				Radius(16).
				Material(glasswork.Dark()).
				AlignItems(ui.Center).
				Transition(glasswork.PanelTransition(0, -6))
			bar.Clicked()
			bar.Children(func() {
				if pinIconBtn(c, icons.Copy, "复制 (Ctrl+C)", false) {
					p.copyImage()
				}
				if pinIconBtn(c, icons.Pen, "在工作台编辑", false) {
					if img := decodeImageRGBA(p.pngBytes); img != nil {
						p.app.OpenImageInEditor(img, "贴图编辑")
					}
				}
				if pinIconBtn(c, icons.Close, "关闭 (Esc / 右键)", true) {
					if p.win != nil {
						p.win.Close()
					}
				}
			})
		}
	})
}

func pinIconBtn(c *ui.Context, ic icons.IconPair, tip string, danger bool) bool {
	inks := glasswork.KindDark.Inks()
	btn := ui.Box(c).Size(24, 24).Center().Radius(12).
		Role(ui.RoleButton).Label(tip).
		Cursor(ui.CursorPointer).Tooltip(tip)
	hov := btn.Hovered()
	col := inks.Primary
	if hov {
		if danger {
			btn.Background(inks.DangerFill)
			col = inks.Danger
		} else {
			btn.Background(inks.AccentFill)
			col = inks.Accent
		}
	}
	btn.Transition(ui.ElementTransition{Colors: true, Animation: &glasswork.AnimColorFade})
	glasswork.ApplyLiquidButton(btn, glasswork.CompactButtonOpts())
	glyph := ic.For(hov || btn.Pressed())
	btn.Children(func() {
		ui.Icon(c, glyph).Size(15, 15).TextColor(col).ContentTransition(ui.ContentTransition{})
	})
	return btn.Clicked()
}
