package app

import (
	"fmt"
	"image"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"koma/internal/canvas"
	"koma/internal/glasswork"
	"koma/internal/icons"
	"koma/internal/redact"
	"koma/internal/winapi"
)

type overlaySession struct {
	app      *App
	panes    []*overlayPane
	windows  []winapi.WindowInfo
	longMode bool
	closedMu sync.Mutex
	closed   bool
}

type dragMode int

const (
	dragNone dragMode = iota
	dragCreate
	dragMove
	dragResizeNW
	dragResizeN
	dragResizeNE
	dragResizeE
	dragResizeSE
	dragResizeS
	dragResizeSW
	dragResizeW
)

// overlayPane represents one Per-Monitor DPI-aligned fullscreen frozen desktop
// layer with QQ-screenshot-style in-place Glasswork liquid glass tools.
type overlayPane struct {
	session *overlaySession
	win     *mygo.Window
	display mygo.Display
	capture winapi.MonitorCapture
	bmp     *ui.Bitmap

	loupeRGBA   *image.RGBA
	loupeBmp    *ui.Bitmap
	lastLoupeSX int
	lastLoupeSY int

	selEffectRect image.Rectangle
	selEffectBmp  *ui.Bitmap
	effectDirty   bool

	localWins []localWinRect

	mouseX, mouseY float32
	mouseInside    bool
	snapEnabled    bool
	rulerMode      bool

	hasSelection   bool
	selX0, selY0   float32
	selX1, selY1   float32
	dragMode       dragMode
	dragStartX     float32
	dragStartY     float32
	origX0, origY0 float32
	origX1, origY1 float32
	hoveredWinIdx  int

	// Adaptive Glasswork kind cache & measured bar widths so toolbar never flickers or overflows
	barKind       glasswork.Kind
	hasBarKind    bool
	barSurf       glasswork.Surface
	barInks       glasswork.InkColors
	lastBarRect   ui.Rect
	measuredMainW float32
	measuredSubW  float32
	loupeKind     glasswork.Kind
	hasLoupeKnd   bool
	loupeSurf     glasswork.Surface
	loupeInks     glasswork.InkColors
	lastLoupeAdX  float32
	lastLoupeAdY  float32

	lastMeasure    winapi.MeasureResult
	lastMeasurePxX int
	lastMeasurePxY int

	// Color picker format cycled via Shift before selection (0=HEX, 1=RGB, 2=HSL)
	colorFmt      int
	surfaceHandle ui.Handle

	// In-place QQ-style annotation state (monitor physical pixels)
	activeTool  canvas.Tool
	mosaicStyle canvas.MosaicStyle
	colorIdx    int
	strokeWidth float32
	filled      bool
	rounded     bool
	fontSize    float32
	textDraft   string
	blockSize   float64
	nextAnnID   int
	nextCounter int

	// Offline smart redaction & point-to-redact OCR word cache (scoped to current selection)
	redactMu      sync.Mutex
	redactBusy    bool
	redactGen     int
	ocrWords      []winapi.OCRWord
	ocrLines      []winapi.OCRWord
	ocrSelRect    image.Rectangle
	pendingRedact *pendingRedactOutcome
	redactNotice  string
	redactUntil   time.Time

	annotations []canvas.Annotation
	undoStack   [][]canvas.Annotation
	redoStack   [][]canvas.Annotation

	drawing  bool
	draftAnn canvas.Annotation

	// In-place annotation selection & drag-move state
	selectedAnnIdx    int
	movingAnn         bool
	moveAnnLastPx     canvas.PointF
	moveAnnUndoPushed bool

	// Inline canvas text editor state (sibling of surface so typing never fights selection input)
	textEditing       bool
	textEditIdx       int // -1 for a new text annotation, >= 0 when editing annotations[textEditIdx]
	textEditPx        canvas.PointF
	textEditVal       string
	textEditHandle    ui.Handle
	textEditNeedFocus bool
}

type localWinRect struct {
	title          string
	x0, y0, x1, y1 float32
	pxRect         image.Rectangle
}

func (s *overlaySession) closeAll() {
	s.closedMu.Lock()
	if s.closed {
		s.closedMu.Unlock()
		return
	}
	s.closed = true
	panes := s.panes
	s.panes = nil
	s.windows = nil
	s.closedMu.Unlock()

	s.app.clearOverlaySession(s)
	for _, p := range panes {
		w := p.win
		p.win = nil
		if w != nil {
			w.Close()
		}
		p.releaseBuffers()
	}
	reclaimMemoryAsync()
	s.app.scheduleTrayTrim()
}

type pendingRedactOutcome struct {
	gen       int
	selRect   image.Rectangle
	words     []winapi.OCRWord
	kind      canvas.Tool
	style     canvas.MosaicStyle
	blockSize int
	errMsg    string
}

func (p *overlayPane) releaseBuffers() {
	p.bmp = nil
	p.loupeRGBA = nil
	p.loupeBmp = nil
	p.selEffectBmp = nil
	p.capture.Image = nil
	p.localWins = nil
	p.annotations = nil
	p.undoStack = nil
	p.redoStack = nil
	p.clearRedactCache()
}

func (p *overlayPane) clearRedactCache() {
	p.redactMu.Lock()
	p.redactGen++
	p.redactBusy = false
	p.pendingRedact = nil
	p.redactMu.Unlock()
	p.ocrWords = nil
	p.ocrLines = nil
	p.ocrSelRect = image.Rectangle{}
	p.redactNotice = ""
	p.redactUntil = time.Time{}
}

// StartAreaCapture freezes all monitors instantaneously, detects desktop windows
// via DWM, and launches Glasswork Liquid Glass QQ-style capture overlays.
func (a *App) StartAreaCapture(hideEditorFirst bool) {
	a.startAreaCaptureMode(hideEditorFirst, false)
}

// StartLongCapture opens the capture overlay in direct long-screenshot mode:
// once a window or region is selected, it transitions straight into live scrolling capture.
func (a *App) StartLongCapture(hideEditorFirst bool) {
	a.startAreaCaptureMode(hideEditorFirst, true)
}

func (a *App) startAreaCaptureMode(hideEditorFirst bool, longMode bool) {
	a.mu.Lock()
	if a.activeOverlay != nil || a.activeScroll != nil {
		a.mu.Unlock()
		return
	}
	wasEditorVisible := a.editorWin != nil && !a.editorHidden
	if hideEditorFirst && a.editorWin != nil {
		a.editorWin.Hide()
		a.editorHidden = true
	}
	a.mu.Unlock()

	go func() {
		if hideEditorFirst && wasEditorVisible {
			sleepMs(180)
		}
		captures, err := winapi.CaptureAllMonitors()
		if err != nil || len(captures) == 0 {
			if hideEditorFirst && wasEditorVisible {
				a.ShowEditor()
			}
			return
		}
		winList := winapi.ListVisibleWindows()
		displays := mygo.Screen.Displays()

		session := &overlaySession{
			app:      a,
			windows:  winList,
			longMode: longMode,
		}
		a.mu.Lock()
		a.activeOverlay = session
		a.mu.Unlock()

		pairs := matchDisplaysToCaptures(displays, captures)
		for _, pair := range pairs {
			pane := newOverlayPane(session, pair.display, pair.capture, winList)
			session.panes = append(session.panes, pane)
		}

		for _, pane := range session.panes {
			p := pane
			w := mygo.NewWindow(mygo.WindowOptions{
				Title:         "Koma Capture",
				X:             p.display.Bounds.X,
				Y:             p.display.Bounds.Y,
				Width:         p.display.Bounds.Width,
				Height:        p.display.Bounds.Height,
				Frameless:     true,
				AlwaysOnTop:   true,
				SkipTaskbar:   true,
				DisableShadow: true,
				DisableResize: true,
				DisableMove:   true,
				Content:       ui.View(p.view),
			})
			p.win = w
			w.OnClosed(func() {
				session.closeAll()
			})
		}

		cur := mygo.Screen.CursorScreenPoint()
		for _, p := range session.panes {
			b := p.display.Bounds
			if cur.X >= b.X && cur.X < b.X+b.Width && cur.Y >= b.Y && cur.Y < b.Y+b.Height {
				if p.win != nil {
					p.win.Focus()
				}
				break
			}
		}
	}()
}

// TestOverlayHandle exposes overlay state for automated UI click & render tests.
type TestOverlayHandle struct {
	pane *overlayPane
	View func(*ui.Context)
}

func (h *TestOverlayHandle) ActiveTool() canvas.Tool          { return h.pane.activeTool }
func (h *TestOverlayHandle) MosaicStyle() canvas.MosaicStyle  { return h.pane.mosaicStyle }
func (h *TestOverlayHandle) SetMosaicStyle(s canvas.MosaicStyle) { h.pane.mosaicStyle = s }
func (h *TestOverlayHandle) HasSelection() bool               { return h.pane.hasSelection }
func (h *TestOverlayHandle) ColorIdx() int                    { return h.pane.colorIdx }
func (h *TestOverlayHandle) Annotations() []canvas.Annotation {
	return canvas.CloneAnnotations(h.pane.annotations)
}
func (h *TestOverlayHandle) ApplyOCRWordsForTest(words []winapi.OCRWord) int {
	return h.pane.applySmartRedactWords(words, h.pane.capture.Image.Bounds(), h.pane.activeTool, h.pane.mosaicStyle, int(math.Round(h.pane.blockSize)))
}
func (h *TestOverlayHandle) SelectedAnnIdx() int { return h.pane.selectedAnnIdx }
func (h *TestOverlayHandle) TextEditing() bool   { return h.pane.textEditing }
func (h *TestOverlayHandle) SelectionBounds() (x0, y0, x1, y1 float32) {
	return h.pane.normSel()
}

// ColorFormat identifies the loupe color-picker display & clipboard format.
const (
	ColorFormatHEX = 0
	ColorFormatRGB = 1
	ColorFormatHSL = 2
)

// FormatPickedColor formats an RGB pixel in HEX, RGB, or HSL notation.
func FormatPickedColor(r, g, b uint8, fmtIdx int) string {
	switch fmtIdx % 3 {
	case ColorFormatRGB:
		return fmt.Sprintf("rgb(%d, %d, %d)", r, g, b)
	case ColorFormatHSL:
		h, s, l := rgbToHSL(r, g, b)
		return fmt.Sprintf("hsl(%d, %d%%, %d%%)", h, s, l)
	default:
		return fmt.Sprintf("#%02X%02X%02X", r, g, b)
	}
}

func rgbToHSL(r, g, b uint8) (h, s, l int) {
	rf := float64(r) / 255.0
	gf := float64(g) / 255.0
	bf := float64(b) / 255.0
	mx := math.Max(rf, math.Max(gf, bf))
	mn := math.Min(rf, math.Min(gf, bf))
	lf := (mx + mn) * 0.5
	var hf, sf float64
	if mx != mn {
		d := mx - mn
		if lf > 0.5 {
			sf = d / (2.0 - mx - mn)
		} else {
			sf = d / (mx + mn)
		}
		switch mx {
		case rf:
			hf = (gf - bf) / d
			if gf < bf {
				hf += 6
			}
		case gf:
			hf = (bf-rf)/d + 2
		default:
			hf = (rf-gf)/d + 4
		}
		hf *= 60
	}
	return int(math.Round(hf)) % 360, int(math.Round(sf * 100)), int(math.Round(lf * 100))
}

// SetSelectionBounds overrides the test overlay's selected region (e.g. fullscreen testing).
func (h *TestOverlayHandle) SetSelectionBounds(x0, y0, x1, y1 float32) {
	h.pane.selX0, h.pane.selY0, h.pane.selX1, h.pane.selY1 = x0, y0, x1, y1
	h.pane.hasSelection = true
	h.pane.hasBarKind = false
}

// ComputeToolbarLayout determines the floating QQ toolbar coordinates so BOTH the main bar
// and secondary bar stay strictly inside the viewport even on 100% fullscreen selections.
func ComputeToolbarLayout(x0, y0, x1, y1, viewW, viewH, mainW, subW float32, showSub bool) (barX, barY, subX, subY float32, placeSubBelow bool) {
	const (
		mainBarH float32 = 44
		subBarH  float32 = 40
		margin   float32 = 16
		gap      float32 = 8
	)
	if mainW < 520 {
		mainW = 740
	}
	if subW < 180 {
		subW = 420
	}

	totalH := mainBarH
	if showSub {
		totalH += gap + subBarH
	}

	maxBarX := max(margin, viewW-mainW-margin)
	barX = clamp32(x1-mainW, margin, maxBarX)

	maxSubX := max(margin, viewW-subW-margin)
	subX = clamp32(barX, margin, maxSubX)

	switch {
	case y1+margin+totalH <= viewH-margin:
		// 1. Classic QQ position: below selection
		barY = y1 + margin
		placeSubBelow = true
	case y0-margin-totalH >= margin:
		// 2. Above selection (with secondary bar stacked above main bar)
		barY = y0 - mainBarH - margin
		placeSubBelow = false
	default:
		// 3. Near-fullscreen or fullscreen selection: dock cleanly INSIDE the bottom of selection
		barY = y1 - totalH - margin
		placeSubBelow = true
	}

	if placeSubBelow {
		barY = clamp32(barY, margin, max(margin, viewH-totalH-margin))
		subY = clamp32(barY+mainBarH+gap, margin, max(margin, viewH-subBarH-margin))
	} else {
		minMainY := margin
		if showSub {
			minMainY = margin + subBarH + gap
		}
		barY = clamp32(barY, minMainY, max(minMainY, viewH-mainBarH-margin))
		subY = clamp32(barY-subBarH-gap, margin, max(margin, viewH-subBarH-margin))
	}
	return barX, barY, subX, subY, placeSubBelow
}

// NewTestOverlayView builds a headless-testable overlay view with a preselected
// region and tool state for visual QA and automated click verification.
func NewTestOverlayView(a *App, img *image.RGBA, activeTool canvas.Tool) *TestOverlayHandle {
	session := &overlaySession{app: a}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	disp := mygo.Display{
		Bounds:      mygo.Rectangle{Width: w, Height: h},
		ScaleFactor: 1,
		Primary:     true,
	}
	cap := winapi.MonitorCapture{
		Monitor: winapi.Monitor{Bounds: image.Rect(0, 0, w, h), DPI: 96, Scale: 1, Primary: true},
		Image:   img,
	}
	p := newOverlayPane(session, disp, cap, nil)
	p.hasSelection = true
	p.selX0, p.selY0, p.selX1, p.selY1 = 140, 70, 740, 410
	p.activeTool = activeTool
	p.annotations = []canvas.Annotation{
		{
			ID:        1,
			Kind:      canvas.ToolPixelate,
			Start:     canvas.PointF{X: 658, Y: 126},
			End:       canvas.PointF{X: 735, Y: 158},
			BlockSize: 10,
		},
		{
			ID:          2,
			Kind:        canvas.ToolArrow,
			Start:       canvas.PointF{X: 540, Y: 132},
			End:         canvas.PointF{X: 642, Y: 140},
			Color:       ui.Hex("#FF3B30"),
			StrokeWidth: 4,
		},
		{
			ID:          3,
			Kind:        canvas.ToolRect,
			Start:       canvas.PointF{X: 238, Y: 80},
			End:         canvas.PointF{X: 430, Y: 192},
			Color:       ui.Hex("#007AFF"),
			StrokeWidth: 3,
			Rounded:     true,
		},
		{
			ID:         4,
			Kind:       canvas.ToolCounter,
			Start:      canvas.PointF{X: 242, Y: 84},
			Color:      ui.Hex("#007AFF"),
			CounterNum: 1,
		},
	}
	p.effectDirty = true
	return &TestOverlayHandle{pane: p, View: p.view}
}

type displayCapturePair struct {
	display mygo.Display
	capture winapi.MonitorCapture
}

func matchDisplaysToCaptures(displays []mygo.Display, captures []winapi.MonitorCapture) []displayCapturePair {
	if len(displays) == 0 {
		c := captures[0]
		scale := c.Monitor.Scale
		if scale <= 0 {
			scale = 1
		}
		return []displayCapturePair{{
			display: mygo.Display{
				Bounds: mygo.Rectangle{
					Width:  int(math.Round(float64(c.Monitor.Bounds.Dx()) / scale)),
					Height: int(math.Round(float64(c.Monitor.Bounds.Dy()) / scale)),
				},
				ScaleFactor: scale,
				Primary:     true,
			},
			capture: c,
		}}
	}

	minDPI := 96
	for i, c := range captures {
		dpi := c.Monitor.DPI
		if dpi <= 0 {
			dpi = 96
		}
		if i == 0 || dpi < minDPI {
			minDPI = dpi
		}
	}

	used := make([]bool, len(captures))
	pairs := make([]displayCapturePair, 0, len(displays))
	for _, d := range displays {
		bestIdx := -1
		bestScore := math.MaxFloat64
		for i, c := range captures {
			if used[i] {
				continue
			}
			expX := float64(c.Monitor.Bounds.Min.X) * 96.0 / float64(minDPI)
			expY := float64(c.Monitor.Bounds.Min.Y) * 96.0 / float64(minDPI)
			dpi := float64(c.Monitor.DPI)
			if dpi <= 0 {
				dpi = 96
			}
			expW := float64(c.Monitor.Bounds.Dx()) * 96.0 / dpi
			expH := float64(c.Monitor.Bounds.Dy()) * 96.0 / dpi
			score := math.Abs(expX-float64(d.Bounds.X)) +
				math.Abs(expY-float64(d.Bounds.Y)) +
				math.Abs(expW-float64(d.Bounds.Width)) +
				math.Abs(expH-float64(d.Bounds.Height))
			if d.Primary != c.Monitor.Primary {
				score += 500
			}
			if score < bestScore {
				bestScore = score
				bestIdx = i
			}
		}
		if bestIdx >= 0 {
			used[bestIdx] = true
			pairs = append(pairs, displayCapturePair{display: d, capture: captures[bestIdx]})
		}
	}
	return pairs
}

func newOverlayPane(session *overlaySession, d mygo.Display, cap winapi.MonitorCapture, wins []winapi.WindowInfo) *overlayPane {
	scaleX := float32(cap.Monitor.Bounds.Dx()) / max(float32(1), float32(d.Bounds.Width))
	scaleY := float32(cap.Monitor.Bounds.Dy()) / max(float32(1), float32(d.Bounds.Height))

	var local []localWinRect
	monB := cap.Monitor.Bounds
	for _, w := range wins {
		inter := w.Bounds.Intersect(monB)
		if inter.Dx() < 24 || inter.Dy() < 24 {
			continue
		}
		relPx := image.Rect(
			inter.Min.X-monB.Min.X,
			inter.Min.Y-monB.Min.Y,
			inter.Max.X-monB.Min.X,
			inter.Max.Y-monB.Min.Y,
		)
		local = append(local, localWinRect{
			title:  w.Title,
			x0:     float32(relPx.Min.X) / scaleX,
			y0:     float32(relPx.Min.Y) / scaleY,
			x1:     float32(relPx.Max.X) / scaleX,
			y1:     float32(relPx.Max.Y) / scaleY,
			pxRect: relPx,
		})
	}

	return &overlayPane{
		session:        session,
		display:        d,
		capture:        cap,
		bmp:            ui.NewBitmap(cap.Image),
		lastLoupeSX:    -1,
		lastLoupeSY:    -1,
		localWins:      local,
		snapEnabled:    true,
		hoveredWinIdx:  -1,
		lastMeasurePxX: -1,
		lastMeasurePxY: -1,
		activeTool:     canvas.ToolSelect,
		colorIdx:       0,
		strokeWidth:    4,
		rounded:        true,
		fontSize:       20,
		textDraft:      "标注",
		blockSize:      14,
		nextCounter:    1,
		selectedAnnIdx: -1,
		textEditIdx:    -1,
	}
}

func (p *overlayPane) findPaletteIndex(col ui.Color) int {
	for i, sw := range canvas.DefaultPalette {
		if sw.Color == col {
			return i
		}
	}
	return p.colorIdx
}

func (p *overlayPane) beginInlineTextNew(px, py float32) {
	p.commitTextEdit()
	p.selectedAnnIdx = -1
	p.textEditing = true
	p.textEditIdx = -1
	p.textEditPx = canvas.PointF{X: px, Y: py}
	p.textEditVal = ""
	p.textEditNeedFocus = true
}

func (p *overlayPane) beginInlineTextEdit(idx int) {
	if idx < 0 || idx >= len(p.annotations) || p.annotations[idx].Kind != canvas.ToolText {
		return
	}
	p.commitTextEdit()
	ann := p.annotations[idx]
	p.activeTool = canvas.ToolText
	p.selectedAnnIdx = -1
	p.textEditing = true
	p.textEditIdx = idx
	p.textEditPx = ann.Start
	p.textEditVal = ann.Text
	if ann.FontSize > 0 {
		p.fontSize = ann.FontSize
	}
	p.filled = ann.Filled
	p.colorIdx = p.findPaletteIndex(ann.Color)
	p.textEditNeedFocus = true
}

func (p *overlayPane) commitTextEdit() {
	if !p.textEditing {
		return
	}
	val := strings.TrimRight(p.textEditVal, "\r\n")
	idx := p.textEditIdx
	pos := p.textEditPx

	p.textEditing = false
	p.textEditIdx = -1
	p.textEditVal = ""
	p.textEditNeedFocus = false

	if strings.TrimSpace(val) == "" {
		// Clearing an existing text annotation removes it cleanly.
		if idx >= 0 && idx < len(p.annotations) {
			p.pushUndo()
			p.annotations = append(p.annotations[:idx], p.annotations[idx+1:]...)
			p.selectedAnnIdx = -1
		}
		return
	}

	p.pushUndo()
	if idx >= 0 && idx < len(p.annotations) {
		p.annotations[idx].Text = val
		p.annotations[idx].FontSize = p.fontSize
		p.annotations[idx].Filled = p.filled
		p.annotations[idx].Color = p.activeColor()
		p.selectedAnnIdx = idx
		return
	}

	p.nextAnnID++
	p.annotations = append(p.annotations, canvas.Annotation{
		ID:       p.nextAnnID,
		Kind:     canvas.ToolText,
		Start:    pos,
		Color:    p.activeColor(),
		FontSize: p.fontSize,
		Filled:   p.filled,
		Text:     val,
	})
	p.selectedAnnIdx = len(p.annotations) - 1
}

func (p *overlayPane) cancelTextEdit() {
	if !p.textEditing {
		return
	}
	p.textEditing = false
	p.textEditIdx = -1
	p.textEditVal = ""
	p.textEditNeedFocus = false
}

func (p *overlayPane) nudgeSelection(dxPx, dyPx int, resizeEnd, resizeStart bool, viewW, viewH float32) {
	if !p.hasSelection || viewW <= 0 || viewH <= 0 || p.capture.Image == nil {
		return
	}
	iw := float32(max(1, p.capture.Image.Bounds().Dx()))
	ih := float32(max(1, p.capture.Image.Bounds().Dy()))
	dx := float32(dxPx) * viewW / iw
	dy := float32(dyPx) * viewH / ih

	x0, y0, x1, y1 := p.normSel()
	const minDim float32 = 8
	switch {
	case resizeEnd:
		x1 = clamp32(x1+dx, x0+minDim, viewW)
		y1 = clamp32(y1+dy, y0+minDim, viewH)
	case resizeStart:
		x0 = clamp32(x0+dx, 0, x1-minDim)
		y0 = clamp32(y0+dy, 0, y1-minDim)
	default:
		w, h := x1-x0, y1-y0
		x0 = clamp32(x0+dx, 0, max(0, viewW-w))
		y0 = clamp32(y0+dy, 0, max(0, viewH-h))
		x1, y1 = x0+w, y0+h
	}
	p.selX0, p.selY0, p.selX1, p.selY1 = x0, y0, x1, y1
	p.effectDirty = true
	p.clearRedactCache()
}

func (p *overlayPane) activeColor() ui.Color {
	if p.colorIdx < 0 || p.colorIdx >= len(canvas.DefaultPalette) {
		return canvas.DefaultPalette[0].Color
	}
	return canvas.DefaultPalette[p.colorIdx].Color
}

func (p *overlayPane) normSel() (x0, y0, x1, y1 float32) {
	x0, x1 = p.selX0, p.selX1
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	y0, y1 = p.selY0, p.selY1
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	return x0, y0, x1, y1
}

func (p *overlayPane) dipToPxF(dx, dy, viewW, viewH float32) (float32, float32) {
	iw := float32(p.capture.Image.Bounds().Dx())
	ih := float32(p.capture.Image.Bounds().Dy())
	if viewW <= 0 || viewH <= 0 {
		return 0, 0
	}
	return dx * iw / viewW, dy * ih / viewH
}

func (p *overlayPane) dipToPx(dx, dy, viewW, viewH float32) (int, int) {
	px, py := p.dipToPxF(dx, dy, viewW, viewH)
	iw, ih := p.capture.Image.Bounds().Dx(), p.capture.Image.Bounds().Dy()
	return winapi.ClampInt(int(math.Round(float64(px))), 0, iw-1),
		winapi.ClampInt(int(math.Round(float64(py))), 0, ih-1)
}

func (p *overlayPane) dipRectToPx(r ui.Rect, viewW, viewH float32) image.Rectangle {
	x0, y0 := p.dipToPx(r.X, r.Y, viewW, viewH)
	x1, y1 := p.dipToPx(r.X+r.W, r.Y+r.H, viewW, viewH)
	return image.Rect(x0, y0, max(x0+1, x1), max(y0+1, y1))
}

// adaptGlassForRect runs Glasswork's 5x3 adapt.js sampler on the frozen desktop
// region directly behind a floating surface, picking Light or Dark crystal glass
// with WCAG 4.5:1 ink legibility!
func (p *overlayPane) adaptGlassForRect(r ui.Rect, viewW, viewH float32, prev *glasswork.Kind, hasPrev *bool) (glasswork.Surface, glasswork.InkColors) {
	pxBox := p.dipRectToPx(r, viewW, viewH)
	samp, ok := glasswork.SampleRegion(p.capture.Image, pxBox)
	if !ok {
		return glasswork.Dark(), glasswork.KindDark.Inks()
	}
	kind := glasswork.PickKind(samp.Luminance, *prev, *hasPrev)
	*prev = kind
	*hasPrev = true
	ad := glasswork.Adapt(samp, kind)
	return glasswork.FromAdaptation(ad), kind.Inks()
}

func (p *overlayPane) selectionPxRect(viewW, viewH float32) image.Rectangle {
	x0, y0, x1, y1 := p.normSel()
	iw, ih := p.capture.Image.Bounds().Dx(), p.capture.Image.Bounds().Dy()
	if viewW <= 0 || viewH <= 0 {
		return image.Rectangle{}
	}
	px0 := winapi.ClampInt(int(math.Round(float64(x0)*float64(iw)/float64(viewW))), 0, iw)
	py0 := winapi.ClampInt(int(math.Round(float64(y0)*float64(ih)/float64(viewH))), 0, ih)
	px1 := winapi.ClampInt(int(math.Round(float64(x1)*float64(iw)/float64(viewW))), 0, iw)
	py1 := winapi.ClampInt(int(math.Round(float64(y1)*float64(ih)/float64(viewH))), 0, ih)
	return image.Rect(px0, py0, px1, py1).Canon()
}

func (p *overlayPane) shiftedAnnotations(selPx image.Rectangle) []canvas.Annotation {
	if len(p.annotations) == 0 {
		return nil
	}
	out := canvas.CloneAnnotations(p.annotations)
	dx, dy := -float32(selPx.Min.X), -float32(selPx.Min.Y)
	for i := range out {
		out[i].Translate(dx, dy)
	}
	return out
}

func (p *overlayPane) hasRasterAnnotations() bool {
	return canvas.HasRasterAnnotations(p.annotations)
}

func (p *overlayPane) ensureRasterCache(viewW, viewH float32) {
	if !p.hasSelection || !p.hasRasterAnnotations() {
		p.selEffectBmp = nil
		p.effectDirty = false
		return
	}
	// Avoid thrashing full-selection CropRGBA + blur/pixelate allocations on every
	// pointer-move frame while dragging/resizing the selection or moving a raster annotation.
	if p.dragMode != dragNone || p.movingAnn {
		return
	}
	r := p.selectionPxRect(viewW, viewH)
	if r.Dx() < 2 || r.Dy() < 2 {
		p.selEffectBmp = nil
		return
	}
	if !p.effectDirty && p.selEffectBmp != nil && p.selEffectRect == r {
		return
	}
	cropped := winapi.CropRGBA(p.capture.Image, r)
	if cropped == nil {
		return
	}
	processed := canvas.RenderRasterEffects(cropped, p.shiftedAnnotations(r))
	if p.selEffectBmp != nil {
		bw, bh := p.selEffectBmp.Size()
		if bw == processed.Bounds().Dx() && bh == processed.Bounds().Dy() {
			p.selEffectBmp.Update(processed)
		} else {
			p.selEffectBmp = ui.NewBitmap(processed)
		}
	} else {
		p.selEffectBmp = ui.NewBitmap(processed)
	}
	p.selEffectRect = r
	p.effectDirty = false
}

func (p *overlayPane) renderFinalSelection(viewW, viewH float32) *image.RGBA {
	r := p.selectionPxRect(viewW, viewH)
	if r.Dx() < 2 || r.Dy() < 2 {
		return nil
	}
	cropped := winapi.CropRGBA(p.capture.Image, r)
	if cropped == nil {
		return nil
	}
	if len(p.annotations) == 0 {
		return cropped
	}
	return canvas.RenderComposite(cropped, p.shiftedAnnotations(r))
}

func (p *overlayPane) pushUndo() {
	p.undoStack = append(p.undoStack, canvas.CloneAnnotations(p.annotations))
	if len(p.undoStack) > 24 {
		p.undoStack = p.undoStack[len(p.undoStack)-24:]
	}
	p.redoStack = nil
}

func (p *overlayPane) undo() {
	p.cancelTextEdit()
	if len(p.undoStack) == 0 {
		return
	}
	p.redoStack = append(p.redoStack, canvas.CloneAnnotations(p.annotations))
	p.annotations = p.undoStack[len(p.undoStack)-1]
	p.undoStack = p.undoStack[:len(p.undoStack)-1]
	p.selectedAnnIdx = -1
	p.effectDirty = true
}

func (p *overlayPane) redo() {
	p.cancelTextEdit()
	if len(p.redoStack) == 0 {
		return
	}
	p.undoStack = append(p.undoStack, canvas.CloneAnnotations(p.annotations))
	p.annotations = p.redoStack[len(p.redoStack)-1]
	p.redoStack = p.redoStack[:len(p.redoStack)-1]
	p.selectedAnnIdx = -1
	p.effectDirty = true
}

func (p *overlayPane) commitCopy(viewW, viewH float32) {
	p.commitTextEdit()
	finalImg := p.renderFinalSelection(viewW, viewH)
	if finalImg == nil {
		return
	}
	app := p.session.app
	p.session.closeAll()
	pngBytes, err := canvas.EncodePNG(finalImg)
	if err == nil {
		_ = mygo.Clipboard.WriteImage(pngBytes)
	}
	app.AddHistoryEntryWithPNG(finalImg, pngBytes, "快捷截图")
	app.NotifyToast(fmt.Sprintf("已复制 %d × %d 到剪贴板", finalImg.Bounds().Dx(), finalImg.Bounds().Dy()))
}

func (p *overlayPane) commitEdit(viewW, viewH float32) {
	p.commitTextEdit()
	r := p.selectionPxRect(viewW, viewH)
	if r.Dx() < 2 || r.Dy() < 2 {
		return
	}
	cropped := winapi.CropRGBA(p.capture.Image, r)
	anns := p.shiftedAnnotations(r)
	app := p.session.app
	p.session.closeAll()
	app.OpenImageInEditorWithAnnotations(cropped, anns, "截图编辑")
}

func (p *overlayPane) commitPin(viewW, viewH float32) {
	p.commitTextEdit()
	finalImg := p.renderFinalSelection(viewW, viewH)
	if finalImg == nil {
		return
	}
	x0, y0, _, _ := p.normSel()
	pinX := p.display.Bounds.X + int(math.Round(float64(x0)))
	pinY := p.display.Bounds.Y + int(math.Round(float64(y0)))
	scale := p.display.ScaleFactor
	if scale <= 0 {
		scale = 1
	}
	app := p.session.app
	p.session.closeAll()
	pngBytes, _ := canvas.EncodePNG(finalImg)
	app.AddHistoryEntryWithPNG(finalImg, pngBytes, "桌面贴图")
	app.OpenPinWithPNG(finalImg, pngBytes, pinX, pinY, scale)
}

func (p *overlayPane) commitSave(viewW, viewH float32) {
	p.commitTextEdit()
	finalImg := p.renderFinalSelection(viewW, viewH)
	if finalImg == nil {
		return
	}
	app := p.session.app
	p.session.closeAll()
	app.AddHistoryEntry(finalImg, "已保存截图")
	go app.SaveImagePreferred(finalImg, nil, nil)
}

func (p *overlayPane) commitOCR(viewW, viewH float32) {
	p.commitTextEdit()
	finalImg := p.renderFinalSelection(viewW, viewH)
	if finalImg == nil {
		return
	}
	app := p.session.app
	p.session.closeAll()
	app.RecognizeTextFromImage(finalImg)
}

func (p *overlayPane) commitLongCapture(viewW, viewH float32) {
	p.commitTextEdit()
	relPx := p.selectionPxRect(viewW, viewH)
	if relPx.Dx() < 24 || relPx.Dy() < 24 {
		return
	}
	x0, y0, x1, y1 := p.normSel()
	firstFrame := winapi.CropRGBA(p.capture.Image, relPx)
	physRect := relPx.Add(p.capture.Monitor.Bounds.Min)
	selDip := ui.Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
	disp := p.display
	app := p.session.app

	// Hand foreground focus directly to the target window underneath the selected
	// region WHILE Koma's overlay still holds the Windows foreground lock!
	cx := (physRect.Min.X + physRect.Max.X) / 2
	cy := (physRect.Min.Y + physRect.Max.Y) / 2
	winapi.ActivateWindowAt(cx, cy)

	p.session.closeAll()
	go func() {
		sleepMs(40)
		app.StartScrollCapture(ScrollCaptureParams{
			Display:    disp,
			SelDip:     selDip,
			PhysRect:   physRect,
			FirstFrame: firstFrame,
		})
	}()
}

func (p *overlayPane) resetSelection() {
	p.cancelTextEdit()
	p.hasSelection = false
	p.hasBarKind = false
	p.dragMode = dragNone
	p.drawing = false
	p.movingAnn = false
	p.selectedAnnIdx = -1
	p.activeTool = canvas.ToolSelect
	p.annotations = nil
	p.undoStack = nil
	p.redoStack = nil
	p.nextCounter = 1
	p.selEffectBmp = nil
	p.clearRedactCache()
}

func (p *overlayPane) setRedactNotice(msg string) {
	p.redactNotice = msg
	p.redactUntil = time.Now().Add(2600 * time.Millisecond)
}

func (p *overlayPane) isRedactBusy() bool {
	p.redactMu.Lock()
	busy := p.redactBusy
	p.redactMu.Unlock()
	return busy
}

func (p *overlayPane) drainPendingRedact() {
	p.redactMu.Lock()
	out := p.pendingRedact
	gen := p.redactGen
	p.pendingRedact = nil
	p.redactMu.Unlock()
	if out == nil || out.gen != gen {
		return
	}
	if out.errMsg != "" {
		p.setRedactNotice(out.errMsg)
		return
	}
	n := p.applySmartRedactWords(out.words, out.selRect, out.kind, out.style, out.blockSize)
	if n > 0 {
		p.setRedactNotice(fmt.Sprintf("已智能打码 %d 处", n))
	} else {
		p.setRedactNotice("未发现敏感信息 · 点击文字可单独打码")
	}
}

func (p *overlayPane) applySmartRedactWords(words []winapi.OCRWord, selRect image.Rectangle, kind canvas.Tool, style canvas.MosaicStyle, blockSize int) int {
	if p.capture.Image == nil {
		return 0
	}
	// Shift OCR words from selection-local coords into full monitor pixel space and
	// build semantically split, ink-tightened interactive word & whole-line caches.
	shifted := make([]winapi.OCRWord, 0, len(words))
	for _, w := range words {
		shifted = append(shifted, winapi.OCRWord{
			Text: w.Text,
			Rect: w.Rect.Add(selRect.Min),
		})
	}
	p.ocrWords, p.ocrLines = redact.BuildInteractiveTokens(shifted, p.capture.Image)
	p.ocrSelRect = selRect

	var customKWs []string
	if p.session != nil && p.session.app != nil {
		customKWs = redact.ParseKeywords(p.session.app.RedactKeywords())
	}
	localBounds := image.Rect(0, 0, selRect.Dx(), selRect.Dy())
	candidates := buildRedactionAnnotations(words, localBounds, p.capture.Image, selRect.Min.X, selRect.Min.Y, customKWs, kind, style, blockSize, &p.nextAnnID)
	added := filterNonOverlappingRedactions(p.annotations, candidates)
	if len(added) == 0 {
		return 0
	}
	p.pushUndo()
	p.annotations = append(p.annotations, added...)
	p.selectedAnnIdx = -1
	p.effectDirty = true
	return len(added)
}

func (p *overlayPane) triggerSmartRedact(viewW, viewH float32) {
	if !p.hasSelection || p.capture.Image == nil {
		return
	}
	p.commitTextEdit()
	kind := p.activeTool
	if kind != canvas.ToolBlur {
		kind = canvas.ToolPixelate
		p.activeTool = canvas.ToolPixelate
	}
	style := p.mosaicStyle
	bs := int(math.Round(p.blockSize))
	relPx := p.selectionPxRect(viewW, viewH)
	if relPx.Dx() < 8 || relPx.Dy() < 8 {
		return
	}

	// Fast path: reuse already-refined cached OCR words without re-running token/ink passes.
	if len(p.ocrWords) > 0 && p.ocrSelRect == relPx {
		var customKWs []string
		if p.session != nil && p.session.app != nil {
			customKWs = redact.ParseKeywords(p.session.app.RedactKeywords())
		}
		candidates := buildRedactionAnnotations(p.ocrWords, p.capture.Image.Bounds(), p.capture.Image, 0, 0, customKWs, kind, style, bs, &p.nextAnnID)
		added := filterNonOverlappingRedactions(p.annotations, candidates)
		if len(added) > 0 {
			p.pushUndo()
			p.annotations = append(p.annotations, added...)
			p.selectedAnnIdx = -1
			p.effectDirty = true
			p.setRedactNotice(fmt.Sprintf("已智能打码 %d 处", len(added)))
		} else {
			p.setRedactNotice("未发现新的敏感信息")
		}
		return
	}

	p.redactMu.Lock()
	if p.redactBusy {
		p.redactMu.Unlock()
		return
	}
	p.redactBusy = true
	p.redactGen++
	gen := p.redactGen
	p.redactMu.Unlock()

	crop := winapi.CropRGBA(p.capture.Image, relPx)
	appRef := p.session.app
	winRef := p.win
	go func() {
		var out pendingRedactOutcome
		out.gen = gen
		out.selRect = relPx
		out.kind = kind
		out.style = style
		out.blockSize = bs
		if appRef == nil {
			out.errMsg = "离线识别不可用"
		} else {
			res, _, err := appRef.RecognizeOCR(crop)
			if err != nil {
				out.errMsg = "识别失败"
			} else {
				out.words = res.Words
			}
		}
		p.redactMu.Lock()
		if p.redactGen == gen {
			p.redactBusy = false
			p.pendingRedact = &out
		}
		p.redactMu.Unlock()
		if winRef != nil {
			winRef.Update(func() {})
		}
	}()
}

func filterNonOverlappingRedactions(existing, candidates []canvas.Annotation) []canvas.Annotation {
	if len(candidates) == 0 {
		return nil
	}
	out := make([]canvas.Annotation, 0, len(candidates))
	for _, cand := range candidates {
		cMinX, cMinY, cMaxX, cMaxY := cand.Bounds()
		cr := image.Rect(int(cMinX), int(cMinY), int(cMaxX), int(cMaxY))
		cArea := cr.Dx() * cr.Dy()
		if cArea <= 0 {
			continue
		}
		dup := false
		for _, ex := range existing {
			if ex.Kind != canvas.ToolPixelate && ex.Kind != canvas.ToolBlur {
				continue
			}
			eMinX, eMinY, eMaxX, eMaxY := ex.Bounds()
			er := image.Rect(int(eMinX), int(eMinY), int(eMaxX), int(eMaxY))
			inter := cr.Intersect(er)
			if !inter.Empty() && float64(inter.Dx()*inter.Dy())/float64(cArea) >= 0.72 {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, cand)
		}
	}
	return out
}

func (p *overlayPane) hitHandle(mx, my float32) dragMode {
	if !p.hasSelection {
		return dragNone
	}
	x0, y0, x1, y1 := p.normSel()
	cx, cy := (x0+x1)/2, (y0+y1)/2
	const tol = 9
	near := func(ax, ay float32) bool {
		return math.Abs(float64(mx-ax)) <= tol && math.Abs(float64(my-ay)) <= tol
	}
	switch {
	case near(x0, y0):
		return dragResizeNW
	case near(x1, y0):
		return dragResizeNE
	case near(x0, y1):
		return dragResizeSW
	case near(x1, y1):
		return dragResizeSE
	case near(cx, y0):
		return dragResizeN
	case near(cx, y1):
		return dragResizeS
	case near(x0, cy):
		return dragResizeW
	case near(x1, cy):
		return dragResizeE
	case mx >= x0 && mx <= x1 && my >= y0 && my <= y1:
		return dragMove
	default:
		return dragNone
	}
}

func (p *overlayPane) findHoveredWindow(mx, my float32) int {
	for i, lw := range p.localWins {
		if mx >= lw.x0 && mx <= lw.x1 && my >= lw.y0 && my <= lw.y1 {
			return i
		}
	}
	return -1
}

func (p *overlayPane) view(c *ui.Context) {
	if p.bmp == nil || p.capture.Image == nil {
		return
	}
	c.SetTheme(ui.DarkTheme())
	viewW, viewH := c.Size()
	p.drainPendingRedact()

	// Keyboard shortcuts (instant, zero animation delay)
	if c.Shortcut(0, ui.KeyEscape) {
		if p.textEditing {
			p.cancelTextEdit()
			return
		}
		p.session.closeAll()
		return
	}
	if !p.hasSelection {
		if c.Shortcut(0, ui.KeySpace) {
			p.snapEnabled = !p.snapEnabled
		}
		if c.Shortcut(0, ui.KeyTab) {
			p.colorFmt = (p.colorFmt + 1) % 3
		}
		if c.Shortcut(0, ui.KeyM) {
			p.rulerMode = !p.rulerMode
			p.lastMeasurePxX = -1
		}
		if c.Shortcut(0, ui.KeyC) && p.mouseInside {
			px, py := p.dipToPx(p.mouseX, p.mouseY, viewW, viewH)
			if r, g, b, ok := winapi.ColorAt(p.capture.Image, px, py); ok {
				colStr := FormatPickedColor(r, g, b, p.colorFmt)
				mygo.Clipboard.WriteText(colStr)
				p.session.app.NotifyToast("已复制色号 " + colStr)
				p.session.closeAll()
				return
			}
		}
	} else {
		if c.Shortcut(0, ui.KeyEnter) && !c.FocusTakesText() {
			p.commitCopy(viewW, viewH)
			return
		}
		if c.Shortcut(ui.Ctrl, ui.KeyC) && !c.FocusTakesText() {
			p.commitCopy(viewW, viewH)
			return
		}
		if c.Shortcut(ui.Ctrl, ui.KeyS) {
			p.commitSave(viewW, viewH)
			return
		}
		if c.Shortcut(0, ui.KeyE) && !c.FocusTakesText() {
			p.commitEdit(viewW, viewH)
			return
		}
		if c.Shortcut(0, ui.KeyL) && !c.FocusTakesText() {
			p.commitLongCapture(viewW, viewH)
			return
		}
		if c.Shortcut(ui.Ctrl, ui.KeyZ) && !c.FocusTakesText() {
			p.undo()
		}
		if (c.Shortcut(ui.Ctrl, ui.KeyY) || c.Shortcut(ui.Ctrl|ui.Shift, ui.KeyZ)) && !c.FocusTakesText() {
			p.redo()
		}
		if c.Shortcut(ui.Shift, ui.KeyX) && !c.FocusTakesText() {
			p.triggerSmartRedact(viewW, viewH)
		}
		if !c.FocusTakesText() {
			if (c.Shortcut(0, ui.KeyDelete) || c.Shortcut(0, ui.KeyBackspace)) && p.selectedAnnIdx >= 0 && p.selectedAnnIdx < len(p.annotations) {
				p.pushUndo()
				p.annotations = append(p.annotations[:p.selectedAnnIdx], p.annotations[p.selectedAnnIdx+1:]...)
				p.selectedAnnIdx = -1
				p.effectDirty = true
			}

			// Pixel-precise keyboard nudging for selected annotation or selection box
			nudgeDirs := []struct {
				key    ui.Key
				dx, dy int
			}{
				{ui.KeyLeft, -1, 0},
				{ui.KeyRight, 1, 0},
				{ui.KeyUp, 0, -1},
				{ui.KeyDown, 0, 1},
			}
			for _, d := range nudgeDirs {
				if c.Shortcut(0, d.key) {
					if p.selectedAnnIdx >= 0 && p.selectedAnnIdx < len(p.annotations) {
						p.pushUndo()
						p.annotations[p.selectedAnnIdx].Translate(float32(d.dx), float32(d.dy))
						p.effectDirty = true
					} else {
						p.nudgeSelection(d.dx, d.dy, false, false, viewW, viewH)
					}
					break
				}
				if c.Shortcut(ui.Shift, d.key) {
					if p.selectedAnnIdx >= 0 && p.selectedAnnIdx < len(p.annotations) {
						p.pushUndo()
						p.annotations[p.selectedAnnIdx].Translate(float32(d.dx*10), float32(d.dy*10))
						p.effectDirty = true
					} else {
						p.nudgeSelection(d.dx, d.dy, true, false, viewW, viewH)
					}
					break
				}
				if c.Shortcut(ui.Alt, d.key) && p.selectedAnnIdx < 0 {
					p.nudgeSelection(d.dx, d.dy, false, true, viewW, viewH)
					break
				}
			}

			if c.Shortcut(0, ui.KeyB) {
				p.commitTextEdit()
				if p.activeTool == canvas.ToolPixelate && p.mosaicStyle == canvas.MosaicBlur {
					p.activeTool = canvas.ToolSelect
				} else {
					p.activeTool = canvas.ToolPixelate
					p.mosaicStyle = canvas.MosaicBlur
					p.selectedAnnIdx = -1
				}
			}

			shortcuts := []struct {
				k ui.Key
				t canvas.Tool
			}{
				{ui.KeyR, canvas.ToolRect},
				{ui.KeyO, canvas.ToolEllipse},
				{ui.KeyA, canvas.ToolArrow},
				{ui.KeyP, canvas.ToolPen},
				{ui.KeyH, canvas.ToolHighlighter},
				{ui.KeyX, canvas.ToolPixelate},
				{ui.KeyT, canvas.ToolText},
				{ui.KeyN, canvas.ToolCounter},
				{ui.KeyM, canvas.ToolMeasure},
			}
			for _, sk := range shortcuts {
				if c.Shortcut(0, sk.k) {
					p.commitTextEdit()
					if p.activeTool == sk.t {
						p.activeTool = canvas.ToolSelect
					} else {
						p.activeTool = sk.t
						p.selectedAnnIdx = -1
					}
					break
				}
			}
		}
	}

	p.ensureRasterCache(viewW, viewH)

	altLineMode := c.Modifiers()&ui.Alt != 0
	hoverRemoveIdx := -1
	var hoverWord winapi.OCRWord
	hasHoverWord := false
	hoverWordIsLine := false

	cur := ui.CursorCrosshair
	if p.hasSelection && p.dragMode == dragNone {
		if p.activeTool == canvas.ToolText {
			cur = ui.CursorText
		} else if (p.activeTool == canvas.ToolPixelate || p.activeTool == canvas.ToolBlur) && !p.drawing && p.mouseInside {
			mpx, mpy := p.dipToPxF(p.mouseX, p.mouseY, viewW, viewH)
			if hit := hitRedactionAnnotation(p.annotations, mpx, mpy); hit >= 0 {
				hoverRemoveIdx = hit
				cur = ui.CursorPointer
			} else if altLineMode && len(p.ocrLines) > 0 {
				if lineW, ok := redact.FindLineAt(p.ocrLines, mpx, mpy); ok {
					hoverWord = lineW
					hasHoverWord = true
					hoverWordIsLine = true
					cur = ui.CursorPointer
				}
			} else if len(p.ocrWords) > 0 {
				if word, ok := redact.FindWordAt(p.ocrWords, mpx, mpy); ok {
					hoverWord = word
					hasHoverWord = true
					cur = ui.CursorPointer
				}
			}
		} else if p.activeTool == canvas.ToolSelect {
			switch p.hitHandle(p.mouseX, p.mouseY) {
			case dragResizeNW, dragResizeSE:
				cur = ui.CursorResizeNWSE
			case dragResizeNE, dragResizeSW:
				cur = ui.CursorResizeNESW
			case dragResizeN, dragResizeS:
				cur = ui.CursorResizeNS
			case dragResizeE, dragResizeW:
				cur = ui.CursorResizeEW
			case dragMove:
				cur = ui.CursorMove
			}
		}
	}

	// CRITICAL ARCHITECTURE FIX:
	// `surface` (which calls HandleInput) and `renderQQGlassToolbar` are SIBLINGS
	// inside the root container! That way clicking ANY button on the QQ toolbar is
	// never intercepted or reset by `surface.HandleInput` on pointer-down!
	ui.Box(c).Fill().Children(func() {
		surface := ui.Box(c.Key("overlay-surface")).Absolute().Fill().Focusable().Bind(&p.surfaceHandle).Cursor(cur)
		if !p.hasSelection && !p.textEditing && !p.surfaceHandle.Focused(c) {
			p.surfaceHandle.Focus(c)
		}

		var snapGoalX0, snapGoalY0, snapGoalX1, snapGoalY1 float32
		hasSnapTarget := !p.hasSelection && p.dragMode == dragNone && p.snapEnabled && p.hoveredWinIdx >= 0 && p.hoveredWinIdx < len(p.localWins)
		if hasSnapTarget {
			lw := p.localWins[p.hoveredWinIdx]
			snapGoalX0, snapGoalY0, snapGoalX1, snapGoalY1 = lw.x0, lw.y0, lw.x1, lw.y1
		}
		animSnapX0 := surface.AnimateTo("snapX0", snapGoalX0, ui.Snappy)
		animSnapY0 := surface.AnimateTo("snapY0", snapGoalY0, ui.Snappy)
		animSnapX1 := surface.AnimateTo("snapX1", snapGoalX1, ui.Snappy)
		animSnapY1 := surface.AnimateTo("snapY1", snapGoalY1, ui.Snappy)

		surface.HandleInput(func(ev ui.InputEvent) bool {
			switch ev.Kind {
			case ui.InputKeyDown:
				if !p.hasSelection && !ev.Repeat && (ev.Code == ui.CodeShiftLeft || ev.Code == ui.CodeShiftRight) {
					p.colorFmt = (p.colorFmt + 1) % 3
					return true
				}
				return false

			case ui.InputPointerMove:
				p.mouseX, p.mouseY = ev.X, ev.Y
				p.mouseInside = true

				if p.movingAnn && p.selectedAnnIdx >= 0 && p.selectedAnnIdx < len(p.annotations) {
					px, py := p.dipToPxF(ev.X, ev.Y, viewW, viewH)
					dx := px - p.moveAnnLastPx.X
					dy := py - p.moveAnnLastPx.Y
					if math.Abs(float64(dx)) >= 0.5 || math.Abs(float64(dy)) >= 0.5 {
						if !p.moveAnnUndoPushed {
							p.pushUndo()
							p.moveAnnUndoPushed = true
						}
						p.annotations[p.selectedAnnIdx].Translate(dx, dy)
						p.moveAnnLastPx = canvas.PointF{X: px, Y: py}
						k := p.annotations[p.selectedAnnIdx].Kind
						if k == canvas.ToolPixelate || k == canvas.ToolBlur || k == canvas.ToolSpotlight {
							p.effectDirty = true
						}
					}
					return true
				}

				if p.drawing {
					x0, y0, x1, y1 := p.normSel()
					cx := clamp32(ev.X, x0, x1)
					cy := clamp32(ev.Y, y0, y1)
					px, py := p.dipToPxF(cx, cy, viewW, viewH)
					if ev.Mods&ui.Shift != 0 {
						dx, dy := px-p.draftAnn.Start.X, py-p.draftAnn.Start.Y
						switch p.draftAnn.Kind {
						case canvas.ToolRect, canvas.ToolEllipse:
							side := float32(math.Max(math.Abs(float64(dx)), math.Abs(float64(dy))))
							if dx < 0 {
								px = p.draftAnn.Start.X - side
							} else {
								px = p.draftAnn.Start.X + side
							}
							if dy < 0 {
								py = p.draftAnn.Start.Y - side
							} else {
								py = p.draftAnn.Start.Y + side
							}
						case canvas.ToolArrow, canvas.ToolLine:
							ang := math.Atan2(float64(dy), float64(dx))
							snapped := math.Round(ang/(math.Pi/4)) * (math.Pi / 4)
							dist := math.Hypot(float64(dx), float64(dy))
							px = p.draftAnn.Start.X + float32(math.Cos(snapped)*dist)
							py = p.draftAnn.Start.Y + float32(math.Sin(snapped)*dist)
						}
					}
					p.draftAnn.End = canvas.PointF{X: px, Y: py}
					if p.draftAnn.Kind == canvas.ToolPen || p.draftAnn.Kind == canvas.ToolHighlighter {
						pts := p.draftAnn.Points
						last := pts[len(pts)-1]
						if math.Hypot(float64(px-last.X), float64(py-last.Y)) >= 2.0 {
							p.draftAnn.Points = append(pts, canvas.PointF{X: px, Y: py})
						}
					}
					return true
				}

				switch p.dragMode {
				case dragNone:
					if !p.hasSelection {
						if p.snapEnabled {
							p.hoveredWinIdx = p.findHoveredWindow(ev.X, ev.Y)
						} else {
							p.hoveredWinIdx = -1
						}
						if p.rulerMode {
							px, py := p.dipToPx(ev.X, ev.Y, viewW, viewH)
							if px != p.lastMeasurePxX || py != p.lastMeasurePxY {
								p.lastMeasure = winapi.MeasureEdges(p.capture.Image, px, py, 28)
								p.lastMeasurePxX, p.lastMeasurePxY = px, py
							}
						}
					} else if p.activeTool == canvas.ToolMeasure {
						px, py := p.dipToPx(ev.X, ev.Y, viewW, viewH)
						if px != p.lastMeasurePxX || py != p.lastMeasurePxY {
							p.lastMeasure = winapi.MeasureEdges(p.capture.Image, px, py, 28)
							p.lastMeasurePxX, p.lastMeasurePxY = px, py
						}
					}
				case dragCreate:
					p.selX1 = clamp32(ev.X, 0, viewW)
					p.selY1 = clamp32(ev.Y, 0, viewH)
				case dragMove:
					dx := ev.X - p.dragStartX
					dy := ev.Y - p.dragStartY
					w := p.origX1 - p.origX0
					h := p.origY1 - p.origY0
					nx0 := clamp32(p.origX0+dx, 0, viewW-w)
					ny0 := clamp32(p.origY0+dy, 0, viewH-h)
					p.selX0, p.selY0, p.selX1, p.selY1 = nx0, ny0, nx0+w, ny0+h
					p.effectDirty = true
				case dragResizeNW:
					p.selX0, p.selY0 = clamp32(ev.X, 0, viewW), clamp32(ev.Y, 0, viewH)
					p.effectDirty = true
				case dragResizeNE:
					p.selX1, p.selY0 = clamp32(ev.X, 0, viewW), clamp32(ev.Y, 0, viewH)
					p.effectDirty = true
				case dragResizeSW:
					p.selX0, p.selY1 = clamp32(ev.X, 0, viewW), clamp32(ev.Y, 0, viewH)
					p.effectDirty = true
				case dragResizeSE:
					p.selX1, p.selY1 = clamp32(ev.X, 0, viewW), clamp32(ev.Y, 0, viewH)
					p.effectDirty = true
				case dragResizeN:
					p.selY0 = clamp32(ev.Y, 0, viewH)
					p.effectDirty = true
				case dragResizeS:
					p.selY1 = clamp32(ev.Y, 0, viewH)
					p.effectDirty = true
				case dragResizeW:
					p.selX0 = clamp32(ev.X, 0, viewW)
					p.effectDirty = true
				case dragResizeE:
					p.selX1 = clamp32(ev.X, 0, viewW)
					p.effectDirty = true
				}
				return true

			case ui.InputPointerDown:
				if ev.Button == 1 {
					if p.textEditing {
						p.cancelTextEdit()
						return true
					}
					if p.hasSelection {
						p.resetSelection()
					} else {
						p.session.closeAll()
					}
					return true
				}
				if ev.Button != 0 {
					return false
				}
				p.mouseX, p.mouseY = ev.X, ev.Y
				p.mouseInside = true

				// Clicking anywhere on the canvas while inline text is open commits it first.
				if p.textEditing {
					p.commitTextEdit()
					if p.activeTool != canvas.ToolText {
						return true
					}
				}

				if p.hasSelection {
					x0, y0, x1, y1 := p.normSel()
					insideSel := ev.X >= x0 && ev.X <= x1 && ev.Y >= y0 && ev.Y <= y1

					if p.activeTool != canvas.ToolSelect && insideSel {
						px, py := p.dipToPxF(ev.X, ev.Y, viewW, viewH)
						switch p.activeTool {
						case canvas.ToolCounter:
							p.pushUndo()
							p.nextAnnID++
							p.annotations = append(p.annotations, canvas.Annotation{
								ID:         p.nextAnnID,
								Kind:       canvas.ToolCounter,
								Start:      canvas.PointF{X: px, Y: py},
								Color:      p.activeColor(),
								CounterNum: p.nextCounter,
							})
							p.nextCounter++
							p.selectedAnnIdx = len(p.annotations) - 1
							return true

						case canvas.ToolText:
							iw := float32(max(1, p.capture.Image.Bounds().Dx()))
							tol := 10 * iw / max(1, viewW)
							if hit := canvas.HitTestAnnotation(p.annotations, px, py, tol); hit >= 0 && p.annotations[hit].Kind == canvas.ToolText {
								p.beginInlineTextEdit(hit)
							} else {
								p.beginInlineTextNew(px, py)
							}
							return true

						case canvas.ToolMeasure:
							ipx, ipy := p.dipToPx(ev.X, ev.Y, viewW, viewH)
							m := winapi.MeasureEdges(p.capture.Image, ipx, ipy, 28)
							if m.Valid {
								p.pushUndo()
								p.nextAnnID++
								p.annotations = append(p.annotations, canvas.Annotation{
									ID:         p.nextAnnID,
									Kind:       canvas.ToolMeasure,
									Start:      canvas.PointF{X: float32(m.OriginX), Y: float32(m.OriginY)},
									Color:      p.activeColor(),
									MeasLeft:   m.Left,
									MeasRight:  m.Right,
									MeasTop:    m.Top,
									MeasBottom: m.Bottom,
								})
								p.selectedAnnIdx = len(p.annotations) - 1
							}
							return true

						default:
							p.drawing = true
							p.selectedAnnIdx = -1
							p.nextAnnID++
							pt := canvas.PointF{X: px, Y: py}
							p.draftAnn = canvas.Annotation{
								ID:          p.nextAnnID,
								Kind:        p.activeTool,
								Start:       pt,
								End:         pt,
								Color:       p.activeColor(),
								StrokeWidth: p.strokeWidth,
								Filled:      p.filled,
								Rounded:     p.rounded,
								BlockSize:   int(math.Round(p.blockSize)),
								MosaicStyle: p.mosaicStyle,
							}
							if p.activeTool == canvas.ToolPen || p.activeTool == canvas.ToolHighlighter {
								p.draftAnn.Points = []canvas.PointF{pt}
							}
							return true
						}
					}

					mode := p.hitHandle(ev.X, ev.Y)
					// When in ToolSelect inside selection (not grabbing a resize handle), allow
					// selecting/dragging/editing existing annotations directly on the overlay!
					if p.activeTool == canvas.ToolSelect && mode == dragMove && len(p.annotations) > 0 {
						px, py := p.dipToPxF(ev.X, ev.Y, viewW, viewH)
						iw := float32(max(1, p.capture.Image.Bounds().Dx()))
						tol := 10 * iw / max(1, viewW)
						if hit := canvas.HitTestAnnotation(p.annotations, px, py, tol); hit >= 0 {
							if ev.Clicks >= 2 && p.annotations[hit].Kind == canvas.ToolText {
								p.beginInlineTextEdit(hit)
								return true
							}
							p.selectedAnnIdx = hit
							p.movingAnn = true
							p.moveAnnLastPx = canvas.PointF{X: px, Y: py}
							p.moveAnnUndoPushed = false
							return true
						}
						p.selectedAnnIdx = -1
					}

					if mode == dragMove && ev.Clicks >= 2 && p.activeTool == canvas.ToolSelect {
						p.commitCopy(viewW, viewH)
						return true
					}
					if mode != dragNone && (p.activeTool == canvas.ToolSelect || mode != dragMove) {
						p.selectedAnnIdx = -1
						p.selX0, p.selY0, p.selX1, p.selY1 = p.normSel()
						p.origX0, p.origY0, p.origX1, p.origY1 = p.selX0, p.selY0, p.selX1, p.selY1
						p.dragStartX, p.dragStartY = ev.X, ev.Y
						p.dragMode = mode
						return true
					}
				}

				p.resetSelection()
				p.dragMode = dragCreate
				p.dragStartX, p.dragStartY = ev.X, ev.Y
				p.selX0, p.selY0 = ev.X, ev.Y
				p.selX1, p.selY1 = ev.X, ev.Y
				return true

			case ui.InputPointerUp:
				if ev.Button != 0 {
					return false
				}
				if p.movingAnn {
					p.movingAnn = false
					p.moveAnnUndoPushed = false
					return true
				}
				if p.drawing {
					p.drawing = false
					if !ev.Canceled {
						minX, minY, maxX, maxY := p.draftAnn.Bounds()
						isRedactTool := p.draftAnn.Kind == canvas.ToolPixelate || p.draftAnn.Kind == canvas.ToolBlur
						clickTol := float32(3)
						if isRedactTool {
							clickTol = 5
						}
						if maxX-minX >= clickTol || maxY-minY >= clickTol || len(p.draftAnn.Points) >= 2 {
							p.pushUndo()
							p.annotations = append(p.annotations, p.draftAnn)
							p.selectedAnnIdx = len(p.annotations) - 1
							if isRedactTool || p.draftAnn.Kind == canvas.ToolSpotlight {
								p.effectDirty = true
							}
						} else if isRedactTool {
							// One-click toggle: clicking an existing mosaic/blur block removes it;
							// otherwise clicking a recognized OCR word tightly redacts that token.
							if hit := hitRedactionAnnotation(p.annotations, p.draftAnn.Start.X, p.draftAnn.Start.Y); hit >= 0 {
								p.pushUndo()
								p.annotations = append(p.annotations[:hit], p.annotations[hit+1:]...)
								if p.selectedAnnIdx == hit {
									p.selectedAnnIdx = -1
								} else if p.selectedAnnIdx > hit {
									p.selectedAnnIdx--
								}
								p.effectDirty = true
							} else if (ev.Mods&ui.Alt != 0) && len(p.ocrLines) > 0 {
								if lineW, ok := redact.FindLineAt(p.ocrLines, p.draftAnn.Start.X, p.draftAnn.Start.Y); ok {
									p.nextAnnID++
									if ann, valid := makeWordRedactionAnnotation(lineW, p.capture.Image.Bounds(), p.draftAnn.Kind, p.mosaicStyle, int(math.Round(p.blockSize)), p.nextAnnID); valid {
										p.pushUndo()
										p.annotations = append(p.annotations, ann)
										p.selectedAnnIdx = len(p.annotations) - 1
										p.effectDirty = true
									}
								}
							} else if len(p.ocrWords) > 0 {
								if word, ok := redact.FindWordAt(p.ocrWords, p.draftAnn.Start.X, p.draftAnn.Start.Y); ok {
									p.nextAnnID++
									if ann, valid := makeWordRedactionAnnotation(word, p.capture.Image.Bounds(), p.draftAnn.Kind, p.mosaicStyle, int(math.Round(p.blockSize)), p.nextAnnID); valid {
										p.pushUndo()
										p.annotations = append(p.annotations, ann)
										p.selectedAnnIdx = len(p.annotations) - 1
										p.effectDirty = true
									}
								}
							}
						}
					}
					return true
				}

				mode := p.dragMode
				p.dragMode = dragNone
				if ev.Canceled {
					return true
				}
				if mode == dragCreate {
					x0, y0, x1, y1 := p.normSel()
					if x1-x0 < 5 && y1-y0 < 5 {
						if p.snapEnabled && p.hoveredWinIdx >= 0 && p.hoveredWinIdx < len(p.localWins) {
							lw := p.localWins[p.hoveredWinIdx]
							p.selX0, p.selY0, p.selX1, p.selY1 = lw.x0, lw.y0, lw.x1, lw.y1
							p.hasSelection = true
							if p.session.longMode {
								p.commitLongCapture(viewW, viewH)
							}
							return true
						}
						p.hasSelection = false
						return true
					}
					p.selX0, p.selY0, p.selX1, p.selY1 = x0, y0, x1, y1
					p.hasSelection = true
					if p.session.longMode {
						p.commitLongCapture(viewW, viewH)
					}
				} else if mode != dragNone {
					p.selX0, p.selY0, p.selX1, p.selY1 = p.normSel()
					p.clearRedactCache()
				}
				return true
			}
			return false
		})

		surface.Draw(func(pt *ui.Painter, r ui.Rect) {
			pt.Image(p.bmp, r, ui.FillBox)

			veil := ui.RGBA(0, 0, 0, 0.42)
			appleBlue := ui.Hex("#0A84FF")

			showBox := p.dragMode == dragCreate || p.hasSelection
			var bx0, by0, bx1, by1 float32
			winSnapTitle := ""

			if showBox {
				bx0, by0, bx1, by1 = p.normSel()
			} else if hasSnapTarget {
				bx0, by0, bx1, by1 = animSnapX0, animSnapY0, animSnapX1, animSnapY1
				winSnapTitle = p.localWins[p.hoveredWinIdx].title
				showBox = true
			}

			if showBox && bx1-bx0 >= 2 && by1-by0 >= 2 {
				pt.Fill(ui.Rect{X: 0, Y: 0, W: r.W, H: by0}, veil, 0)
				pt.Fill(ui.Rect{X: 0, Y: by1, W: r.W, H: r.H - by1}, veil, 0)
				pt.Fill(ui.Rect{X: 0, Y: by0, W: bx0, H: by1 - by0}, veil, 0)
				pt.Fill(ui.Rect{X: bx1, Y: by0, W: r.W - bx1, H: by1 - by0}, veil, 0)

				selRect := ui.Rect{X: bx0, Y: by0, W: bx1 - bx0, H: by1 - by0}

				if p.hasSelection {
					iw := float32(p.capture.Image.Bounds().Dx())
					ih := float32(p.capture.Image.Bounds().Dy())
					pxToDipX := r.W / max(1, iw)
					pxToDipY := r.H / max(1, ih)
					scale := (pxToDipX + pxToDipY) * 0.5

					pt.Clip(selRect, 0, func() {
						if p.selEffectBmp != nil {
							er := p.selEffectRect
							dstR := ui.Rect{
								X: float32(er.Min.X) * pxToDipX,
								Y: float32(er.Min.Y) * pxToDipY,
								W: float32(er.Dx()) * pxToDipX,
								H: float32(er.Dy()) * pxToDipY,
							}
							pt.Image(p.selEffectBmp, dstR, ui.FillBox)
						}
						for i, a := range p.annotations {
							if p.textEditing && i == p.textEditIdx {
								continue
							}
							canvas.DrawAnnotationLive(pt, a, 0, 0, scale, i == p.selectedAnnIdx, false)
						}
						if p.drawing {
							canvas.DrawAnnotationLive(pt, p.draftAnn, 0, 0, scale, false, true)
						}
						if !p.drawing && (p.activeTool == canvas.ToolPixelate || p.activeTool == canvas.ToolBlur) && p.mouseInside {
							if hoverRemoveIdx >= 0 && hoverRemoveIdx < len(p.annotations) {
								minX, minY, maxX, maxY := p.annotations[hoverRemoveIdx].Bounds()
								hr := ui.Rect{
									X: minX * pxToDipX,
									Y: minY * pxToDipY,
									W: (maxX - minX) * pxToDipX,
									H: (maxY - minY) * pxToDipY,
								}
								drawRedactionRemoveHint(pt, hr)
							} else if hasHoverWord {
								wr := ui.Rect{
									X: float32(hoverWord.Rect.Min.X-3) * pxToDipX,
									Y: float32(hoverWord.Rect.Min.Y-2) * pxToDipY,
									W: float32(hoverWord.Rect.Dx()+6) * pxToDipX,
									H: float32(hoverWord.Rect.Dy()+4) * pxToDipY,
								}
								drawWordRedactHint(pt, wr, hoverWordIsLine)
							}
						}
						if p.activeTool == canvas.ToolMeasure && p.mouseInside && p.lastMeasure.Valid {
							m := p.lastMeasure
							preview := canvas.Annotation{
								Kind:       canvas.ToolMeasure,
								Start:      canvas.PointF{X: float32(m.OriginX), Y: float32(m.OriginY)},
								Color:      p.activeColor(),
								MeasLeft:   m.Left,
								MeasRight:  m.Right,
								MeasTop:    m.Top,
								MeasBottom: m.Bottom,
							}
							canvas.DrawAnnotationLive(pt, preview, 0, 0, scale, false, true)
						}
					})
				}

				pt.Stroke(ui.Rect{X: selRect.X - 1, Y: selRect.Y - 1, W: selRect.W + 2, H: selRect.H + 2}, ui.RGBA(255, 255, 255, 0.72), 2, 1)
				pt.Stroke(selRect, appleBlue, 1, 1.8)

				if p.hasSelection && p.dragMode == dragNone && !p.drawing {
					cx, cy := (bx0+bx1)/2, (by0+by1)/2
					pts := [8][2]float32{
						{bx0, by0}, {cx, by0}, {bx1, by0},
						{bx1, cy}, {bx1, by1}, {cx, by1},
						{bx0, by1}, {bx0, cy},
					}
					for _, hp := range pts {
						hr := ui.Rect{X: hp[0] - 4.5, Y: hp[1] - 4.5, W: 9, H: 9}
						pt.Shadow(hr, 4.5, 0, 1, 4, 0, ui.RGBA(0, 0, 0, 0.35))
						pt.Fill(hr, ui.Hex("#FFFFFF"), 4.5)
						pt.Stroke(hr, appleBlue, 4.5, 1.5)
					}
				}

				iw, ih := p.capture.Image.Bounds().Dx(), p.capture.Image.Bounds().Dy()
				pxW := int(math.Round(float64(selRect.W) * float64(iw) / max(1, float64(r.W))))
				pxH := int(math.Round(float64(selRect.H) * float64(ih) / max(1, float64(r.H))))
				badge := fmt.Sprintf("%d × %d", pxW, pxH)
				if winSnapTitle != "" {
					if len([]rune(winSnapTitle)) > 30 {
						winSnapTitle = string([]rune(winSnapTitle)[:29]) + "…"
					}
					badge = fmt.Sprintf("%s  ·  %d × %d", winSnapTitle, pxW, pxH)
				}
				if p.session.longMode {
					badge = "长截图 · " + badge
				}
				if p.redactNotice != "" && time.Now().Before(p.redactUntil) {
					badge = badge + "  ·  " + p.redactNotice
				}
				span := ui.Span{Text: badge, Size: 12, Weight: 600, Color: ui.Hex("#FFFFFF")}
				tw, th := pt.MeasureText(0, span)
				badgeW, badgeH := tw+22, th+10
				badgeX := clamp32(bx0, 10, r.W-badgeW-10)
				badgeY := by0 - badgeH - 10
				if badgeY < 10 {
					badgeY = by0 + 10
				}
				badgeR := ui.Rect{X: badgeX, Y: badgeY, W: badgeW, H: badgeH}
				glasswork.Paint(pt, badgeR, badgeH/2, glasswork.Pill(glasswork.KindDark))
				pt.RichText(badgeR.X+11, badgeR.Y+5, 0, span)
			} else {
				pt.Fill(r, veil, 0)
			}

			if p.rulerMode && !p.hasSelection && p.mouseInside && p.lastMeasure.Valid {
				m := p.lastMeasure
				iw := float32(p.capture.Image.Bounds().Dx())
				ih := float32(p.capture.Image.Bounds().Dy())
				sx, sy := r.W/max(1, iw), r.H/max(1, ih)
				lx, rx := float32(m.Left)*sx, float32(m.Right)*sx
				ty, by := float32(m.Top)*sy, float32(m.Bottom)*sy
				cx, cy := float32(m.OriginX)*sx, float32(m.OriginY)*sy
				rc := ui.Hex("#FF3B30")
				pt.Line(lx, cy, rx, cy, 1.5, rc)
				pt.Line(lx, cy-5, lx, cy+5, 1.5, rc)
				pt.Line(rx, cy-5, rx, cy+5, 1.5, rc)
				pt.Line(cx, ty, cx, by, 1.5, rc)
				pt.Line(cx-5, ty, cx+5, ty, 1.5, rc)
				pt.Line(cx-5, by, cx+5, by, 1.5, rc)

				mlabel := fmt.Sprintf("%d × %d px", m.Width, m.Height)
				mspan := ui.Span{Text: mlabel, Size: 12, Weight: 700, Color: ui.Hex("#FFFFFF")}
				mtw, mth := pt.MeasureText(0, mspan)
				mpill := ui.Rect{X: clamp32(cx+12, 8, r.W-mtw-24), Y: clamp32(cy-mth-18, 8, r.H-mth-16), W: mtw + 18, H: mth + 10}
				glasswork.Paint(pt, mpill, mpill.H/2, glasswork.Tinted(rc))
				pt.RichText(mpill.X+9, mpill.Y+5, 0, mspan)
			}

			if p.mouseInside && (!p.hasSelection || p.dragMode != dragNone) {
				if p.dragMode == dragNone && !p.snapEnabled {
					guide := ui.RGBA(255, 255, 255, 0.35)
					pt.Line(0, p.mouseY, r.W, p.mouseY, 1, guide)
					pt.Line(p.mouseX, 0, p.mouseX, r.H, 1, guide)
				}
				p.drawGlassworkLoupe(pt, r)
			}
		})

		// SIBLING layer above `surface`: Inline canvas text editor (click-to-type directly on selection)
		if p.hasSelection && p.textEditing {
			p.renderInlineTextEditor(c, viewW, viewH)
		}

		// SIBLING layer above `surface`: QQ-style Glasswork Liquid Glass Toolbar!
		if p.hasSelection && p.dragMode == dragNone && !p.drawing {
			p.renderQQGlassToolbar(c, viewW, viewH)
		}
	})
}

func (p *overlayPane) renderInlineTextEditor(c *ui.Context, viewW, viewH float32) {
	iw := float32(max(1, p.capture.Image.Bounds().Dx()))
	ih := float32(max(1, p.capture.Image.Bounds().Dy()))
	pxToDipX := viewW / iw
	pxToDipY := viewH / ih
	scale := (pxToDipX + pxToDipY) * 0.5

	dipX := p.textEditPx.X * pxToDipX
	dipY := p.textEditPx.Y * pxToDipY
	fs := p.fontSize * scale
	if fs < 12 {
		fs = 12
	}

	const boxW float32 = 220
	const minH float32 = 34
	x0, y0, x1, y1 := p.normSel()
	maxLeft := max(x0, min(viewW-boxW-12, x1-96))
	maxTop := max(y0, min(viewH-minH-12, y1-minH))
	left := clamp32(dipX-6, 8, max(8, maxLeft))
	top := clamp32(dipY-6, 8, max(8, maxTop))

	accentCol := p.activeColor()
	txtCol := accentCol
	bgCol := ui.RGBA(15, 23, 42, 0.56)
	if p.filled {
		bgCol = accentCol.Alpha(0.92)
		lum := 0.2126*float64(accentCol.R) + 0.7152*float64(accentCol.G) + 0.0722*float64(accentCol.B)
		if lum > 175 {
			txtCol = ui.Hex("#0F172A")
		} else {
			txtCol = ui.Hex("#FFFFFF")
		}
	}

	wrap := ui.Box(c.Key("qq-inline-text-wrap")).
		Absolute().Left(left).Top(top).MinWidth(148).MaxWidth(max(220, x1-left-4)).
		Radius(8).Background(bgCol).Border(1.5, ui.Hex("#0A84FF"))
	wrap.Clicked()

	wrap.Children(func() {
		ta := ui.TextArea(c.Key("qq-inline-text"), &p.textEditVal).
			Bind(&p.textEditHandle).
			Width(boxW).MinHeight(minH).
			Padding(4, 8, 4, 8).
			FontSize(fs).FontWeight(700).TextColor(txtCol).
			Background(ui.Transparent).Border(0, ui.Transparent).
			Placeholder("输入文字… (Enter 确认，Shift+Enter 换行)").
			PlaceholderColor(txtCol.Alpha(0.55))

		ta.OnTextKey(func(k ui.TextKey) bool {
			if k.Key == ui.KeyEscape {
				p.cancelTextEdit()
				return true
			}
			if k.Key == ui.KeyEnter && k.Mods&ui.Shift == 0 {
				p.textEditVal = k.Text()
				p.commitTextEdit()
				return true
			}
			return false
		})
	})

	if p.textEditNeedFocus {
		p.textEditHandle.Focus(c)
		p.textEditNeedFocus = false
	}
}

func (p *overlayPane) renderQQGlassToolbar(c *ui.Context, viewW, viewH float32) {
	x0, y0, x1, y1 := p.normSel()
	const mainBarH float32 = 44
	const subBarH float32 = 40

	mainW := p.measuredMainW
	if mainW < 520 {
		mainW = 706
	}
	subW := p.measuredSubW
	if subW < 180 {
		subW = 470
	}

	showSub := p.activeTool != canvas.ToolSelect
	barX, barY, subX, subY, placeSubBelow := ComputeToolbarLayout(x0, y0, x1, y1, viewW, viewH, mainW, subW, showSub)

	// Sample adaptive glass surface once per bar placement so it never flickers between Light/Dark!
	barRect := ui.Rect{X: barX, Y: barY, W: mainW, H: mainBarH}
	movedFar := math.Abs(float64(barRect.X-p.lastBarRect.X)) > 32 || math.Abs(float64(barRect.Y-p.lastBarRect.Y)) > 32
	if !p.hasBarKind || movedFar {
		p.barSurf, p.barInks = p.adaptGlassForRect(barRect, viewW, viewH, &p.barKind, &p.hasBarKind)
		p.lastBarRect = barRect
	}
	barSurf, inks := p.barSurf, p.barInks

	// Main Glasswork Liquid Glass Toolbar Capsule (flicker-free rise+fade entrance)
	mainBar := ui.Row(c.Key("qq-main-bar")).
		Absolute().Left(barX).Top(barY).Height(mainBarH).
		Padding(5, 8, 5, 8).Gap(2).
		Radius(22).
		Material(barSurf).
		AlignItems(ui.Center).
		Transition(glasswork.ToolbarAppearTransition(6))
	if mb := mainBar.Bounds(); mb.W > 100 && math.Abs(float64(mb.W-p.measuredMainW)) > 1 {
		p.measuredMainW = mb.W
	}
	// Absorb clicks on gaps inside the bar so clicks never leak through to `surface`!
	mainBar.Clicked()

	mainBar.Children(func() {
		tools := []struct {
			t  canvas.Tool
			ic icons.IconPair
		}{
			{canvas.ToolRect, icons.Rect},
			{canvas.ToolEllipse, icons.Ellipse},
			{canvas.ToolArrow, icons.Arrow},
			{canvas.ToolPen, icons.Pen},
			{canvas.ToolHighlighter, icons.Highlighter},
			{canvas.ToolPixelate, icons.Pixelate},
			{canvas.ToolText, icons.Text},
			{canvas.ToolCounter, icons.Counter},
			{canvas.ToolMeasure, icons.Measure},
		}
		for _, item := range tools {
			active := p.activeTool == item.t
			tip := fmt.Sprintf("%s (%s)", item.t.Name(), item.t.Shortcut())
			if glassToolBtnScoped(c, inks, item.ic, item.t.Name(), tip, active, false, "qq-tool") {
				p.commitTextEdit()
				if active {
					p.activeTool = canvas.ToolSelect
				} else {
					p.activeTool = item.t
					p.selectedAnnIdx = -1
				}
			}
		}

		glassSep(c, inks)

		if glassToolBtnDisabled(c, inks, icons.Undo, "撤销", "撤销 (Ctrl+Z)", len(p.undoStack) == 0) {
			p.undo()
		}
		if glassToolBtnDisabled(c, inks, icons.Redo, "重做", "重做 (Ctrl+Y)", len(p.redoStack) == 0) {
			p.redo()
		}

		glassSep(c, inks)

		if glassToolBtn(c, inks, icons.LongCapture, "长截图", "滚动长截图 (L)", false, false) {
			p.commitLongCapture(viewW, viewH)
		}
		if glassToolBtn(c, inks, icons.OCR, "提取文字", "文字识别 OCR", false, false) {
			p.commitOCR(viewW, viewH)
		}
		if glassToolBtn(c, inks, icons.Pin, "贴图", "贴图到桌面", false, false) {
			p.commitPin(viewW, viewH)
		}
		if glassToolBtn(c, inks, icons.EditStudio, "编辑", "在工作台继续编辑 (E)", false, false) {
			p.commitEdit(viewW, viewH)
		}
		if glassToolBtn(c, inks, icons.Save, "保存", "保存 (Ctrl+S)", false, false) {
			p.commitSave(viewW, viewH)
		}
		if glassToolBtn(c, inks, icons.Close, "取消", "取消 (Esc)", false, true) {
			p.session.closeAll()
		}

		// Prominent Tinted Glasswork Liquid Glass Primary Commit Pill ("完成")
		// with Solar Bold Duotone icon + droplet wobble + gel-pull stretch.
		doneBtn := ui.Row(c.Key("qq-done-btn")).
			Height(32).Padding(0, 14, 0, 11).Gap(5).Margin(0, 0, 0, 4).
			Radius(16).AlignItems(ui.Center).
			Role(ui.RoleButton).Label("完成").
			Material(glasswork.Tinted(ui.Hex("#0A84FF"))).
			Cursor(ui.CursorPointer).Tooltip("完成并复制到剪贴板 (Enter / 双击选区)")
		glasswork.ApplyLiquidButton(doneBtn, glasswork.PrimaryButtonOpts())
		doneBtn.Children(func() {
			ui.Icon(c, icons.Check.Idle).Size(17, 17).TextColor(ui.Hex("#FFFFFF")).Shrink(0)
			ui.Text(c, "完成").FontSize(12.5).FontWeight(600).TextColor(ui.Hex("#FFFFFF")).SingleLine().Shrink(0)
		})
		if doneBtn.Clicked() {
			p.commitCopy(viewW, viewH)
		}
	})

	// Secondary Glasswork Liquid Glass Property Pill (QQ截图二级参数条)
	if showSub {
		subEnterY := float32(-6)
		if !placeSubBelow {
			subEnterY = 6
		}
		subBar := ui.Row(c.Key("qq-sub-bar")).
			Absolute().Left(subX).Top(subY).Height(subBarH).
			Padding(4, 12, 4, 12).Gap(10).
			Radius(20).
			Material(barSurf).
			AlignItems(ui.Center).
			ClipX().
			Transition(glasswork.MorphBarTransition(subEnterY))
		if sb := subBar.Bounds(); sb.W > 60 && math.Abs(float64(sb.W-p.measuredSubW)) > 1 {
			p.measuredSubW = sb.W
		}
		subBar.Clicked()

		subBar.Children(func() {
			switch p.activeTool {
			case canvas.ToolPixelate, canvas.ToolBlur:
				styles := []canvas.MosaicStyle{
					canvas.MosaicCrystal,
					canvas.MosaicBlur,
					canvas.MosaicPixel,
					canvas.MosaicRedact,
				}
				ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
					for _, st := range styles {
						if glassChipScoped(c, inks, st.Name(), p.mosaicStyle == st, "qq-mosaic-style") {
							p.activeTool = canvas.ToolPixelate
							p.mosaicStyle = st
							if p.selectedAnnIdx >= 0 && p.selectedAnnIdx < len(p.annotations) {
								if k := p.annotations[p.selectedAnnIdx].Kind; k == canvas.ToolPixelate || k == canvas.ToolBlur {
									p.annotations[p.selectedAnnIdx].Kind = canvas.ToolPixelate
									p.annotations[p.selectedAnnIdx].MosaicStyle = st
									p.effectDirty = true
								}
							}
						}
					}
				})
				if p.mosaicStyle != canvas.MosaicRedact {
					glassSep(c, inks)
					tip := "马赛克粒度"
					if p.mosaicStyle == canvas.MosaicBlur {
						tip = "模糊强度"
					}
					ui.Slider(c, &p.blockSize, 6, 32).Width(92).Label(tip).Tooltip(tip)
					ui.Textf(c, "%d px", int(math.Round(p.blockSize))).
						FontSize(11.5).FontWeight(600).TextColor(inks.Primary).SingleLine().Shrink(0).
						ContentTransition(glasswork.ContentNumeric)
				}
				glassSep(c, inks)
				busy := p.isRedactBusy()
				smartLabel := "智能打码"
				if busy {
					smartLabel = "扫描中…"
				}
				smartBtn := ui.Row(c.Key("qq-smart-redact")).
					Height(26).Padding(0, 10, 0, 8).Gap(5).Radius(13).
					AlignItems(ui.Center).Shrink(0).
					Role(ui.RoleButton).Label("智能打码").
					Cursor(ui.CursorPointer).Tooltip("离线识别并打码敏感信息 (Shift+X)")
				fg := inks.Accent
				if busy {
					smartBtn.Background(inks.AccentFill).Border(1, inks.AccentBorder).Opacity(0.75)
				} else if smartBtn.Hovered() {
					smartBtn.Background(inks.AccentFill).Border(1, inks.AccentBorder)
				} else {
					smartBtn.Background(inks.Hover).Border(1, inks.Hairline)
					fg = inks.Primary
				}
				smartBtn.Transition(ui.ElementTransition{Colors: true, Animation: &glasswork.AnimColorFade})
				glasswork.ApplyLiquidButton(smartBtn, glasswork.CompactButtonOpts())
				smartBtn.Children(func() {
					ui.Icon(c, icons.SmartRedact.For(busy || smartBtn.Hovered() || smartBtn.Pressed())).
						Size(14.5, 14.5).TextColor(fg).Shrink(0).ContentTransition(ui.ContentTransition{})
					ui.Text(c, smartLabel).FontSize(11.5).FontWeight(600).TextColor(fg).SingleLine().Shrink(0)
				})
				if smartBtn.Clicked() && !busy {
					p.triggerSmartRedact(viewW, viewH)
				}

			case canvas.ToolText:
				for _, sz := range []float32{16, 22, 30} {
					if glassChipScoped(c, inks, fmt.Sprintf("%d", int(sz)), p.fontSize == sz, "qq-font") {
						p.fontSize = sz
					}
				}
				if glassChip(c, inks, "底色", p.filled) {
					p.filled = !p.filled
				}
				glassSep(c, inks)
				p.renderOverlayColorDots(c, inks)

			case canvas.ToolCounter:
				ui.Textf(c, "#%d", p.nextCounter).
					FontSize(12).FontWeight(700).TextColor(inks.Primary).SingleLine().Shrink(0).
					ContentTransition(glasswork.ContentNumeric)
				if glassChip(c, inks, "重置", false) {
					p.nextCounter = 1
				}
				glassSep(c, inks)
				p.renderOverlayColorDots(c, inks)

			case canvas.ToolMeasure:
				p.renderOverlayColorDots(c, inks)

			default:
				sizes := []struct {
					dot float32
					sw  float32
					tip string
				}{{5, 2, "细"}, {8, 4, "中"}, {11, 7, "粗"}}
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					for _, s := range sizes {
						active := p.strokeWidth == s.sw
						btn := ui.Box(c).Size(24, 24).Radius(12).Center().
							Role(ui.RoleButton).Label(s.tip).
							Cursor(ui.CursorPointer).Tooltip(s.tip)
						if !active && btn.Hovered() {
							btn.Background(inks.Hover)
						}
						btn.Transition(ui.ElementTransition{Colors: true, Animation: &glasswork.AnimColorFade})
						glasswork.ApplyLiquidButton(btn, glasswork.CompactButtonOpts())
						btn.Children(func() {
							if active {
								glasswork.SlidingAccentLens(c, "qq-stroke", 12, inks.AccentFill, inks.AccentBorder)
							}
							dotCol := inks.Secondary
							if active {
								dotCol = inks.Accent
							}
							ui.Box(c).Size(s.dot, s.dot).Radius(s.dot / 2).Background(dotCol)
						})
						if btn.Clicked() {
							p.strokeWidth = s.sw
						}
					}
				})
				glassSep(c, inks)
				p.renderOverlayColorDots(c, inks)
				if p.activeTool == canvas.ToolRect || p.activeTool == canvas.ToolEllipse {
					glassSep(c, inks)
					if glassChip(c, inks, "填充", p.filled) {
						p.filled = !p.filled
					}
					if p.activeTool == canvas.ToolRect {
						if glassChip(c, inks, "圆角", p.rounded) {
							p.rounded = !p.rounded
						}
					}
				}
			}
		})
	}
}

func (p *overlayPane) renderOverlayColorDots(c *ui.Context, inks glasswork.InkColors) {
	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		for i, sw := range canvas.DefaultPalette {
			active := p.colorIdx == i
			ring := ui.Box(c).Size(22, 22).Radius(11).Center().
				Role(ui.RoleButton).Label(sw.Name).
				Cursor(ui.CursorPointer).Tooltip(sw.Name)
			glasswork.ApplyLiquidButton(ring, glasswork.CompactButtonOpts())
			ring.Children(func() {
				if active {
					glasswork.SlidingRingLens(c, "qq-color", 11, inks.Accent)
				}
				ui.Box(c).Size(14, 14).Radius(7).Background(sw.Color).Border(1, inks.Hairline)
			})
			if ring.Clicked() {
				p.colorIdx = i
			}
		}
	})
}

func (p *overlayPane) drawGlassworkLoupe(pt *ui.Painter, r ui.Rect) {
	const (
		gridCells = 13
		cellSize  = 10
		gridDim   = gridCells * cellSize
		cardW     = gridDim + 14
		cardH     = gridDim + 54
	)

	lx := p.mouseX + 24
	ly := p.mouseY + 24
	if lx+cardW > r.W-12 {
		lx = p.mouseX - cardW - 24
	}
	if ly+cardH > r.H-12 {
		ly = p.mouseY - cardH - 24
	}
	lx = clamp32(lx, 12, r.W-cardW-12)
	ly = clamp32(ly, 12, r.H-cardH-12)

	card := ui.Rect{X: lx, Y: ly, W: cardW, H: cardH}
	if !p.hasLoupeKnd || math.Abs(float64(lx-p.lastLoupeAdX)) >= 20 || math.Abs(float64(ly-p.lastLoupeAdY)) >= 20 {
		p.loupeSurf, p.loupeInks = p.adaptGlassForRect(card, r.W, r.H, &p.loupeKind, &p.hasLoupeKnd)
		p.loupeSurf = glasswork.Pill(p.loupeKind)
		p.lastLoupeAdX, p.lastLoupeAdY = lx, ly
	}
	glasswork.Paint(pt, card, 18, p.loupeSurf)
	inks := p.loupeInks

	px, py := p.dipToPx(p.mouseX, p.mouseY, r.W, r.H)
	iw, ih := p.capture.Image.Bounds().Dx(), p.capture.Image.Bounds().Dy()

	half := gridCells / 2
	sx := winapi.ClampInt(px-half, 0, max(0, iw-gridCells))
	sy := winapi.ClampInt(py-half, 0, max(0, ih-gridCells))
	dstGrid := ui.Rect{X: lx + 7, Y: ly + 7, W: gridDim, H: gridDim}

	if p.loupeRGBA == nil {
		p.loupeRGBA = image.NewRGBA(image.Rect(0, 0, gridCells, gridCells))
	}
	if p.loupeBmp == nil || sx != p.lastLoupeSX || sy != p.lastLoupeSY {
		srcImg := p.capture.Image
		for row := 0; row < gridCells; row++ {
			srcY := winapi.ClampInt(sy+row, 0, ih-1)
			dOff := row * p.loupeRGBA.Stride
			for col := 0; col < gridCells; col++ {
				srcX := winapi.ClampInt(sx+col, 0, iw-1)
				sOff := srcY*srcImg.Stride + srcX*4
				copy(p.loupeRGBA.Pix[dOff+col*4:dOff+col*4+4], srcImg.Pix[sOff:sOff+4])
			}
		}
		if p.loupeBmp == nil {
			p.loupeBmp = ui.NewBitmap(p.loupeRGBA).Pixelated()
		} else {
			p.loupeBmp.Update(p.loupeRGBA)
		}
		p.lastLoupeSX, p.lastLoupeSY = sx, sy
	}

	pt.Clip(dstGrid, 11, func() {
		pt.Image(p.loupeBmp, dstGrid, ui.FillBox)
		gridLine := ui.RGBA(255, 255, 255, 0.14)
		for i := 1; i < gridCells; i++ {
			off := float32(i * cellSize)
			pt.Line(dstGrid.X+off, dstGrid.Y, dstGrid.X+off, dstGrid.Y+gridDim, 0.75, gridLine)
			pt.Line(dstGrid.X, dstGrid.Y+off, dstGrid.X+gridDim, dstGrid.Y+off, 0.75, gridLine)
		}
		cxCell := px - sx
		cyCell := py - sy
		cellR := ui.Rect{
			X: dstGrid.X + float32(cxCell*cellSize),
			Y: dstGrid.Y + float32(cyCell*cellSize),
			W: cellSize,
			H: cellSize,
		}
		pt.Stroke(cellR, ui.Hex("#007AFF"), 1.5, 2)
	})
	pt.Stroke(dstGrid, inks.Hairline, 11, 1)

	cr, cg, cb, _ := winapi.ColorAt(p.capture.Image, px, py)
	colStr := FormatPickedColor(cr, cg, cb, p.colorFmt)
	infoY := dstGrid.Y + gridDim + 8

	swatch := ui.Rect{X: lx + 9, Y: infoY + 2, W: 14, H: 14}
	pt.Fill(swatch, ui.RGB(cr, cg, cb), 7)
	pt.Stroke(swatch, inks.Hairline, 7, 1)

	colSize := float32(11.5)
	if len(colStr) > 10 {
		colSize = 9.5
	}
	pt.RichText(lx+28, infoY+(11.5-colSize)*0.5, 0, ui.Span{Text: colStr, Size: colSize, Weight: 700, Color: inks.Primary})
	pt.RichText(lx+9, infoY+20, 0, ui.Span{
		Text:  fmt.Sprintf("%d, %d  ·  Shift", px, py),
		Size:  10.5,
		Color: inks.Secondary,
	})
}

func glassToolBtn(c *ui.Context, inks glasswork.InkColors, ic icons.IconPair, label, tip string, active, danger bool) bool {
	return glassToolBtnScoped(c, inks, ic, label, tip, active, danger, "")
}

func glassToolBtnScoped(c *ui.Context, inks glasswork.InkColors, ic icons.IconPair, label, tip string, active, danger bool, lensScope string) bool {
	btn := ui.Box(c).Size(32, 32).Center().Radius(16).
		Role(ui.RoleButton).Label(label).
		Cursor(ui.CursorPointer).Tooltip(tip)
	iconCol := inks.Primary
	if active {
		if lensScope == "" {
			btn.Background(inks.AccentFill).Border(1, inks.AccentBorder)
		}
		iconCol = inks.Accent
	} else if btn.Hovered() {
		if danger {
			btn.Background(inks.DangerFill)
			iconCol = inks.Danger
		} else {
			btn.Background(inks.Hover)
		}
	}
	btn.Transition(ui.ElementTransition{Colors: true, Animation: &glasswork.AnimColorFade})
	glasswork.ApplyLiquidButton(btn, glasswork.CompactButtonOpts())
	glyph := ic.For(active || btn.Pressed() || (danger && btn.Hovered()))
	btn.Children(func() {
		if active && lensScope != "" {
			glasswork.SlidingAccentLens(c, lensScope, 16, inks.AccentFill, inks.AccentBorder)
		}
		ui.Icon(c, glyph).Size(17, 17).TextColor(iconCol).ContentTransition(ui.ContentTransition{})
	})
	return btn.Clicked()
}

func glassToolBtnDisabled(c *ui.Context, inks glasswork.InkColors, ic icons.IconPair, label, tip string, disabled bool) bool {
	if disabled {
		ui.Box(c).Size(30, 30).Center().Radius(15).Opacity(0.32).Tooltip(tip).Children(func() {
			ui.Icon(c, ic.Idle).Size(16.5, 16.5).TextColor(inks.Primary)
		})
		return false
	}
	return glassToolBtn(c, inks, ic, label, tip, false, false)
}

func glassChip(c *ui.Context, inks glasswork.InkColors, label string, active bool) bool {
	return glassChipScoped(c, inks, label, active, "")
}

func glassChipScoped(c *ui.Context, inks glasswork.InkColors, label string, active bool, lensScope string) bool {
	btn := ui.Row(c).Height(24).Padding(0, 9, 0, 9).Radius(12).AlignItems(ui.Center).Shrink(0).
		Role(ui.RoleButton).Label(label).Cursor(ui.CursorPointer)
	fg := inks.Primary
	if active {
		if lensScope == "" {
			btn.Background(inks.AccentFill).Border(1, inks.AccentBorder)
		}
		fg = inks.Accent
	} else if btn.Hovered() {
		btn.Background(inks.Active)
	} else {
		btn.Background(inks.Hover)
	}
	btn.Transition(ui.ElementTransition{Colors: true, Animation: &glasswork.AnimColorFade})
	glasswork.ApplyLiquidButton(btn, glasswork.CompactButtonOpts())
	btn.Children(func() {
		if active && lensScope != "" {
			glasswork.SlidingAccentLens(c, lensScope, 12, inks.AccentFill, inks.AccentBorder)
		}
		ui.Text(c, label).FontSize(11.5).FontWeight(600).TextColor(fg).SingleLine()
	})
	return btn.Clicked()
}

func glassSep(c *ui.Context, inks glasswork.InkColors) {
	ui.Box(c).Size(1, 18).Background(inks.Hairline).Margin(0, 4, 0, 4)
}

func clamp32(v, lo, hi float32) float32 {
	if hi < lo {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
