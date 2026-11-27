package app

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/draw"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"koma/internal/canvas"
	"koma/internal/icons"
	"koma/internal/ocr"
	"koma/internal/winapi"
)

const (
	maxHistoryEntries = 12
	maxHistoryBytes   = 24 << 20 // 24 MiB cap across retained compressed history PNGs
)

// HistoryEntry keeps a compressed PNG screenshot and a small thumbnail in session memory
// so repeated captures keep RAM footprint tiny (~200-500 KB per capture instead of 15-35 MB).
type HistoryEntry struct {
	ID         int
	Title      string
	CapturedAt time.Time
	PNG        []byte
	Thumb      *ui.Bitmap
	Width      int
	Height     int
}

// DecodeImage decodes the compressed history PNG back into an *image.RGBA on demand.
func (h HistoryEntry) DecodeImage() *image.RGBA {
	return decodeImageRGBA(h.PNG)
}

// App coordinates Koma's Studio window, system tray, global shortcuts,
// QQ-style capture overlays, floating pin windows, and capture history.
type App struct {
	mu sync.Mutex

	tray          *mygo.Tray
	editorWin     *mygo.Window
	editorHidden  bool
	quitting      bool
	activeOverlay *overlaySession
	activeScroll  *scrollSession
	toastHUD      *globalToastHUD
	delayHUD      *delayCountdownHUD
	delayCancelCh chan struct{}
	ocrWin        *ocrResultWindow
	pins          []*PinWindow
	editor        *EditorState
	trayTrimTimer *time.Timer

	history       []HistoryEntry
	nextHistoryID int

	// Preferences & settings
	settingsPath   string
	darkMode       bool
	openAtLogin    bool
	hotkeysPaused  bool
	delayCountdown int
	saveDir        string
	saveFormat     string
	quickSave      bool

	// Offline OCR engine selection & on-demand PP-OCRv6 model manager
	ocrEngine       ocr.Engine
	ocrDataDir      string
	ppocr           *ocr.PPOCREngine
	ocrDownloading  bool
	ocrDownloadPct  float64
	ocrDownloadMsg  string
	ocrDownloadErr  string
	ocrDownloadStop context.CancelFunc

	// Auto-updater state (backed by MyGo's native updater plugin when running)
	updaterReady     bool
	autoCheckUpdates bool
	availableUpdate  string
	lastUpdateCheck  time.Time
	checkUpdatesHook func()

	// Global hotkey accelerators ("" means disabled)
	hotkeyArea  string
	hotkeyLong  string
	hotkeyFull  string
	hotkeyDelay string
	hotkeyPin   string
}

// New creates and initializes a Koma application state.
func New() *App {
	a := &App{
		darkMode:         false, // Crisp Apple Light Studio default (toggleable to Space Graphite Dark)
		autoCheckUpdates: true,
		saveFormat:       SaveFormatPNG,
		ocrEngine:        ocr.EngineSystem,
		hotkeyArea:       DefaultHotkeyArea,
		hotkeyLong:       DefaultHotkeyLong,
		hotkeyFull:       DefaultHotkeyFull,
		hotkeyDelay:      DefaultHotkeyDelay,
		hotkeyPin:        DefaultHotkeyPin,
	}
	a.ppocr = ocr.NewPPOCREngine("")
	a.toastHUD = newGlobalToastHUD(a)
	a.delayHUD = newDelayCountdownHUD(a)
	a.editor = NewEditorState(a)
	return a
}

// IsDarkMode reports whether Koma is currently using dark appearance.
func (a *App) IsDarkMode() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.darkMode
}

// Run starts the Koma desktop application lifecycle.
func (a *App) Run() error {
	mygo.App.SetName("Koma")
	a.setupUpdater()

	// Stay resident in the system tray when windows close so hotkeys stay instant.
	mygo.App.OnWindowAllClosed(func() {})

	mygo.App.WhenReady(func() {
		a.initSettingsPath()
		_ = a.LoadSettings()
		a.SyncOpenAtLogin()
		a.SyncUpdaterState()
		a.setupTray()
		a.setupGlobalShortcuts()
		a.SyncWindowTheme()
		// When launched silently at Windows login, stay tray-only without creating a
		// window or initializing the Direct3D 11 device until the user opens Studio.
		if !mygo.App.WasOpenedAtLogin() {
			a.openEditorWindow()
		} else {
			a.mu.Lock()
			a.editorHidden = true
			a.mu.Unlock()
			a.scheduleTrayTrim()
		}
	})

	mygo.App.OnBeforeQuit(func(e *mygo.QuitEvent) {
		a.mu.Lock()
		a.quitting = true
		a.mu.Unlock()
	})

	mygo.App.OnWillQuit(func(e *mygo.QuitEvent) {
		mygo.GlobalShortcut.UnregisterAll()
		if a.tray != nil {
			a.tray.Destroy()
			a.tray = nil
		}
	})

	return mygo.App.Run()
}

func (a *App) buildTrayMenu() *mygo.Menu {
	items := []*mygo.MenuItem{
		{
			Label:       "区域截图",
			Accelerator: a.Hotkey(HotkeyArea),
			Click:       func(*mygo.MenuItem, *mygo.Window) { a.StartAreaCapture(true) },
		},
		{
			Label:       "长截图",
			Accelerator: a.Hotkey(HotkeyLong),
			Click:       func(*mygo.MenuItem, *mygo.Window) { a.StartLongCapture(true) },
		},
		{
			Label:       "全屏截图",
			Accelerator: a.Hotkey(HotkeyFull),
			Click:       func(*mygo.MenuItem, *mygo.Window) { a.CaptureFullscreen(true) },
		},
		{
			Label:       "延时截图 (3 秒)",
			Accelerator: a.Hotkey(HotkeyDelay),
			Click:       func(*mygo.MenuItem, *mygo.Window) { a.StartDelayedCapture(3) },
		},
		mygo.Separator(),
		{
			Label:       "贴图剪贴板内容",
			Accelerator: a.Hotkey(HotkeyPin),
			Click:       func(*mygo.MenuItem, *mygo.Window) { a.PinFromClipboard() },
		},
	}

	if a.HasPins() {
		if a.HasClickThroughPins() {
			items = append(items, &mygo.MenuItem{
				Label: "恢复全部贴图穿透",
				Click: func(*mygo.MenuItem, *mygo.Window) { a.RestoreAllPinsClickThrough() },
			})
		}
		if a.AllPinsHidden() {
			items = append(items, &mygo.MenuItem{
				Label: "显示全部贴图",
				Click: func(*mygo.MenuItem, *mygo.Window) { a.SetAllPinsVisible(true) },
			})
		} else {
			items = append(items, &mygo.MenuItem{
				Label: "隐藏全部贴图",
				Click: func(*mygo.MenuItem, *mygo.Window) { a.SetAllPinsVisible(false) },
			})
		}
		items = append(items, &mygo.MenuItem{
			Label: "关闭全部贴图",
			Click: func(*mygo.MenuItem, *mygo.Window) { a.CloseAllPins() },
		})
	}

	items = append(items,
		mygo.Separator(),
		&mygo.MenuItem{
			Label: "打开 Koma",
			Click: func(*mygo.MenuItem, *mygo.Window) { a.ShowEditor() },
		},
		&mygo.MenuItem{
			Label: "设置…",
			Click: func(*mygo.MenuItem, *mygo.Window) { a.ShowSettings() },
		},
		&mygo.MenuItem{
			Label: a.trayUpdateLabel(),
			Click: func(*mygo.MenuItem, *mygo.Window) { a.CheckForUpdates() },
		},
		mygo.Separator(),
		&mygo.MenuItem{
			Label: "退出 Koma",
			Click: func(*mygo.MenuItem, *mygo.Window) { a.Quit() },
		},
	)
	return mygo.NewMenu(items)
}

func (a *App) trayTooltip() string {
	if hk := a.Hotkey(HotkeyArea); hk != "" {
		return "Koma — 按 " + FormatHotkeyDisplay(hk) + " 随时截屏"
	}
	return "Koma — 截图与标注"
}

func (a *App) refreshTrayMenu() {
	a.mu.Lock()
	tray := a.tray
	a.mu.Unlock()
	if tray == nil {
		return
	}
	tray.SetMenu(a.buildTrayMenu())
	tray.SetToolTip(a.trayTooltip())
}

func (a *App) setupTray() {
	iconPNG := icons.AppIconPNG(32)
	tray, err := mygo.NewTray(mygo.TrayOptions{
		ID:      "koma-tray",
		Icon:    iconPNG,
		ToolTip: a.trayTooltip(),
		Menu:    a.buildTrayMenu(),
	})
	if err == nil {
		a.mu.Lock()
		a.tray = tray
		a.mu.Unlock()
		tray.OnClick(func() {
			a.ShowEditor()
		})
	}
}

func (a *App) setupGlobalShortcuts() {
	a.reregisterAllGlobalShortcuts()
}

func (a *App) openEditorWindow() *mygo.Window {
	a.mu.Lock()
	if a.editorWin != nil {
		w := a.editorWin
		a.editorHidden = false
		a.mu.Unlock()
		return w
	}
	a.mu.Unlock()

	a.editor.RestoreFromTray()

	w := mygo.NewWindow(mygo.WindowOptions{
		Title:          "Koma",
		Width:          1220,
		Height:         780,
		MinWidth:       920,
		MinHeight:      580,
		TitleBarStyle:  mygo.TitleBarHidden,
		TitleBarHeight: 44,
		StateKey:       "koma-studio-v1",
		Content:        ui.View(a.editor.View),
	})
	a.mu.Lock()
	a.editorWin = w
	a.editorHidden = false
	a.mu.Unlock()

	a.SyncWindowTheme()

	// Allow the Studio window to truly close instead of staying hidden with a live
	// D3D11 swap chain and shared GPU device; OnWindowAllClosed keeps Koma alive
	// in the system tray, and ensureEditorWindow recreates Studio on demand.
	w.OnClosed(func() {
		a.mu.Lock()
		isCurrent := a.editorWin == w
		quitting := a.quitting
		if isCurrent {
			a.editorWin = nil
			a.editorHidden = true
		}
		a.mu.Unlock()
		if isCurrent && !quitting {
			a.editor.ParkForTray()
			reclaimMemoryAsync()
			a.scheduleTrayTrim()
		}
	})
	return w
}

func (a *App) ensureEditorWindow() *mygo.Window {
	a.mu.Lock()
	w := a.editorWin
	a.mu.Unlock()
	if w != nil {
		return w
	}
	if !mygo.App.IsReady() {
		return nil
	}
	return a.openEditorWindow()
}

// Quit exits Koma cleanly, closing Studio, overlays, HUDs, OCR windows, and pins.
func (a *App) Quit() {
	a.CancelDelayedCapture()

	a.mu.Lock()
	a.quitting = true
	ov := a.activeOverlay
	sc := a.activeScroll
	edWin := a.editorWin
	ocrW := a.ocrWin
	pins := append([]*PinWindow(nil), a.pins...)
	a.mu.Unlock()

	if a.toastHUD != nil {
		a.toastHUD.Close()
	}
	if a.delayHUD != nil {
		a.delayHUD.Close()
	}
	if ocrW != nil {
		ocrW.Close()
	}
	a.CancelPPOCRDownload()
	if a.ppocr != nil {
		a.ppocr.Release()
	}
	if ov != nil {
		ov.closeAll()
	}
	if sc != nil {
		sc.close()
	}
	for _, p := range pins {
		if p.win != nil {
			p.win.Close()
		}
	}
	if edWin != nil {
		edWin.Close()
	}
	mygo.App.Quit()
}

// SyncWindowTheme aligns Windows native caption buttons (min/max/close) and menus
// with Koma's light/dark appearance so top-right controls always have razor-sharp contrast.
func (a *App) SyncWindowTheme() {
	a.mu.Lock()
	dark := a.darkMode
	w := a.editorWin
	a.mu.Unlock()

	src := mygo.ThemeLight
	if dark {
		src = mygo.ThemeDark
	}
	if mygo.App.IsReady() {
		mygo.Theme.SetSource(src)
	}
	if w != nil {
		w.SetWindowButtonsTheme(src)
	}
}

// ToggleDarkMode switches between high-contrast light and dark modes and updates native window controls.
func (a *App) ToggleDarkMode() {
	a.mu.Lock()
	dark := !a.darkMode
	a.mu.Unlock()
	a.SetDarkMode(dark)
}

// SetDarkMode sets Koma's light/dark appearance, syncs native window chrome, and persists the preference.
func (a *App) SetDarkMode(dark bool) {
	a.mu.Lock()
	a.darkMode = dark
	a.mu.Unlock()
	a.SyncWindowTheme()
	_ = a.SaveSettings()
}

// ShowSettings brings Studio to the front and opens the Liquid Glass Settings sheet.
func (a *App) ShowSettings() {
	a.SyncOpenAtLogin()
	w := a.ensureEditorWindow()
	a.mu.Lock()
	a.editorHidden = false
	a.mu.Unlock()
	if w != nil {
		w.Update(func() {
			a.editor.OpenSettings()
		})
		w.Show()
		w.Focus()
	} else {
		a.editor.OpenSettings()
	}
}

var (
	reclaimMu    sync.Mutex
	reclaimTimer *time.Timer
)

// reclaimMemoryAsync returns released screenshot buffers promptly to the OS,
// coalescing rapid consecutive triggers into a single GC + FreeOSMemory pass.
func reclaimMemoryAsync() {
	reclaimMu.Lock()
	if reclaimTimer != nil {
		reclaimTimer.Reset(180 * time.Millisecond)
		reclaimMu.Unlock()
		return
	}
	reclaimTimer = time.AfterFunc(180*time.Millisecond, func() {
		reclaimMu.Lock()
		reclaimTimer = nil
		reclaimMu.Unlock()
		runtime.GC()
		debug.FreeOSMemory()
	})
	reclaimMu.Unlock()
}

// scheduleTrayTrim schedules a debounced OS working-set trim once all windows
// have closed and Koma is resting idle in the system tray. If any window opens
// before the timer fires, the trim is skipped so active UI never stalls.
func (a *App) scheduleTrayTrim() {
	const delay = 1200 * time.Millisecond
	a.mu.Lock()
	if a.quitting {
		a.mu.Unlock()
		return
	}
	if a.trayTrimTimer != nil {
		a.trayTrimTimer.Reset(delay)
		a.mu.Unlock()
		return
	}
	a.trayTrimTimer = time.AfterFunc(delay, func() {
		a.mu.Lock()
		a.trayTrimTimer = nil
		idleInTray := !a.quitting && a.editorWin == nil && a.activeOverlay == nil && a.activeScroll == nil && a.ocrWin == nil && len(a.pins) == 0 && (a.toastHUD == nil || !a.toastHUD.Active())
		a.mu.Unlock()
		if !idleInTray {
			return
		}
		runtime.GC()
		debug.FreeOSMemory()
		winapi.TrimWorkingSet()
	})
	a.mu.Unlock()
}

// ShowEditor brings the Koma Studio window to the front, recreating it if closed to tray.
func (a *App) ShowEditor() {
	w := a.ensureEditorWindow()
	a.mu.Lock()
	a.editorHidden = false
	a.mu.Unlock()
	if w != nil {
		w.Show()
		w.Focus()
		w.Update(func() {})
	}
}

func (a *App) clearOverlaySession(s *overlaySession) {
	a.mu.Lock()
	if a.activeOverlay == s {
		a.activeOverlay = nil
	}
	a.mu.Unlock()
}

func (a *App) clearScrollSession(s *scrollSession) {
	a.mu.Lock()
	if a.activeScroll == s {
		a.activeScroll = nil
	}
	a.mu.Unlock()
}

// CaptureFullscreen grabs the primary monitor immediately and opens it in Studio.
func (a *App) CaptureFullscreen(hideEditorFirst bool) {
	a.mu.Lock()
	wasVisible := a.editorWin != nil && !a.editorHidden
	if hideEditorFirst && a.editorWin != nil {
		a.editorWin.Hide()
		a.editorHidden = true
	}
	a.mu.Unlock()

	go func() {
		if hideEditorFirst && wasVisible {
			sleepMs(190)
		}
		img, _, err := winapi.CapturePrimaryMonitor()
		if err != nil || img == nil {
			a.ShowEditor()
			return
		}
		a.OpenImageInEditor(img, "全屏截图")
	}()
}

// CancelDelayedCapture aborts an in-flight delayed capture countdown cleanly.
func (a *App) CancelDelayedCapture() {
	a.mu.Lock()
	ch := a.delayCancelCh
	a.delayCancelCh = nil
	a.delayCountdown = 0
	w := a.editorWin
	a.mu.Unlock()

	if ch != nil {
		close(ch)
	}
	if a.delayHUD != nil {
		a.delayHUD.Close()
	}
	if w != nil {
		w.Update(func() {})
	}
}

// StartDelayedCapture counts down N seconds (showing a floating Liquid Glass
// countdown pill even when Studio is closed in tray) then launches area capture.
func (a *App) StartDelayedCapture(seconds int) {
	if seconds <= 0 {
		a.StartAreaCapture(true)
		return
	}
	a.mu.Lock()
	if a.delayCountdown > 0 {
		a.mu.Unlock()
		return
	}
	cancelCh := make(chan struct{})
	a.delayCountdown = seconds
	a.delayCancelCh = cancelCh
	w := a.editorWin
	a.mu.Unlock()

	go func() {
		for s := seconds; s > 0; s-- {
			a.mu.Lock()
			a.delayCountdown = s
			a.mu.Unlock()
			if w != nil {
				w.Update(func() {})
			}
			if a.delayHUD != nil {
				a.delayHUD.Show(s, a.CancelDelayedCapture)
			}
			select {
			case <-cancelCh:
				return
			case <-time.After(time.Second):
			}
		}
		a.mu.Lock()
		if a.delayCancelCh == cancelCh {
			a.delayCancelCh = nil
		}
		a.delayCountdown = 0
		a.mu.Unlock()
		if a.delayHUD != nil {
			a.delayHUD.Close()
		}
		if w != nil {
			w.Update(func() {})
		}
		// Give DWM a brief frame so the countdown HUD is gone before grabbing monitors.
		sleepMs(80)
		a.StartAreaCapture(true)
	}()
}

// OpenImageInEditor loads an image into Studio, records it in history, and focuses Studio.
func (a *App) OpenImageInEditor(img *image.RGBA, title string) {
	a.OpenImageInEditorWithAnnotations(img, nil, title)
}

// OpenImageInEditorWithAnnotations opens a capture along with any in-place
// annotations drawn on the desktop overlay.
func (a *App) OpenImageInEditorWithAnnotations(img *image.RGBA, anns []canvas.Annotation, title string) {
	if img == nil {
		return
	}
	pngBytes, _ := canvas.EncodePNG(img)
	a.AddHistoryEntryWithPNG(img, pngBytes, title)
	w := a.ensureEditorWindow()
	a.mu.Lock()
	a.editorHidden = false
	a.mu.Unlock()

	apply := func() {
		a.editor.loadOwnedImage(img, pngBytes, title)
		if len(anns) > 0 {
			a.editor.annotations = canvas.CloneAnnotations(anns)
			a.editor.rasterDirty = true
		}
		a.editor.rebuildCaches()
	}

	if w != nil {
		w.Update(apply)
		w.Show()
		w.Focus()
	} else {
		apply()
	}
}

// AddHistoryEntry appends a compressed PNG screenshot to the in-memory recent captures list.
func (a *App) AddHistoryEntry(img *image.RGBA, title string) {
	if img == nil {
		return
	}
	pngBytes, err := canvas.EncodePNG(img)
	if err != nil {
		return
	}
	a.AddHistoryEntryWithPNG(img, pngBytes, title)
}

// AddHistoryEntryWithPNG appends a screenshot using already-encoded PNG bytes so callers
// that just encoded for clipboard or pin avoid a second full PNG compression pass.
func (a *App) AddHistoryEntryWithPNG(img *image.RGBA, pngBytes []byte, title string) {
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
	thumbRGBA := canvas.MakeThumbnail(img, 240, 150)
	var thumbBmp *ui.Bitmap
	if thumbRGBA != nil {
		thumbBmp = ui.NewBitmap(thumbRGBA)
	}
	a.mu.Lock()
	a.nextHistoryID++
	entry := HistoryEntry{
		ID:         a.nextHistoryID,
		Title:      title,
		CapturedAt: time.Now(),
		PNG:        pngBytes,
		Thumb:      thumbBmp,
		Width:      img.Bounds().Dx(),
		Height:     img.Bounds().Dy(),
	}
	a.history = append([]HistoryEntry{entry}, a.history...)
	a.trimHistoryLocked()
	w := a.editorWin
	a.mu.Unlock()
	if w != nil {
		w.Update(func() {})
	}
}

func (a *App) trimHistoryLocked() {
	if len(a.history) > maxHistoryEntries {
		for i := maxHistoryEntries; i < len(a.history); i++ {
			a.history[i].PNG = nil
			a.history[i].Thumb = nil
		}
		a.history = a.history[:maxHistoryEntries]
	}
	totalBytes := 0
	for _, h := range a.history {
		totalBytes += len(h.PNG)
	}
	// Enforce byte budget while always keeping at least the newest capture.
	for len(a.history) > 1 && totalBytes > maxHistoryBytes {
		last := len(a.history) - 1
		totalBytes -= len(a.history[last].PNG)
		a.history[last].PNG = nil
		a.history[last].Thumb = nil
		a.history = a.history[:last]
	}
}

// DeleteHistoryEntry removes a history item by ID and frees its buffers.
func (a *App) DeleteHistoryEntry(id int) {
	a.mu.Lock()
	for i, h := range a.history {
		if h.ID == id {
			a.history[i].PNG = nil
			a.history[i].Thumb = nil
			a.history = append(a.history[:i], a.history[i+1:]...)
			break
		}
	}
	a.mu.Unlock()
	reclaimMemoryAsync()
}

// PasteFromClipboard decodes an image from the system clipboard into Studio.
func (a *App) PasteFromClipboard() bool {
	data := mygo.Clipboard.ReadImage()
	if len(data) == 0 {
		a.NotifyToast("剪贴板中没有图片")
		return false
	}
	rgba := decodeImageRGBA(data)
	if rgba == nil {
		a.NotifyToast("无法解析剪贴板图片")
		return false
	}
	a.OpenImageInEditor(rgba, "剪贴板图片")
	a.NotifyToast(fmt.Sprintf("已载入剪贴板图片 (%d × %d)", rgba.Bounds().Dx(), rgba.Bounds().Dy()))
	return true
}

// PinFromClipboard pins whatever image is on the clipboard (or the active Studio composite).
func (a *App) PinFromClipboard() {
	data := mygo.Clipboard.ReadImage()
	if len(data) > 0 {
		if rgba := decodeImageRGBA(data); rgba != nil {
			a.OpenPin(rgba, 0, 0, 0)
			return
		}
	}
	if comp := a.editor.ExportComposite(); comp != nil {
		a.OpenPin(comp, 0, 0, 0)
	}
}

// OpenImageFromDisk opens a native Windows file picker for PNG/JPG images.
func (a *App) OpenImageFromDisk() {
	a.mu.Lock()
	parent := a.editorWin
	a.mu.Unlock()
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent: parent,
			Title:  "选择图片",
			Filters: []mygo.FileFilter{
				{Name: "图片文件", Extensions: []string{"png", "jpg", "jpeg"}},
			},
		})
		if err != nil || len(paths) == 0 {
			return
		}
		a.LoadFilePath(paths[0])
	}()
}

// LoadFilePath reads an image file from disk and opens it in Studio.
func (a *App) LoadFilePath(path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		a.NotifyToast("读取文件失败")
		return
	}
	rgba := decodeImageRGBA(raw)
	if rgba == nil {
		a.NotifyToast("不支持的图片格式")
		return
	}
	a.OpenImageInEditor(rgba, filepath.Base(path))
}

// SaveImagePreferred saves img either silently to EffectiveSaveDir (when QuickSave
// is enabled) or via the native Save dialog pre-seeded with the user's preferred folder & format.
func (a *App) SaveImagePreferred(img *image.RGBA, pngBytes []byte, parent *mygo.Window) {
	if img == nil {
		return
	}
	if a.QuickSave() {
		path, err := a.QuickSaveImage(img, pngBytes)
		if err != nil {
			a.NotifyToast("快速保存失败")
			return
		}
		a.NotifyToast("已快速保存 " + filepath.Base(path))
		return
	}
	a.SaveImageWithDialogPNG(img, pngBytes, parent)
}

// QuickSaveImage encodes and writes img directly into EffectiveSaveDir using the
// configured SaveFormat without prompting for a dialog, returning the written path.
func (a *App) QuickSaveImage(img *image.RGBA, pngBytes []byte) (string, error) {
	if img == nil {
		return "", fmt.Errorf("koma: nil image")
	}
	dir := a.EffectiveSaveDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	format := a.SaveFormat()
	ext := ".png"
	if format == SaveFormatJPEG {
		ext = ".jpg"
	}
	stamp := time.Now().Format("20060102_150405")
	savePath := filepath.Join(dir, fmt.Sprintf("Koma_%s%s", stamp, ext))
	for i := 1; i < 100; i++ {
		if _, err := os.Stat(savePath); os.IsNotExist(err) {
			break
		}
		savePath = filepath.Join(dir, fmt.Sprintf("Koma_%s_%d%s", stamp, i, ext))
	}

	var (
		encoded []byte
		err     error
	)
	if format == SaveFormatJPEG {
		encoded, err = canvas.EncodeJPEG(img, 93)
	} else if len(pngBytes) > 0 {
		encoded = pngBytes
	} else {
		encoded, err = canvas.EncodePNG(img)
	}
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(savePath, encoded, 0o644); err != nil {
		return "", err
	}
	return savePath, nil
}

// SaveImageWithDialog opens a native Save dialog and writes PNG or JPEG.
func (a *App) SaveImageWithDialog(img *image.RGBA, parent *mygo.Window) {
	a.SaveImageWithDialogPNG(img, nil, parent)
}

// SaveImageWithDialogPNG opens a native Save dialog and writes PNG (reusing
// pre-encoded pngBytes when provided to avoid duplicate encoding) or JPEG.
func (a *App) SaveImageWithDialogPNG(img *image.RGBA, pngBytes []byte, parent *mygo.Window) {
	if img == nil {
		return
	}
	format := a.SaveFormat()
	ext := ".png"
	filters := []mygo.FileFilter{
		{Name: "PNG 图片", Extensions: []string{"png"}},
		{Name: "JPEG 图片", Extensions: []string{"jpg", "jpeg"}},
	}
	if format == SaveFormatJPEG {
		ext = ".jpg"
		filters = []mygo.FileFilter{
			{Name: "JPEG 图片", Extensions: []string{"jpg", "jpeg"}},
			{Name: "PNG 图片", Extensions: []string{"png"}},
		}
	}
	defaultName := fmt.Sprintf("Koma_%s%s", time.Now().Format("20060102_150405"), ext)
	if dir := a.EffectiveSaveDir(); dir != "" {
		defaultName = filepath.Join(dir, defaultName)
	}
	savePath, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
		Parent:      parent,
		Title:       "保存截图",
		DefaultPath: defaultName,
		Filters:     filters,
	})
	if err != nil || savePath == "" {
		return
	}

	var encoded []byte
	chosenExt := strings.ToLower(filepath.Ext(savePath))
	if chosenExt == ".jpg" || chosenExt == ".jpeg" {
		encoded, err = canvas.EncodeJPEG(img, 93)
	} else {
		if chosenExt == "" {
			savePath += ext
		}
		if strings.HasSuffix(strings.ToLower(savePath), ".jpg") {
			encoded, err = canvas.EncodeJPEG(img, 93)
		} else if len(pngBytes) > 0 {
			encoded = pngBytes
		} else {
			encoded, err = canvas.EncodePNG(img)
		}
	}
	if err != nil {
		a.NotifyToast("图片编码失败")
		return
	}
	if err := os.WriteFile(savePath, encoded, 0o644); err != nil {
		a.NotifyToast("写入文件失败")
		return
	}
	a.NotifyToast("已保存 " + filepath.Base(savePath))
}

// NotifyToast shows a calm floating Liquid Glass toast inside Studio, or falls
// back to a transient global Liquid Glass HUD pill when Studio is closed in tray.
func (a *App) NotifyToast(msg string) {
	a.mu.Lock()
	w := a.editorWin
	hidden := a.editorHidden
	a.mu.Unlock()
	if w != nil && !hidden {
		w.Update(func() {
			a.editor.SetToast(msg)
		})
		return
	}
	if a.toastHUD != nil {
		a.toastHUD.Show(msg)
	}
}

func decodeImageRGBA(data []byte) *image.RGBA {
	if len(data) == 0 {
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	if r, ok := img.(*image.RGBA); ok && r.Bounds().Min == (image.Point{}) && r.Stride == 4*r.Bounds().Dx() {
		return r
	}
	return toRGBA(img)
}

func toRGBA(src image.Image) *image.RGBA {
	if src == nil {
		return nil
	}
	if r, ok := src.(*image.RGBA); ok {
		return canvas.CloneRGBA(r)
	}
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	return dst
}

func sleepMs(ms int) {
	time.Sleep(time.Duration(ms) * time.Millisecond)
}
