package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSettingsPersistenceAndClearedHotkeys(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "settings.json")

	a := New()
	a.SetSettingsPath(cfgPath)

	// Modify area hotkey, clear pin hotkey, toggle dark mode
	if err := a.SetHotkey(HotkeyArea, "Alt+Q"); err != nil {
		t.Fatalf("SetHotkey(area, Alt+Q) failed: %v", err)
	}
	if err := a.SetHotkey(HotkeyPin, ""); err != nil {
		t.Fatalf("SetHotkey(pin, empty) failed: %v", err)
	}
	a.SetDarkMode(true)

	// Duplicate conflict must be rejected
	if err := a.SetHotkey(HotkeyFull, "Alt+Q"); err == nil {
		t.Fatalf("expected conflict error when assigning duplicate Alt+Q")
	}

	// Reload into a fresh App instance and verify cleared "" stays disabled!
	b := New()
	b.SetSettingsPath(cfgPath)
	if err := b.LoadSettings(); err != nil {
		t.Fatalf("LoadSettings failed: %v", err)
	}
	if got := b.Hotkey(HotkeyArea); got != "Alt+Q" {
		t.Fatalf("reloaded HotkeyArea = %q, want Alt+Q", got)
	}
	if got := b.Hotkey(HotkeyPin); got != "" {
		t.Fatalf("reloaded cleared HotkeyPin = %q, want empty string", got)
	}
	if got := b.Hotkey(HotkeyFull); got != DefaultHotkeyFull {
		t.Fatalf("reloaded HotkeyFull = %q, want %q", got, DefaultHotkeyFull)
	}
	if !b.darkMode {
		t.Fatalf("expected reloaded darkMode = true")
	}

	// Reset defaults restores all 4 shortcuts
	if err := b.ResetDefaultHotkeys(); err != nil {
		t.Fatalf("ResetDefaultHotkeys failed: %v", err)
	}
	if b.Hotkey(HotkeyArea) != DefaultHotkeyArea || b.Hotkey(HotkeyPin) != DefaultHotkeyPin {
		t.Fatalf("defaults not restored: area=%q pin=%q", b.Hotkey(HotkeyArea), b.Hotkey(HotkeyPin))
	}
}

func TestParseHotkeyInput(t *testing.T) {
	// Valid modifier + letter
	acc, clear, cancel, ignored, errMsg := ParseHotkeyInput(ui.InputEvent{
		Kind: ui.InputKeyDown, Key: ui.KeyQ, Mods: ui.Ctrl | ui.Alt,
	})
	if acc != "Ctrl+Alt+Q" || clear || cancel || ignored || errMsg != "" {
		t.Fatalf("Ctrl+Alt+Q got acc=%q clear=%v cancel=%v ignored=%v err=%q", acc, clear, cancel, ignored, errMsg)
	}

	// Standalone F4 allowed
	acc, _, _, ignored, errMsg = ParseHotkeyInput(ui.InputEvent{
		Kind: ui.InputKeyDown, Key: ui.KeyF4,
	})
	if acc != "F4" || ignored || errMsg != "" {
		t.Fatalf("F4 got acc=%q ignored=%v err=%q", acc, ignored, errMsg)
	}

	// Standalone PrintScreen allowed via physical code
	acc, _, _, ignored, errMsg = ParseHotkeyInput(ui.InputEvent{
		Kind: ui.InputKeyDown, Code: ui.CodePrintScreen,
	})
	if acc != "PrintScreen" || ignored || errMsg != "" {
		t.Fatalf("PrintScreen got acc=%q ignored=%v err=%q", acc, ignored, errMsg)
	}

	// Bare letter rejected
	_, _, _, _, errMsg = ParseHotkeyInput(ui.InputEvent{
		Kind: ui.InputKeyDown, Key: ui.KeyA,
	})
	if errMsg == "" {
		t.Fatalf("expected error for bare letter key A")
	}

	// Bare Shift+A rejected
	_, _, _, _, errMsg = ParseHotkeyInput(ui.InputEvent{
		Kind: ui.InputKeyDown, Key: ui.KeyA, Mods: ui.Shift,
	})
	if errMsg == "" {
		t.Fatalf("expected error for Shift+A without Ctrl/Alt/Win")
	}

	// Bare Backspace clears shortcut
	_, clear, _, _, _ = ParseHotkeyInput(ui.InputEvent{
		Kind: ui.InputKeyDown, Key: ui.KeyBackspace,
	})
	if !clear {
		t.Fatalf("expected Backspace to request clear")
	}

	// Bare Escape cancels recording
	_, _, cancel, _, _ = ParseHotkeyInput(ui.InputEvent{
		Kind: ui.InputKeyDown, Key: ui.KeyEscape,
	})
	if !cancel {
		t.Fatalf("expected Escape to request cancel")
	}

	// Standalone modifier ignored
	_, _, _, ignored, _ = ParseHotkeyInput(ui.InputEvent{
		Kind: ui.InputKeyDown, Code: ui.CodeControlLeft, Mods: ui.Ctrl,
	})
	if !ignored {
		t.Fatalf("expected standalone Ctrl press to be ignored")
	}
}

func TestOpenAtLoginStateHeadless(t *testing.T) {
	a := New()
	if a.OpenAtLogin() {
		t.Fatalf("expected OpenAtLogin initially false")
	}
	if err := a.SetOpenAtLogin(true); err != nil {
		t.Fatalf("SetOpenAtLogin(true) failed: %v", err)
	}
	if !a.OpenAtLogin() {
		t.Fatalf("expected OpenAtLogin true after enable")
	}
	if err := a.SetOpenAtLogin(false); err != nil {
		t.Fatalf("SetOpenAtLogin(false) failed: %v", err)
	}
	if a.OpenAtLogin() {
		t.Fatalf("expected OpenAtLogin false after disable")
	}
}

func TestUpdaterStateAndSettingsControlsHeadless(t *testing.T) {
	a := New()
	if v := a.AppVersion(); v == "" {
		t.Fatalf("expected non-empty AppVersion")
	}
	if !a.AutomaticUpdateChecks() {
		t.Fatalf("expected AutomaticUpdateChecks initially true")
	}
	if got := a.trayUpdateLabel(); got != "检查更新…" {
		t.Fatalf("initial trayUpdateLabel = %q, want 检查更新…", got)
	}

	checks := 0
	a.SetTestUpdateState("", func() { checks++ })

	ed := NewEditorState(a)
	ed.OpenSettings()
	tt := ui.NewTesterWith(ed.View, 1220, 780, ui.TesterOptions{
		Preferences: ui.Preferences{ReduceMotion: true, ReduceTransparency: true},
	})

	if !tt.HasText("v" + a.AppVersion()) {
		t.Fatalf("expected current version v%s in settings sheet, got %q", a.AppVersion(), tt.Texts())
	}
	if err := tt.Click("自动检查更新"); err != nil {
		t.Fatalf("click 自动检查更新 switch failed: %v", err)
	}
	if a.AutomaticUpdateChecks() {
		t.Fatalf("expected AutomaticUpdateChecks() == false after toggling off")
	}
	if err := tt.Click("检查更新"); err != nil {
		t.Fatalf("click 检查更新 failed: %v", err)
	}
	if checks != 1 {
		t.Fatalf("expected CheckForUpdates callback count = 1, got %d", checks)
	}

	// Simulate an available update discovered by a background check
	a.SetTestUpdateState("v0.3.0", func() { checks++ })
	if got := a.AvailableUpdate(); got != "0.3.0" {
		t.Fatalf("AvailableUpdate = %q, want 0.3.0", got)
	}
	if got := a.trayUpdateLabel(); got != "发现新版本 v0.3.0…" {
		t.Fatalf("trayUpdateLabel with update = %q, want 发现新版本 v0.3.0…", got)
	}
	tt.Frame()
	if !tt.HasText("新版本 v0.3.0") {
		t.Fatalf("expected 新版本 v0.3.0 badge in settings sheet, got %q", tt.Texts())
	}
	if err := tt.Click("立即更新"); err != nil {
		t.Fatalf("click 立即更新 failed: %v", err)
	}
	if checks != 2 {
		t.Fatalf("expected CheckForUpdates callback count = 2, got %d", checks)
	}
}

func TestSettingsSelectControlsHeadless(t *testing.T) {
	a := New()
	a.SetSettingsPath(filepath.Join(t.TempDir(), "settings.json"))
	a.SetOCRDataDir(t.TempDir())

	ed := NewEditorState(a)
	ed.OpenSettings()
	tt := ui.NewTesterWith(ed.View, 1220, 780, ui.TesterOptions{
		Preferences: ui.Preferences{ReduceMotion: true, ReduceTransparency: true},
	})

	// 1. Appearance Select: defaults to 跟随系统, open and switch 跟随系统 -> 深色 -> 跟随系统
	if got := a.ThemeMode(); got != ThemeModeSystem {
		t.Fatalf("expected initial ThemeMode == ThemeModeSystem, got %q", got)
	}
	if err := tt.Click("选择外观"); err != nil {
		t.Fatalf("click 选择外观 failed: %v", err)
	}
	if !tt.HasText("跟随系统") || !tt.HasText("浅色") || !tt.HasText("深色") {
		t.Fatalf("expected 跟随系统/浅色/深色 options after opening 外观 select, got %q", tt.Texts())
	}
	if err := tt.Click("深色"); err != nil {
		t.Fatalf("click 深色 option failed: %v", err)
	}
	if a.ThemeMode() != ThemeModeDark || !a.IsDarkMode() {
		t.Fatalf("expected ThemeModeDark / dark=true after selecting 深色, got mode=%q dark=%v", a.ThemeMode(), a.IsDarkMode())
	}

	// Switch back to 跟随系统 and verify OS theme changes propagate live
	if err := tt.Click("选择外观"); err != nil {
		t.Fatalf("re-open 选择外观 failed: %v", err)
	}
	if err := tt.Click("跟随系统"); err != nil {
		t.Fatalf("click 跟随系统 option failed: %v", err)
	}
	if a.ThemeMode() != ThemeModeSystem || a.IsDarkMode() {
		t.Fatalf("expected ThemeModeSystem / light when systemDark=false, got mode=%q dark=%v", a.ThemeMode(), a.IsDarkMode())
	}
	a.SetSystemDarkForTest(true)
	if !a.IsDarkMode() {
		t.Fatalf("expected IsDarkMode() == true when system switches dark in ThemeModeSystem")
	}
	a.SetSystemDarkForTest(false)
	if a.IsDarkMode() {
		t.Fatalf("expected IsDarkMode() == false when system switches light in ThemeModeSystem")
	}

	// 2. Escape closes ONLY an open Select popup, keeping the Settings sheet open
	if err := tt.Click("选择外观"); err != nil {
		t.Fatalf("re-open 选择外观 failed: %v", err)
	}
	if !ed.anySettingsSelectOpen() {
		t.Fatalf("expected anySettingsSelectOpen() == true")
	}
	tt.Key(0, ui.KeyEscape)
	if ed.anySettingsSelectOpen() {
		t.Fatalf("expected Escape to close open Select popup")
	}
	if !ed.SettingsOpen() {
		t.Fatalf("expected Settings sheet to stay open after Escape dismissed Select popup")
	}

	// 3. Default Format Select: switch PNG -> JPEG
	if err := tt.Click("选择默认格式"); err != nil {
		t.Fatalf("click 选择默认格式 failed: %v", err)
	}
	if err := tt.Click("JPEG"); err != nil {
		t.Fatalf("click JPEG option failed: %v", err)
	}
	if got := a.SaveFormat(); got != SaveFormatJPEG {
		t.Fatalf("SaveFormat() = %q, want %q", got, SaveFormatJPEG)
	}

	// 4. OCR Engine Select: opens and lists 系统原生 + PP-OCRv6 (with 需下载 badge when uninstalled)
	if err := tt.Click("选择识别引擎"); err != nil {
		t.Fatalf("click 选择识别引擎 failed: %v", err)
	}
	if !tt.HasText("需下载") {
		t.Fatalf("expected 需下载 badge for uninstalled PP-OCRv6 option, got %q", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)

	// Second Escape (no select open) closes the Settings sheet
	tt.Key(0, ui.KeyEscape)
	if ed.SettingsOpen() {
		t.Fatalf("expected Escape with no open select to close Settings sheet")
	}
}

func TestThemeModeSystemPersistenceAndLegacyMigration(t *testing.T) {
	dir := t.TempDir()

	// 1. Persisting ThemeModeSystem round-trips cleanly and follows OS theme
	cfgSystem := filepath.Join(dir, "system.json")
	a := New()
	a.SetSettingsPath(cfgSystem)
	a.SetThemeMode(ThemeModeSystem)

	b := New()
	b.SetSettingsPath(cfgSystem)
	if err := b.LoadSettings(); err != nil {
		t.Fatalf("LoadSettings(system): %v", err)
	}
	if b.ThemeMode() != ThemeModeSystem {
		t.Fatalf("reloaded ThemeMode = %q, want system", b.ThemeMode())
	}
	b.SetSystemDarkForTest(true)
	if !b.IsDarkMode() {
		t.Fatalf("expected ThemeModeSystem to follow system dark=true")
	}
	// Quick toggle switches to explicit opposite mode and stops following system
	b.ToggleDarkMode()
	if b.ThemeMode() != ThemeModeLight || b.IsDarkMode() {
		t.Fatalf("after ToggleDarkMode from system-dark: mode=%q dark=%v, want light/false", b.ThemeMode(), b.IsDarkMode())
	}
	b.SetSystemDarkForTest(true)
	if b.IsDarkMode() {
		t.Fatalf("explicit ThemeModeLight must ignore system dark changes")
	}

	// 2. Legacy settings.json with only dark_mode migrates cleanly to light/dark
	cfgLegacy := filepath.Join(dir, "legacy.json")
	if err := os.WriteFile(cfgLegacy, []byte(`{"dark_mode": true}`), 0o644); err != nil {
		t.Fatalf("write legacy settings: %v", err)
	}
	c := New()
	c.SetSettingsPath(cfgLegacy)
	if err := c.LoadSettings(); err != nil {
		t.Fatalf("LoadSettings(legacy): %v", err)
	}
	if c.ThemeMode() != ThemeModeDark || !c.IsDarkMode() {
		t.Fatalf("legacy dark_mode=true migrated to mode=%q dark=%v, want dark/true", c.ThemeMode(), c.IsDarkMode())
	}
}
