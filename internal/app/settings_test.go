package app

import (
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
