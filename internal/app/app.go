package app

import (
	"bytes"
	"fmt"
	"image"
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
	"koma/internal/winapi"
)

const maxHistoryEntries = 12

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
	if len(h.PNG) == 0 {
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(h.PNG))
	if err != nil {
		return nil
	}
	return toRGBA(img)
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
	pins          []*PinWindow
	editor        *EditorState

	history       []HistoryEntry
	nextHistoryID int

	// Preferences & settings
	settingsPath   string
	darkMode       bool
	openAtLogin    bool
	hotkeysPaused  bool
	delayCountdown int

	// Global hotkey accelerators ("" means disabled)
	hotkeyArea  string
	hotkeyFull  string
	hotkeyDelay string
	hotkeyPin   string
}

// New creates and initializes a Koma application state.
func New() *App {
	a := &App{
		darkMode:    false, // Crisp Apple Light Studio default (toggleable to Space Graphite Dark)
		hotkeyArea:  DefaultHotkeyArea,
		hotkeyFull:  DefaultHotkeyFull,
		hotkeyDelay: DefaultHotkeyDelay,
		hotkeyPin:   DefaultHotkeyPin,
	}
	a.editor = NewEditorState(a)
	return a
}

// Run starts the Koma desktop application lifecycle.
func (a *App) Run() error {
	mygo.App.SetName("Koma")

	// Stay resident in the system tray when windows close so hotkeys stay instant.
	mygo.App.OnWindowAllClosed(func() {})

	mygo.App.WhenReady(func() {
		a.initSettingsPath()
		_ = a.LoadSettings()
		a.SyncOpenAtLogin()
		a.setupTray()
		a.setupGlobalShortcuts()
		a.openEditorWindow()
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
	return mygo.NewMenu([]*mygo.MenuItem{
		{
			Label:       "区域截图",
			Accelerator: a.Hotkey(HotkeyArea),
			Click:       func(*mygo.MenuItem, *mygo.Window) { a.StartAreaCapture(true) },
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
		{
			Label: "打开 Koma",
			Click: func(*mygo.MenuItem, *mygo.Window) { a.ShowEditor() },
		},
		{
			Label: "设置…",
			Click: func(*mygo.MenuItem, *mygo.Window) { a.ShowSettings() },
		},
		mygo.Separator(),
		{
			Label: "退出 Koma",
			Click: func(*mygo.MenuItem, *mygo.Window) { a.Quit() },
		},
	})
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

func (a *App) openEditorWindow() {
	startHidden := mygo.App.WasOpenedAtLogin()
	w := mygo.NewWindow(mygo.WindowOptions{
		Title:          "Koma",
		Width:          1220,
		Height:         780,
		MinWidth:       920,
		MinHeight:      580,
		Hidden:         startHidden,
		TitleBarStyle:  mygo.TitleBarHidden,
		TitleBarHeight: 44,
		StateKey:       "koma-studio-v1",
		Content:        ui.View(a.editor.View),
	})
	a.mu.Lock()
	a.editorWin = w
	a.editorHidden = startHidden
	a.mu.Unlock()

	a.SyncWindowTheme()

	w.OnClose(func(e *mygo.CloseEvent) {
		a.mu.Lock()
		quitting := a.quitting
		hasTray := a.tray != nil
		a.mu.Unlock()
		if !quitting && hasTray {
			e.PreventDefault()
			w.Hide()
			a.mu.Lock()
			a.editorHidden = true
			a.mu.Unlock()
		}
	})
}

// Quit exits Koma cleanly, bypassing hide-to-tray interception and closing overlays/pins.
func (a *App) Quit() {
	a.mu.Lock()
	a.quitting = true
	ov := a.activeOverlay
	pins := append([]*PinWindow(nil), a.pins...)
	a.mu.Unlock()

	if ov != nil {
		ov.closeAll()
	}
	for _, p := range pins {
		if p.win != nil {
			p.win.Close()
		}
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
	if w != nil {
		mygo.Theme.SetSource(src)
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
	a.mu.Lock()
	w := a.editorWin
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

// reclaimMemoryAsync returns released screenshot buffers promptly to the OS.
func reclaimMemoryAsync() {
	go func() {
		time.Sleep(180 * time.Millisecond)
		runtime.GC()
		debug.FreeOSMemory()
	}()
}

// ShowEditor brings the Koma Studio window to the front.
func (a *App) ShowEditor() {
	a.mu.Lock()
	w := a.editorWin
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

// StartDelayedCapture counts down N seconds then launches the QQ-style area overlay.
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
	a.delayCountdown = seconds
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
			time.Sleep(time.Second)
		}
		a.mu.Lock()
		a.delayCountdown = 0
		a.mu.Unlock()
		if w != nil {
			w.Update(func() {})
		}
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
	a.AddHistoryEntry(img, title)
	a.mu.Lock()
	w := a.editorWin
	a.editorHidden = false
	a.mu.Unlock()

	apply := func() {
		a.editor.LoadImage(img, title)
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
	if len(a.history) > maxHistoryEntries {
		for i := maxHistoryEntries; i < len(a.history); i++ {
			a.history[i].PNG = nil
			a.history[i].Thumb = nil
		}
		a.history = a.history[:maxHistoryEntries]
	}
	w := a.editorWin
	a.mu.Unlock()
	if w != nil {
		w.Update(func() {})
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
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		a.NotifyToast("无法解析剪贴板图片")
		return false
	}
	rgba := toRGBA(img)
	a.OpenImageInEditor(rgba, "剪贴板图片")
	a.NotifyToast(fmt.Sprintf("已载入剪贴板图片 (%d × %d)", rgba.Bounds().Dx(), rgba.Bounds().Dy()))
	return true
}

// PinFromClipboard pins whatever image is on the clipboard (or the active Studio composite).
func (a *App) PinFromClipboard() {
	data := mygo.Clipboard.ReadImage()
	if len(data) > 0 {
		if img, _, err := image.Decode(bytes.NewReader(data)); err == nil {
			a.OpenPin(toRGBA(img), 0, 0, 0)
			return
		}
	}
	if comp := a.editor.ExportComposite(); comp != nil {
		a.OpenPin(comp, 0, 0, 0)
	}
}

// OpenImageFromDisk opens a native Windows file picker for PNG/JPG images.
func (a *App) OpenImageFromDisk() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent: a.editorWin,
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
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		a.NotifyToast("不支持的图片格式")
		return
	}
	rgba := toRGBA(img)
	a.OpenImageInEditor(rgba, filepath.Base(path))
}

// SaveImageWithDialog opens a native Save dialog and writes PNG or JPEG.
func (a *App) SaveImageWithDialog(img *image.RGBA, parent *mygo.Window) {
	if img == nil {
		return
	}
	defaultName := fmt.Sprintf("Koma_%s.png", time.Now().Format("20060102_150405"))
	if picDir, err := mygo.App.Path(mygo.PathPictures); err == nil && picDir != "" {
		defaultName = filepath.Join(picDir, defaultName)
	}
	savePath, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
		Parent:      parent,
		Title:       "保存截图",
		DefaultPath: defaultName,
		Filters: []mygo.FileFilter{
			{Name: "PNG 图片", Extensions: []string{"png"}},
			{Name: "JPEG 图片", Extensions: []string{"jpg", "jpeg"}},
		},
	})
	if err != nil || savePath == "" {
		return
	}

	var encoded []byte
	ext := strings.ToLower(filepath.Ext(savePath))
	if ext == ".jpg" || ext == ".jpeg" {
		encoded, err = canvas.EncodeJPEG(img, 93)
	} else {
		if ext == "" {
			savePath += ".png"
		}
		encoded, err = canvas.EncodePNG(img)
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

// NotifyToast shows a calm floating Liquid Glass toast inside Studio.
func (a *App) NotifyToast(msg string) {
	a.mu.Lock()
	w := a.editorWin
	a.mu.Unlock()
	if w != nil {
		w.Update(func() {
			a.editor.SetToast(msg)
		})
	}
}

func toRGBA(src image.Image) *image.RGBA {
	if r, ok := src.(*image.RGBA); ok {
		return canvas.CloneRGBA(r)
	}
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			dst.Set(x, y, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

func sleepMs(ms int) {
	time.Sleep(time.Duration(ms) * time.Millisecond)
}
