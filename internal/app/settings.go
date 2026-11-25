package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// HotkeyAction identifies one of Koma's configurable global capture shortcuts.
type HotkeyAction string

const (
	HotkeyArea  HotkeyAction = "area"
	HotkeyFull  HotkeyAction = "full"
	HotkeyDelay HotkeyAction = "delay"
	HotkeyPin   HotkeyAction = "pin"
)

// Default global shortcut accelerators.
const (
	DefaultHotkeyArea  = "Ctrl+Shift+A"
	DefaultHotkeyFull  = "Ctrl+Shift+S"
	DefaultHotkeyDelay = "Ctrl+Shift+D"
	DefaultHotkeyPin   = "Ctrl+Shift+P"
)

type hotkeyMeta struct {
	Action  HotkeyAction
	Label   string
	Default string
}

var hotkeyOrder = []hotkeyMeta{
	{HotkeyArea, "区域截图", DefaultHotkeyArea},
	{HotkeyFull, "全屏截图", DefaultHotkeyFull},
	{HotkeyDelay, "延时 3 秒截图", DefaultHotkeyDelay},
	{HotkeyPin, "贴图剪贴板", DefaultHotkeyPin},
}

// persistedSettings uses pointer fields so an explicitly cleared ("") shortcut
// is preserved across restarts rather than overwritten by defaults.
type persistedSettings struct {
	DarkMode    *bool   `json:"dark_mode,omitempty"`
	HotkeyArea  *string `json:"hotkey_area,omitempty"`
	HotkeyFull  *string `json:"hotkey_full,omitempty"`
	HotkeyDelay *string `json:"hotkey_delay,omitempty"`
	HotkeyPin   *string `json:"hotkey_pin,omitempty"`
}

// SetSettingsPath overrides where settings.json is stored (used by tests and startup).
func (a *App) SetSettingsPath(path string) {
	a.mu.Lock()
	a.settingsPath = path
	a.mu.Unlock()
}

func (a *App) initSettingsPath() {
	a.mu.Lock()
	existing := a.settingsPath
	a.mu.Unlock()
	if existing != "" {
		return
	}
	if dir, err := mygo.App.Path(mygo.PathUserData); err == nil && dir != "" {
		a.SetSettingsPath(filepath.Join(dir, "settings.json"))
	}
}

// LoadSettings reads persisted preferences from settingsPath if present.
func (a *App) LoadSettings() error {
	a.mu.Lock()
	path := a.settingsPath
	a.mu.Unlock()
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var ps persistedSettings
	if err := json.Unmarshal(raw, &ps); err != nil {
		return err
	}
	a.mu.Lock()
	if ps.DarkMode != nil {
		a.darkMode = *ps.DarkMode
	}
	if ps.HotkeyArea != nil {
		a.hotkeyArea = strings.TrimSpace(*ps.HotkeyArea)
	}
	if ps.HotkeyFull != nil {
		a.hotkeyFull = strings.TrimSpace(*ps.HotkeyFull)
	}
	if ps.HotkeyDelay != nil {
		a.hotkeyDelay = strings.TrimSpace(*ps.HotkeyDelay)
	}
	if ps.HotkeyPin != nil {
		a.hotkeyPin = strings.TrimSpace(*ps.HotkeyPin)
	}
	a.mu.Unlock()
	return nil
}

// SaveSettings writes current preferences to settingsPath when configured.
func (a *App) SaveSettings() error {
	a.mu.Lock()
	path := a.settingsPath
	dark := a.darkMode
	hkArea := a.hotkeyArea
	hkFull := a.hotkeyFull
	hkDelay := a.hotkeyDelay
	hkPin := a.hotkeyPin
	a.mu.Unlock()

	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	ps := persistedSettings{
		DarkMode:    &dark,
		HotkeyArea:  &hkArea,
		HotkeyFull:  &hkFull,
		HotkeyDelay: &hkDelay,
		HotkeyPin:   &hkPin,
	}
	data, err := json.MarshalIndent(ps, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// Hotkey returns the configured global shortcut accelerator for action ("" if disabled).
func (a *App) Hotkey(action HotkeyAction) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.hotkeyLocked(action)
}

func (a *App) hotkeyLocked(action HotkeyAction) string {
	switch action {
	case HotkeyArea:
		return a.hotkeyArea
	case HotkeyFull:
		return a.hotkeyFull
	case HotkeyDelay:
		return a.hotkeyDelay
	case HotkeyPin:
		return a.hotkeyPin
	default:
		return ""
	}
}

func (a *App) setHotkeyLocked(action HotkeyAction, acc string) {
	switch action {
	case HotkeyArea:
		a.hotkeyArea = acc
	case HotkeyFull:
		a.hotkeyFull = acc
	case HotkeyDelay:
		a.hotkeyDelay = acc
	case HotkeyPin:
		a.hotkeyPin = acc
	}
}

func (a *App) hotkeyHandler(action HotkeyAction) func() {
	switch action {
	case HotkeyArea:
		return func() { a.StartAreaCapture(false) }
	case HotkeyFull:
		return func() { a.CaptureFullscreen(false) }
	case HotkeyDelay:
		return func() { a.StartDelayedCapture(3) }
	case HotkeyPin:
		return func() { a.PinFromClipboard() }
	default:
		return func() {}
	}
}

func hotkeyLabel(action HotkeyAction) string {
	for _, m := range hotkeyOrder {
		if m.Action == action {
			return m.Label
		}
	}
	return string(action)
}

// PauseGlobalShortcuts temporarily unregisters all Koma global shortcuts while the
// user is recording a new shortcut so pressing an existing combo doesn't fire it.
func (a *App) PauseGlobalShortcuts() {
	a.mu.Lock()
	if a.hotkeysPaused {
		a.mu.Unlock()
		return
	}
	a.hotkeysPaused = true
	a.mu.Unlock()
	if mygo.App.IsReady() {
		mygo.GlobalShortcut.UnregisterAll()
	}
}

// ResumeGlobalShortcuts re-registers all configured non-empty global shortcuts.
func (a *App) ResumeGlobalShortcuts() {
	a.mu.Lock()
	wasPaused := a.hotkeysPaused
	a.hotkeysPaused = false
	a.mu.Unlock()
	if !wasPaused {
		return
	}
	a.reregisterAllGlobalShortcuts()
}

func (a *App) reregisterAllGlobalShortcuts() []string {
	if !mygo.App.IsReady() {
		return nil
	}
	mygo.GlobalShortcut.UnregisterAll()

	a.mu.Lock()
	pairs := []struct {
		action HotkeyAction
		acc    string
	}{
		{HotkeyArea, a.hotkeyArea},
		{HotkeyFull, a.hotkeyFull},
		{HotkeyDelay, a.hotkeyDelay},
		{HotkeyPin, a.hotkeyPin},
	}
	a.mu.Unlock()

	var failed []string
	for _, p := range pairs {
		if p.acc == "" {
			continue
		}
		if err := mygo.GlobalShortcut.Register(p.acc, a.hotkeyHandler(p.action)); err != nil {
			failed = append(failed, fmt.Sprintf("%s (%s)", hotkeyLabel(p.action), FormatHotkeyDisplay(p.acc)))
		}
	}
	return failed
}

// SetHotkey updates or disables (acc == "") one global shortcut, validates conflicts,
// re-registers with the OS when running, persists settings.json, and updates the tray menu.
func (a *App) SetHotkey(action HotkeyAction, acc string) error {
	acc = strings.TrimSpace(acc)

	a.mu.Lock()
	if acc != "" {
		for _, m := range hotkeyOrder {
			if m.Action == action {
				continue
			}
			other := a.hotkeyLocked(m.Action)
			if other != "" && strings.EqualFold(other, acc) {
				a.mu.Unlock()
				return fmt.Errorf("快捷键 %s 已被「%s」使用", FormatHotkeyDisplay(acc), m.Label)
			}
		}
	}
	oldAcc := a.hotkeyLocked(action)
	wasPaused := a.hotkeysPaused
	a.mu.Unlock()

	// If identical to current binding, simply resume shortcuts if paused.
	if strings.EqualFold(oldAcc, acc) {
		if wasPaused {
			a.ResumeGlobalShortcuts()
		}
		return nil
	}

	if mygo.App.IsReady() {
		// Unregister all before testing the new binding so rebinding works atomically.
		mygo.GlobalShortcut.UnregisterAll()
		if acc != "" {
			if err := mygo.GlobalShortcut.Register(acc, a.hotkeyHandler(action)); err != nil {
				// Roll back and restore previous shortcuts.
				a.mu.Lock()
				a.hotkeysPaused = false
				a.mu.Unlock()
				a.reregisterAllGlobalShortcuts()
				return fmt.Errorf("无法注册 %s（可能已被其他程序占用）", FormatHotkeyDisplay(acc))
			}
		}
		a.mu.Lock()
		a.setHotkeyLocked(action, acc)
		a.hotkeysPaused = false
		a.mu.Unlock()
		// Register the remaining shortcuts alongside the newly verified one.
		a.reregisterAllGlobalShortcuts()
	} else {
		a.mu.Lock()
		a.setHotkeyLocked(action, acc)
		a.hotkeysPaused = false
		a.mu.Unlock()
	}

	_ = a.SaveSettings()
	a.refreshTrayMenu()
	return nil
}

// ResetDefaultHotkeys restores all 4 global shortcuts to their default bindings.
func (a *App) ResetDefaultHotkeys() error {
	a.mu.Lock()
	a.hotkeyArea = DefaultHotkeyArea
	a.hotkeyFull = DefaultHotkeyFull
	a.hotkeyDelay = DefaultHotkeyDelay
	a.hotkeyPin = DefaultHotkeyPin
	a.hotkeysPaused = false
	a.mu.Unlock()

	failed := a.reregisterAllGlobalShortcuts()
	_ = a.SaveSettings()
	a.refreshTrayMenu()
	if len(failed) > 0 {
		return fmt.Errorf("部分默认快捷键被占用：%s", strings.Join(failed, "、"))
	}
	return nil
}

// OpenAtLogin reports whether Koma is configured to launch automatically at OS login.
func (a *App) OpenAtLogin() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.openAtLogin
}

// SyncOpenAtLogin refreshes the cached startup state from the OS login item registry.
func (a *App) SyncOpenAtLogin() {
	if !mygo.App.IsReady() {
		return
	}
	enabled := mygo.App.OpenAtLogin()
	a.mu.Lock()
	a.openAtLogin = enabled
	a.mu.Unlock()
}

// SetOpenAtLogin enables or disables launching Koma automatically at Windows login.
func (a *App) SetOpenAtLogin(enabled bool) error {
	if mygo.App.IsReady() {
		if err := mygo.App.SetOpenAtLogin(enabled); err != nil {
			return fmt.Errorf("开机自启设置失败：%v", err)
		}
	}
	a.mu.Lock()
	a.openAtLogin = enabled
	a.mu.Unlock()
	return nil
}

// FormatHotkeyDisplay formats a canonical accelerator (e.g. "Ctrl+Shift+A") for UI display.
func FormatHotkeyDisplay(acc string) string {
	acc = strings.TrimSpace(acc)
	if acc == "" {
		return "未设置"
	}
	parts := strings.Split(acc, "+")
	for i, p := range parts {
		switch p {
		case "Super":
			parts[i] = "Win"
		case "PrintScreen":
			parts[i] = "PrtSc"
		}
	}
	return strings.Join(parts, " + ")
}

// FormatHotkeyTooltip appends the shortcut in parentheses when bound, or returns label alone.
func FormatHotkeyTooltip(label, acc string) string {
	if strings.TrimSpace(acc) == "" {
		return label
	}
	return fmt.Sprintf("%s (%s)", label, FormatHotkeyDisplay(acc))
}

// ParseHotkeyInput converts a MyGo key-down event into a canonical MyGo GlobalShortcut
// accelerator string. It returns clear=true on bare Backspace/Delete, cancel=true on bare
// Escape, ignored=true on pure modifier presses/repeats, or errMsg for invalid combos.
func ParseHotkeyInput(ev ui.InputEvent) (acc string, clear, cancel, ignored bool, errMsg string) {
	if ev.Kind != ui.InputKeyDown || ev.Repeat {
		return "", false, false, true, ""
	}
	// Ignore standalone modifier or lock key presses while recording.
	switch ev.Code {
	case ui.CodeControlLeft, ui.CodeControlRight,
		ui.CodeShiftLeft, ui.CodeShiftRight,
		ui.CodeAltLeft, ui.CodeAltRight,
		ui.CodeSuperLeft, ui.CodeSuperRight,
		ui.CodeCapsLock, ui.CodeNumLock, ui.CodeScrollLock:
		return "", false, false, true, ""
	}

	if ev.Mods == 0 {
		if ev.Key == ui.KeyEscape {
			return "", false, true, false, ""
		}
		if ev.Key == ui.KeyBackspace || ev.Key == ui.KeyDelete {
			return "", true, false, false, ""
		}
	}

	keyName, allowSolo := hotkeyNameFromEvent(ev)
	if keyName == "" {
		return "", false, false, true, ""
	}

	hasPrimaryMod := ev.Mods&(ui.Ctrl|ui.Alt|ui.Super) != 0
	if !allowSolo && !hasPrimaryMod {
		return "", false, false, false, "请搭配 Ctrl、Alt 或 Win 组合键"
	}

	var parts []string
	if ev.Mods&ui.Ctrl != 0 {
		parts = append(parts, "Ctrl")
	}
	if ev.Mods&ui.Alt != 0 {
		parts = append(parts, "Alt")
	}
	if ev.Mods&ui.Shift != 0 {
		parts = append(parts, "Shift")
	}
	if ev.Mods&ui.Super != 0 {
		parts = append(parts, "Super")
	}
	parts = append(parts, keyName)
	return strings.Join(parts, "+"), false, false, false, ""
}

func hotkeyNameFromEvent(ev ui.InputEvent) (name string, allowSolo bool) {
	// Check special physical codes first (PrintScreen, F13-F24).
	switch ev.Code {
	case ui.CodePrintScreen:
		return "PrintScreen", true
	case ui.CodeF13:
		return "F13", true
	case ui.CodeF14:
		return "F14", true
	case ui.CodeF15:
		return "F15", true
	case ui.CodeF16:
		return "F16", true
	case ui.CodeF17:
		return "F17", true
	case ui.CodeF18:
		return "F18", true
	case ui.CodeF19:
		return "F19", true
	case ui.CodeF20:
		return "F20", true
	case ui.CodeF21:
		return "F21", true
	case ui.CodeF22:
		return "F22", true
	case ui.CodeF23:
		return "F23", true
	case ui.CodeF24:
		return "F24", true
	}

	k := ev.Key
	if k == ui.KeyUnknown {
		k = ev.PhysicalKey
	}

	if k >= ui.KeyA && k <= ui.KeyZ {
		return string(rune('A' + (k - ui.KeyA))), false
	}
	if k >= ui.Key0 && k <= ui.Key9 {
		return string(rune('0' + (k - ui.Key0))), false
	}
	if k >= ui.KeyF1 && k <= ui.KeyF12 {
		return fmt.Sprintf("F%d", int(k-ui.KeyF1)+1), true
	}

	switch k {
	case ui.KeySpace:
		return "Space", false
	case ui.KeyTab:
		return "Tab", false
	case ui.KeyEnter:
		return "Enter", false
	case ui.KeyInsert:
		return "Insert", false
	case ui.KeyDelete:
		return "Delete", false
	case ui.KeyBackspace:
		return "Backspace", false
	case ui.KeyHome:
		return "Home", false
	case ui.KeyEnd:
		return "End", false
	case ui.KeyPageUp:
		return "PageUp", false
	case ui.KeyPageDown:
		return "PageDown", false
	case ui.KeyUp:
		return "Up", false
	case ui.KeyDown:
		return "Down", false
	case ui.KeyLeft:
		return "Left", false
	case ui.KeyRight:
		return "Right", false
	case ui.KeyMinus:
		return "-", false
	case ui.KeyEqual:
		return "=", false
	case ui.KeyComma:
		return ",", false
	case ui.KeyPeriod:
		return ".", false
	case ui.KeySlash:
		return "/", false
	case ui.KeySemicolon:
		return ";", false
	case ui.KeyQuote:
		return "'", false
	case ui.KeyBracketLeft:
		return "[", false
	case ui.KeyBracketRight:
		return "]", false
	case ui.KeyBackslash:
		return "\\", false
	case ui.KeyBackquote:
		return "`", false
	}
	return "", false
}
