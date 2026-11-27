package app

import (
	"fmt"
	"image"
	"math"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"koma/internal/canvas"
	"koma/internal/glasswork"
	"koma/internal/icons"
	"koma/internal/stitch"
	"koma/internal/winapi"
)

type scrollStateKind int

const (
	scrollStateManual scrollStateKind = iota
	scrollStateAuto
	scrollStateBottom
	scrollStateTooFast
	scrollStateMaxHeight
)

const (
	scrollShadowPad     float32 = 12
	scrollBarTooltipPad float32 = 44
)

// ScrollCaptureParams describes the screen region selected for live scrolling capture.
type ScrollCaptureParams struct {
	Display    mygo.Display
	SelDip     ui.Rect         // selection in display-local DIPs
	PhysRect   image.Rectangle // selection in virtual-desktop physical pixels
	FirstFrame *image.RGBA     // initial frame cropped from the frozen overlay (or captured live)
}

// scrollSession orchestrates a zero-jank QQ-style live scrolling capture using
// three lightweight, non-activating AlwaysOnTop windows:
//  1. frameWin: permanently click-through (IgnoreMouseEvents), static border outside
//     selDip — 0 repaints/sec during normal scrolling so the target app stays 60–144fps!
//  2. previewWin: compact floating Liquid Glass live stitched preview beside selDip.
//  3. barWin: compact floating Liquid Glass control bar that also forwards stray wheel
//     events straight to the target window so scrolling works immediately without clicking!
type scrollSession struct {
	app      *App
	display  mygo.Display
	selDip   ui.Rect
	physRect image.Rectangle

	// Lightweight per-surface windows (nil in headless tests)
	frameWin   *mygo.Window
	previewWin *mygo.Window
	barWin     *mygo.Window

	frameWinOffX, frameWinOffY float32
	prevCardR                  ui.Rect
	prevWinOffX, prevWinOffY   float32
	barCardR                   ui.Rect
	barWinOffX, barWinOffY     float32
	showPrev                   bool

	// stitchMu exclusively guards the stitcher & pixel-heavy operations (NEVER locked by UI views!)
	stitchMu sync.Mutex
	stitcher *stitch.Stitcher

	// uiMu guards only lightweight UI state & double-buffered preview swap (<1µs hold time).
	uiMu              sync.Mutex
	stitchedW         int
	stitchedH         int
	framesCount       int
	autoScroll        bool
	state             scrollStateKind
	warningUntil      time.Time
	previewReady      *image.RGBA
	previewScaledH    int
	previewScrollFrac float32
	previewVer        uint64
	previewTargetH    int
	closed            bool

	// UI-thread-only bitmap state for previewWin
	previewBmp    *ui.Bitmap
	previewBmpVer uint64

	// Signals worker goroutine immediately when user wheels inside previewWin
	previewReqCh chan struct{}
	stopOnce     sync.Once
	stopCh       chan struct{}
}

// StartScrollCapture launches a live QQ-style scrolling screenshot session for params.
func (a *App) StartScrollCapture(params ScrollCaptureParams) {
	if params.PhysRect.Dx() < 24 || params.PhysRect.Dy() < 24 {
		return
	}
	a.mu.Lock()
	if a.activeScroll != nil {
		prev := a.activeScroll
		a.mu.Unlock()
		prev.close()
	} else {
		a.mu.Unlock()
	}

	first := params.FirstFrame
	if first == nil {
		var err error
		first, err = winapi.CaptureRect(params.PhysRect)
		if err != nil || first == nil {
			a.NotifyToast("无法启动长截图")
			return
		}
	}

	s := newScrollSession(a, params, first)

	a.mu.Lock()
	a.activeScroll = s
	a.mu.Unlock()

	// Bring the exact target window under the selected region into foreground focus
	// BEFORE showing our non-activating overlay windows so scrolling works immediately!
	cx, cy := s.targetCenterPx()
	winapi.ActivateWindowAt(cx, cy)

	disp := params.Display.Bounds
	dispW, dispH := float32(disp.Width), float32(disp.Height)

	// 1. Static click-through capture frame window (IgnoreMouseEvents: true, 0 repaints during normal scroll).
	const framePad float32 = 10
	fx0 := clamp32(s.selDip.X-framePad, 0, max(0, dispW-4))
	fy0 := clamp32(s.selDip.Y-framePad, 0, max(0, dispH-4))
	fx1 := clamp32(s.selDip.X+s.selDip.W+framePad, fx0+4, dispW)
	fy1 := clamp32(s.selDip.Y+s.selDip.H+framePad, fy0+4, dispH)
	s.frameWinOffX, s.frameWinOffY = fx0, fy0

	fw := mygo.NewWindow(mygo.WindowOptions{
		Title:             "Koma 长截图框",
		X:                 disp.X + int(math.Floor(float64(fx0))),
		Y:                 disp.Y + int(math.Floor(float64(fy0))),
		Width:             max(32, int(math.Ceil(float64(fx1-fx0)))),
		Height:            max(32, int(math.Ceil(float64(fy1-fy0)))),
		Frameless:         true,
		Transparent:       true,
		AlwaysOnTop:       true,
		SkipTaskbar:       true,
		DisableShadow:     true,
		DisableResize:     true,
		DisableMove:       true,
		IgnoreMouseEvents: true,
		Hidden:            true,
		Content:           ui.View(s.frameView),
	})
	fw.SetContentProtection(true)
	fw.OnClosed(func() { s.close() })
	s.frameWin = fw

	// 2. Compact floating Liquid Glass Live Preview window beside selDip.
	if s.showPrev {
		px0 := clamp32(s.prevCardR.X-scrollShadowPad, 0, max(0, dispW-40))
		py0 := clamp32(s.prevCardR.Y-scrollShadowPad, 0, max(0, dispH-40))
		px1 := clamp32(s.prevCardR.X+s.prevCardR.W+scrollShadowPad, px0+40, dispW)
		py1 := clamp32(s.prevCardR.Y+s.prevCardR.H+scrollShadowPad, py0+40, dispH)
		// Guarantee zero transparent-margin intrusion into selDip when placed beside it.
		if s.prevCardR.X >= s.selDip.X+s.selDip.W {
			px0 = max(px0, s.selDip.X+s.selDip.W+2)
		} else if s.prevCardR.X+s.prevCardR.W <= s.selDip.X {
			px1 = min(px1, s.selDip.X-2)
		}
		s.prevWinOffX, s.prevWinOffY = px0, py0

		pw := mygo.NewWindow(mygo.WindowOptions{
			Title:         "Koma 长截图预览",
			X:             disp.X + int(math.Floor(float64(px0))),
			Y:             disp.Y + int(math.Floor(float64(py0))),
			Width:         max(80, int(math.Ceil(float64(px1-px0)))),
			Height:        max(80, int(math.Ceil(float64(py1-py0)))),
			Frameless:     true,
			Transparent:   true,
			AlwaysOnTop:   true,
			SkipTaskbar:   true,
			DisableShadow: true,
			DisableResize: true,
			DisableMove:   true,
			Hidden:        true,
			Content:       ui.View(s.previewView),
		})
		pw.SetContentProtection(true)
		pw.OnClosed(func() { s.close() })
		s.previewWin = pw
	}

	// 3. Compact floating Liquid Glass Control Bar window.
	// Reserve scrollBarTooltipPad on the open vertical side so button tooltips render at
	// full height without squashing or flickering between the top and bottom edges.
	barWinR := computeScrollBarWindowRect(s.selDip, s.barCardR, dispW, dispH)
	s.barWinOffX, s.barWinOffY = barWinR.X, barWinR.Y

	bw := mygo.NewWindow(mygo.WindowOptions{
		Title:         "Koma 长截图工具栏",
		X:             disp.X + int(barWinR.X),
		Y:             disp.Y + int(barWinR.Y),
		Width:         int(barWinR.W),
		Height:        int(barWinR.H),
		Frameless:     true,
		Transparent:   true,
		AlwaysOnTop:   true,
		SkipTaskbar:   true,
		DisableShadow: true,
		DisableResize: true,
		DisableMove:   true,
		Hidden:        true,
		Content:       ui.View(s.barView),
	})
	bw.SetContentProtection(true)
	bw.OnClosed(func() { s.close() })
	s.barWin = bw

	// Show all three windows WITHOUT activating/stealing focus from the target app!
	fw.ShowInactive()
	if s.previewWin != nil {
		s.previewWin.ShowInactive()
	}
	bw.ShowInactive()
	winapi.ActivateWindowAt(cx, cy)

	go s.captureLoop()
}

func newScrollSession(a *App, params ScrollCaptureParams, first *image.RGBA) *scrollSession {
	st := stitch.New(first, stitch.DefaultMaxHeight)
	dispW := float32(params.Display.Bounds.Width)
	dispH := float32(params.Display.Bounds.Height)
	barR, prevR, showPrev := ComputeScrollOverlayLayout(
		params.SelDip.X, params.SelDip.Y,
		params.SelDip.X+params.SelDip.W, params.SelDip.Y+params.SelDip.H,
		dispW, dispH, 392,
	)

	targetH := clampIntScroll(int(prevR.H-56), 140, 460)
	initPreview := image.NewRGBA(image.Rect(0, 0, 204, targetH))
	scaledH, _ := st.RenderPreview(initPreview, 1.0)

	return &scrollSession{
		app:               a,
		display:           params.Display,
		selDip:            params.SelDip,
		physRect:          params.PhysRect,
		prevCardR:         prevR,
		barCardR:          barR,
		showPrev:          showPrev,
		stitcher:          st,
		stitchedW:         st.Width(),
		stitchedH:         st.TotalHeight(),
		framesCount:       1,
		state:             scrollStateManual,
		previewReady:      initPreview,
		previewScaledH:    scaledH,
		previewScrollFrac: 1.0,
		previewVer:        1,
		previewTargetH:    targetH,
		previewReqCh:      make(chan struct{}, 1),
		stopCh:            make(chan struct{}),
	}
}

func (s *scrollSession) targetCenterPx() (int, int) {
	return (s.physRect.Min.X + s.physRect.Max.X) / 2, (s.physRect.Min.Y + s.physRect.Max.Y) / 2
}

func (s *scrollSession) close() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})

	s.uiMu.Lock()
	if s.closed {
		s.uiMu.Unlock()
		return
	}
	s.closed = true
	s.autoScroll = false
	fw, pw, bw := s.frameWin, s.previewWin, s.barWin
	s.frameWin, s.previewWin, s.barWin = nil, nil, nil
	s.previewReady = nil
	s.previewBmp = nil
	s.uiMu.Unlock()

	s.stitchMu.Lock()
	if s.stitcher != nil {
		s.stitcher.Release()
		s.stitcher = nil
	}
	s.stitchMu.Unlock()

	s.app.clearScrollSession(s)
	for _, w := range []*mygo.Window{bw, pw, fw} {
		if w != nil {
			w.Close()
		}
	}
	reclaimMemoryAsync()
	s.app.scheduleTrayTrim()
}

func (s *scrollSession) composeAndClose() *image.RGBA {
	s.stitchMu.Lock()
	var img *image.RGBA
	if s.stitcher != nil {
		img = s.stitcher.Compose()
	}
	s.stitchMu.Unlock()
	s.close()
	return img
}

func (s *scrollSession) finishCopy() {
	img := s.composeAndClose()
	if img == nil {
		return
	}
	app := s.app
	go func() {
		pngBytes, err := canvas.EncodePNG(img)
		if err == nil {
			_ = mygo.Clipboard.WriteImage(pngBytes)
		}
		app.AddHistoryEntryWithPNG(img, pngBytes, "长截图")
		app.NotifyToast(fmt.Sprintf("已复制长截图 %d × %d 到剪贴板", img.Bounds().Dx(), img.Bounds().Dy()))
	}()
}

func (s *scrollSession) finishEdit() {
	img := s.composeAndClose()
	if img == nil {
		return
	}
	app := s.app
	go app.OpenImageInEditor(img, "长截图")
}

func (s *scrollSession) finishPin() {
	img := s.composeAndClose()
	if img == nil {
		return
	}
	scale := s.display.ScaleFactor
	if scale <= 0 {
		scale = 1
	}
	pinX := s.display.Bounds.X + int(math.Round(float64(s.selDip.X)))
	pinY := s.display.Bounds.Y + int(math.Round(float64(s.selDip.Y)))
	app := s.app
	go func() {
		pngBytes, _ := canvas.EncodePNG(img)
		app.AddHistoryEntryWithPNG(img, pngBytes, "长截图贴图")
		app.OpenPinWithPNG(img, pngBytes, pinX, pinY, scale)
	}()
}

func (s *scrollSession) finishSave() {
	img := s.composeAndClose()
	if img == nil {
		return
	}
	app := s.app
	go func() {
		pngBytes, _ := canvas.EncodePNG(img)
		app.AddHistoryEntryWithPNG(img, pngBytes, "长截图")
		app.SaveImagePreferred(img, pngBytes, nil)
	}()
}

func (s *scrollSession) toggleAutoScroll() {
	s.uiMu.Lock()
	if s.closed {
		s.uiMu.Unlock()
		return
	}
	if s.state == scrollStateMaxHeight {
		s.autoScroll = false
		s.uiMu.Unlock()
		return
	}
	s.autoScroll = !s.autoScroll
	turningOn := s.autoScroll
	if turningOn {
		s.state = scrollStateAuto
		s.previewScrollFrac = 1.0
	} else {
		s.state = scrollStateManual
	}
	fw, pw, bw := s.frameWin, s.previewWin, s.barWin
	s.uiMu.Unlock()

	if turningOn {
		cx, cy := s.targetCenterPx()
		winapi.ActivateWindowAt(cx, cy)
		s.requestPreviewRefresh()
	}
	if fw != nil {
		fw.Update(func() {})
	}
	if pw != nil {
		pw.Update(func() {})
	}
	if bw != nil {
		bw.Update(func() {})
	}
}

func (s *scrollSession) resetStitch() {
	fresh, err := winapi.CaptureRect(s.physRect)
	if err != nil || fresh == nil {
		return
	}
	s.stitchMu.Lock()
	if s.stitcher == nil {
		s.stitchMu.Unlock()
		return
	}
	s.stitcher.Reset(fresh)
	w, h := s.stitcher.Width(), s.stitcher.TotalHeight()
	s.uiMu.Lock()
	targetH := s.previewTargetH
	s.uiMu.Unlock()
	freshPrev := image.NewRGBA(image.Rect(0, 0, 204, max(120, targetH)))
	scaledH, _ := s.stitcher.RenderPreview(freshPrev, 1.0)
	s.stitchMu.Unlock()

	s.uiMu.Lock()
	if s.closed {
		s.uiMu.Unlock()
		return
	}
	s.autoScroll = false
	s.stitchedW = w
	s.stitchedH = h
	s.framesCount = 1
	s.state = scrollStateManual
	s.warningUntil = time.Time{}
	s.previewScrollFrac = 1.0
	s.previewReady = freshPrev
	s.previewScaledH = scaledH
	s.previewVer++
	fw, pw, bw := s.frameWin, s.previewWin, s.barWin
	s.uiMu.Unlock()

	cx, cy := s.targetCenterPx()
	winapi.ActivateWindowAt(cx, cy)

	if fw != nil {
		fw.Update(func() {})
	}
	if pw != nil {
		pw.Update(func() {})
	}
	if bw != nil {
		bw.Update(func() {})
	}
}

func (s *scrollSession) requestPreviewRefresh() {
	select {
	case s.previewReqCh <- struct{}{}:
	default:
	}
}

// forwardWheel forwards mouse-wheel scrolling received while hovering over the
// control bar directly to the target window under selDip so the user can scroll
// immediately after clicking "长截图" without even moving the mouse off the toolbar.
// Using hardware-level SendInput moves the cursor into selDip on the very first
// wheel notch so all subsequent scrolling goes natively to the target app at 0ms lag.
func (s *scrollSession) forwardWheel(dy float32, precise bool) {
	if dy == 0 {
		return
	}
	s.uiMu.Lock()
	wasAuto := s.autoScroll
	if wasAuto {
		s.autoScroll = false
		if s.state == scrollStateAuto {
			s.state = scrollStateManual
		}
	}
	fw, pw, bw := s.frameWin, s.previewWin, s.barWin
	s.uiMu.Unlock()
	if wasAuto {
		if fw != nil {
			fw.Update(func() {})
		}
		if pw != nil {
			pw.Update(func() {})
		}
		if bw != nil {
			bw.Update(func() {})
		}
	}

	var delta int
	if !precise {
		if dy > 0 {
			delta = -winapi.WheelDelta
		} else {
			delta = winapi.WheelDelta
		}
	} else {
		delta = -int(math.Round(float64(dy) * 3.0))
		if delta == 0 {
			if dy > 0 {
				delta = -40
			} else {
				delta = 40
			}
		}
		if delta < -winapi.WheelDelta*3 {
			delta = -winapi.WheelDelta * 3
		} else if delta > winapi.WheelDelta*3 {
			delta = winapi.WheelDelta * 3
		}
	}
	cx, cy := s.targetCenterPx()
	winapi.ActivateWindowAt(cx, cy)
	go winapi.ScrollWindowAt(cx, cy, delta, true)
}

// captureLoop runs at 25 fps (40ms) with reusable GDI & preview buffers completely
// off the UI thread so manual scrolling tracks smoothly at native refresh rate and
// auto-scroll glides smoothly with gentle rhythmic wheel steps.
func (s *scrollSession) captureLoop() {
	grabber, err := winapi.NewScreenGrabber(s.physRect)
	if err != nil {
		return
	}
	defer grabber.Close()

	var frameBuf *image.RGBA
	s.uiMu.Lock()
	previewH := max(120, s.previewTargetH)
	s.uiMu.Unlock()
	previewWork := image.NewRGBA(image.Rect(0, 0, 204, previewH))

	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()

	cx, cy := s.targetCenterPx()
	autoStepInterval := 105 * time.Millisecond
	if s.physRect.Dy() < 360 {
		autoStepInterval = 135 * time.Millisecond
	}

	var (
		wasEscDown    = winapi.IsEscapePressed()
		lastAutoTick  time.Time
		autoNoMove    int
		postMsgWorked bool
		useSendInput  bool
		sentCursorX   int
		sentCursorY   int
		hadSentCursor bool
	)

	renderPreviewToUI := func() {
		s.uiMu.Lock()
		if s.closed {
			s.uiMu.Unlock()
			return
		}
		frac := s.previewScrollFrac
		s.uiMu.Unlock()

		s.stitchMu.Lock()
		if s.stitcher == nil {
			s.stitchMu.Unlock()
			return
		}
		scaledH, _ := s.stitcher.RenderPreview(previewWork, frac)
		s.stitchMu.Unlock()

		s.uiMu.Lock()
		if s.closed {
			s.uiMu.Unlock()
			return
		}
		s.previewReady, previewWork = previewWork, s.previewReady
		if previewWork == nil || previewWork.Bounds().Dy() != previewH {
			previewWork = image.NewRGBA(image.Rect(0, 0, 204, previewH))
		}
		s.previewScaledH = scaledH
		s.previewVer++
		pw := s.previewWin
		s.uiMu.Unlock()

		if pw != nil {
			pw.Update(func() {})
		}
	}

	for {
		select {
		case <-s.stopCh:
			return

		case <-s.previewReqCh:
			renderPreviewToUI()

		case now := <-ticker.C:
			escDown := winapi.IsEscapePressed()
			if escDown && !wasEscDown {
				s.close()
				return
			}
			wasEscDown = escDown

			s.uiMu.Lock()
			if s.closed {
				s.uiMu.Unlock()
				return
			}
			isAuto := s.autoScroll
			prevState := s.state
			s.uiMu.Unlock()

			// Yield gracefully if the user grabs and moves the mouse during SendInput auto-scroll.
			if isAuto && useSendInput && hadSentCursor {
				if mx, my, ok := winapi.GetCursorPosPx(); ok {
					if absIntScroll(mx-sentCursorX) > 48 || absIntScroll(my-sentCursorY) > 48 {
						s.uiMu.Lock()
						s.autoScroll = false
						if s.state == scrollStateAuto {
							s.state = scrollStateManual
						}
						fw, pw, bw := s.frameWin, s.previewWin, s.barWin
						s.uiMu.Unlock()
						hadSentCursor = false
						isAuto = false
						if fw != nil {
							fw.Update(func() {})
						}
						if pw != nil {
							pw.Update(func() {})
						}
						if bw != nil {
							bw.Update(func() {})
						}
					}
				}
			}

			if !isAuto {
				autoNoMove = 0
				hadSentCursor = false
			}

			// Capture desktop rect into reusable buffer (no lock held).
			frameBuf, err = grabber.GrabInto(frameBuf)
			if err != nil {
				continue
			}

			// Run vertical stitcher completely off UI thread under stitchMu.
			s.stitchMu.Lock()
			if s.stitcher == nil {
				s.stitchMu.Unlock()
				return
			}
			res := s.stitcher.Feed(frameBuf)
			stW := s.stitcher.Width()
			stH := res.TotalHeight
			frames := s.stitcher.FramesAccepted()

			needPreview := res.Status == stitch.StatusStitched || res.Status == stitch.StatusMaxHeight
			var scaledH int
			if needPreview {
				s.uiMu.Lock()
				frac := s.previewScrollFrac
				s.uiMu.Unlock()
				scaledH, _ = s.stitcher.RenderPreview(previewWork, frac)
			}
			s.stitchMu.Unlock()

			// Publish compact UI state snapshot under uiMu (<1µs).
			s.uiMu.Lock()
			if s.closed {
				s.uiMu.Unlock()
				return
			}
			switch res.Status {
			case stitch.StatusStitched:
				s.stitchedW = stW
				s.stitchedH = stH
				s.framesCount = frames
				s.warningUntil = time.Time{}
				if s.autoScroll {
					s.state = scrollStateAuto
					autoNoMove = 0
					if !useSendInput {
						postMsgWorked = true
					}
				} else {
					s.state = scrollStateManual
				}
				s.previewReady, previewWork = previewWork, s.previewReady
				if previewWork == nil || previewWork.Bounds().Dy() != previewH {
					previewWork = image.NewRGBA(image.Rect(0, 0, 204, previewH))
				}
				s.previewScaledH = scaledH
				s.previewVer++

			case stitch.StatusMaxHeight:
				s.stitchedW = stW
				s.stitchedH = stH
				s.framesCount = frames
				s.autoScroll = false
				s.state = scrollStateMaxHeight
				s.previewReady, previewWork = previewWork, s.previewReady
				if previewWork == nil || previewWork.Bounds().Dy() != previewH {
					previewWork = image.NewRGBA(image.Rect(0, 0, 204, previewH))
				}
				s.previewScaledH = scaledH
				s.previewVer++

			case stitch.StatusTooFast:
				s.state = scrollStateTooFast
				s.warningUntil = now.Add(1400 * time.Millisecond)
				if s.autoScroll && autoStepInterval < 180*time.Millisecond {
					autoStepInterval += 30 * time.Millisecond
				}

			case stitch.StatusUnchanged:
				if s.state == scrollStateTooFast && now.After(s.warningUntil) {
					if s.autoScroll {
						s.state = scrollStateAuto
					} else {
						s.state = scrollStateManual
					}
				}
			}

			stateChanged := s.state != prevState
			isAuto = s.autoScroll
			fw, pw, bw := s.frameWin, s.previewWin, s.barWin
			s.uiMu.Unlock()

			// Smooth rhythmic auto-scroll: gentle 1-notch steps every ~105ms trigger
			// native 60fps smooth scrolling without ever outrunning stitch overlap.
			if isAuto && now.Sub(lastAutoTick) >= autoStepInterval {
				lastAutoTick = now
				if res.Status == stitch.StatusUnchanged {
					autoNoMove++
					if !useSendInput && !postMsgWorked && autoNoMove >= 3 {
						useSendInput = true
						autoNoMove = 0
					} else if autoNoMove >= 11 {
						s.uiMu.Lock()
						s.autoScroll = false
						s.state = scrollStateBottom
						s.uiMu.Unlock()
						stateChanged = true
						isAuto = false
					}
				}
				if isAuto {
					winapi.ScrollWindowAt(cx, cy, -winapi.WheelDelta, useSendInput)
					if useSendInput {
						sentCursorX, sentCursorY = cx, cy
						hadSentCursor = true
					}
				}
			}

			// Wake ONLY the tiny windows that actually changed!
			// During smooth manual scrolling, frameWin and barWin do ZERO repaints!
			if needPreview && pw != nil {
				pw.Update(func() {})
			}
			if stateChanged {
				if fw != nil {
					fw.Update(func() {})
				}
				if bw != nil {
					bw.Update(func() {})
				}
				if pw != nil && !needPreview {
					pw.Update(func() {})
				}
			}
		}
	}
}

// ComputeScrollOverlayLayout computes strictly clamped positions for both the floating
// control bar and the live stitched preview card around the selected capture region.
func ComputeScrollOverlayLayout(x0, y0, x1, y1, viewW, viewH, barW float32) (barRect, previewRect ui.Rect, showPreview bool) {
	const (
		barH    float32 = 44
		margin  float32 = 16
		gap     float32 = 16
		prevW   float32 = 224
		minPrev float32 = 220
		maxPrev float32 = 520
	)
	if barW < 300 {
		barW = 392
	}

	// 1. Place control bar: below selection -> above selection -> docked inside bottom.
	// Reserve scrollBarTooltipPad on the open side so hover tooltips inside barWin have
	// full height without clipping or oscillating between top and bottom edges.
	maxBarX := max(margin, viewW-barW-margin)
	barX := clamp32(x1-barW, margin, maxBarX)
	var barY float32
	switch {
	case y1+gap+barH <= viewH-scrollBarTooltipPad:
		barY = y1 + gap
	case y0-gap-barH >= scrollBarTooltipPad:
		barY = y0 - gap - barH
	default:
		barY = y1 - barH - margin
	}
	barY = clamp32(barY, margin, max(margin, viewH-barH-margin))
	barRect = ui.Rect{X: barX, Y: barY, W: barW, H: barH}

	// Vertical clearance between the preview card and the bar on whichever side barWin
	// expands for tooltips so barWin's transparent tooltip band never covers previewWin.
	barTopGap := gap
	barBottomGap := gap
	if barRect.Y >= y1 {
		barBottomGap = max(gap, scrollBarTooltipPad)
	} else {
		barTopGap = max(gap, scrollBarTooltipPad)
	}

	// 2. Place live preview card beside the capture box without overlapping the control bar.
	if viewW < prevW+margin*2+60 || viewH < minPrev+margin*2 {
		return barRect, ui.Rect{}, false
	}

	selH := y1 - y0
	prevH := clamp32(selH, minPrev, min(maxPrev, viewH-margin*2-barH-gap))
	prevY := clamp32(y0, margin, max(margin, viewH-prevH-margin))

	var prevX float32
	switch {
	case x1+gap+prevW <= viewW-margin:
		prevX = x1 + gap
	case x0-gap-prevW >= margin:
		prevX = x0 - gap - prevW
	default:
		prevX = viewW - prevW - margin
		if prevY+prevH+barTopGap > barY && barY > margin+minPrev+barTopGap {
			prevH = min(prevH, barY-barTopGap-margin)
			prevY = max(margin, barY-barTopGap-prevH)
		}
	}

	horizOverlap := prevX < barRect.X+barRect.W && prevX+prevW > barRect.X
	vertOverlap := prevY < barRect.Y+barRect.H+barBottomGap && prevY+prevH > barRect.Y-barTopGap
	if horizOverlap && vertOverlap {
		if barRect.Y-barTopGap-minPrev >= margin {
			prevH = min(prevH, barRect.Y-barTopGap-margin)
			prevY = barRect.Y - barTopGap - prevH
		} else if barRect.Y+barRect.H+barBottomGap+minPrev <= viewH-margin {
			prevY = barRect.Y + barRect.H + barBottomGap
			prevH = min(prevH, viewH-margin-prevY)
		}
	}

	prevX = clamp32(prevX, margin, max(margin, viewW-prevW-margin))
	prevY = clamp32(prevY, margin, max(margin, viewH-prevH-margin))
	previewRect = ui.Rect{X: prevX, Y: prevY, W: prevW, H: prevH}
	return barRect, previewRect, true
}

// computeScrollBarWindowRect sizes the compact transparent window hosting the scroll
// control bar, adding scrollBarTooltipPad on the open vertical side (never intruding
// into selDip) so MyGo hover tooltips render at full height without top/bottom flicker.
func computeScrollBarWindowRect(selDip, barCardR ui.Rect, dispW, dispH float32) ui.Rect {
	padTop := scrollShadowPad
	padBottom := scrollShadowPad
	belowSel := barCardR.Y >= selDip.Y+selDip.H
	aboveSel := barCardR.Y+barCardR.H <= selDip.Y

	switch {
	case belowSel && dispH-(barCardR.Y+barCardR.H) >= scrollBarTooltipPad:
		padBottom = scrollBarTooltipPad
	case aboveSel && barCardR.Y >= scrollBarTooltipPad:
		padTop = scrollBarTooltipPad
	default:
		spaceAbove := barCardR.Y
		spaceBelow := dispH - (barCardR.Y + barCardR.H)
		if belowSel {
			spaceAbove = min(spaceAbove, max(float32(0), barCardR.Y-(selDip.Y+selDip.H+2)))
		} else if aboveSel {
			spaceBelow = min(spaceBelow, max(float32(0), (selDip.Y-2)-(barCardR.Y+barCardR.H)))
		}
		if spaceAbove >= spaceBelow {
			padTop = min(scrollBarTooltipPad, max(scrollShadowPad, spaceAbove))
		} else {
			padBottom = min(scrollBarTooltipPad, max(scrollShadowPad, spaceBelow))
		}
	}

	bx0 := clamp32(barCardR.X-scrollShadowPad, 0, max(0, dispW-40))
	by0 := clamp32(barCardR.Y-padTop, 0, max(0, dispH-40))
	bx1 := clamp32(barCardR.X+barCardR.W+scrollShadowPad, bx0+40, dispW)
	by1 := clamp32(barCardR.Y+barCardR.H+padBottom, by0+40, dispH)
	// Guarantee zero transparent-margin intrusion into selDip when placed below/above it.
	if belowSel {
		by0 = max(by0, selDip.Y+selDip.H+2)
	} else if aboveSel {
		by1 = min(by1, selDip.Y-2)
	}

	x := float32(math.Floor(float64(bx0)))
	y := float32(math.Floor(float64(by0)))
	w := float32(max(120, int(math.Ceil(float64(bx1-x)))))
	h := float32(max(56, int(math.Ceil(float64(by1-y)))))
	return ui.Rect{X: x, Y: y, W: w, H: h}
}

func (s *scrollSession) syncPreviewBitmap() (scaledTotalH int) {
	s.uiMu.Lock()
	if s.closed {
		s.uiMu.Unlock()
		return 0
	}
	ready := s.previewReady
	ver := s.previewVer
	scaledTotalH = s.previewScaledH
	s.uiMu.Unlock()

	if ready != nil && (s.previewBmp == nil || s.previewBmpVer != ver) {
		if s.previewBmp == nil {
			s.previewBmp = ui.NewBitmap(ready)
		} else {
			bw, bh := s.previewBmp.Size()
			if bw == ready.Bounds().Dx() && bh == ready.Bounds().Dy() {
				s.previewBmp.Update(ready)
			} else {
				s.previewBmp = ui.NewBitmap(ready)
			}
		}
		s.previewBmpVer = ver
	}
	return scaledTotalH
}

func (s *scrollSession) handleCommonShortcuts(c *ui.Context) bool {
	if c.Shortcut(0, ui.KeyEscape) {
		s.close()
		return true
	}
	if c.Shortcut(0, ui.KeyEnter) {
		s.finishCopy()
		return true
	}
	if c.Shortcut(0, ui.KeySpace) {
		s.toggleAutoScroll()
	}
	return false
}

// frameView renders ONLY the static click-through capture frame around selDip.
func (s *scrollSession) frameView(c *ui.Context) {
	c.SetTheme(ui.DarkTheme())
	c.Root().Background(ui.Transparent)

	s.uiMu.Lock()
	if s.closed {
		s.uiMu.Unlock()
		return
	}
	state := s.state
	s.uiMu.Unlock()

	localSel := ui.Rect{
		X: s.selDip.X - s.frameWinOffX,
		Y: s.selDip.Y - s.frameWinOffY,
		W: s.selDip.W,
		H: s.selDip.H,
	}
	ui.Box(c).Fill().PassThrough().Draw(func(pt *ui.Painter, _ ui.Rect) {
		drawScrollCaptureFrame(pt, localSel, state)
	})
}

// previewView renders ONLY the compact floating Liquid Glass live preview card.
func (s *scrollSession) previewView(c *ui.Context) {
	c.SetTheme(ui.DarkTheme())
	c.Root().Background(ui.Transparent)
	if s.handleCommonShortcuts(c) {
		return
	}
	s.uiMu.Lock()
	if s.closed {
		s.uiMu.Unlock()
		return
	}
	stW, stH := s.stitchedW, s.stitchedH
	state := s.state
	scrollFrac := s.previewScrollFrac
	s.uiMu.Unlock()

	localR := ui.Rect{
		X: s.prevCardR.X - s.prevWinOffX,
		Y: s.prevCardR.Y - s.prevWinOffY,
		W: s.prevCardR.W,
		H: s.prevCardR.H,
	}
	ui.Box(c).Fill().Children(func() {
		s.renderPreviewCard(c, localR, stW, stH, state, scrollFrac)
	})
}

// barView renders ONLY the compact floating Liquid Glass control bar and forwards
// mouse-wheel scrolling on the bar straight to the target window under selDip.
func (s *scrollSession) barView(c *ui.Context) {
	c.SetTheme(ui.DarkTheme())
	c.Root().Background(ui.Transparent)
	if s.handleCommonShortcuts(c) {
		return
	}
	s.uiMu.Lock()
	if s.closed {
		s.uiMu.Unlock()
		return
	}
	isAuto := s.autoScroll
	s.uiMu.Unlock()

	localY := s.barCardR.Y - s.barWinOffY
	root := ui.Box(c).Fill()
	root.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind == ui.InputScroll {
			s.forwardWheel(ev.DY, ev.Precise)
			return true
		}
		return false
	})
	root.Children(func() {
		ui.Row(c).Absolute().Top(localY).Left(0).Right(0).Justify(ui.Center).Children(func() {
			s.renderControlBar(c, isAuto)
		})
	})
}

// testCompositeView renders frame + preview + control bar together on one canvas
// for headless ui.Tester click, transparency, and visual QA verification.
func (s *scrollSession) testCompositeView(c *ui.Context) {
	c.SetTheme(ui.DarkTheme())
	c.Root().Background(ui.Transparent)
	if s.handleCommonShortcuts(c) {
		return
	}
	viewW, viewH := c.Size()

	s.uiMu.Lock()
	if s.closed {
		s.uiMu.Unlock()
		return
	}
	stW, stH := s.stitchedW, s.stitchedH
	isAuto := s.autoScroll
	state := s.state
	scrollFrac := s.previewScrollFrac
	s.uiMu.Unlock()

	barR, prevR, showPrev := ComputeScrollOverlayLayout(
		s.selDip.X, s.selDip.Y, s.selDip.X+s.selDip.W, s.selDip.Y+s.selDip.H,
		viewW, viewH, 392,
	)

	ui.Box(c).Fill().Children(func() {
		ui.Box(c).Absolute().Fill().PassThrough().Draw(func(pt *ui.Painter, _ ui.Rect) {
			drawScrollCaptureFrame(pt, s.selDip, state)
		})
		if showPrev {
			s.renderPreviewCard(c, prevR, stW, stH, state, scrollFrac)
		}
		ui.Box(c).Absolute().Left(barR.X).Top(barR.Y).Children(func() {
			s.renderControlBar(c, isAuto)
		})
	})
}

func drawScrollCaptureFrame(pt *ui.Painter, sel ui.Rect, state scrollStateKind) {
	frameCol := ui.Hex("#0A84FF")
	switch state {
	case scrollStateAuto:
		frameCol = ui.Hex("#34C759")
	case scrollStateTooFast:
		frameCol = ui.Hex("#FF9500")
	}

	// Draw 2px frame strictly outside the captured rectangle so pixels inside sel stay untouched.
	outR := ui.Rect{
		X: sel.X - 2,
		Y: sel.Y - 2,
		W: sel.W + 4,
		H: sel.H + 4,
	}
	pt.Stroke(ui.Rect{X: outR.X - 1.5, Y: outR.Y - 1.5, W: outR.W + 3, H: outR.H + 3}, frameCol.Alpha(0.24), 4, 3)
	pt.Stroke(outR, frameCol, 2, 2.0)

	const arm float32 = 14
	const halfW float32 = 1.25
	bx0, by0 := outR.X-1, outR.Y-1
	bx1, by1 := outR.X+outR.W+1, outR.Y+outR.H+1
	drawBrackets := func(strokeW float32, col ui.Color) {
		pt.Line(bx0-halfW, by0, bx0+arm, by0, strokeW, col)
		pt.Line(bx0, by0-halfW, bx0, by0+arm, strokeW, col)
		pt.Line(bx1+halfW, by0, bx1-arm, by0, strokeW, col)
		pt.Line(bx1, by0-halfW, bx1, by0+arm, strokeW, col)
		pt.Line(bx0-halfW, by1, bx0+arm, by1, strokeW, col)
		pt.Line(bx0, by1+halfW, bx0, by1-arm, strokeW, col)
		pt.Line(bx1+halfW, by1, bx1-arm, by1, strokeW, col)
		pt.Line(bx1, by1+halfW, bx1, by1-arm, strokeW, col)
	}
	drawBrackets(4.2, ui.RGBA(0, 0, 0, 0.42))
	drawBrackets(2.5, ui.Hex("#FFFFFF").Alpha(0.95))
}

func (s *scrollSession) renderPreviewCard(c *ui.Context, rect ui.Rect, stW, stH int, state scrollStateKind, scrollFrac float32) {
	scaledTotalH := s.syncPreviewBitmap()
	previewContentH := int(max(120, rect.H-56))

	inks := glasswork.KindDark.Inks()
	cardSurf := glasswork.Card(glasswork.KindDark)
	cardSurf.Floor = 0.78
	cardSurf.Density = 0.78
	statusText, statusColor := scrollStatusInfo(state)

	prevCard := ui.Column(c.Key("scroll-preview-card")).
		Absolute().Left(rect.X).Top(rect.Y).Width(rect.W).Height(rect.H).
		Padding(10).Gap(8).
		Radius(18).
		Material(cardSurf).
		Transition(glasswork.ToolbarAppearTransition(8))
	prevCard.Clicked()

	prevCard.Children(func() {
		ui.Row(c).Height(22).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Size(7, 7).Radius(3.5).Background(statusColor).Shrink(0)
			ui.Text(c, statusText).FontSize(11.5).FontWeight(600).TextColor(inks.Primary).SingleLine().Grow(1)
			ui.Textf(c, "%d×%d", stW, stH).FontSize(11).FontWeight(600).TextColor(inks.Secondary).SingleLine().Shrink(0).
				ContentTransition(glasswork.ContentNumeric)
		})

		vp := ui.Box(c.Key("scroll-preview-vp")).
			Grow(1).Fill().Radius(11).
			Background(ui.Hex("#0D0F14")).
			Border(1, ui.RGBA(255, 255, 255, 0.10))

		vp.HandleInput(func(ev ui.InputEvent) bool {
			if ev.Kind == ui.InputScroll && scaledTotalH > previewContentH {
				step := float32(ev.DY) / float32(max(1, scaledTotalH-previewContentH))
				s.uiMu.Lock()
				s.previewScrollFrac = clamp32(s.previewScrollFrac+step*1.4, 0, 1)
				s.uiMu.Unlock()
				s.requestPreviewRefresh()
				return true
			}
			return false
		})

		vp.Draw(func(pt *ui.Painter, r ui.Rect) {
			if s.previewBmp == nil {
				return
			}
			pt.Clip(r, 10, func() {
				pt.Image(s.previewBmp, r, ui.FillBox)
				if scaledTotalH > int(r.H) && r.H > 32 {
					ratio := clamp32(r.H/float32(scaledTotalH), 0.08, 1.0)
					thumbH := max(22, r.H*ratio)
					thumbY := r.Y + 4 + (r.H-thumbH-8)*scrollFrac
					thumbR := ui.Rect{
						X: r.X + r.W - 7.5,
						Y: thumbY,
						W: 4.5,
						H: thumbH,
					}
					pt.Fill(ui.Rect{X: thumbR.X - 1, Y: thumbR.Y - 1, W: thumbR.W + 2, H: thumbR.H + 2}, ui.RGBA(0, 0, 0, 0.34), 3.2)
					pt.Fill(thumbR, ui.RGBA(255, 255, 255, 0.88), 2.25)
				}
			})
		})

		if scrollFrac < 0.96 && scaledTotalH > previewContentH {
			ui.Row(c).Justify(ui.Center).Children(func() {
				if glassChip(c, inks, "跟踪底部", false) {
					s.uiMu.Lock()
					s.previewScrollFrac = 1.0
					s.uiMu.Unlock()
					s.requestPreviewRefresh()
				}
			})
		}
	})
}

func (s *scrollSession) renderControlBar(c *ui.Context, isAuto bool) {
	inks := glasswork.KindDark.Inks()
	barSurf := glasswork.Dark()
	barSurf.Density = 0.72
	barSurf.Floor = 0.74

	ctrlBar := ui.Row(c.Key("scroll-ctrl-bar")).
		Height(44).
		Padding(5, 8, 5, 8).Gap(3).
		Radius(22).
		Material(barSurf).
		AlignItems(ui.Center).
		Transition(glasswork.ToolbarAppearTransition(6))
	ctrlBar.Clicked()

	ctrlBar.Children(func() {
		autoLabel := "自动滚动"
		autoTip := "自动向下滚动并拼接"
		autoIcon := icons.Play
		if isAuto {
			autoLabel = "暂停滚动"
			autoTip = "暂停自动滚动"
			autoIcon = icons.Pause
		}
		autoBtn := ui.Row(c.Key("scroll-auto-btn")).
			Height(32).Padding(0, 12, 0, 9).Gap(5).Shrink(0).
			Radius(16).AlignItems(ui.Center).
			Role(ui.RoleButton).Label(autoLabel).
			Cursor(ui.CursorPointer).Tooltip(autoTip)
		autoFg := inks.Primary
		if isAuto {
			autoBtn.Background(ui.Hex("#34C759").Alpha(0.24)).Border(1, ui.Hex("#34C759").Alpha(0.65))
			autoFg = ui.Hex("#4ADE80")
		} else if autoBtn.Hovered() {
			autoBtn.Background(inks.Hover).Border(1, ui.Transparent)
		} else {
			autoBtn.Border(1, ui.Transparent)
		}
		autoBtn.Transition(ui.ElementTransition{Colors: true, Animation: &glasswork.AnimColorFade})
		glasswork.ApplyLiquidButton(autoBtn, glasswork.CompactButtonOpts())
		autoBtn.Children(func() {
			ui.Icon(c, autoIcon.For(isAuto || autoBtn.Pressed())).Size(16.5, 16.5).Shrink(0).TextColor(autoFg)
			ui.Text(c, autoLabel).FontSize(12).FontWeight(600).TextColor(autoFg).SingleLine().Shrink(0)
		})
		if autoBtn.Clicked() {
			s.toggleAutoScroll()
		}

		if glassToolBtn(c, inks, icons.Restart, "重置", "重置长截图", false, false) {
			s.resetStitch()
		}

		glassSep(c, inks)

		if glassToolBtn(c, inks, icons.Pen, "编辑", "在工作台编辑长截图", false, false) {
			s.finishEdit()
		}
		if glassToolBtn(c, inks, icons.Pin, "贴图", "贴图到桌面", false, false) {
			s.finishPin()
		}
		if glassToolBtn(c, inks, icons.Save, "保存", "保存长截图", false, false) {
			s.finishSave()
		}
		if glassToolBtn(c, inks, icons.Close, "取消", "取消长截图 (Esc)", false, true) {
			s.close()
		}

		doneBtn := ui.Row(c.Key("scroll-done-btn")).
			Height(32).Padding(0, 14, 0, 11).Gap(5).Margin(0, 0, 0, 4).Shrink(0).
			Radius(16).AlignItems(ui.Center).
			Role(ui.RoleButton).Label("完成").
			Material(glasswork.Tinted(ui.Hex("#0A84FF"))).
			Cursor(ui.CursorPointer).Tooltip("完成并复制到剪贴板")
		glasswork.ApplyLiquidButton(doneBtn, glasswork.PrimaryButtonOpts())
		doneBtn.Children(func() {
			ui.Icon(c, icons.Check.Idle).Size(17, 17).Shrink(0).TextColor(ui.Hex("#FFFFFF"))
			ui.Text(c, "完成").FontSize(12.5).FontWeight(600).TextColor(ui.Hex("#FFFFFF")).SingleLine().Shrink(0)
		})
		if doneBtn.Clicked() {
			s.finishCopy()
		}
	})
}

func scrollStatusInfo(st scrollStateKind) (label string, dot ui.Color) {
	switch st {
	case scrollStateAuto:
		return "自动滚动中", ui.Hex("#34C759")
	case scrollStateBottom:
		return "已到达底部", ui.Hex("#0A84FF")
	case scrollStateTooFast:
		return "请慢速滚动", ui.Hex("#FF9500")
	case scrollStateMaxHeight:
		return "已达最大高度", ui.Hex("#FF9500")
	default:
		return "滚动以拼接", ui.Hex("#0A84FF")
	}
}

func clampIntScroll(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func absIntScroll(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// TestScrollHandle exposes scrolling capture overlay state for headless UI click & render tests.
type TestScrollHandle struct {
	session *scrollSession
	View    func(*ui.Context)
}

// AutoScrolling reports whether automatic scrolling is currently active.
func (h *TestScrollHandle) AutoScrolling() bool {
	h.session.uiMu.Lock()
	defer h.session.uiMu.Unlock()
	return h.session.autoScroll
}

// StitchedHeight reports the current stitched pixel height.
func (h *TestScrollHandle) StitchedHeight() int {
	h.session.uiMu.Lock()
	defer h.session.uiMu.Unlock()
	return h.session.stitchedH
}

// Closed reports whether the scroll session has been closed.
func (h *TestScrollHandle) Closed() bool {
	h.session.uiMu.Lock()
	defer h.session.uiMu.Unlock()
	return h.session.closed
}

// NewTestScrollView builds a headless-testable live scroll capture overlay with
// pre-stitched frames for automated click, layout, and visual QA verification.
func NewTestScrollView(a *App, frames []*image.RGBA) *TestScrollHandle {
	if len(frames) == 0 {
		frames = []*image.RGBA{GenerateSampleCanvas()}
	}
	fw, fh := frames[0].Bounds().Dx(), frames[0].Bounds().Dy()
	disp := mygo.Display{
		Bounds:      mygo.Rectangle{Width: 1120, Height: 680},
		ScaleFactor: 1,
		Primary:     true,
	}
	selDip := ui.Rect{X: 140, Y: 72, W: float32(min(580, fw)), H: float32(min(460, fh))}
	params := ScrollCaptureParams{
		Display:    disp,
		SelDip:     selDip,
		PhysRect:   image.Rect(140, 72, 140+fw, 72+fh),
		FirstFrame: frames[0],
	}
	s := newScrollSession(a, params, frames[0])
	for _, f := range frames[1:] {
		res := s.stitcher.Feed(f)
		if res.Status == stitch.StatusStitched || res.Status == stitch.StatusMaxHeight {
			s.stitchedW = s.stitcher.Width()
			s.stitchedH = res.TotalHeight
			s.framesCount = s.stitcher.FramesAccepted()
		}
	}
	scaledH, _ := s.stitcher.RenderPreview(s.previewReady, 1.0)
	s.previewScaledH = scaledH
	s.previewVer++
	return &TestScrollHandle{session: s, View: s.testCompositeView}
}
