package app

import (
	"fmt"
	"image"
	"math"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/shader"

	"koma/internal/canvas"
	"koma/internal/glasswork"
	"koma/internal/icons"
	"koma/internal/ocr"
	"koma/internal/redact"
	"koma/internal/winapi"
)

var (
	dotGridShader = shader.MustCompile(`package main

var DotColor vec4

func Fragment(dst vec4, pos vec2) vec4 {
	cell := mod(pos, vec2(28.0, 28.0)) - vec2(14.75, 14.75)
	d := length(cell)
	aa := max(0.75/pixelScale(), 0.35)
	a := 1.0 - smoothstep(0.75-aa, 0.75+aa, d)
	alpha := a * DotColor.a
	return vec4(DotColor.rgb*alpha, alpha)
}
`)
	uDotColor         = dotGridShader.Uniform("DotColor")
	dotGridValuesPool = sync.Pool{
		New: func() any { return dotGridShader.NewValues() },
	}
)

// EditorState holds all state for Koma's Glasswork Liquid Glass Companion Studio.
type EditorState struct {
	app *App

	title       string
	baseImg     *image.RGBA
	basePNG     []byte // compressed backing copy so ParkForTray drops uncompressed RGBA cleanly
	annotations []canvas.Annotation
	nextAnnID   int
	nextCounter int

	undoStack []canvas.Snapshot
	redoStack []canvas.Snapshot

	rasterImg       *image.RGBA
	rasterBmp       *ui.Bitmap
	rasterBmpOnBase bool
	rasterDirty     bool

	tool        canvas.Tool
	mosaicStyle canvas.MosaicStyle
	colorIdx    int
	strokeWidth float32
	filled      bool
	rounded     bool
	fontSize    float32
	textDraft   string
	blockSize   float64
	measureSens float64

	// Offline smart redaction & point-to-redact OCR cache (scoped to current document)
	redactMu      sync.Mutex
	redactBusy    bool
	redactGen     int
	ocrWords      []winapi.OCRWord
	ocrLines      []winapi.OCRWord
	pendingRedact *pendingEditorRedact

	selectedIdx  int
	drawing      bool
	draftAnn     canvas.Annotation
	movingAnn    bool
	moveLastImgX float32
	moveLastImgY float32
	panning      bool
	panLastX     float32
	panLastY     float32

	hasCrop  bool
	cropX0   float32
	cropY0   float32
	cropX1   float32
	cropY1   float32
	cropDrag bool

	zoom       float32
	panX, panY float32
	fitPending bool
	lastViewW  float32
	lastViewH  float32

	hoverValid  bool
	hoverImgX   int
	hoverImgY   int
	hoverR      uint8
	hoverG      uint8
	hoverB      uint8
	liveMeasure winapi.MeasureResult
	lastMeasX   int
	lastMeasY   int

	historyOpen            bool
	helpOpen               bool
	settingsOpen           bool
	openAtLoginToggle      bool
	autoCheckUpdatesToggle bool
	quickSaveToggle        bool
	themeChoice            ThemeMode
	themeChoiceSynced      ThemeMode
	themeSelectOpen        bool
	formatChoice           string
	formatChoiceSynced     string
	formatSelectOpen       bool
	ocrEngineChoice        ocr.Engine
	ocrEngineSynced        ocr.Engine
	ocrEngineSelectOpen    bool
	redactKeywordsInput    string
	recordingHotkey        HotkeyAction
	settingsErr            string
	hotkeyRecHandles       [5]ui.Handle
	toastMsg               string
	toastUntil             time.Time
	brandBmp               *ui.Bitmap
}

// NewEditorState creates an initialized Glasswork Liquid Glass Studio editor.
func NewEditorState(a *App) *EditorState {
	brandBmp, _ := ui.DecodeBitmap(icons.AppIconPNG(128))
	return &EditorState{
		app:         a,
		tool:        canvas.ToolArrow,
		colorIdx:    0,
		strokeWidth: 4,
		filled:      false,
		rounded:     true,
		fontSize:    22,
		textDraft:   "标注文字",
		blockSize:   14,
		measureSens: 28,
		nextCounter: 1,
		selectedIdx: -1,
		zoom:        1.0,
		lastMeasX:   -1,
		lastMeasY:   -1,
		brandBmp:    brandBmp,
	}
}

// ActiveTool exposes the selected tool for automated testing.
func (e *EditorState) ActiveTool() canvas.Tool { return e.tool }

// MosaicStyle returns the active mosaic visual style.
func (e *EditorState) MosaicStyle() canvas.MosaicStyle { return e.mosaicStyle }

// SetMosaicStyle updates the active mosaic visual style.
func (e *EditorState) SetMosaicStyle(s canvas.MosaicStyle) { e.mosaicStyle = s }

// ApplyOCRWordsForTest injects recognized OCR words and runs smart redaction synchronously (used by tests).
func (e *EditorState) ApplyOCRWordsForTest(words []winapi.OCRWord) int {
	return e.applySmartRedactWords(words, e.tool, e.mosaicStyle, int(math.Round(e.blockSize)))
}

// SettingsOpen reports whether the Settings sheet is currently visible.
func (e *EditorState) SettingsOpen() bool { return e.settingsOpen }

// RecordingHotkey reports which hotkey action is currently listening for input ("" if none).
func (e *EditorState) RecordingHotkey() HotkeyAction { return e.recordingHotkey }

// OpenSettings syncs OS startup and updater state and opens the Settings sheet.
func (e *EditorState) OpenSettings() {
	e.app.SyncOpenAtLogin()
	e.app.SyncUpdaterState()
	e.openAtLoginToggle = e.app.OpenAtLogin()
	e.autoCheckUpdatesToggle = e.app.AutomaticUpdateChecks()
	e.quickSaveToggle = e.app.QuickSave()
	e.redactKeywordsInput = e.app.RedactKeywords()
	e.syncSettingsSelectChoices()
	e.themeSelectOpen = false
	e.formatSelectOpen = false
	e.ocrEngineSelectOpen = false
	e.helpOpen = false
	e.settingsOpen = true
	e.settingsErr = ""
}

func (e *EditorState) syncSettingsSelectChoices() {
	curTheme := e.app.ThemeMode()
	if e.themeChoice == "" || curTheme != e.themeChoiceSynced {
		e.themeChoice = curTheme
		e.themeChoiceSynced = curTheme
	}
	curFmt := e.app.SaveFormat()
	if e.formatChoice == "" || curFmt != e.formatChoiceSynced {
		e.formatChoice = curFmt
		e.formatChoiceSynced = curFmt
	}
	curOCR := e.app.OCREngine()
	if e.ocrEngineChoice == "" || curOCR != e.ocrEngineSynced {
		e.ocrEngineChoice = curOCR
		e.ocrEngineSynced = curOCR
	}
}

func (e *EditorState) anySettingsSelectOpen() bool {
	return e.themeSelectOpen || e.formatSelectOpen || e.ocrEngineSelectOpen
}

// SyncUpdaterState refreshes Settings toggles after background updater state changes.
func (e *EditorState) SyncUpdaterState() {
	e.autoCheckUpdatesToggle = e.app.AutomaticUpdateChecks()
}

// CloseSettings closes the Settings sheet and restores paused global shortcuts.
func (e *EditorState) CloseSettings() {
	e.stopRecordingHotkey()
	e.app.SetRedactKeywords(e.redactKeywordsInput)
	e.themeSelectOpen = false
	e.formatSelectOpen = false
	e.ocrEngineSelectOpen = false
	e.settingsOpen = false
	e.settingsErr = ""
}

func (e *EditorState) startRecordingHotkey(c *ui.Context, idx int, action HotkeyAction) {
	e.recordingHotkey = action
	e.settingsErr = ""
	e.app.PauseGlobalShortcuts()
	if idx >= 0 && idx < len(e.hotkeyRecHandles) {
		e.hotkeyRecHandles[idx].Focus(c)
	}
}

func (e *EditorState) stopRecordingHotkey() {
	if e.recordingHotkey != "" {
		e.recordingHotkey = ""
		e.app.ResumeGlobalShortcuts()
	}
}

func (e *EditorState) commitHotkey(action HotkeyAction, acc string) {
	err := e.app.SetHotkey(action, acc)
	e.recordingHotkey = ""
	if err != nil {
		e.settingsErr = err.Error()
		return
	}
	e.settingsErr = ""
	if acc == "" {
		e.SetToast("已取消「" + hotkeyLabel(action) + "」快捷键")
	} else {
		e.SetToast(fmt.Sprintf("已设置「%s」为 %s", hotkeyLabel(action), FormatHotkeyDisplay(acc)))
	}
}

// LoadImage replaces the active document with an independent copy of img and fits it cleanly to viewport.
func (e *EditorState) LoadImage(img *image.RGBA, title string) {
	if img == nil {
		return
	}
	e.loadOwnedImage(canvas.CloneRGBA(img), nil, title)
}

type pendingEditorRedact struct {
	gen       int
	words     []winapi.OCRWord
	kind      canvas.Tool
	style     canvas.MosaicStyle
	blockSize int
	errMsg    string
}

func (e *EditorState) clearRedactCache() {
	e.redactMu.Lock()
	e.redactGen++
	e.redactBusy = false
	e.pendingRedact = nil
	e.redactMu.Unlock()
	e.ocrWords = nil
	e.ocrLines = nil
}

func (e *EditorState) isRedactBusy() bool {
	e.redactMu.Lock()
	busy := e.redactBusy
	e.redactMu.Unlock()
	return busy
}

func (e *EditorState) drainPendingRedact() {
	e.redactMu.Lock()
	out := e.pendingRedact
	gen := e.redactGen
	e.pendingRedact = nil
	e.redactMu.Unlock()
	if out == nil || out.gen != gen {
		return
	}
	if out.errMsg != "" {
		e.SetToast(out.errMsg)
		return
	}
	n := e.applySmartRedactWords(out.words, out.kind, out.style, out.blockSize)
	if n > 0 {
		e.SetToast(fmt.Sprintf("已智能打码 %d 处敏感信息", n))
	} else {
		e.SetToast("未检测到敏感信息 · 单击文字可精准打码")
	}
}

func (e *EditorState) applySmartRedactWords(words []winapi.OCRWord, kind canvas.Tool, style canvas.MosaicStyle, blockSize int) int {
	if e.baseImg == nil {
		return 0
	}
	e.ocrWords, e.ocrLines = redact.BuildInteractiveTokens(words, e.baseImg)
	return e.applySmartRedactFromWords(words, kind, style, blockSize)
}

func (e *EditorState) applySmartRedactFromCached(kind canvas.Tool, style canvas.MosaicStyle, blockSize int) int {
	return e.applySmartRedactFromWords(e.ocrWords, kind, style, blockSize)
}

func (e *EditorState) applySmartRedactFromWords(words []winapi.OCRWord, kind canvas.Tool, style canvas.MosaicStyle, blockSize int) int {
	if e.baseImg == nil {
		return 0
	}
	var customKWs []string
	if e.app != nil {
		customKWs = redact.ParseKeywords(e.app.RedactKeywords())
	}
	candidates := buildRedactionAnnotations(words, e.baseImg.Bounds(), e.baseImg, 0, 0, customKWs, kind, style, blockSize, &e.nextAnnID)
	added := filterNonOverlappingRedactions(e.annotations, candidates)
	if len(added) == 0 {
		return 0
	}
	e.pushUndo()
	e.annotations = append(e.annotations, added...)
	e.selectedIdx = -1
	e.rasterDirty = true
	return len(added)
}

func (e *EditorState) triggerSmartRedact() {
	if e.baseImg == nil {
		return
	}
	kind := e.tool
	if kind != canvas.ToolBlur {
		kind = canvas.ToolPixelate
		e.tool = canvas.ToolPixelate
	}
	style := e.mosaicStyle
	bs := int(math.Round(e.blockSize))

	if len(e.ocrWords) > 0 {
		n := e.applySmartRedactFromCached(kind, style, bs)
		if n > 0 {
			e.SetToast(fmt.Sprintf("已智能打码 %d 处敏感信息", n))
		} else {
			e.SetToast("未发现新的敏感信息 · 单击文字可精准打码")
		}
		return
	}

	e.redactMu.Lock()
	if e.redactBusy {
		e.redactMu.Unlock()
		return
	}
	e.redactBusy = true
	e.redactGen++
	gen := e.redactGen
	e.redactMu.Unlock()

	// baseImg is immutable during an edit session (applyCrop replaces the pointer),
	// so passing it read-only avoids an extra 15-35 MB full-resolution CloneRGBA.
	src := e.baseImg
	appRef := e.app
	go func() {
		var out pendingEditorRedact
		out.gen = gen
		out.kind = kind
		out.style = style
		out.blockSize = bs
		if appRef == nil {
			out.errMsg = "离线识别不可用"
		} else {
			res, _, err := appRef.RecognizeOCR(src)
			if err != nil {
				out.errMsg = "识别失败：" + err.Error()
			} else {
				out.words = res.Words
			}
		}
		e.redactMu.Lock()
		if e.redactGen == gen {
			e.redactBusy = false
			e.pendingRedact = &out
		}
		e.redactMu.Unlock()
		if appRef != nil {
			appRef.mu.Lock()
			w := appRef.editorWin
			appRef.mu.Unlock()
			if w != nil {
				w.Update(func() {})
			}
		}
	}()
}

// loadOwnedImage takes ownership of an already-fresh *image.RGBA without cloning it a second time.
func (e *EditorState) loadOwnedImage(img *image.RGBA, pngBytes []byte, title string) {
	if img == nil {
		return
	}
	e.clearRedactCache()
	e.title = title
	e.baseImg = img
	e.basePNG = pngBytes
	e.annotations = nil
	e.undoStack = nil
	e.redoStack = nil
	e.nextCounter = 1
	e.selectedIdx = -1
	e.drawing = false
	e.hasCrop = false
	e.rasterImg = nil
	e.rasterBmpOnBase = false
	e.rasterDirty = true
	e.fitPending = true
	e.lastMeasX, e.lastMeasY = -1, -1
	e.rebuildCaches()
}

// ClearDocument releases active document buffers when returning to Home stage.
func (e *EditorState) ClearDocument() {
	e.clearRedactCache()
	e.baseImg = nil
	e.basePNG = nil
	e.rasterImg = nil
	e.rasterBmp = nil
	e.rasterBmpOnBase = false
	e.annotations = nil
	e.undoStack = nil
	e.redoStack = nil
	e.selectedIdx = -1
	e.drawing = false
	e.hasCrop = false
	reclaimMemoryAsync()
}

// ParkForTray releases uncompressed RGBA buffers, GPU bitmaps, and undo snapshots
// when the Studio window is closed to the system tray, keeping only compressed PNG
// bytes (~200-500 KB) so reopening Studio restores the document and annotations seamlessly.
func (e *EditorState) ParkForTray() {
	e.clearRedactCache()
	if e.baseImg != nil && len(e.basePNG) == 0 {
		if encoded, err := canvas.EncodePNG(e.baseImg); err == nil {
			e.basePNG = encoded
		}
	}
	e.baseImg = nil
	e.rasterImg = nil
	e.rasterBmp = nil
	e.rasterBmpOnBase = false
	e.rasterDirty = true
	e.undoStack = nil
	e.redoStack = nil
	e.selectedIdx = -1
	e.drawing = false
	e.hasCrop = false
}

// RestoreFromTray inflates the parked compressed PNG back into baseImg when Studio reopens.
func (e *EditorState) RestoreFromTray() {
	if e.baseImg != nil || len(e.basePNG) == 0 {
		return
	}
	if img := decodeImageRGBA(e.basePNG); img != nil {
		e.baseImg = img
		e.rasterImg = nil
		e.rasterBmp = nil
		e.rasterBmpOnBase = false
		e.rasterDirty = true
		e.fitPending = true
	}
}

// SetToast displays a floating Glasswork confirmation pill.
func (e *EditorState) SetToast(msg string) {
	e.toastMsg = msg
	e.toastUntil = time.Now().Add(2200 * time.Millisecond)
}

func (e *EditorState) activeColor() ui.Color {
	if e.colorIdx < 0 || e.colorIdx >= len(canvas.DefaultPalette) {
		return canvas.DefaultPalette[0].Color
	}
	return canvas.DefaultPalette[e.colorIdx].Color
}

func (e *EditorState) pushUndo() {
	if e.baseImg == nil {
		return
	}
	// Share the immutable baseImg pointer (only applyCrop allocates a new image)
	// so drawing annotations never duplicates 15–35 MB of RGBA pixels per step!
	snap := canvas.Snapshot{
		Image:       e.baseImg,
		Annotations: canvas.CloneAnnotations(e.annotations),
		NextCounter: e.nextCounter,
	}
	e.undoStack = append(e.undoStack, snap)
	if len(e.undoStack) > 24 {
		e.undoStack = e.undoStack[len(e.undoStack)-24:]
	}
	e.redoStack = nil
}

func (e *EditorState) undo() {
	if len(e.undoStack) == 0 {
		return
	}
	cur := canvas.Snapshot{
		Image:       e.baseImg,
		Annotations: canvas.CloneAnnotations(e.annotations),
		NextCounter: e.nextCounter,
	}
	e.redoStack = append(e.redoStack, cur)
	prev := e.undoStack[len(e.undoStack)-1]
	e.undoStack = e.undoStack[:len(e.undoStack)-1]
	if prev.Image != e.baseImg {
		e.basePNG = nil
		e.rasterBmpOnBase = false
	}
	e.baseImg = prev.Image
	e.annotations = prev.Annotations
	e.nextCounter = prev.NextCounter
	e.selectedIdx = -1
	e.rasterDirty = true
}

func (e *EditorState) redo() {
	if len(e.redoStack) == 0 {
		return
	}
	cur := canvas.Snapshot{
		Image:       e.baseImg,
		Annotations: canvas.CloneAnnotations(e.annotations),
		NextCounter: e.nextCounter,
	}
	e.undoStack = append(e.undoStack, cur)
	next := e.redoStack[len(e.redoStack)-1]
	e.redoStack = e.redoStack[:len(e.redoStack)-1]
	if next.Image != e.baseImg {
		e.basePNG = nil
		e.rasterBmpOnBase = false
	}
	e.baseImg = next.Image
	e.annotations = next.Annotations
	e.nextCounter = next.NextCounter
	e.selectedIdx = -1
	e.rasterDirty = true
}

func (e *EditorState) setRasterBitmap(src *image.RGBA) {
	if src == nil {
		e.rasterBmp = nil
		return
	}
	if e.rasterBmp != nil {
		bw, bh := e.rasterBmp.Size()
		if bw == src.Bounds().Dx() && bh == src.Bounds().Dy() {
			e.rasterBmp.Update(src)
			return
		}
	}
	e.rasterBmp = ui.NewBitmap(src)
}

func (e *EditorState) rebuildCaches() {
	if e.baseImg == nil {
		return
	}
	if !e.rasterDirty && e.rasterBmp != nil {
		return
	}
	if !canvas.HasRasterAnnotations(e.annotations) {
		// No pixel-modifying effects: share baseImg directly instead of keeping
		// a duplicate 10-35 MB full-resolution RGBA copy in e.rasterImg!
		e.rasterImg = nil
		if !e.rasterBmpOnBase || e.rasterBmp == nil {
			e.setRasterBitmap(e.baseImg)
			e.rasterBmpOnBase = true
		}
		e.rasterDirty = false
		return
	}
	e.rasterImg = canvas.RenderRasterEffects(e.baseImg, e.annotations)
	e.setRasterBitmap(e.rasterImg)
	e.rasterBmpOnBase = false
	e.rasterDirty = false
}

// ExportComposite returns the final export-ready image with raster effects and vector annotations.
func (e *EditorState) ExportComposite() *image.RGBA {
	base := e.baseImg
	if base == nil && len(e.basePNG) > 0 {
		base = decodeImageRGBA(e.basePNG)
	}
	if base == nil {
		return nil
	}
	return canvas.RenderComposite(base, e.annotations)
}

func (e *EditorState) copyComposite() {
	comp := e.ExportComposite()
	if comp == nil {
		e.SetToast("当前没有可复制的截图")
		return
	}
	pngBytes, err := canvas.EncodePNG(comp)
	if err != nil {
		e.SetToast("图片编码失败")
		return
	}
	if err := mygo.Clipboard.WriteImage(pngBytes); err != nil {
		e.SetToast("写入剪贴板失败")
		return
	}
	e.SetToast(fmt.Sprintf("已复制 %d × %d 到剪贴板", comp.Bounds().Dx(), comp.Bounds().Dy()))
}

func (e *EditorState) saveComposite() {
	comp := e.ExportComposite()
	if comp == nil {
		return
	}
	e.app.mu.Lock()
	parent := e.app.editorWin
	e.app.mu.Unlock()
	go e.app.SaveImagePreferred(comp, nil, parent)
}

func (e *EditorState) pinComposite() {
	comp := e.ExportComposite()
	if comp == nil {
		return
	}
	e.app.OpenPin(comp, 0, 0, 0)
	e.SetToast("已贴图至桌面顶层")
}

func (e *EditorState) applyCrop() {
	if e.baseImg == nil || !e.hasCrop {
		return
	}
	x0, x1 := e.cropX0, e.cropX1
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	y0, y1 := e.cropY0, e.cropY1
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	r := image.Rect(int(math.Round(float64(x0))), int(math.Round(float64(y0))),
		int(math.Round(float64(x1))), int(math.Round(float64(y1)))).Intersect(e.baseImg.Bounds())
	if r.Dx() < 8 || r.Dy() < 8 {
		e.SetToast("裁剪区域过小")
		return
	}
	e.pushUndo()
	e.clearRedactCache()
	e.baseImg = winapi.CropRGBA(e.baseImg, r)
	e.basePNG = nil
	e.rasterBmpOnBase = false
	dx, dy := -float32(r.Min.X), -float32(r.Min.Y)
	for i := range e.annotations {
		e.annotations[i].Translate(dx, dy)
	}
	e.hasCrop = false
	e.tool = canvas.ToolSelect
	e.rasterDirty = true
	e.fitPending = true
	e.SetToast(fmt.Sprintf("已裁剪为 %d × %d", r.Dx(), r.Dy()))
}

func (e *EditorState) docDisplaySize() (w, h, imgOffX, imgOffY int) {
	if e.baseImg == nil {
		return 0, 0, 0, 0
	}
	return e.baseImg.Bounds().Dx(), e.baseImg.Bounds().Dy(), 0, 0
}

func (e *EditorState) fitToViewport(viewW, viewH float32) {
	dw, dh, _, _ := e.docDisplaySize()
	if dw <= 0 || dh <= 0 || viewW <= 80 || viewH <= 140 {
		return
	}
	padX := float32(64)
	padTop := float32(76)
	padBottom := float32(126)
	availW := max(float32(120), viewW-padX*2)
	availH := max(float32(120), viewH-padTop-padBottom)

	// Tall long screenshots fit comfortably to width and align to the top so text stays crisp
	// and immediately readable instead of shrinking into a thin vertical strip.
	fitW := min(float32(1.0), availW/float32(dw))
	if float32(dh) > float32(dw)*2.0 && float32(dh)*fitW > availH*1.35 {
		z := clamp32(min(float32(1.0), (availW*0.72)/float32(dw)), 0.25, 1.0)
		e.zoom = z
		e.panX = (viewW - float32(dw)*z) * 0.5
		e.panY = padTop + 16
		e.fitPending = false
		return
	}

	z := min(availW/float32(dw), availH/float32(dh))
	if z > 1.0 {
		z = 1.0
	}
	if z < 0.15 {
		z = 0.15
	}
	e.zoom = z
	e.panX = (viewW - float32(dw)*z) * 0.5
	e.panY = padTop + (availH-float32(dh)*z)*0.5
	e.fitPending = false
}

func (e *EditorState) zoomAround(viewX, viewY, factor float32) {
	oldZ := e.zoom
	newZ := clamp32(oldZ*factor, 0.15, 8.0)
	if math.Abs(float64(newZ-oldZ)) < 0.001 {
		return
	}
	ratio := newZ / oldZ
	e.panX = viewX - (viewX-e.panX)*ratio
	e.panY = viewY - (viewY-e.panY)*ratio
	e.zoom = newZ
}

// appleTokens ties Koma's light/dark modes directly to Glasswork's two materials.
type appleTokens struct {
	dark          bool
	glass         glasswork.Surface
	inks          glasswork.InkColors
	canvasBg      ui.Color
	dotColor      ui.Color
	textPrimary   ui.Color
	textSecondary ui.Color
	textTertiary  ui.Color
	accent        ui.Color
	hoverFill     ui.Color
	hairline      ui.Color
	cardBg        ui.Color
}

func (e *EditorState) tokens() appleTokens {
	if e.app.IsDarkMode() {
		g := glasswork.Dark()
		g.Density = 0.56
		g.Floor = 0.38
		inks := glasswork.KindDark.Inks()
		return appleTokens{
			dark:          true,
			glass:         g,
			inks:          inks,
			canvasBg:      ui.Hex("#111319"),
			dotColor:      ui.RGBA(255, 255, 255, 0.08),
			textPrimary:   ui.Hex("#F8FAFC"),
			textSecondary: ui.Hex("#CBD5E1"),
			textTertiary:  ui.Hex("#94A3B8"),
			accent:        ui.Hex("#0A84FF"),
			hoverFill:     ui.RGBA(255, 255, 255, 0.14),
			hairline:      ui.RGBA(255, 255, 255, 0.16),
			cardBg:        ui.Hex("#1E222B"),
		}
	}
	g := glasswork.Light()
	g.Density = 0.62
	g.Floor = 0.48
	inks := glasswork.KindLight.Inks()
	return appleTokens{
		dark:          false,
		glass:         g,
		inks:          inks,
		canvasBg:      ui.Hex("#DFE6F0"),
		dotColor:      ui.RGBA(15, 23, 42, 0.11),
		textPrimary:   ui.Hex("#0F172A"),
		textSecondary: ui.Hex("#334155"),
		textTertiary:  ui.Hex("#475569"),
		accent:        ui.Hex("#0066E6"),
		hoverFill:     ui.RGBA(15, 23, 42, 0.09),
		hairline:      ui.RGBA(15, 23, 42, 0.16),
		cardBg:        ui.Hex("#FFFFFF"),
	}
}

// View renders the full-bleed canvas with floating Glasswork liquid glass chrome.
func (e *EditorState) View(c *ui.Context) {
	if e.app.IsDarkMode() {
		c.SetTheme(ui.DarkTheme())
	} else {
		c.SetTheme(ui.LightTheme())
	}
	tk := e.tokens()
	e.drainPendingRedact()
	e.handleShortcuts(c)

	if e.baseImg != nil {
		e.rebuildCaches()
	}

	root := ui.Box(c).Fill().Background(tk.canvasBg)
	if files := root.DroppedFiles(); len(files) > 0 {
		go e.app.LoadFilePath(files[0])
	}

	root.Children(func() {
		// Layer 1: Edge-to-edge Workspace (Wallpaper Home Stage OR Full-Bleed Canvas)
		if e.baseImg == nil {
			e.renderAppleHomeStage(c, tk)
		} else {
			e.renderCanvasViewport(c, tk)
		}

		// Layer 2: Top drag strip (sits BELOW the top glass capsules so capsule buttons
		// take all clicks first while empty middle top space drags the window cleanly!)
		tb := c.TitleBar()
		dragH := max(float32(46), tb.Height)
		ui.Box(c).Absolute().Top(0).Left(0).Right(0).Height(dragH).DragWindow()

		// Layer 3: Floating Glasswork Top Header Capsules
		e.renderFloatingTopChrome(c, tk)

		// Layer 4: Floating Glasswork History Drawer (left)
		if e.historyOpen {
			e.renderFloatingHistoryDrawer(c, tk)
		}

		// Layer 5: Floating Glasswork Bottom Tool Dock + Corner Pills
		if e.baseImg != nil {
			e.renderFloatingBottomDock(c, tk)
			e.renderFloatingCornerPills(c, tk)
		} else {
			e.renderHomeCornerControls(c, tk)
		}
	})

	if e.helpOpen {
		e.renderHelpSheet(c, tk)
	}
	if e.settingsOpen {
		e.renderSettingsSheet(c, tk)
	}

	// Calm floating Glasswork Toast with liquid materialize enter/exit
	if e.toastMsg != "" && time.Now().Before(e.toastUntil) {
		ui.Overlay(c, func() {
			ui.Row(c).
				Absolute().Top(64).Left(0).Right(0).
				Justify(ui.Center).PassThrough().
				Children(func() {
					ui.Row(c.Key("glasswork-toast")).
						Padding(8, 16, 8, 14).Gap(7).
						Radius(20).
						Material(tk.glass).
						AlignItems(ui.Center).
						Transition(glasswork.PanelTransition(0, -12)).
						Children(func() {
							ui.Icon(c, icons.Check.Active).Size(16, 16).TextColor(tk.inks.Accent)
							ui.Text(c, e.toastMsg).FontSize(12.5).FontWeight(600).TextColor(tk.textPrimary)
						})
				})
		})
		if rem := time.Until(e.toastUntil); rem > 0 {
			c.After(rem + 20*time.Millisecond)
		}
	}
}

func (e *EditorState) handleShortcuts(c *ui.Context) {
	// While recording a global shortcut, let the focused recorder receive every keystroke.
	if e.recordingHotkey != "" {
		return
	}
	// While a Settings Liquid Glass Select popup is open, let it handle Escape / Arrow / Enter keys
	// without closing the Settings sheet underneath.
	if e.settingsOpen && e.anySettingsSelectOpen() {
		return
	}
	if c.Shortcut(ui.Ctrl, ui.KeyComma) {
		if e.settingsOpen {
			e.CloseSettings()
		} else {
			e.OpenSettings()
		}
		return
	}
	if c.Shortcut(0, ui.KeyEscape) {
		if e.settingsOpen {
			e.CloseSettings()
			return
		}
		if e.helpOpen {
			e.helpOpen = false
			return
		}
		if e.historyOpen {
			e.historyOpen = false
			return
		}
		if e.hasCrop {
			e.hasCrop = false
			return
		}
		e.selectedIdx = -1
		return
	}
	if e.settingsOpen {
		return
	}

	if c.Shortcut(ui.Ctrl, ui.KeyC) && !c.FocusTakesText() {
		e.copyComposite()
		return
	}
	if c.Shortcut(ui.Ctrl, ui.KeyV) && !c.FocusTakesText() {
		e.app.PasteFromClipboard()
		return
	}
	if c.Shortcut(ui.Ctrl, ui.KeyS) {
		e.saveComposite()
		return
	}
	if c.Shortcut(ui.Ctrl, ui.KeyO) {
		e.app.OpenImageFromDisk()
		return
	}
	if c.Shortcut(ui.Ctrl, ui.KeyP) {
		e.pinComposite()
		return
	}
	if c.Shortcut(ui.Ctrl, ui.KeyZ) && !c.FocusTakesText() {
		e.undo()
		return
	}
	if (c.Shortcut(ui.Ctrl|ui.Shift, ui.KeyZ) || c.Shortcut(ui.Ctrl, ui.KeyY)) && !c.FocusTakesText() {
		e.redo()
		return
	}
	if c.Shortcut(ui.Ctrl, ui.Key0) && e.baseImg != nil {
		e.fitPending = true
		return
	}
	if c.Shortcut(ui.Ctrl, ui.Key1) && e.baseImg != nil {
		e.zoom = 1.0
		dw, dh, _, _ := e.docDisplaySize()
		e.panX = (e.lastViewW - float32(dw)) * 0.5
		e.panY = (e.lastViewH - float32(dh)) * 0.5
		return
	}
	if c.Shortcut(0, ui.KeyF1) {
		e.helpOpen = !e.helpOpen
		return
	}

	if c.FocusTakesText() || e.baseImg == nil {
		return
	}

	if e.tool == canvas.ToolCrop && e.hasCrop && c.Shortcut(0, ui.KeyEnter) {
		e.applyCrop()
		return
	}
	if (c.Shortcut(0, ui.KeyDelete) || c.Shortcut(0, ui.KeyBackspace)) && e.selectedIdx >= 0 && e.selectedIdx < len(e.annotations) {
		e.pushUndo()
		e.annotations = append(e.annotations[:e.selectedIdx], e.annotations[e.selectedIdx+1:]...)
		e.selectedIdx = -1
		e.rasterDirty = true
		return
	}

	if c.Shortcut(ui.Shift, ui.KeyX) {
		e.triggerSmartRedact()
		return
	}
	if c.Shortcut(0, ui.KeyB) {
		e.tool = canvas.ToolPixelate
		e.mosaicStyle = canvas.MosaicBlur
		e.selectedIdx = -1
		return
	}

	toolKeys := []struct {
		key  ui.Key
		tool canvas.Tool
	}{
		{ui.KeyV, canvas.ToolSelect},
		{ui.KeyA, canvas.ToolArrow},
		{ui.KeyL, canvas.ToolLine},
		{ui.KeyR, canvas.ToolRect},
		{ui.KeyO, canvas.ToolEllipse},
		{ui.KeyP, canvas.ToolPen},
		{ui.KeyH, canvas.ToolHighlighter},
		{ui.KeyT, canvas.ToolText},
		{ui.KeyN, canvas.ToolCounter},
		{ui.KeyX, canvas.ToolPixelate},
		{ui.KeyS, canvas.ToolSpotlight},
		{ui.KeyM, canvas.ToolMeasure},
		{ui.KeyC, canvas.ToolCrop},
	}
	for _, item := range toolKeys {
		if c.Shortcut(0, item.key) {
			e.tool = item.tool
			if item.tool != canvas.ToolSelect {
				e.selectedIdx = -1
			}
			return
		}
	}
}

func (e *EditorState) renderFloatingTopChrome(c *ui.Context, tk appleTokens) {
	tb := c.TitleBar()
	topY := float32(10)
	leftX := tb.Left + 14
	rightX := tb.Right + 14

	e.app.mu.Lock()
	histCount := len(e.app.history)
	countdown := e.app.delayCountdown
	e.app.mu.Unlock()

	// Top-Left Glasswork Liquid Glass Capsule
	leftCap := ui.Row(c).
		Absolute().Top(topY).Left(leftX).Height(40).
		Padding(4, 10, 4, 8).Gap(4).
		Radius(20).
		Material(tk.glass).
		AlignItems(ui.Center)
	leftCap.Clicked()
	leftCap.Children(func() {
		brand := ui.Row(c).Gap(7).Padding(0, 4, 0, 2).AlignItems(ui.Center).
			Role(ui.RoleButton).Label("Koma 首页").
			Cursor(ui.CursorPointer).Tooltip("返回首页")
		brand.Children(func() {
			if e.brandBmp != nil {
				ui.Image(c, e.brandBmp).Size(22, 22)
			}
			ui.Text(c, "Koma").FontSize(13.5).FontWeight(700).TextColor(tk.textPrimary)
		})
		if brand.Clicked() && e.baseImg != nil {
			e.ClearDocument()
		}

		appleGlassDivider(c, tk)

		histTip := fmt.Sprintf("最近截图 (%d)", histCount)
		if appleIconBtn(c, tk, icons.History, "最近截图", histTip, e.historyOpen) {
			e.historyOpen = !e.historyOpen
		}
		if appleIconBtn(c, tk, icons.CaptureArea, "区域截图", FormatHotkeyTooltip("区域截图", e.app.Hotkey(HotkeyArea)), false) {
			e.app.StartAreaCapture(true)
		}
		if appleIconBtn(c, tk, icons.LongCapture, "长截图", FormatHotkeyTooltip("滚动长截图", e.app.Hotkey(HotkeyLong)), false) {
			e.app.StartLongCapture(true)
		}
		if appleIconBtn(c, tk, icons.CaptureScreen, "全屏截图", FormatHotkeyTooltip("全屏截图", e.app.Hotkey(HotkeyFull)), false) {
			e.app.CaptureFullscreen(true)
		}
		if countdown > 0 {
			ui.Row(c).Height(26).Padding(0, 8, 0, 8).Radius(13).
				Background(ui.Hex("#FF9500").Alpha(0.20)).AlignItems(ui.Center).Children(func() {
				ui.Textf(c, "%d 秒…", countdown).FontSize(11.5).FontWeight(700).TextColor(ui.Hex("#FF9500")).
					ContentTransition(glasswork.ContentNumericDown)
			})
		} else if appleIconBtn(c, tk, icons.CaptureDelay, "延时截图", FormatHotkeyTooltip("延时 3 秒截图", e.app.Hotkey(HotkeyDelay)), false) {
			e.app.StartDelayedCapture(3)
		}
	})

	// Top-Right Glasswork Liquid Glass Capsule (when editing an image)
	if e.baseImg != nil {
		rightCap := ui.Row(c.Key("studio-top-right")).
			Absolute().Top(topY).Right(rightX).Height(40).
			Padding(4, 6, 4, 8).Gap(4).
			Radius(20).
			Material(tk.glass).
			AlignItems(ui.Center).
			Transition(glasswork.PanelTransition(10, 0))
		rightCap.Clicked()
		rightCap.Children(func() {
			if appleIconBtnDisabled(c, tk, icons.Undo, "撤销", "撤销 (Ctrl+Z)", len(e.undoStack) == 0) {
				e.undo()
			}
			if appleIconBtnDisabled(c, tk, icons.Redo, "重做", "重做 (Ctrl+Y)", len(e.redoStack) == 0) {
				e.redo()
			}
			appleGlassDivider(c, tk)

			if appleIconBtn(c, tk, icons.OCR, "提取文字", "文字识别 OCR", false) {
				if comp := e.ExportComposite(); comp != nil {
					e.app.RecognizeTextFromImage(comp)
				}
			}
			if appleIconBtn(c, tk, icons.Pin, "贴图", "贴图到桌面 (Ctrl+P)", false) {
				e.pinComposite()
			}
			if appleIconBtn(c, tk, icons.Save, "保存", "保存 (Ctrl+S)", false) {
				e.saveComposite()
			}

			copyBtn := ui.Row(c.Key("studio-copy-btn")).
				Height(30).Padding(0, 14, 0, 11).Gap(5).Margin(0, 0, 0, 2).
				Radius(15).AlignItems(ui.Center).
				Role(ui.RoleButton).Label("复制").
				Material(glasswork.Tinted(tk.accent)).
				Cursor(ui.CursorPointer).Tooltip("复制成品图到剪贴板 (Ctrl+C)")
			glasswork.ApplyLiquidButton(copyBtn, glasswork.PrimaryButtonOpts())
			copyBtn.Children(func() {
				ui.Icon(c, icons.Copy.Active).Size(16, 16).TextColor(ui.Hex("#FFFFFF")).Shrink(0)
				ui.Text(c, "复制").FontSize(12.5).FontWeight(600).TextColor(ui.Hex("#FFFFFF")).SingleLine().Shrink(0)
			})
			if copyBtn.Clicked() {
				e.copyComposite()
			}
		})
	}
}

func (e *EditorState) renderAppleHomeStage(c *ui.Context, tk appleTokens) {
	e.app.mu.Lock()
	recent := append([]HistoryEntry(nil), e.app.history...)
	e.app.mu.Unlock()

	// Clean, uncluttered Apple-style Home stage: let the focal CTA and layout speak.
	ui.Column(c).Absolute().Fill().Center().Padding(40).Gap(20).Draw(func(pt *ui.Painter, r ui.Rect) {
		drawAppleMeshWallpaper(pt, r, tk.dark)
	}).Children(func() {
		ui.Column(c).AlignItems(ui.Center).Gap(10).Children(func() {
			if e.brandBmp != nil {
				ui.Box(c).Size(76, 76).Center().Draw(func(pt *ui.Painter, r ui.Rect) {
					sr := ui.Rect{X: r.X + 2.5, Y: r.Y + 2.5, W: r.W - 5, H: r.H - 5}
					pt.Shadow(sr, 17, 0, 10, 24, 0, ui.Hex("#007AFF").Alpha(0.26))
					pt.Image(e.brandBmp, r, ui.Contain)
				})
			}
			ui.Text(c, "Koma").FontSize(32).FontWeight(700).TextColor(tk.textPrimary)
		})

		// Primary Tinted Glasswork Liquid Glass CTA with droplet wobble + gel-pull stretch
		primaryCTA := ui.Row(c.Key("home-primary-cta")).
			Height(46).Padding(0, 22, 0, 18).Gap(9).
			Radius(23).AlignItems(ui.Center).
			Role(ui.RoleButton).Label("区域截图").
			Material(glasswork.Tinted(tk.accent)).
			Cursor(ui.CursorPointer)
		glasswork.ApplyLiquidButton(primaryCTA, glasswork.PrimaryButtonOpts())
		primaryCTA.Children(func() {
			ui.Icon(c, icons.CaptureArea.Active).Size(19.5, 19.5).TextColor(ui.Hex("#FFFFFF"))
			ui.Text(c, "区域截图").FontSize(14.5).FontWeight(600).TextColor(ui.Hex("#FFFFFF"))
			if hkArea := e.app.Hotkey(HotkeyArea); hkArea != "" {
				ui.Row(c).Padding(2, 8, 2, 8).Radius(9).Background(ui.RGBA(255, 255, 255, 0.22)).Children(func() {
					ui.Text(c, FormatHotkeyDisplay(hkArea)).FontSize(11).FontWeight(600).TextColor(ui.Hex("#FFFFFF"))
				})
			}
		})
		if primaryCTA.Clicked() {
			e.app.StartAreaCapture(true)
		}

		// Compact Secondary Glasswork Action Capsule (essential capture & open actions only)
		ui.Row(c).
			Padding(5, 8, 5, 8).Gap(4).
			Radius(22).
			Material(tk.glass).
			AlignItems(ui.Center).
			Children(func() {
				if appleDockPillBtn(c, tk, icons.LongCapture, "长截图") {
					e.app.StartLongCapture(true)
				}
				if appleDockPillBtn(c, tk, icons.CaptureScreen, "全屏") {
					e.app.CaptureFullscreen(true)
				}
				if appleDockPillBtn(c, tk, icons.CaptureDelay, "3s 延时") {
					e.app.StartDelayedCapture(3)
				}
				appleGlassDivider(c, tk)
				if appleDockPillBtn(c, tk, icons.ClipboardIn, "粘贴") {
					e.app.PasteFromClipboard()
				}
				if appleDockPillBtn(c, tk, icons.FolderOpen, "打开") {
					e.app.OpenImageFromDisk()
				}
			})

		if len(recent) > 0 {
			limit := min(4, len(recent))
			ui.Row(c).Gap(12).Margin(8, 0, 0, 0).AlignItems(ui.Center).Children(func() {
				for i := 0; i < limit; i++ {
					it := recent[i]
					tip := fmt.Sprintf("%s · %d×%d", it.Title, it.Width, it.Height)
					cardBg := ui.RGBA(255, 255, 255, 0.58)
					cardBorder := ui.RGBA(255, 255, 255, 0.65)
					if tk.dark {
						cardBg = ui.RGBA(30, 34, 43, 0.72)
						cardBorder = ui.RGBA(255, 255, 255, 0.14)
					}
					card := ui.Column(c.Key(it.ID)).
						Width(148).Padding(6).Radius(16).
						Role(ui.RoleButton).Label(tip).Tooltip(tip).
						Background(cardBg).Border(1, cardBorder).Cursor(ui.CursorPointer).
						Transition(glasswork.StaggeredEnterTransition(i))
					glasswork.ApplyLiquidButton(card, glasswork.CardHoverOpts())
					card.Children(func() {
						if it.Thumb != nil {
							ui.Box(c).Height(84).Radius(10).Background(tk.canvasBg).Center().Children(func() {
								ui.Image(c, it.Thumb).Fill().Fit(ui.Contain).Radius(10)
							})
						}
					})
					if card.Clicked() {
						if img := it.DecodeImage(); img != nil {
							e.loadOwnedImage(img, it.PNG, it.Title)
						}
					}
				}
			})
		}
	})
}

func drawAppleMeshWallpaper(pt *ui.Painter, r ui.Rect, dark bool) {
	radBig := max(r.W, r.H) * 0.60
	if dark {
		// High-contrast Space Graphite backdrop: crisp white Windows controls & bright dark glass
		pt.FillGradient(r, ui.LinearGradient{
			Angle: 140,
			Oklab: true,
			Stops: []ui.GradientStop{
				{Color: ui.Hex("#0E1117"), At: 0},
				{Color: ui.Hex("#131823"), At: 0.55},
				{Color: ui.Hex("#171522"), At: 1},
			},
		}, 0)
		pt.FillRadialGradient(r, ui.RadialGradient{
			Center: ui.Point{X: 0.22, Y: 0.22},
			Radius: radBig,
			Stops:  []ui.GradientStop{{Color: ui.Hex("#0A84FF").Alpha(0.24), At: 0}, {Color: ui.Transparent, At: 1}},
			Oklab:  true,
		}, 0)
		pt.FillRadialGradient(r, ui.RadialGradient{
			Center: ui.Point{X: 0.78, Y: 0.76},
			Radius: radBig,
			Stops:  []ui.GradientStop{{Color: ui.Hex("#8B5CF6").Alpha(0.20), At: 0}, {Color: ui.Transparent, At: 1}},
			Oklab:  true,
		}, 0)
		return
	}

	// Crisp Cool-Slate Light backdrop: strong contrast for dark ink, glass capsules, and dark Windows controls!
	pt.FillGradient(r, ui.LinearGradient{
		Angle: 135,
		Oklab: true,
		Stops: []ui.GradientStop{
			{Color: ui.Hex("#D5E1F2"), At: 0},
			{Color: ui.Hex("#E3E9F3"), At: 0.52},
			{Color: ui.Hex("#E3DCEB"), At: 1},
		},
	}, 0)
	pt.FillRadialGradient(r, ui.RadialGradient{
		Center: ui.Point{X: 0.20, Y: 0.22},
		Radius: radBig,
		Stops:  []ui.GradientStop{{Color: ui.Hex("#60A5FA").Alpha(0.32), At: 0}, {Color: ui.Transparent, At: 1}},
		Oklab:  true,
	}, 0)
	pt.FillRadialGradient(r, ui.RadialGradient{
		Center: ui.Point{X: 0.78, Y: 0.72},
		Radius: radBig * 0.88,
		Stops:  []ui.GradientStop{{Color: ui.Hex("#C084FC").Alpha(0.20), At: 0}, {Color: ui.Transparent, At: 1}},
		Oklab:  true,
	}, 0)
	pt.FillRadialGradient(r, ui.RadialGradient{
		Center: ui.Point{X: 0.42, Y: 0.88},
		Radius: radBig * 0.82,
		Stops:  []ui.GradientStop{{Color: ui.Hex("#38BDF8").Alpha(0.22), At: 0}, {Color: ui.Transparent, At: 1}},
		Oklab:  true,
	}, 0)
}

func (e *EditorState) hasContextualControls() bool {
	switch e.tool {
	case canvas.ToolSelect:
		return (e.selectedIdx >= 0 && e.selectedIdx < len(e.annotations)) || len(e.annotations) > 0
	case canvas.ToolSpotlight:
		return false
	case canvas.ToolCrop:
		return e.hasCrop
	default:
		return true
	}
}

func (e *EditorState) renderFloatingBottomDock(c *ui.Context, tk appleTokens) {
	// Secondary Contextual Glasswork Pill — only appears when the active tool has actionable controls!
	if e.hasContextualControls() {
		ui.Row(c).
			Absolute().Bottom(70).Left(0).Right(0).
			Justify(ui.Center).PassThrough().
			Children(func() {
				sub := ui.Row(c.Key("studio-sub-pill")).
					Height(40).Padding(4, 14, 4, 14).Gap(10).
					Radius(20).
					Material(tk.glass).
					AlignItems(ui.Center).
					ClipX().
					Transition(glasswork.MorphBarTransition(8))
				sub.Clicked()

				sub.Children(func() {
					switch e.tool {
					case canvas.ToolSelect:
						if e.selectedIdx >= 0 && e.selectedIdx < len(e.annotations) {
							if appleSubChip(c, tk, "删除", false) {
								e.pushUndo()
								e.annotations = append(e.annotations[:e.selectedIdx], e.annotations[e.selectedIdx+1:]...)
								e.selectedIdx = -1
								e.rasterDirty = true
							}
						}
						if len(e.annotations) > 0 {
							if e.selectedIdx >= 0 && e.selectedIdx < len(e.annotations) {
								appleGlassDivider(c, tk)
							}
							if appleSubChip(c, tk, "清空标注", false) {
								e.pushUndo()
								e.annotations = nil
								e.selectedIdx = -1
								e.rasterDirty = true
							}
						}

					case canvas.ToolPixelate, canvas.ToolBlur:
						ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
							for _, st := range []canvas.MosaicStyle{canvas.MosaicCrystal, canvas.MosaicBlur, canvas.MosaicPixel, canvas.MosaicRedact} {
								if appleSubChipScoped(c, tk, st.Name(), e.mosaicStyle == st, "studio-mosaic-style") {
									e.tool = canvas.ToolPixelate
									e.mosaicStyle = st
									if e.selectedIdx >= 0 && e.selectedIdx < len(e.annotations) {
										if k := e.annotations[e.selectedIdx].Kind; k == canvas.ToolPixelate || k == canvas.ToolBlur {
											e.annotations[e.selectedIdx].Kind = canvas.ToolPixelate
											e.annotations[e.selectedIdx].MosaicStyle = st
											e.rasterDirty = true
										}
									}
								}
							}
						})
						if e.mosaicStyle != canvas.MosaicRedact {
							appleGlassDivider(c, tk)
							tip := "马赛克粒度"
							if e.mosaicStyle == canvas.MosaicBlur {
								tip = "模糊强度"
							}
							ui.Slider(c, &e.blockSize, 6, 36).Width(100).Label(tip).Tooltip(tip)
							ui.Textf(c, "%d px", int(math.Round(e.blockSize))).
								FontSize(12).FontWeight(600).TextColor(tk.textPrimary).SingleLine().Shrink(0).
								ContentTransition(glasswork.ContentNumeric)
						}
						appleGlassDivider(c, tk)
						busy := e.isRedactBusy()
						smartLabel := "智能打码"
						if busy {
							smartLabel = "扫描中…"
						}
						smartBtn := ui.Row(c.Key("studio-smart-redact")).
							Height(26).Padding(0, 10, 0, 8).Gap(5).Radius(13).
							AlignItems(ui.Center).Shrink(0).
							Role(ui.RoleButton).Label("智能打码").
							Cursor(ui.CursorPointer).Tooltip("离线识别并打码敏感信息 (Shift+X)")
						sFg := tk.inks.Accent
						if busy {
							smartBtn.Background(tk.inks.AccentFill).Border(1, tk.inks.AccentBorder).Opacity(0.75)
						} else if smartBtn.Hovered() {
							smartBtn.Background(tk.inks.AccentFill).Border(1, tk.inks.AccentBorder)
						} else {
							smartBtn.Background(tk.hoverFill).Border(1, tk.hairline)
							sFg = tk.textPrimary
						}
						smartBtn.Transition(ui.ElementTransition{Colors: true, Animation: &glasswork.AnimColorFade})
						glasswork.ApplyLiquidButton(smartBtn, glasswork.CompactButtonOpts())
						smartBtn.Children(func() {
							ui.Icon(c, icons.SmartRedact.For(busy || smartBtn.Hovered() || smartBtn.Pressed())).
								Size(14.5, 14.5).TextColor(sFg).Shrink(0).ContentTransition(ui.ContentTransition{})
							ui.Text(c, smartLabel).FontSize(11.5).FontWeight(600).TextColor(sFg).SingleLine().Shrink(0)
						})
						if smartBtn.Clicked() && !busy {
							e.triggerSmartRedact()
						}

					case canvas.ToolMeasure:
						e.renderAppleColorDots(c, tk)
						appleGlassDivider(c, tk)
						ui.Slider(c, &e.measureSens, 10, 75).Width(110).Label("边缘灵敏度").Tooltip("边缘灵敏度").Shrink(0)
						ui.Textf(c, "%d", int(math.Round(e.measureSens))).
							FontSize(11.5).FontWeight(600).TextColor(tk.textPrimary).SingleLine().Shrink(0).
							ContentTransition(glasswork.ContentNumeric)

					case canvas.ToolCrop:
						w := int(math.Abs(float64(e.cropX1 - e.cropX0)))
						h := int(math.Abs(float64(e.cropY1 - e.cropY0)))
						ui.Textf(c, "%d × %d", w, h).
							FontSize(12).FontWeight(600).TextColor(tk.textPrimary).SingleLine().Shrink(0).
							ContentTransition(glasswork.ContentNumeric)
						if appleSubChip(c, tk, "裁剪", true) {
							e.applyCrop()
						}
						if appleSubChip(c, tk, "取消", false) {
							e.hasCrop = false
						}

					default:
						e.renderAppleColorDots(c, tk)
						if e.tool == canvas.ToolText {
							appleGlassDivider(c, tk)
							inp := ui.TextInput(c.Key("studio-text-draft"), &e.textDraft).Placeholder("输入文字…").Width(160)
							if inp.Changed() && e.selectedIdx >= 0 && e.selectedIdx < len(e.annotations) && e.annotations[e.selectedIdx].Kind == canvas.ToolText {
								e.annotations[e.selectedIdx].Text = e.textDraft
							}
							for _, sz := range []float32{16, 22, 30} {
								if appleSubChipScoped(c, tk, fmt.Sprintf("%d", int(sz)), e.fontSize == sz, "studio-font") {
									e.fontSize = sz
								}
							}
							if appleSubChip(c, tk, "底色", e.filled) {
								e.filled = !e.filled
							}
						} else if e.tool == canvas.ToolCounter {
							appleGlassDivider(c, tk)
							ui.Textf(c, "#%d", e.nextCounter).
								FontSize(12).FontWeight(700).TextColor(tk.textPrimary).SingleLine().Shrink(0).
								ContentTransition(glasswork.ContentNumeric)
							if appleSubChip(c, tk, "重置", false) {
								e.nextCounter = 1
							}
						} else {
							appleGlassDivider(c, tk)
							for _, s := range []struct {
								dot float32
								sw  float32
								tip string
							}{{5, 2, "细"}, {8, 4, "中"}, {11, 7, "粗"}} {
								active := e.strokeWidth == s.sw
								btn := ui.Box(c).Size(24, 24).Radius(12).Center().
									Role(ui.RoleButton).Label(s.tip).
									Cursor(ui.CursorPointer).Tooltip(s.tip)
								if !active && btn.Hovered() {
									btn.Background(tk.hoverFill)
								}
								btn.Transition(ui.ElementTransition{Colors: true, Animation: &glasswork.AnimColorFade})
								glasswork.ApplyLiquidButton(btn, glasswork.CompactButtonOpts())
								btn.Children(func() {
									if active {
										glasswork.SlidingAccentLens(c, "studio-stroke", 12, tk.inks.AccentFill, tk.inks.AccentBorder)
									}
									dotCol := tk.textSecondary
									if active {
										dotCol = tk.inks.Accent
									}
									ui.Box(c).Size(s.dot, s.dot).Radius(s.dot / 2).Background(dotCol)
								})
								if btn.Clicked() {
									e.strokeWidth = s.sw
								}
							}
							if e.tool == canvas.ToolRect || e.tool == canvas.ToolEllipse {
								appleGlassDivider(c, tk)
								if appleSubChip(c, tk, "填充", e.filled) {
									e.filled = !e.filled
								}
								if e.tool == canvas.ToolRect {
									if appleSubChip(c, tk, "圆角", e.rounded) {
										e.rounded = !e.rounded
									}
								}
							}
						}
					}
				})
			})
	}

	// Primary Floating Glasswork Liquid Glass Bottom Tool Dock
	ui.Row(c).
		Absolute().Bottom(16).Left(0).Right(0).
		Justify(ui.Center).PassThrough().
		Children(func() {
			dock := ui.Row(c.Key("studio-bottom-dock")).
				Height(46).Padding(5, 8, 5, 8).Gap(3).
				Radius(23).
				Material(tk.glass).
				AlignItems(ui.Center).
				Transition(glasswork.PanelTransition(0, 14))
			dock.Clicked()
			dock.Children(func() {
				tools := []struct {
					tool canvas.Tool
					ic   icons.IconPair
				}{
					{canvas.ToolSelect, icons.Select},
					{canvas.ToolRect, icons.Rect},
					{canvas.ToolEllipse, icons.Ellipse},
					{canvas.ToolArrow, icons.Arrow},
					{canvas.ToolLine, icons.Line},
					{canvas.ToolPen, icons.Pen},
					{canvas.ToolHighlighter, icons.Highlighter},
					{canvas.ToolText, icons.Text},
					{canvas.ToolCounter, icons.Counter},
					{canvas.ToolPixelate, icons.Pixelate},
					{canvas.ToolSpotlight, icons.Spotlight},
					{canvas.ToolMeasure, icons.Measure},
					{canvas.ToolCrop, icons.Crop},
				}
				for idx, item := range tools {
					if idx == 1 || idx == 9 || idx == 11 {
						appleGlassDivider(c, tk)
					}
					active := e.tool == item.tool
					tip := fmt.Sprintf("%s (%s)", item.tool.Name(), item.tool.Shortcut())
					if appleDockToolBtn(c, tk, item.ic, item.tool.Name(), tip, active) {
						e.tool = item.tool
						if item.tool != canvas.ToolSelect {
							e.selectedIdx = -1
						}
					}
				}
			})
		})
}

func (e *EditorState) renderAppleColorDots(c *ui.Context, tk appleTokens) {
	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		for i, sw := range canvas.DefaultPalette {
			active := e.colorIdx == i
			ring := ui.Box(c).Size(22, 22).Radius(11).Center().
				Role(ui.RoleButton).Label(sw.Name).
				Cursor(ui.CursorPointer).Tooltip(sw.Name)
			glasswork.ApplyLiquidButton(ring, glasswork.CompactButtonOpts())
			ring.Children(func() {
				if active {
					glasswork.SlidingRingLens(c, "studio-color", 11, tk.inks.Accent)
				}
				ui.Box(c).Size(14, 14).Radius(7).Background(sw.Color).Border(1, tk.hairline)
			})
			if ring.Clicked() {
				e.colorIdx = i
				if e.selectedIdx >= 0 && e.selectedIdx < len(e.annotations) {
					e.annotations[e.selectedIdx].Color = sw.Color
				}
			}
		}
	})
}

func (e *EditorState) renderFloatingCornerPills(c *ui.Context, tk appleTokens) {
	leftPill := ui.Row(c).
		Absolute().Bottom(18).Left(16).Height(34).
		Padding(0, 12, 0, 12).Gap(10).
		Radius(17).
		Material(tk.glass).
		AlignItems(ui.Center)
	leftPill.Clicked()
	leftPill.Children(func() {
		bw, bh := e.baseImg.Bounds().Dx(), e.baseImg.Bounds().Dy()
		ui.Textf(c, "%d × %d", bw, bh).FontSize(11.5).FontWeight(600).TextColor(tk.textSecondary)

		if e.hoverValid {
			hex := fmt.Sprintf("#%02X%02X%02X", e.hoverR, e.hoverG, e.hoverB)
			chip := ui.Row(c).Gap(5).Padding(2, 6, 2, 4).Radius(10).AlignItems(ui.Center).
				Role(ui.RoleButton).Label(hex).
				Cursor(ui.CursorPointer).Tooltip("点击复制 " + hex)
			if chip.Hovered() {
				chip.Background(tk.hoverFill)
			}
			chip.Children(func() {
				ui.Box(c).Size(10, 10).Radius(5).Background(ui.RGB(e.hoverR, e.hoverG, e.hoverB)).Border(1, tk.hairline)
				ui.Text(c, hex).FontSize(11).FontWeight(600).TextColor(tk.textPrimary)
			})
			if chip.Clicked() {
				mygo.Clipboard.WriteText(hex)
				e.SetToast("已复制色号 " + hex)
			}
		}
	})

	rightPill := ui.Row(c).
		Absolute().Bottom(18).Right(16).Height(34).
		Padding(0, 8, 0, 8).Gap(2).
		Radius(17).
		Material(tk.glass).
		AlignItems(ui.Center)
	rightPill.Clicked()
	rightPill.Children(func() {
		if appleMiniBtn(c, tk, icons.ZoomOut, "缩小") {
			e.zoomAround(e.lastViewW/2, e.lastViewH/2, 1.0/1.25)
		}
		zBtn := ui.Box(c).Padding(2, 6, 2, 6).Radius(8).
			Role(ui.RoleButton).Label("100%").
			Cursor(ui.CursorPointer).Tooltip("100% 原始尺寸 (Ctrl+1)")
		if zBtn.Hovered() {
			zBtn.Background(tk.hoverFill)
		}
		zBtn.Transition(ui.ElementTransition{Colors: true, Animation: &glasswork.AnimColorFade})
		glasswork.ApplyLiquidButton(zBtn, glasswork.CompactButtonOpts())
		zBtn.Children(func() {
			ui.Textf(c, "%d%%", int(math.Round(float64(e.zoom*100)))).
				FontSize(11).FontWeight(600).TextColor(tk.textPrimary).
				ContentTransition(glasswork.ContentNumeric)
		})
		if zBtn.Clicked() {
			e.zoom = 1.0
			dw, dh, _, _ := e.docDisplaySize()
			e.panX = (e.lastViewW - float32(dw)) * 0.5
			e.panY = (e.lastViewH - float32(dh)) * 0.5
		}
		if appleMiniBtn(c, tk, icons.ZoomIn, "放大") {
			e.zoomAround(e.lastViewW/2, e.lastViewH/2, 1.25)
		}
		if appleMiniBtn(c, tk, icons.FitScreen, "适应窗口 (Ctrl+0)") {
			e.fitPending = true
		}
		appleGlassDivider(c, tk)
		e.renderThemeAndHelpBtns(c, tk)
	})
}

func (e *EditorState) renderHomeCornerControls(c *ui.Context, tk appleTokens) {
	pill := ui.Row(c).
		Absolute().Bottom(18).Right(18).Height(34).
		Padding(0, 8, 0, 8).Gap(4).
		Radius(17).
		Material(tk.glass).
		AlignItems(ui.Center)
	pill.Clicked()
	pill.Children(func() {
		e.renderThemeAndHelpBtns(c, tk)
	})
}

func (e *EditorState) renderThemeAndHelpBtns(c *ui.Context, tk appleTokens) {
	themeIcon := icons.Moon.Active
	themeTip := "切换深色石墨外观"
	if e.app.IsDarkMode() {
		themeIcon = icons.Sun.Active
		themeTip = "切换浅色瓷白外观"
	}
	themeBtn := ui.Box(c.Key("theme-toggle-btn")).Size(24, 24).Center().Radius(12).
		Role(ui.RoleButton).Label(themeTip).
		Cursor(ui.CursorPointer).Tooltip(themeTip)
	col := tk.textSecondary
	if themeBtn.Hovered() {
		themeBtn.Background(tk.hoverFill)
		col = tk.textPrimary
	}
	themeBtn.Transition(ui.ElementTransition{Colors: true, Animation: &glasswork.AnimColorFade})
	glasswork.ApplyLiquidButton(themeBtn, glasswork.CompactButtonOpts())
	themeBtn.Children(func() {
		ui.Icon(c, themeIcon).Size(15.5, 15.5).TextColor(col).ContentTransition(ui.ContentTransition{})
	})
	if themeBtn.Clicked() {
		e.app.ToggleDarkMode()
	}
	availUpdate := e.app.AvailableUpdate()
	settingsTip := "设置 (Ctrl+,)"
	if availUpdate != "" {
		settingsTip = fmt.Sprintf("设置 · 发现新版本 v%s (Ctrl+,)", availUpdate)
	}
	settingsBtn := ui.Box(c.Key("settings-toggle-btn")).Size(24, 24).Center().Radius(12).
		Role(ui.RoleButton).Label("设置").
		Cursor(ui.CursorPointer).Tooltip(settingsTip)
	sCol := tk.textSecondary
	if e.settingsOpen {
		settingsBtn.Background(tk.inks.AccentFill).Border(1, tk.inks.AccentBorder)
		sCol = tk.inks.Accent
	} else if settingsBtn.Hovered() {
		settingsBtn.Background(tk.hoverFill)
		sCol = tk.textPrimary
	}
	settingsBtn.Transition(ui.ElementTransition{Colors: true, Animation: &glasswork.AnimColorFade})
	glasswork.ApplyLiquidButton(settingsBtn, glasswork.CompactButtonOpts())
	sGlyph := icons.Settings.For(e.settingsOpen || settingsBtn.Hovered() || settingsBtn.Pressed())
	settingsBtn.Children(func() {
		ui.Icon(c, sGlyph).Size(15, 15).TextColor(sCol).ContentTransition(ui.ContentTransition{})
		if availUpdate != "" && !e.settingsOpen {
			ui.Box(c).Absolute().Top(2).Right(2).Size(6, 6).Radius(3).
				Background(tk.accent).PassThrough()
		}
	})
	if settingsBtn.Clicked() {
		if e.settingsOpen {
			e.CloseSettings()
		} else {
			e.OpenSettings()
		}
	}
	if appleMiniBtn(c, tk, icons.Help, "快捷键说明 (F1)") {
		e.helpOpen = !e.helpOpen
	}
}

func (e *EditorState) renderFloatingHistoryDrawer(c *ui.Context, tk appleTokens) {
	e.app.mu.Lock()
	items := append([]HistoryEntry(nil), e.app.history...)
	e.app.mu.Unlock()

	drawer := ui.Column(c.Key("history-drawer")).
		Absolute().Top(62).Bottom(64).Left(14).Width(244).
		Padding(10).Gap(8).Radius(24).
		Material(glasswork.Card(tk.glass.Kind)).
		Transition(glasswork.PanelTransition(-24, 0))
	drawer.Clicked()

	drawer.Children(func() {
		ui.Row(c).Padding(2, 6, 4, 6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "历史").FontSize(13).FontWeight(700).TextColor(tk.textPrimary).Grow(1)
			if appleMiniBtn(c, tk, icons.Close, "关闭") {
				e.historyOpen = false
			}
		})
		if len(items) == 0 {
			ui.Column(c).Grow(1).Center().Children(func() {
				ui.Icon(c, icons.History.Idle).Size(28, 28).TextColor(tk.textTertiary)
			})
			return
		}
		ui.Scroll(c.Key("apple-history-scroll")).Grow(1).Gap(8).Children(func() {
			for idx, item := range items {
				it := item
				tip := fmt.Sprintf("%s · %d×%d · %s", it.Title, it.Width, it.Height, it.CapturedAt.Format("15:04:05"))
				card := ui.Column(c.Key(it.ID)).
					Padding(6).Gap(4).Radius(12).
					Role(ui.RoleButton).Label(tip).Tooltip(tip).
					Background(tk.cardBg.Alpha(0.75)).Border(1, tk.hairline).
					Cursor(ui.CursorPointer).
					Transition(glasswork.StaggeredEnterTransition(idx))
				hov := card.Hovered()
				if hov {
					card.Border(1, tk.accent.Alpha(0.7))
				}
				glasswork.ApplyLiquidButton(card, glasswork.CardHoverOpts())
				card.Children(func() {
					if it.Thumb != nil {
						ui.Box(c).Height(96).Radius(8).Background(tk.canvasBg).Center().Children(func() {
							ui.Image(c, it.Thumb).Fill().Fit(ui.Contain).Radius(8)
						})
					}
					ui.Row(c).Padding(0, 3, 0, 3).AlignItems(ui.Center).Gap(4).Children(func() {
						ui.Textf(c, "%d×%d · %s", it.Width, it.Height, it.CapturedAt.Format("15:04")).
							FontSize(10.5).TextColor(tk.textSecondary).Grow(1)
						if hov {
							if appleMiniBtn(c, tk, icons.Pin, "贴图") {
								if img := it.DecodeImage(); img != nil {
									e.app.OpenPinWithPNG(img, it.PNG, 0, 0, 0)
								}
							}
							if appleMiniBtn(c, tk, icons.Trash, "移除") {
								e.app.DeleteHistoryEntry(it.ID)
							}
						}
					})
				})
				if card.Clicked() {
					if img := it.DecodeImage(); img != nil {
						e.loadOwnedImage(img, it.PNG, it.Title)
					}
				}
			}
		})
	})
}

func (e *EditorState) renderCanvasViewport(c *ui.Context, tk appleTokens) {
	vp := ui.Box(c).Absolute().Fill().Background(tk.canvasBg)
	altLineMode := c.Modifiers()&ui.Alt != 0
	hoverRemoveIdx := -1
	var hoverWord winapi.OCRWord
	hasHoverWord := false
	hoverWordIsLine := false

	cur := ui.CursorCrosshair
	switch {
	case e.panning:
		cur = ui.CursorGrabbing
	case e.tool == canvas.ToolSelect:
		cur = ui.CursorDefault
	case e.tool == canvas.ToolText:
		cur = ui.CursorText
	case (e.tool == canvas.ToolPixelate || e.tool == canvas.ToolBlur) && !e.drawing && e.hoverValid:
		if hit := hitRedactionAnnotation(e.annotations, float32(e.hoverImgX), float32(e.hoverImgY)); hit >= 0 {
			hoverRemoveIdx = hit
			cur = ui.CursorPointer
		} else if altLineMode && len(e.ocrLines) > 0 {
			if lineW, ok := redact.FindLineAt(e.ocrLines, float32(e.hoverImgX), float32(e.hoverImgY)); ok {
				hoverWord = lineW
				hasHoverWord = true
				hoverWordIsLine = true
				cur = ui.CursorPointer
			}
		} else if len(e.ocrWords) > 0 {
			if word, ok := redact.FindWordAt(e.ocrWords, float32(e.hoverImgX), float32(e.hoverImgY)); ok {
				hoverWord = word
				hasHoverWord = true
				cur = ui.CursorPointer
			}
		}
	}
	vp.Cursor(cur)

	_, _, imgOffX, imgOffY := e.docDisplaySize()
	viewToImg := func(vx, vy float32) (float32, float32) {
		return (vx-e.panX)/e.zoom - float32(imgOffX), (vy-e.panY)/e.zoom - float32(imgOffY)
	}
	imgOriginOnView := func() (float32, float32) {
		return e.panX + float32(imgOffX)*e.zoom, e.panY + float32(imgOffY)*e.zoom
	}

	vp.HandleInput(func(ev ui.InputEvent) bool {
		bw := float32(e.baseImg.Bounds().Dx())
		bh := float32(e.baseImg.Bounds().Dy())

		switch ev.Kind {
		case ui.InputZoom:
			if ev.Scale > 0 {
				e.zoomAround(ev.X, ev.Y, ev.Scale)
			}
			return true

		case ui.InputScroll:
			dw, dh, _, _ := e.docDisplaySize()
			tallScroll := ev.Mods&ui.Ctrl == 0 && !ev.Precise && dh > dw*2 && float32(dh)*e.zoom > e.lastViewH-160
			if tallScroll {
				e.panY -= ev.DY
				minPanY := e.lastViewH - 140 - float32(dh)*e.zoom
				maxPanY := float32(92)
				e.panY = clamp32(e.panY, minPanY, maxPanY)
			} else if ev.Mods&ui.Ctrl != 0 || !ev.Precise {
				if ev.DY < 0 {
					e.zoomAround(ev.X, ev.Y, 1.15)
				} else if ev.DY > 0 {
					e.zoomAround(ev.X, ev.Y, 1.0/1.15)
				}
			} else {
				e.panX -= ev.DX
				e.panY -= ev.DY
			}
			return true

		case ui.InputPointerDown:
			if ev.Button == 2 || (ev.Button == 0 && c.KeyDown(ui.KeySpace)) {
				e.panning = true
				e.panLastX, e.panLastY = ev.X, ev.Y
				return true
			}
			if ev.Button != 0 {
				return false
			}
			ix, iy := viewToImg(ev.X, ev.Y)

			switch e.tool {
			case canvas.ToolSelect:
				hit := canvas.HitTestAnnotation(e.annotations, ix, iy, 8/e.zoom)
				e.selectedIdx = hit
				if hit >= 0 {
					e.pushUndo()
					e.movingAnn = true
					e.moveLastImgX, e.moveLastImgY = ix, iy
					if e.annotations[hit].Kind == canvas.ToolText {
						e.textDraft = e.annotations[hit].Text
					}
				}
				return true

			case canvas.ToolCounter:
				e.pushUndo()
				e.nextAnnID++
				e.annotations = append(e.annotations, canvas.Annotation{
					ID:         e.nextAnnID,
					Kind:       canvas.ToolCounter,
					Start:      canvas.PointF{X: clamp32(ix, 0, bw), Y: clamp32(iy, 0, bh)},
					Color:      e.activeColor(),
					CounterNum: e.nextCounter,
				})
				e.nextCounter++
				return true

			case canvas.ToolText:
				e.pushUndo()
				e.nextAnnID++
				txt := e.textDraft
				if txt == "" {
					txt = "标注"
				}
				e.annotations = append(e.annotations, canvas.Annotation{
					ID:       e.nextAnnID,
					Kind:     canvas.ToolText,
					Start:    canvas.PointF{X: clamp32(ix, 0, bw), Y: clamp32(iy, 0, bh)},
					Color:    e.activeColor(),
					FontSize: e.fontSize,
					Filled:   e.filled,
					Text:     txt,
				})
				e.selectedIdx = len(e.annotations) - 1
				return true

			case canvas.ToolMeasure:
				px, py := int(math.Round(float64(ix))), int(math.Round(float64(iy)))
				m := winapi.MeasureEdges(e.baseImg, px, py, e.measureSens)
				if m.Valid {
					e.pushUndo()
					e.nextAnnID++
					e.annotations = append(e.annotations, canvas.Annotation{
						ID:         e.nextAnnID,
						Kind:       canvas.ToolMeasure,
						Start:      canvas.PointF{X: float32(m.OriginX), Y: float32(m.OriginY)},
						Color:      e.activeColor(),
						MeasLeft:   m.Left,
						MeasRight:  m.Right,
						MeasTop:    m.Top,
						MeasBottom: m.Bottom,
					})
				}
				return true

			case canvas.ToolCrop:
				e.cropDrag = true
				e.hasCrop = true
				cx, cy := clamp32(ix, 0, bw), clamp32(iy, 0, bh)
				e.cropX0, e.cropY0 = cx, cy
				e.cropX1, e.cropY1 = cx, cy
				return true

			default:
				e.drawing = true
				e.nextAnnID++
				pt := canvas.PointF{X: clamp32(ix, 0, bw), Y: clamp32(iy, 0, bh)}
				e.draftAnn = canvas.Annotation{
					ID:          e.nextAnnID,
					Kind:        e.tool,
					Start:       pt,
					End:         pt,
					Color:       e.activeColor(),
					StrokeWidth: e.strokeWidth,
					Filled:      e.filled,
					Rounded:     e.rounded,
					BlockSize:   int(math.Round(e.blockSize)),
					MosaicStyle: e.mosaicStyle,
				}
				if e.tool == canvas.ToolPen || e.tool == canvas.ToolHighlighter {
					e.draftAnn.Points = []canvas.PointF{pt}
				}
				return true
			}

		case ui.InputPointerMove:
			if e.panning {
				e.panX += ev.X - e.panLastX
				e.panY += ev.Y - e.panLastY
				e.panLastX, e.panLastY = ev.X, ev.Y
				return true
			}
			ix, iy := viewToImg(ev.X, ev.Y)

			px, py := int(math.Floor(float64(ix))), int(math.Floor(float64(iy)))
			if r, g, b, ok := winapi.ColorAt(e.baseImg, px, py); ok {
				e.hoverValid = true
				e.hoverImgX, e.hoverImgY = px, py
				e.hoverR, e.hoverG, e.hoverB = r, g, b
				if e.tool == canvas.ToolMeasure && (px != e.lastMeasX || py != e.lastMeasY) {
					e.liveMeasure = winapi.MeasureEdges(e.baseImg, px, py, e.measureSens)
					e.lastMeasX, e.lastMeasY = px, py
				}
			} else {
				e.hoverValid = false
			}

			if e.movingAnn && e.selectedIdx >= 0 && e.selectedIdx < len(e.annotations) {
				dx, dy := ix-e.moveLastImgX, iy-e.moveLastImgY
				e.annotations[e.selectedIdx].Translate(dx, dy)
				e.moveLastImgX, e.moveLastImgY = ix, iy
				// Defer expensive CPU blur/pixelate re-bake until pointer-up so dragging stays 60-144fps!
				return true
			}

			if e.cropDrag {
				e.cropX1 = clamp32(ix, 0, bw)
				e.cropY1 = clamp32(iy, 0, bh)
				return true
			}

			if e.drawing {
				ex, ey := clamp32(ix, 0, bw), clamp32(iy, 0, bh)
				if ev.Mods&ui.Shift != 0 {
					dx, dy := ex-e.draftAnn.Start.X, ey-e.draftAnn.Start.Y
					switch e.draftAnn.Kind {
					case canvas.ToolRect, canvas.ToolEllipse:
						side := float32(math.Max(math.Abs(float64(dx)), math.Abs(float64(dy))))
						if dx < 0 {
							ex = e.draftAnn.Start.X - side
						} else {
							ex = e.draftAnn.Start.X + side
						}
						if dy < 0 {
							ey = e.draftAnn.Start.Y - side
						} else {
							ey = e.draftAnn.Start.Y + side
						}
					case canvas.ToolLine, canvas.ToolArrow:
						ang := math.Atan2(float64(dy), float64(dx))
						snapped := math.Round(ang/(math.Pi/4)) * (math.Pi / 4)
						dist := math.Hypot(float64(dx), float64(dy))
						ex = e.draftAnn.Start.X + float32(math.Cos(snapped)*dist)
						ey = e.draftAnn.Start.Y + float32(math.Sin(snapped)*dist)
					}
				}
				e.draftAnn.End = canvas.PointF{X: ex, Y: ey}
				if e.draftAnn.Kind == canvas.ToolPen || e.draftAnn.Kind == canvas.ToolHighlighter {
					pts := e.draftAnn.Points
					last := pts[len(pts)-1]
					if math.Hypot(float64(ex-last.X), float64(ey-last.Y)) >= 2.0 {
						e.draftAnn.Points = append(pts, canvas.PointF{X: ex, Y: ey})
					}
				}
				return true
			}
			return false

		case ui.InputPointerUp:
			if e.panning {
				e.panning = false
				return true
			}
			if e.movingAnn {
				e.movingAnn = false
				if e.selectedIdx >= 0 && e.selectedIdx < len(e.annotations) {
					if k := e.annotations[e.selectedIdx].Kind; k == canvas.ToolPixelate || k == canvas.ToolBlur || k == canvas.ToolSpotlight {
						e.rasterDirty = true
					}
				}
				return true
			}
			if e.cropDrag {
				e.cropDrag = false
				if math.Abs(float64(e.cropX1-e.cropX0)) < 6 || math.Abs(float64(e.cropY1-e.cropY0)) < 6 {
					e.hasCrop = false
				}
				return true
			}
			if e.drawing {
				e.drawing = false
				if ev.Canceled {
					return true
				}
				minX, minY, maxX, maxY := e.draftAnn.Bounds()
				isRedactTool := e.draftAnn.Kind == canvas.ToolPixelate || e.draftAnn.Kind == canvas.ToolBlur
				clickTol := float32(3)
				if isRedactTool {
					clickTol = 5
				}
				if maxX-minX >= clickTol || maxY-minY >= clickTol || len(e.draftAnn.Points) >= 2 {
					e.pushUndo()
					e.annotations = append(e.annotations, e.draftAnn)
					if isRedactTool || e.draftAnn.Kind == canvas.ToolSpotlight {
						e.rasterDirty = true
					}
				} else if isRedactTool {
					// One-click toggle: clicking an existing mosaic/blur block removes it;
					// otherwise clicking a recognized OCR word tightly redacts that token.
					if hit := hitRedactionAnnotation(e.annotations, e.draftAnn.Start.X, e.draftAnn.Start.Y); hit >= 0 {
						e.pushUndo()
						e.annotations = append(e.annotations[:hit], e.annotations[hit+1:]...)
						if e.selectedIdx == hit {
							e.selectedIdx = -1
						} else if e.selectedIdx > hit {
							e.selectedIdx--
						}
						e.rasterDirty = true
					} else if (ev.Mods&ui.Alt != 0) && len(e.ocrLines) > 0 {
						if lineW, ok := redact.FindLineAt(e.ocrLines, e.draftAnn.Start.X, e.draftAnn.Start.Y); ok {
							e.nextAnnID++
							if ann, valid := makeWordRedactionAnnotation(lineW, e.baseImg.Bounds(), e.draftAnn.Kind, e.mosaicStyle, int(math.Round(e.blockSize)), e.nextAnnID); valid {
								e.pushUndo()
								e.annotations = append(e.annotations, ann)
								e.rasterDirty = true
							}
						}
					} else if len(e.ocrWords) > 0 {
						if word, ok := redact.FindWordAt(e.ocrWords, e.draftAnn.Start.X, e.draftAnn.Start.Y); ok {
							e.nextAnnID++
							if ann, valid := makeWordRedactionAnnotation(word, e.baseImg.Bounds(), e.draftAnn.Kind, e.mosaicStyle, int(math.Round(e.blockSize)), e.nextAnnID); valid {
								e.pushUndo()
								e.annotations = append(e.annotations, ann)
								e.rasterDirty = true
							}
						}
					}
				}
				return true
			}
		}
		return false
	})

	vp.Draw(func(pt *ui.Painter, r ui.Rect) {
		e.lastViewW, e.lastViewH = r.W, r.H
		if e.fitPending {
			e.fitToViewport(r.W, r.H)
		}

		pt.Clip(r, 0, func() {
			drawAppleMeshWallpaper(pt, r, tk.dark)
			drawSubtleDotGrid(pt, r, tk.dotColor)

			dw, dh, _, _ := e.docDisplaySize()
			docRect := ui.Rect{
				X: r.X + e.panX,
				Y: r.Y + e.panY,
				W: float32(dw) * e.zoom,
				H: float32(dh) * e.zoom,
			}

			shAlpha := float32(0.18)
			if tk.dark {
				shAlpha = 0.42
			}
			pt.Shadow(docRect, 12, 0, 14, 38, 0, ui.RGBA(0, 0, 0, shAlpha))

			if e.rasterBmp != nil {
				pt.Clip(docRect, 8, func() {
					pt.Image(e.rasterBmp, docRect, ui.FillBox)
				})
				pt.Stroke(docRect, tk.hairline, 8, 1)
			}

			ox, oy := imgOriginOnView()
			ox += r.X
			oy += r.Y

			for i, a := range e.annotations {
				canvas.DrawAnnotationLive(pt, a, ox, oy, e.zoom, i == e.selectedIdx, false)
			}
			if e.drawing {
				canvas.DrawAnnotationLive(pt, e.draftAnn, ox, oy, e.zoom, false, true)
			}
			if !e.drawing && (e.tool == canvas.ToolPixelate || e.tool == canvas.ToolBlur) && e.hoverValid {
				if hoverRemoveIdx >= 0 && hoverRemoveIdx < len(e.annotations) {
					minX, minY, maxX, maxY := e.annotations[hoverRemoveIdx].Bounds()
					hr := ui.Rect{
						X: ox + minX*e.zoom,
						Y: oy + minY*e.zoom,
						W: (maxX - minX) * e.zoom,
						H: (maxY - minY) * e.zoom,
					}
					drawRedactionRemoveHint(pt, hr)
				} else if hasHoverWord {
					wr := ui.Rect{
						X: ox + float32(hoverWord.Rect.Min.X-3)*e.zoom,
						Y: oy + float32(hoverWord.Rect.Min.Y-2)*e.zoom,
						W: float32(hoverWord.Rect.Dx()+6) * e.zoom,
						H: float32(hoverWord.Rect.Dy()+4) * e.zoom,
					}
					drawWordRedactHint(pt, wr, hoverWordIsLine)
				}
			}

			if e.tool == canvas.ToolMeasure && e.hoverValid && e.liveMeasure.Valid {
				m := e.liveMeasure
				preview := canvas.Annotation{
					Kind:       canvas.ToolMeasure,
					Start:      canvas.PointF{X: float32(m.OriginX), Y: float32(m.OriginY)},
					Color:      e.activeColor(),
					MeasLeft:   m.Left,
					MeasRight:  m.Right,
					MeasTop:    m.Top,
					MeasBottom: m.Bottom,
				}
				canvas.DrawAnnotationLive(pt, preview, ox, oy, e.zoom, false, true)
			}

			if e.tool == canvas.ToolCrop && e.hasCrop {
				bw := float32(e.baseImg.Bounds().Dx())
				bh := float32(e.baseImg.Bounds().Dy())
				x0, x1 := e.cropX0, e.cropX1
				if x0 > x1 {
					x0, x1 = x1, x0
				}
				y0, y1 := e.cropY0, e.cropY1
				if y0 > y1 {
					y0, y1 = y1, y0
				}
				imgR := ui.Rect{X: ox, Y: oy, W: bw * e.zoom, H: bh * e.zoom}
				cr := ui.Rect{X: ox + x0*e.zoom, Y: oy + y0*e.zoom, W: (x1 - x0) * e.zoom, H: (y1 - y0) * e.zoom}
				veil := ui.RGBA(0, 0, 0, 0.48)
				pt.Fill(ui.Rect{X: imgR.X, Y: imgR.Y, W: imgR.W, H: cr.Y - imgR.Y}, veil, 0)
				pt.Fill(ui.Rect{X: imgR.X, Y: cr.Y + cr.H, W: imgR.W, H: imgR.Y + imgR.H - (cr.Y + cr.H)}, veil, 0)
				pt.Fill(ui.Rect{X: imgR.X, Y: cr.Y, W: cr.X - imgR.X, H: cr.H}, veil, 0)
				pt.Fill(ui.Rect{X: cr.X + cr.W, Y: cr.Y, W: imgR.X + imgR.W - (cr.X + cr.W), H: cr.H}, veil, 0)
				pt.Stroke(cr, tk.accent, 0, 2)
			}
		})
	})
}

func (e *EditorState) renderHelpSheet(c *ui.Context, tk appleTokens) {
	ui.Overlay(c, func() {
		scrimAnim := glasswork.AnimColorFade
		scrim := ui.Box(c.Key("help-scrim")).Absolute().Top(0).Left(0).Right(0).Bottom(0).Background(ui.RGBA(0, 0, 0, 0.36)).Center().
			Transition(ui.ElementTransition{
				Animation: &scrimAnim,
				Enter:     &ui.Motion{Opacity: 0},
				Exit:      &ui.Motion{Opacity: 0},
			})
		if scrim.Clicked() {
			e.helpOpen = false
		}
		scrim.Children(func() {
			sheet := ui.Column(c.Key("help-sheet")).
				Width(540).Padding(24).Gap(16).Radius(28).
				Material(glasswork.Card(tk.glass.Kind)).
				Transition(glasswork.PanelTransition(0, 12))
			sheet.Clicked()
			sheet.Children(func() {
				ui.Row(c).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "Koma · 快捷键").FontSize(17).FontWeight(700).TextColor(tk.textPrimary).Grow(1)
					if appleMiniBtn(c, tk, icons.Close, "关闭") {
						e.helpOpen = false
					}
				})
				ui.Row(c).Gap(24).AlignItems(ui.Start).Children(func() {
					ui.Column(c).Grow(1).Gap(7).Children(func() {
						ui.Text(c, "全局与截图遮罩").FontSize(11).FontWeight(700).TextColor(tk.inks.Accent)
						appleHelpRow(c, tk, FormatHotkeyDisplay(e.app.Hotkey(HotkeyArea)), "区域截图（原位标注）")
						appleHelpRow(c, tk, FormatHotkeyDisplay(e.app.Hotkey(HotkeyLong)), "滚动长截图")
						appleHelpRow(c, tk, FormatHotkeyDisplay(e.app.Hotkey(HotkeyFull)), "全屏截图")
						appleHelpRow(c, tk, FormatHotkeyDisplay(e.app.Hotkey(HotkeyDelay)), "延时 3 秒截图")
						appleHelpRow(c, tk, FormatHotkeyDisplay(e.app.Hotkey(HotkeyPin)), "贴图剪贴板")
						appleHelpRow(c, tk, "双击选区 / Enter", "完成并复制")
						appleHelpRow(c, tk, "E / L / 方向键", "工作台 / 长截图 / 1px 微调")
						appleHelpRow(c, tk, "Space / Shift+C / M", "吸附 / 取色格式 / 测距")
					})
					ui.Column(c).Grow(1).Gap(7).Children(func() {
						ui.Text(c, "标注与贴图").FontSize(11).FontWeight(700).TextColor(tk.inks.Accent)
						appleHelpRow(c, tk, "R / O / A / P / H", "矩形 / 椭圆 / 箭头 / 画笔 / 荧光")
						appleHelpRow(c, tk, "X / B / S", "马赛克 / 模糊样式 / 聚光灯")
						appleHelpRow(c, tk, "Shift+X / Alt+单击", "智能打码 / 整行打码")
						appleHelpRow(c, tk, "T / N / M / C", "文字 / 序号 / 测距 / 裁剪")
						appleHelpRow(c, tk, "Ctrl+Z / Ctrl+Y", "撤销 / 重做")
						appleHelpRow(c, tk, "Ctrl+C / S / P", "复制 / 保存 / 贴图")
						appleHelpRow(c, tk, "Ctrl+,", "打开设置")
						appleHelpRow(c, tk, "贴图滚轮 / Alt+滚轮", "缩放 / 调节透明度")
					})
				})
			})
		})
	})
}

func (e *EditorState) renderSettingsSheet(c *ui.Context, tk appleTokens) {
	e.syncSettingsSelectChoices()
	ui.Overlay(c, func() {
		scrimAnim := glasswork.AnimColorFade
		scrim := ui.Box(c.Key("settings-scrim")).Absolute().Top(0).Left(0).Right(0).Bottom(0).Background(ui.RGBA(0, 0, 0, 0.36)).Center().
			Transition(ui.ElementTransition{
				Animation: &scrimAnim,
				Enter:     &ui.Motion{Opacity: 0},
				Exit:      &ui.Motion{Opacity: 0},
			})
		if scrim.Clicked() {
			e.CloseSettings()
		}
		scrim.Children(func() {
			sheet := ui.Column(c.Key("settings-sheet")).
				Width(780).Padding(22).Gap(16).Radius(28).
				Material(glasswork.Card(tk.glass.Kind)).
				Transition(glasswork.PanelTransition(0, 12))
			sheet.Clicked()
			sheet.Children(func() {
				// Header
				ui.Row(c).AlignItems(ui.Center).Children(func() {
					ui.Row(c).AlignItems(ui.Center).Gap(8).Grow(1).Children(func() {
						ui.Icon(c, icons.Settings.Active).Size(18, 18).TextColor(tk.inks.Accent)
						ui.Text(c, "设置").FontSize(17).FontWeight(700).TextColor(tk.textPrimary)
					})
					if appleMiniBtn(c, tk, icons.Close, "关闭") {
						e.CloseSettings()
					}
				})

				// Balanced 2-column Liquid Glass layout so all 4 sections stay visible without scrolling
				ui.Row(c.Key("settings-columns")).Gap(20).AlignItems(ui.Start).Children(func() {
					// Left column: General + Save Preferences
					ui.Column(c).Width(348).Gap(14).Children(func() {
						// Section 1: General (Launch at Startup + Theme)
						ui.Column(c).Gap(8).Children(func() {
							ui.Text(c, "常规").FontSize(11).FontWeight(700).TextColor(tk.inks.Accent)

							e.renderLiquidSwitchRow(c, tk, "settings-autostart", "开机自启", &e.openAtLoginToggle, func(on bool) {
								if err := e.app.SetOpenAtLogin(on); err != nil {
									e.openAtLoginToggle = e.app.OpenAtLogin()
									e.settingsErr = err.Error()
								} else {
									e.settingsErr = ""
									if on {
										e.SetToast("已开启开机自启（后台静默驻留托盘）")
									} else {
										e.SetToast("已关闭开机自启")
									}
								}
							})

							ui.Row(c.Key("settings-theme-row")).
								Height(40).Padding(0, 10, 0, 12).Radius(13).
								Background(tk.hoverFill.Alpha(0.55)).AlignItems(ui.Center).Children(func() {
								ui.Text(c, "外观").FontSize(13).FontWeight(500).TextColor(tk.textPrimary).Grow(1)
								glasswork.Select(c, glasswork.SelectProps[ThemeMode]{
									Key:      "settings-theme-select",
									Label:    "选择外观",
									Tooltip:  "跟随系统或切换浅色/深色外观",
									Selected: &e.themeChoice,
									Open:     &e.themeSelectOpen,
									Width:    132,
									Height:   28,
									Kind:     tk.glass.Kind,
									Accent:   tk.accent,
									AlignEnd: true,
									Options: []glasswork.SelectOption[ThemeMode]{
										{Value: ThemeModeSystem, Label: "跟随系统"},
										{Value: ThemeModeLight, Label: "浅色"},
										{Value: ThemeModeDark, Label: "深色"},
									},
									OnSelect: func(m ThemeMode) {
										e.themeChoiceSynced = m
										e.app.SetThemeMode(m)
									},
								})
							})
						})

						// Section 2: Save Preferences
						ui.Column(c).Gap(8).Children(func() {
							ui.Text(c, "保存偏好").FontSize(11).FontWeight(700).TextColor(tk.inks.Accent)

							e.renderLiquidSwitchRow(c, tk, "settings-quicksave", "静默快速保存", &e.quickSaveToggle, func(on bool) {
								e.app.SetQuickSave(on)
								if on {
									e.SetToast("已开启静默快速保存（跳过保存弹窗）")
								} else {
									e.SetToast("已关闭静默快速保存")
								}
							})

							ui.Row(c.Key("settings-format-row")).
								Height(40).Padding(0, 10, 0, 12).Radius(13).
								Background(tk.hoverFill.Alpha(0.55)).AlignItems(ui.Center).Children(func() {
								ui.Text(c, "默认格式").FontSize(13).FontWeight(500).TextColor(tk.textPrimary).Grow(1)
								glasswork.Select(c, glasswork.SelectProps[string]{
									Key:      "settings-format-select",
									Label:    "选择默认格式",
									Tooltip:  "选择截图默认导出格式",
									Selected: &e.formatChoice,
									Open:     &e.formatSelectOpen,
									Width:    132,
									Height:   28,
									Kind:     tk.glass.Kind,
									Accent:   tk.accent,
									AlignEnd: true,
									Options: []glasswork.SelectOption[string]{
										{Value: SaveFormatPNG, Label: "PNG"},
										{Value: SaveFormatJPEG, Label: "JPEG"},
									},
									OnSelect: func(v string) {
										e.formatChoiceSynced = v
										e.app.SetSaveFormat(v)
									},
								})
							})

							customDir := e.app.SaveDir()
							dirLabel := "系统图片目录"
							dirTip := e.app.EffectiveSaveDir()
							if customDir != "" {
								dirLabel = customDir
								if r := []rune(dirLabel); len(r) > 16 {
									dirLabel = "…" + string(r[len(r)-15:])
								}
							}
							ui.Row(c.Key("settings-savedir-row")).
								Height(40).Padding(0, 10, 0, 12).Radius(13).Gap(6).
								Background(tk.hoverFill.Alpha(0.55)).AlignItems(ui.Center).Children(func() {
								ui.Text(c, "保存目录").FontSize(13).FontWeight(500).TextColor(tk.textPrimary).SingleLine().Shrink(0)
								ui.Text(c, dirLabel).FontSize(11.5).FontWeight(500).TextColor(tk.textSecondary).SingleLine().Grow(1).Tooltip(dirTip)
								chooseBtn := ui.Row(c.Key("settings-choose-dir")).
									Height(26).Padding(0, 10, 0, 10).Radius(13).AlignItems(ui.Center).Shrink(0).
									Role(ui.RoleButton).Label("更改保存目录").
									Cursor(ui.CursorPointer).Tooltip("选择默认保存目录")
								cFg := tk.textPrimary
								if chooseBtn.Hovered() {
									chooseBtn.Background(tk.hoverFill).Border(1, tk.inks.AccentBorder)
									cFg = tk.inks.Accent
								} else {
									chooseBtn.Background(tk.cardBg.Alpha(0.55)).Border(1, tk.hairline)
								}
								chooseBtn.Transition(ui.ElementTransition{Colors: true, Animation: &glasswork.AnimColorFade})
								glasswork.ApplyLiquidButton(chooseBtn, glasswork.CompactButtonOpts())
								chooseBtn.Children(func() {
									ui.Text(c, "更改").FontSize(11.5).FontWeight(600).TextColor(cFg).SingleLine()
								})
								if chooseBtn.Clicked() {
									e.app.ChooseSaveDir()
								}
								if customDir != "" {
									if appleMiniBtn(c, tk, icons.Close, "恢复默认保存目录") {
										e.app.SetSaveDir("")
										e.SetToast("已恢复系统默认图片目录")
									}
								}
							})
						})

						// Section 3: Software Updates
						ui.Column(c).Gap(8).Children(func() {
							ui.Text(c, "软件更新").FontSize(11).FontWeight(700).TextColor(tk.inks.Accent)

							e.renderLiquidSwitchRow(c, tk, "settings-autocheck", "自动检查更新", &e.autoCheckUpdatesToggle, func(on bool) {
								e.app.SetAutomaticUpdateChecks(on)
								if on {
									e.SetToast("已开启每日自动检查更新")
								} else {
									e.SetToast("已关闭自动检查更新")
								}
							})

							avail := e.app.AvailableUpdate()
							verLabel := "v" + e.app.AppVersion()
							ui.Row(c.Key("settings-update-row")).
								Height(40).Padding(0, 10, 0, 12).Radius(13).Gap(8).
								Background(tk.hoverFill.Alpha(0.55)).AlignItems(ui.Center).Children(func() {
								ui.Row(c).AlignItems(ui.Center).Gap(6).Grow(1).Children(func() {
									ui.Text(c, "版本").FontSize(13).FontWeight(500).TextColor(tk.textPrimary).SingleLine()
									ui.Box(c).Padding(2, 7, 2, 7).Radius(8).
										Background(tk.cardBg.Alpha(0.55)).Border(1, tk.hairline).Children(func() {
										ui.Text(c, verLabel).FontSize(11.5).FontWeight(600).TextColor(tk.textSecondary).SingleLine()
									})
									if avail != "" {
										ui.Box(c).Padding(2, 7, 2, 7).Radius(8).
											Background(tk.inks.AccentFill).Border(1, tk.inks.AccentBorder).Children(func() {
											ui.Textf(c, "新版本 v%s", avail).FontSize(11).FontWeight(600).TextColor(tk.inks.Accent).SingleLine()
										})
									}
								})

								btnLabel := "检查更新"
								btnTip := "从 GitHub Releases 检查 Koma 新版本"
								if avail != "" {
									btnLabel = "立即更新"
									btnTip = fmt.Sprintf("查看并安装 Koma v%s", avail)
								}
								checkBtn := ui.Row(c.Key("settings-check-update")).
									Height(28).Padding(0, 11, 0, 9).Gap(5).Radius(14).
									AlignItems(ui.Center).Shrink(0).
									Role(ui.RoleButton).Label(btnLabel).
									Cursor(ui.CursorPointer).Tooltip(btnTip)
								btnFg := tk.textPrimary
								if avail != "" {
									checkBtn.Material(glasswork.Tinted(tk.accent))
									btnFg = ui.Hex("#FFFFFF")
								} else {
									if checkBtn.Hovered() {
										checkBtn.Background(tk.hoverFill).Border(1, tk.inks.AccentBorder)
										btnFg = tk.inks.Accent
									} else {
										checkBtn.Background(tk.cardBg.Alpha(0.55)).Border(1, tk.hairline)
									}
									checkBtn.Transition(ui.ElementTransition{Colors: true, Animation: &glasswork.AnimColorFade})
								}
								glasswork.ApplyLiquidButton(checkBtn, glasswork.CompactButtonOpts())
								checkBtn.Children(func() {
									ui.Icon(c, icons.Restart.For(avail != "" || checkBtn.Pressed())).Size(14, 14).Shrink(0).TextColor(btnFg).ContentTransition(ui.ContentTransition{})
									ui.Text(c, btnLabel).FontSize(11.5).FontWeight(600).TextColor(btnFg).SingleLine().Shrink(0)
								})
								if checkBtn.Clicked() {
									e.app.CheckForUpdates()
								}
							})
						})
					})

					// Right column: Global Hotkeys + Offline OCR Engine
					ui.Column(c).Grow(1).Gap(14).Children(func() {
						// Section 4: Global Hotkeys
						ui.Column(c).Gap(8).Children(func() {
							ui.Row(c).AlignItems(ui.Center).Children(func() {
								ui.Text(c, "全局快捷键").FontSize(11).FontWeight(700).TextColor(tk.inks.Accent).Grow(1)
								resetBtn := ui.Row(c.Key("settings-reset-hotkeys")).
									Height(24).Padding(0, 10, 0, 10).Radius(12).AlignItems(ui.Center).
									Role(ui.RoleButton).Label("恢复默认").
									Cursor(ui.CursorPointer).Tooltip("恢复全部默认快捷键")
								rCol := tk.textSecondary
								if resetBtn.Hovered() {
									resetBtn.Background(tk.hoverFill)
									rCol = tk.inks.Accent
								}
								resetBtn.Transition(ui.ElementTransition{Colors: true, Animation: &glasswork.AnimColorFade})
								glasswork.ApplyLiquidButton(resetBtn, glasswork.CompactButtonOpts())
								resetBtn.Children(func() {
									ui.Text(c, "恢复默认").FontSize(11.5).FontWeight(600).TextColor(rCol).SingleLine()
								})
								if resetBtn.Clicked() {
									e.stopRecordingHotkey()
									if err := e.app.ResetDefaultHotkeys(); err != nil {
										e.settingsErr = err.Error()
									} else {
										e.settingsErr = ""
										e.SetToast("已恢复默认快捷键")
									}
								}
							})

							for i, meta := range hotkeyOrder {
								idx := i
								m := meta
								acc := e.app.Hotkey(m.Action)
								recording := e.recordingHotkey == m.Action

								ui.Row(c.Key("hk-row-"+string(m.Action))).
									Height(40).Padding(0, 10, 0, 12).Radius(13).Gap(6).
									Background(tk.hoverFill.Alpha(0.55)).AlignItems(ui.Center).Children(func() {
									ui.Text(c, m.Label).FontSize(13).FontWeight(500).TextColor(tk.textPrimary).Grow(1)

									recLabel := FormatHotkeyDisplay(acc)
									recTip := "点击录制快捷键（录制时按 Backspace 可取消）"
									if recording {
										recLabel = "请按下快捷键…"
										recTip = "按下组合键保存，Esc 退出，Backspace 取消快捷键"
									}

									pill := ui.Row(c.Key("hk-rec-"+string(m.Action))).
										Height(28).MinWidth(124).Padding(0, 11, 0, 11).Radius(14).
										AlignItems(ui.Center).Justify(ui.Center).
										Focusable().Bind(&e.hotkeyRecHandles[idx]).
										Role(ui.RoleButton).Label("修改" + m.Label + "快捷键").
										Cursor(ui.CursorPointer).Tooltip(recTip)

									fg := tk.textPrimary
									if recording {
										pill.Background(tk.inks.AccentFill).Border(1.5, tk.inks.Accent)
										fg = tk.inks.Accent
									} else {
										if acc == "" {
											fg = tk.textTertiary
										}
										if pill.Hovered() {
											pill.Background(tk.hoverFill).Border(1, tk.inks.AccentBorder)
										} else {
											pill.Background(tk.cardBg.Alpha(0.55)).Border(1, tk.hairline)
										}
									}
									pill.Transition(ui.ElementTransition{Colors: true, Animation: &glasswork.AnimColorFade})
									glasswork.ApplyLiquidButton(pill, glasswork.CompactButtonOpts())

									if recording {
										pill.HandleInput(func(ev ui.InputEvent) bool {
											if ev.Kind != ui.InputKeyDown {
												return false
											}
											nextAcc, clear, cancel, ignored, errMsg := ParseHotkeyInput(ev)
											if ignored {
												return true
											}
											if cancel {
												e.stopRecordingHotkey()
												e.settingsErr = ""
												return true
											}
											if clear {
												e.commitHotkey(m.Action, "")
												return true
											}
											if errMsg != "" {
												e.settingsErr = errMsg
												return true
											}
											e.commitHotkey(m.Action, nextAcc)
											return true
										})
									}

									pill.Children(func() {
										ui.Text(c, recLabel).FontSize(11.5).FontWeight(600).TextColor(fg).SingleLine()
									})

									if pill.Clicked() {
										if recording {
											e.stopRecordingHotkey()
											e.settingsErr = ""
										} else {
											e.startRecordingHotkey(c, idx, m.Action)
										}
									}

									if acc != "" && !recording {
										clrBtn := ui.Box(c.Key("hk-clr-"+string(m.Action))).
											Size(24, 24).Center().Radius(12).
											Role(ui.RoleButton).Label("取消" + m.Label + "快捷键").
											Cursor(ui.CursorPointer).Tooltip("取消快捷键")
										cCol := tk.textTertiary
										hov := clrBtn.Hovered()
										if hov {
											clrBtn.Background(tk.inks.DangerFill)
											cCol = tk.inks.Danger
										}
										clrBtn.Transition(ui.ElementTransition{Colors: true, Animation: &glasswork.AnimColorFade})
										glasswork.ApplyLiquidButton(clrBtn, glasswork.CompactButtonOpts())
										clrBtn.Children(func() {
											ui.Icon(c, icons.Close.For(hov || clrBtn.Pressed())).Size(14, 14).TextColor(cCol).ContentTransition(ui.ContentTransition{})
										})
										if clrBtn.Clicked() {
											e.commitHotkey(m.Action, "")
										}
									} else {
										ui.Box(c).Size(24, 24)
									}
								})
							}
						})

						// Section 5: Offline OCR Engine & On-Demand PP-OCRv6
						ocrSt := e.app.PPOCRStatus()
						ui.Column(c).Gap(8).Children(func() {
							ui.Text(c, "文字识别").FontSize(11).FontWeight(700).TextColor(tk.inks.Accent)

							ppocrBadge := ""
							if ocrSt.Downloading {
								ppocrBadge = "下载中"
							} else if !ocrSt.Installed {
								ppocrBadge = "需下载"
							}
							ui.Row(c.Key("settings-ocr-engine-row")).
								Height(40).Padding(0, 10, 0, 12).Radius(13).
								Background(tk.hoverFill.Alpha(0.55)).AlignItems(ui.Center).Children(func() {
								ui.Text(c, "识别引擎").FontSize(13).FontWeight(500).TextColor(tk.textPrimary).Grow(1)
								glasswork.Select(c, glasswork.SelectProps[ocr.Engine]{
									Key:      "settings-ocr-engine-select",
									Label:    "选择识别引擎",
									Tooltip:  "选择离线文字识别引擎",
									Selected: &e.ocrEngineChoice,
									Open:     &e.ocrEngineSelectOpen,
									Width:    148,
									Height:   28,
									Kind:     tk.glass.Kind,
									Accent:   tk.accent,
									AlignEnd: true,
									Options: []glasswork.SelectOption[ocr.Engine]{
										{Value: ocr.EngineSystem, Label: "系统原生"},
										{Value: ocr.EnginePPOCRv6, Label: "PP-OCRv6", Badge: ppocrBadge},
									},
									OnSelect: func(eng ocr.Engine) {
										if eng == ocr.EngineSystem {
											e.ocrEngineSynced = ocr.EngineSystem
											e.app.SetOCREngine(ocr.EngineSystem)
											e.SetToast("已切换为系统原生文字识别")
											return
										}
										if ocrSt.Installed {
											e.ocrEngineSynced = ocr.EnginePPOCRv6
											e.app.SetOCREngine(ocr.EnginePPOCRv6)
											e.SetToast("已切换为本地 PP-OCRv6 高精度识别")
										} else if !ocrSt.Downloading {
											e.ocrEngineChoice = ocrSt.Engine
											e.ocrEngineSynced = ocrSt.Engine
											e.app.StartPPOCRDownload()
											e.SetToast("开始按需下载 PP-OCRv6 本地模型…")
										} else {
											e.ocrEngineChoice = ocrSt.Engine
											e.ocrEngineSynced = ocrSt.Engine
											e.SetToast("PP-OCRv6 模型正在下载中…")
										}
									},
								})
							})

							ui.Row(c.Key("settings-ocr-model-row")).
								Height(40).Padding(0, 10, 0, 12).Radius(13).Gap(8).
								Background(tk.hoverFill.Alpha(0.55)).AlignItems(ui.Center).Children(func() {
								if ocrSt.Downloading {
									stage := ocrSt.Stage
									if stage == "" {
										stage = "下载中…"
									}
									ui.Text(c, stage).FontSize(12).FontWeight(500).TextColor(tk.textPrimary).SingleLine().Shrink(0)

									pct := float32(ocrSt.Percent / 100.0)
									if pct < 0 {
										pct = 0
									} else if pct > 1 {
										pct = 1
									}
									trackCol := tk.hairline
									if tk.dark {
										trackCol = ui.RGBA(255, 255, 255, 0.16)
									}
									accentCol := tk.accent
									ui.Box(c.Key("settings-ocr-progress")).Height(6).Grow(1).Radius(3).Draw(func(pt *ui.Painter, r ui.Rect) {
										pt.Fill(r, trackCol, r.H/2)
										if pct > 0.01 {
											fillW := r.W * pct
											if fillW < r.H {
												fillW = r.H
											}
											pt.Fill(ui.Rect{X: r.X, Y: r.Y, W: fillW, H: r.H}, accentCol, r.H/2)
										}
									})

									ui.Textf(c, "%d%%", int(math.Round(ocrSt.Percent))).
										FontSize(11.5).FontWeight(600).TextColor(tk.inks.Accent).SingleLine().Shrink(0).
										ContentTransition(glasswork.ContentNumeric)

									cancelBtn := ui.Row(c.Key("settings-ocr-cancel")).
										Height(26).Padding(0, 10, 0, 10).Radius(13).AlignItems(ui.Center).Shrink(0).
										Role(ui.RoleButton).Label("取消下载").
										Cursor(ui.CursorPointer).Tooltip("取消 PP-OCRv6 模型下载")
									cFg := tk.textSecondary
									if cancelBtn.Hovered() {
										cancelBtn.Background(tk.inks.DangerFill)
										cFg = tk.inks.Danger
									} else {
										cancelBtn.Background(tk.cardBg.Alpha(0.55)).Border(1, tk.hairline)
									}
									cancelBtn.Transition(ui.ElementTransition{Colors: true, Animation: &glasswork.AnimColorFade})
									glasswork.ApplyLiquidButton(cancelBtn, glasswork.CompactButtonOpts())
									cancelBtn.Children(func() {
										ui.Text(c, "取消").FontSize(11.5).FontWeight(600).TextColor(cFg).SingleLine()
									})
									if cancelBtn.Clicked() {
										e.app.CancelPPOCRDownload()
									}
								} else if ocrSt.Installed {
									mb := float64(ocrSt.DiskBytes) / (1024 * 1024)
									ui.Row(c).AlignItems(ui.Center).Gap(6).Grow(1).Children(func() {
										ui.Text(c, "PP-OCRv6").FontSize(13).FontWeight(500).TextColor(tk.textPrimary).SingleLine()
										ui.Box(c).Padding(2, 7, 2, 7).Radius(8).
											Background(tk.inks.AccentFill).Border(1, tk.inks.AccentBorder).Children(func() {
											ui.Textf(c, "已就绪 · %.1f MB", mb).FontSize(11).FontWeight(600).TextColor(tk.inks.Accent).SingleLine()
										})
									})

									delBtn := ui.Row(c.Key("settings-ocr-delete")).
										Height(26).Padding(0, 10, 0, 10).Radius(13).AlignItems(ui.Center).Shrink(0).
										Role(ui.RoleButton).Label("清理模型").
										Cursor(ui.CursorPointer).Tooltip("删除本地 PP-OCRv6 模型并切回系统原生")
									dFg := tk.textSecondary
									if delBtn.Hovered() {
										delBtn.Background(tk.inks.DangerFill)
										dFg = tk.inks.Danger
									} else {
										delBtn.Background(tk.cardBg.Alpha(0.55)).Border(1, tk.hairline)
									}
									delBtn.Transition(ui.ElementTransition{Colors: true, Animation: &glasswork.AnimColorFade})
									glasswork.ApplyLiquidButton(delBtn, glasswork.CompactButtonOpts())
									delBtn.Children(func() {
										ui.Text(c, "清理").FontSize(11.5).FontWeight(600).TextColor(dFg).SingleLine()
									})
									if delBtn.Clicked() {
										if err := e.app.DeletePPOCRModels(); err != nil {
											e.settingsErr = err.Error()
										} else {
											e.settingsErr = ""
										}
									}
								} else {
									ui.Row(c).AlignItems(ui.Center).Gap(6).Grow(1).Children(func() {
										ui.Text(c, "PP-OCRv6").FontSize(13).FontWeight(500).TextColor(tk.textPrimary).SingleLine()
										if ocrSt.ErrMsg != "" {
											ui.Text(c, "下载失败").FontSize(11.5).FontWeight(600).TextColor(tk.inks.Danger).SingleLine().Tooltip(ocrSt.ErrMsg)
										} else {
											ui.Text(c, "未下载 · ~30 MB").FontSize(11.5).FontWeight(500).TextColor(tk.textSecondary).SingleLine()
										}
									})

									dlLabel := "下载模型"
									if ocrSt.ErrMsg != "" {
										dlLabel = "重试"
									}
									dlBtn := ui.Row(c.Key("settings-ocr-download")).
										Height(28).Padding(0, 12, 0, 12).Radius(14).AlignItems(ui.Center).Shrink(0).
										Role(ui.RoleButton).Label(dlLabel).
										Cursor(ui.CursorPointer).Tooltip("按需下载 PP-OCRv6 模型与 ONNX Runtime (~30 MB)")
									dlFg := tk.textPrimary
									if dlBtn.Hovered() {
										dlBtn.Background(tk.hoverFill).Border(1, tk.inks.AccentBorder)
										dlFg = tk.inks.Accent
									} else {
										dlBtn.Background(tk.cardBg.Alpha(0.55)).Border(1, tk.hairline)
									}
									dlBtn.Transition(ui.ElementTransition{Colors: true, Animation: &glasswork.AnimColorFade})
									glasswork.ApplyLiquidButton(dlBtn, glasswork.CompactButtonOpts())
									dlBtn.Children(func() {
										ui.Text(c, dlLabel).FontSize(11.5).FontWeight(600).TextColor(dlFg).SingleLine()
									})
									if dlBtn.Clicked() {
										e.app.StartPPOCRDownload()
									}
								}
							})

							ui.Row(c.Key("settings-redact-kw-row")).
								Height(40).Padding(0, 10, 0, 12).Radius(13).Gap(8).
								Background(tk.hoverFill.Alpha(0.55)).AlignItems(ui.Center).Children(func() {
								ui.Text(c, "敏感词").FontSize(13).FontWeight(500).TextColor(tk.textPrimary).SingleLine().Shrink(0)
								kwInp := ui.TextInput(c.Key("settings-redact-keywords"), &e.redactKeywordsInput).
									Grow(1).Height(28).Radius(9).Padding(0, 8, 0, 8).
									FontSize(12).TextColor(tk.textPrimary).
									Background(tk.cardBg.Alpha(0.65)).Border(1, tk.hairline).
									Placeholder("姓名、公司、域名（逗号分隔）").
									Label("自定义敏感词").Tooltip("智能打码时自动遮挡这些关键词（逗号分隔）")
								if kwInp.Changed() {
									e.app.SetRedactKeywords(e.redactKeywordsInput)
								}
							})
						})
					})
				})

				if e.settingsErr != "" {
					ui.Row(c.Key("settings-err")).
						Padding(8, 12, 8, 12).Radius(12).
						Background(tk.inks.DangerFill).AlignItems(ui.Center).Children(func() {
						ui.Text(c, e.settingsErr).FontSize(12).FontWeight(600).TextColor(tk.inks.Danger)
					})
				}
			})
		})
	})
}

func (e *EditorState) renderLiquidSwitchRow(c *ui.Context, tk appleTokens, key, label string, on *bool, onChanged func(bool)) {
	sw := ui.SwitchBase(c.Key(key), on).
		Height(38).Padding(0, 12, 0, 12).Radius(13).
		Background(tk.hoverFill.Alpha(0.55)).AlignItems(ui.Center).
		Label(label).Cursor(ui.CursorPointer)
	if sw.Hovered() {
		sw.Background(tk.hoverFill)
	}
	sw.Transition(ui.ElementTransition{Colors: true, Animation: &glasswork.AnimColorFade})

	target := float32(0)
	if *on {
		target = 1
	}
	prog := sw.AnimateTo("switch-prog", target, glasswork.SpringSlide)
	trackOn := tk.accent
	trackOff := tk.hairline
	if tk.dark {
		trackOff = ui.RGBA(255, 255, 255, 0.22)
	}

	sw.Children(func() {
		ui.Text(c, label).FontSize(13).FontWeight(500).TextColor(tk.textPrimary).Grow(1)
		ui.Box(c).Size(42, 24).Radius(12).Draw(func(pt *ui.Painter, r ui.Rect) {
			t := clamp32(prog, 0, 1)
			trackCol := trackOff.MixOklab(trackOn, t)
			pt.Fill(r, trackCol, r.H/2)
			const pad = float32(3)
			knob := r.H - pad*2
			kx := r.X + pad + (r.W-knob-pad*2)*t
			kr := ui.Rect{X: kx, Y: r.Y + pad, W: knob, H: knob}
			pt.Shadow(kr, knob/2, 0, 1, 3, 0, ui.RGBA(0, 0, 0, 0.24))
			pt.Fill(kr, ui.Hex("#FFFFFF"), knob/2)
		})
	})

	if sw.Changed() {
		onChanged(*on)
	}
}

func drawSubtleDotGrid(pt *ui.Painter, r ui.Rect, col ui.Color) {
	// On GPU, render the entire dot grid in a single shader draw op instead of
	// emitting 1,200-4,600 separate Fill ops into Scene/Differ every frame.
	if pt.Accelerated() {
		vals := dotGridValuesPool.Get().(*shader.Values)
		defer dotGridValuesPool.Put(vals)
		vals.Color(uDotColor, col)
		dotGridShader.Draw(pt, r, shader.Options{Values: vals})
		return
	}
	const step float32 = 28
	for y := r.Y + 14; y < r.Y+r.H; y += step {
		for x := r.X + 14; x < r.X+r.W; x += step {
			pt.Fill(ui.Rect{X: x, Y: y, W: 1.5, H: 1.5}, col, 1)
		}
	}
}

func appleGlassDivider(c *ui.Context, tk appleTokens) {
	ui.Box(c).Size(1, 18).Background(tk.hairline).Margin(0, 3, 0, 3)
}

func appleIconBtn(c *ui.Context, tk appleTokens, ic icons.IconPair, label, tip string, active bool) bool {
	btn := ui.Box(c).Size(30, 30).Center().Radius(15).
		Role(ui.RoleButton).Label(label).
		Cursor(ui.CursorPointer).Tooltip(tip)
	col := tk.textSecondary
	if active {
		btn.Background(tk.inks.AccentFill).Border(1, tk.inks.AccentBorder)
		col = tk.inks.Accent
	} else if btn.Hovered() {
		btn.Background(tk.hoverFill)
		col = tk.textPrimary
	}
	btn.Transition(ui.ElementTransition{Colors: true, Animation: &glasswork.AnimColorFade})
	glasswork.ApplyLiquidButton(btn, glasswork.CompactButtonOpts())
	glyph := ic.For(active || btn.Pressed())
	btn.Children(func() {
		ui.Icon(c, glyph).Size(17, 17).TextColor(col).ContentTransition(ui.ContentTransition{})
	})
	return btn.Clicked()
}

func appleIconBtnDisabled(c *ui.Context, tk appleTokens, ic icons.IconPair, label, tip string, disabled bool) bool {
	if disabled {
		ui.Box(c).Size(28, 28).Center().Radius(14).Opacity(0.30).Tooltip(tip).Children(func() {
			ui.Icon(c, ic.Idle).Size(16.5, 16.5).TextColor(tk.textSecondary)
		})
		return false
	}
	return appleIconBtn(c, tk, ic, label, tip, false)
}

func appleDockToolBtn(c *ui.Context, tk appleTokens, ic icons.IconPair, label, tip string, active bool) bool {
	btn := ui.Box(c).Size(34, 34).Center().Radius(17).
		Role(ui.RoleButton).Label(label).
		Cursor(ui.CursorPointer).Tooltip(tip)
	col := tk.textSecondary
	if active {
		col = tk.inks.Accent
	} else if btn.Hovered() {
		btn.Background(tk.hoverFill)
		col = tk.textPrimary
	}
	btn.Transition(ui.ElementTransition{Colors: true, Animation: &glasswork.AnimColorFade})
	glasswork.ApplyLiquidButton(btn, glasswork.CompactButtonOpts())
	glyph := ic.For(active || btn.Pressed())
	btn.Children(func() {
		if active {
			glasswork.SlidingAccentLens(c, "studio-tool", 17, tk.inks.AccentFill, tk.inks.AccentBorder)
		}
		ui.Icon(c, glyph).Size(17.5, 17.5).TextColor(col).ContentTransition(ui.ContentTransition{})
	})
	return btn.Clicked()
}

func appleDockPillBtn(c *ui.Context, tk appleTokens, ic icons.IconPair, label string) bool {
	btn := ui.Row(c).Height(32).Padding(0, 12, 0, 10).Gap(6).Radius(16).AlignItems(ui.Center).
		Role(ui.RoleButton).Label(label).Cursor(ui.CursorPointer)
	hov := btn.Hovered()
	col := tk.textSecondary
	if hov {
		btn.Background(tk.hoverFill)
		col = tk.textPrimary
	}
	btn.Transition(ui.ElementTransition{Colors: true, Animation: &glasswork.AnimColorFade})
	glasswork.ApplyLiquidButton(btn, glasswork.CompactButtonOpts())
	glyph := ic.For(hov || btn.Pressed())
	btn.Children(func() {
		ui.Icon(c, glyph).Size(16.5, 16.5).TextColor(col).ContentTransition(ui.ContentTransition{})
		ui.Text(c, label).FontSize(12.5).FontWeight(500).TextColor(tk.textPrimary)
	})
	return btn.Clicked()
}

func appleSubChip(c *ui.Context, tk appleTokens, label string, active bool) bool {
	return appleSubChipScoped(c, tk, label, active, "")
}

func appleSubChipScoped(c *ui.Context, tk appleTokens, label string, active bool, lensScope string) bool {
	btn := ui.Row(c).Height(24).Padding(0, 10, 0, 10).Radius(12).AlignItems(ui.Center).Shrink(0).
		Role(ui.RoleButton).Label(label).Cursor(ui.CursorPointer)
	fg := tk.textSecondary
	if active {
		if lensScope == "" {
			btn.Background(tk.inks.AccentFill).Border(1, tk.inks.AccentBorder)
		}
		fg = tk.inks.Accent
	} else if btn.Hovered() {
		btn.Background(tk.hoverFill)
		fg = tk.textPrimary
	}
	btn.Transition(ui.ElementTransition{Colors: true, Animation: &glasswork.AnimColorFade})
	glasswork.ApplyLiquidButton(btn, glasswork.CompactButtonOpts())
	btn.Children(func() {
		if active && lensScope != "" {
			glasswork.SlidingAccentLens(c, lensScope, 12, tk.inks.AccentFill, tk.inks.AccentBorder)
		}
		ui.Text(c, label).FontSize(11.5).FontWeight(600).TextColor(fg).SingleLine()
	})
	return btn.Clicked()
}

func appleMiniBtn(c *ui.Context, tk appleTokens, ic icons.IconPair, tip string) bool {
	btn := ui.Box(c).Size(24, 24).Center().Radius(12).
		Role(ui.RoleButton).Label(tip).
		Cursor(ui.CursorPointer).Tooltip(tip)
	col := tk.textSecondary
	hov := btn.Hovered()
	if hov {
		btn.Background(tk.hoverFill)
		col = tk.textPrimary
	}
	btn.Transition(ui.ElementTransition{Colors: true, Animation: &glasswork.AnimColorFade})
	glasswork.ApplyLiquidButton(btn, glasswork.CompactButtonOpts())
	glyph := ic.For(hov || btn.Pressed())
	btn.Children(func() {
		ui.Icon(c, glyph).Size(15, 15).TextColor(col).ContentTransition(ui.ContentTransition{})
	})
	return btn.Clicked()
}

func appleHelpRow(c *ui.Context, tk appleTokens, key, desc string) {
	ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
		ui.Row(c).Padding(2, 7, 2, 7).Radius(7).Background(tk.hoverFill).Children(func() {
			ui.Text(c, key).FontSize(11).FontWeight(600).TextColor(tk.textPrimary)
		})
		ui.Text(c, desc).FontSize(12).TextColor(tk.textSecondary).Grow(1)
	})
}
