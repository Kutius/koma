package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/egoist/mygo/plugins/updater/native"

	"koma/internal/icons"
)

const defaultAppVersion = "0.2.1"

// setupUpdater registers MyGo's native (zero-WebView) updater plugin and keeps
// Koma's tray menu and Studio settings synchronized with background checks.
func (a *App) setupUpdater() {
	mygo.Use(native.New(updater.Options{
		Language: "zh-Hans",
		Icon:     icons.AppIconPNG(128),
	}))
	a.mu.Lock()
	a.updaterReady = true
	a.mu.Unlock()

	updater.OnChange(func() {
		a.SyncUpdaterState()
		a.refreshTrayMenu()
		a.mu.Lock()
		w := a.editorWin
		a.mu.Unlock()
		if w != nil {
			w.Update(func() {
				a.editor.SyncUpdaterState()
			})
		}
	})
}

// SyncUpdaterState pulls the latest persisted updater preferences and available
// version from MyGo's updater plugin into App's cached fields.
func (a *App) SyncUpdaterState() {
	a.mu.Lock()
	ready := a.updaterReady
	a.mu.Unlock()
	if !ready {
		return
	}
	auto := updater.AutomaticChecks()
	avail := strings.TrimPrefix(strings.TrimSpace(updater.AvailableVersion()), "v")
	last := updater.LastCheck()

	a.mu.Lock()
	a.autoCheckUpdates = auto
	a.availableUpdate = avail
	a.lastUpdateCheck = last
	a.mu.Unlock()
}

// AppVersion returns the running Koma semantic version without a leading "v".
func (a *App) AppVersion() string {
	v := strings.TrimPrefix(strings.TrimSpace(mygo.App.Version()), "v")
	if v == "" || v == "0.0.0" {
		return defaultAppVersion
	}
	return v
}

// AutomaticUpdateChecks reports whether Koma checks for updates in the background.
func (a *App) AutomaticUpdateChecks() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.autoCheckUpdates
}

// SetAutomaticUpdateChecks enables or disables daily background update checks.
func (a *App) SetAutomaticUpdateChecks(on bool) {
	a.mu.Lock()
	a.autoCheckUpdates = on
	ready := a.updaterReady
	a.mu.Unlock()
	if ready {
		updater.SetAutomaticChecks(on)
	}
}

// AvailableUpdate returns the newer version discovered by the updater ("" if up to date).
func (a *App) AvailableUpdate() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.availableUpdate
}

// LastUpdateCheck returns when Koma last checked for updates successfully.
func (a *App) LastUpdateCheck() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastUpdateCheck
}

// CheckForUpdates opens MyGo's native update window and checks for a newer release.
func (a *App) CheckForUpdates() {
	a.mu.Lock()
	hook := a.checkUpdatesHook
	ready := a.updaterReady
	a.mu.Unlock()

	if hook != nil {
		hook()
		return
	}
	if ready {
		updater.CheckForUpdates()
		return
	}
	a.NotifyToast("当前构建未启用自动更新")
}

func (a *App) trayUpdateLabel() string {
	if v := a.AvailableUpdate(); v != "" {
		return fmt.Sprintf("发现新版本 v%s…", v)
	}
	return "检查更新…"
}

// SetTestUpdateState configures updater UI state and a click callback for headless tests.
func (a *App) SetTestUpdateState(available string, onCheck func()) {
	a.mu.Lock()
	a.availableUpdate = strings.TrimPrefix(strings.TrimSpace(available), "v")
	a.checkUpdatesHook = onCheck
	a.mu.Unlock()
}
