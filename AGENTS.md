# AGENTS.md — Koma

> Guidance for coding agents working in **Koma** (「コマ」— a single frame), a native Windows screenshot and annotation desktop app built with pure Go and **MyGo v0.10.0** (Direct3D 11 native UI, zero CGO, zero WebView/Electron).

---

## 1. Brand, Positioning & Design Principles

**Koma** is designed to be **lightweight, quiet, and deeply aesthetic** — a liquid-glass optical instrument on Windows rather than a bloated utility:

- **Brand identity**:
  - Name: **Koma** (`app.koma.capture`, Go module `koma`).
  - Tagline: *Frame the moment, effortlessly.* / *一帧清光，恰如其分。*
  - Palette: Tahoe Sapphire (`#007AFF` → `#2ED3FF` → `#383EE0`) paired with adaptive light/dark Liquid Glass materials.
  - Export naming: `Koma_YYYYMMDD_HHMMSS.png`.
- **Interaction model (QQ截图-style)**: Zero window switching for everyday captures — press `Ctrl+Shift+A` to freeze all monitors in place, auto-snap windows via DWM, annotate directly on the overlay with a floating double-tier toolbar, double-click selection (or `Enter`) to copy & dismiss, right-click to re-select or exit.
- **Visual language (Apple Liquid Glass + Solar Icons)**:
  - Physical optical Liquid Glass GPU shader (`internal/glasswork/glasswork.go`) synthesized from `bilalgursen/liquid-glass` (120-unit curved dome height field, 3D surface normals, meniscus refraction, chromatic dispersion, rim color mirroring, warm bottom caustic) and `kubai087/glasswork` (5×3 background luminance adaptation with WCAG 4.5:1 contrast floor).
  - Physical spring motion (`internal/glasswork/motion.go`): out-of-phase liquid droplet hover wobble, gel-pull rubberband stretch on primary CTAs, sliding tinted selection lenses, asymmetric internal press glow, and SwiftUI-style rolling numeric text.
  - Official **Solar Icons** (CC BY 4.0) paired as `Line Duotone` (idle) ↔ `Bold Duotone` (active/pressed/CTA).
- **UI / UX / AX discipline**:
  - **Avoid unnecessary secondary labels and descriptions — let the layout do the explaining.** Never add instructional banners, footer hint strips, or empty contextual bars that only display helper text.
  - Preserve full accessibility and discoverability through `.Role(ui.RoleButton).Label(...)` and concise `.Tooltip(...)` on every interactive element, slider, swatch, and card.
  - Keep features focused and practical (do not re-introduce decorative bloat such as Beautify backdrops/window frames).
  - Never animate keyboard-initiated actions; respect `ReduceMotion` and `ReduceTransparency`.

---

## 2. Tech Stack & Commands

- **Language / Runtime**: Go `1.27.4` (`windows/amd64`), `CGO_ENABLED=0`, pure Win32 syscall bindings.
- **UI Framework**: `github.com/egoist/mygo v0.10.0` (CLI invoked via `go tool mygo`).
- **Config**: `mygo.json` (`name: "Koma"`, `identifier: "app.koma.capture"`, `windows.console: false`, outputs to `build/windows-amd64/`).

MyGO doc: https://mygo.run

### Common Commands (PowerShell, run from repo root)

```powershell
# Run full unit & headless UI click/layout regression tests
go test -v ./...

# Run tests AND dump rendered PNG previews for visual QA
$env:KOMA_PREVIEW_DIR = ".\scratch\previews"
go test -v -count=1 -run TestKomaGlassworkAndToolbarClicks .

# Regenerate resources/icon.png from procedural icons.AppIconPNG(512)
$env:KOMA_GEN_ICON = "1"
go test -run TestKomaGlassworkAndToolbarClicks .

# Build Koma.exe + NSIS setup installer
# NOTE: Koma stays resident in the Windows system tray when closed, which locks
# build\windows-amd64\Koma.exe during mygo's output directory swap. Always stop
# any running instance before building:
Get-Process -Name Koma -ErrorAction SilentlyContinue | Stop-Process -Force
go tool mygo build
```

Build outputs:
- `build/windows-amd64/Koma.exe` (standalone native executable)
- `build/windows-amd64/koma-setup-<version>-amd64.exe` (Windows installer)

---

## 3. Repository Structure

```text
koma/
├── main.go                       # Entry point: app.New().Run()
├── main_test.go                  # Headless ui.Tester click tests, fullscreen toolbar bounds tests, preview dumper
├── mygo.json                     # MyGo bundle & Windows metadata (Koma / app.koma.capture)
├── README.md                     # Brand overview, features, shortcuts, and build guide
├── resources/icon.png            # Packaged 512×512 Liquid Glass + Solar viewfinder icon
└── internal/
    ├── app/
    │   ├── app.go                # App lifecycle, system tray, global shortcuts, compressed PNG history, theme sync, GC reclaim
    │   ├── overlay.go            # Per-monitor DPI fullscreen frozen capture overlay, QQ in-place tools, loupe, ComputeToolbarLayout
    │   ├── scroll.go             # 3-window non-activating live scrolling capture (frame, preview, bar), 25fps capture loop, wheel forwarding
    │   ├── editor.go             # Minimalist Home stage & Studio editor, floating bottom dock, history drawer, help sheet
    │   ├── pin.go                # Always-on-top frameless desktop Pin windows (scroll zoom / tall scroll, Alt+scroll opacity)
    │   └── sample.go             # Synthetic dashboard canvas & demo annotations used by tests
    ├── canvas/
    │   ├── types.go              # Tool enum, Annotation model, Snapshot, Apple HIG palette
    │   ├── painter.go            # Live GPU vector annotation rendering inside ui.Painter
    │   ├── render.go             # Export compositor: 3-pass separable blur, pixelate, spotlight, SVG CJK/Unicode rasterization
    │   └── render_test.go        # Composite & PNG encoding tests
    ├── stitch/
    │   ├── stitcher.go           # Zero-alloc incremental vertical long-screenshot stitcher (sticky header/footer/scrollbar resilience, 16,000px cap)
    │   └── stitcher_test.go      # Exact shift, sticky bands, upward/too-fast, and preview rendering tests
    ├── glasswork/
    │   ├── glasswork.go          # Physical Liquid Glass Kage GPU shader (PresetLens, PresetCard, PresetTinted) + BuildMaterial
    │   ├── motion.go             # Bilalgursen spring physics, ApplyLiquidButton, SlidingLens, flicker-free panel transitions
    │   ├── adapt.go              # 5×3 background luminance sampler, PickKind hysteresis, WCAG 4.5:1 ink contrast floor
    │   ├── adapt_test.go         # Light/dark adaptation unit tests
    │   └── motion_test.go        # Spring convergence, rubberband stretch volume conservation, transition invariants
    ├── icons/
    │   └── icons.go              # Solar Icons IconPair{Idle: Line Duotone, Active: Bold Duotone} + procedural AppIconPNG
    └── winapi/
        ├── capture_windows.go    # Pure-Go GDI BitBlt multi-monitor capture, EnumDisplayMonitors, Per-Monitor DPI v2
        ├── scroll_windows.go     # Reusable GDI ScreenGrabber, ActivateWindowAt (Z-order focus handoff), ScrollWindowAt, global Esc check
        ├── capture_other.go      # Non-Windows compile stubs
        ├── windows_windows.go    # DWM visible window enumeration (DWMWA_EXTENDED_FRAME_BOUNDS, cloaked filtering)
        ├── measure.go            # Shottr-style 4-ray perceptual edge-distance pixel ruler
        └── measure_test.go       # Edge ruler & CropRGBA unit tests
```

---

## 4. Architecture & Hard-Won Invariants

Read these rules carefully before modifying UI, shaders, overlays, or lifecycle code. Every item below prevents a real bug that previously occurred in this repo.

### 4.1 Input & Hit-Testing Isolation (`overlay.go`, `pin.go`, `editor.go`)
- **Overlay surface & floating toolbars MUST be siblings**, never parent/child, inside a root `ui.Box(c).Fill()`. If a toolbar is placed inside `surface.Children(...)`, `surface.HandleInput` consumes `InputPointerDown` and resets/unmounts the toolbar before button `.Clicked()` can fire.
- Call `bar.Clicked()` on every floating glass bar (`qq-main-bar`, `qq-sub-bar`, `leftCap`, `rightCap`, `dock`, `studio-sub-pill`, `pin-action-bar`) to absorb stray clicks on padding/gaps so they never leak to the canvas or drag strip underneath.
- In `editor.go`, the top `DragWindow()` strip must sit **before** (underneath) the top floating glass capsules so empty center space drags the window while capsule buttons capture clicks cleanly.
- **Never call `.RightClicked()` or `.Clicked()` on a `.DragWindow()` element** (`pin.go`, `ocr.go`, `editor.go`): MyGo's `RightClicked()` sets `flagClickable`, which blocks `DragWindow()`'s native `startDrag()` check (`flags&(interactive&^flagDragWindow) == 0`). Handle right-click inside `.HandleInput` (`ev.Button == 1`) while returning `false` on left-button presses so native window dragging stays intact.

### 4.2 Fullscreen Overlay Toolbar Containment (`overlay.go`)
- `ComputeToolbarLayout` positions the QQ toolbar in priority order: (1) below selection, (2) above selection, (3) docked inside the bottom of the selection (for near-fullscreen / 100% fullscreen selections).
- Always pass **measured widths** (`p.measuredMainW`, `p.measuredSubW` from `Bounds().W`, with conservative frame-0 fallbacks) and clamp both `barX`/`subX` and `barY`/`subY` independently inside `16px` viewport margins. Call `c.Invalidate()` when `measuredMainW`/`measuredSubW` is `0` on frame 0 or updates, so the toolbar settles to its exact position before the first click even under `ReduceMotion`. Never hardcode a smaller estimated width than the real toolbar width or the rightmost `完成` button will clip off-screen.

### 4.3 Liquid Glass Shader & D3D11 Motion Rules (`internal/glasswork/`)
- **No chalky white border rings**: `PresetLens` (toolbars/capsules) and `PresetTinted` (primary CTAs) MUST keep `borderAlpha = 0`, `borderW = 0`, and **no** outer `p.Stroke`. Only `PresetCard` (drawers/sheets, which sets `rimStrength = 0`) uses a subtle `1px` inset hairline.
- **Studio rim lighting**: Keep `studio := right*right*fall*0.90 + down*down*0.20 + left*left*0.55` (left/right curved end glints only — **do not** add an `up` term or every top edge turns into a bright white stripe).
- **No hover-enter top-edge flash**: Cursor rim sheen (`LightPos`) is localized around the pointer with Gaussian falloff. On the first frame `isHovered` becomes `true` (while `LightMix == 0`), `BuildMaterial` MUST call `e.Jump(lightX/Y, px/py)` rather than animating from a resting offset, and keep `lastLX/lastLY` parked while `LightMix` fades out on leave.
- **No D3D11 backdrop flicker**: Never set `Blur > 0` in `ui.Motion` (`Enter`/`Exit`) on elements backed by a `BackdropBlur` shader; on Windows D3D11 that switches between offscreen blur and live backdrop sampling and flickers visibly. Use `ToolbarAppearTransition` (critically damped `SpringPanel`, `ζ = 1.0`, no parent scale) for overlay toolbars, and skip `ApplyLiquidButton` transforms on frame 0 (`b.W <= 1 || b.H <= 1`).
- **Sliding lenses (`SlidingLens` / `SlidingSoftLens` / `SlidingRingLens`)**: Always keep `.Absolute().Fill().PassThrough().ZIndex(-1)` and **omit `Fly: true`**. In MyGo, `Absolute()` children paint over flow children unless `ZIndex(-1)` is set, and `Fly: true` hoists the element above sibling icons during flight.
- **No mid-transition CJK vertical wrapping**: `MorphBarTransition` must NOT set `Size: true` on single-line floating pills; keep short chips and labels `.SingleLine().Shrink(0)`.
- **MyGo `SpringOptions` panic guard**: Never specify both `Duration`/`Bounce` and `Mass`/`Stiffness`/`Damping` in the same `ui.SpringOptions` struct (MyGo panics at runtime).

### 4.4 Windows Lifecycle & Native Theme Contrast (`internal/app/app.go`, `editor.go`)
- **System tray quit**: `mygo.App.Quit()` triggers `OnClose` on every open window (including hidden windows), and any `e.PreventDefault()` cancels quitting. `editorWin.OnClose` MUST check `!a.quitting` before hiding to tray, and both tray Quit (`a.Quit()`) and `mygo.App.OnBeforeQuit` must set `a.quitting = true`.
- **Native caption buttons (Min / Max / Close)**: Always call `a.SyncWindowTheme()` (`mygo.Theme.SetSource` + `w.SetWindowButtonsTheme`) on window creation and whenever toggling dark/light mode so Windows caption glyphs stay dark on light mode and white on dark mode even when the Windows OS theme differs. Do not combine `VibrancyMica` with custom full-bleed painted backgrounds.
- **Transparent MyGo windows (`scroll.go`)**: `c.SetTheme(...)` unconditionally sets `c.Root()` to an opaque background (`#18181B` / `#F5F5F7`) every frame. Every `Transparent: true` window MUST call `c.Root().Background(ui.Transparent)` immediately after `c.SetTheme(...)`, or it renders as an opaque black/white screen. For live scrolling capture, use three compact `Hidden: true` + `ShowInactive()` + `SetContentProtection(true)` windows (`frameWin` with permanent `IgnoreMouseEvents: true`, `previewWin`, `barWin`) and hand foreground focus via `winapi.ActivateWindowAt` before closing the frozen overlay.
- **Seamless gradients**: When calling `pt.FillRadialGradient`, always draw over the full canvas rect `r` with normalized `Center` (`0..1`) and generous `Radius` to avoid rectangular clip seams.

### 4.5 Memory Management (`app.go`, `overlay.go`, `editor.go`, `pin.go`)
- See [docs/performance.md](docs/performance.md) for Koma's overarching performance & memory engineering principles (4-layer memory model, window-as-resource-scope, image cold/hot tiering, render hot-path hygiene, and OS/FFI boundary rules).
- **History**: Store screenshots as compressed `PNG []byte` (capped at `maxHistoryEntries = 12` and `maxHistoryBytes = 24 MiB`) and decode on demand via `HistoryEntry.DecodeImage()`. Never store raw uncompressed `*image.RGBA` in history.
- **Undo stack**: Share the immutable `e.baseImg` pointer across annotation undo snapshots (`pushUndo`); only `applyCrop` allocates a new `*image.RGBA`. Keep undo stacks capped at `24`.
- **Release on close**: Clear `bmp`, `pixBmp`, `selEffectBmp`, `capture.Image`, `img` references in `overlaySession.closeAll()`, `PinWindow` `OnClosed`, and `EditorState.ClearDocument()`, followed by `reclaimMemoryAsync()` (`runtime.GC()` + `debug.FreeOSMemory()`).

### 4.6 Iconography (`internal/icons/icons.go`)
- All icons are **Solar Icons** (CC BY 4.0) wrapped as `IconPair{Idle: Line Duotone, Active: Bold Duotone}` on a `24×24` viewBox with `currentColor` and `opacity=".42–.5"` duotone layers.
- `Line Duotone` stroke widths are optically lifted from `1.5` to `1.72` for legibility at `16.5–17.5px` over translucent Liquid Glass; strip trivial full-canvas `<clipPath>` wrappers when adding new Solar SVGs.
- Use `ic.For(active || pressed)` + `.ContentTransition(ui.ContentTransition{})` on stateful controls, and `.Active` on prominent blue CTAs (`区域截图`, `复制`, `完成`) and the theme switcher (`Moon` ↔ `Sun`).
- Procedural `AppIconPNG` MUST encode with `image.NewNRGBA` (straight alpha) — never write straight RGB into premultiplied `image.NewRGBA` or `png.Encode` overflows anti-aliased edge channels.

### 4.7 MyGo & Win32 Specifics
- Keep `CGO_ENABLED=0` — all Win32 calls live in `internal/winapi/*_windows.go` via `syscall.NewLazyDLL`.
- For negative Win32 index constants like `GWL_EXSTYLE = -20`, convert through `int32` before casting to `uintptr` to avoid constant overflow.
- `mygo.Clipboard.WriteText` returns nothing; `mygo.Clipboard.WriteImage` returns `error`.

### 4.8 Headless UI Testing (`main_test.go`, `*_test.go`)
- **Avoid 20-frame CPU shader stalls**: MyGo's `ui.Tester` runs `settle()` (up to 20 frames at `dt = 0`) on every `NewTester`, `Click`, `Key`, and `Type` while any spring animation is active, software-rasterizing Liquid Glass Kage shaders on CPU-only CI runners.
- **Interaction & layout regression tests** (`main_test.go`, `internal/app/v030_test.go`, `settings_test.go`, `scroll_test.go`) should default to `ui.NewTesterWith(..., ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true, ReduceTransparency: true}})` and guard preview-only `tt.Advance(...)` calls under `if dumpDir != ""`. When `KOMA_PREVIEW_DIR` is set for visual QA, `main_test.go` switches back to full motion + Liquid Glass shaders automatically.
- **Keep real shader & motion smoke coverage**: Preserve `TestLiquidGlassShadersRenderSmoke` (`ReduceTransparency: false, ReduceMotion: true` in `main_test.go`) and the real liquid open/stretch/retract assertions in `TestGlassSelectClickKeyboardAndEscape` (`internal/glasswork/select_test.go`) so Kage shader compilation, uniforms, and spring transitions stay verified on every CI run.

---

## 5. Verification Checklist Before Finishing Changes

1. Run `go test -v -count=1 ./...` and ensure all packages (`koma`, `app`, `canvas`, `stitch`, `glasswork`, `winapi`) pass.
2. When modifying UI, shaders, icons, or layout, dump previews via `KOMA_PREVIEW_DIR` and visually inspect the rendered PNGs (`Home`, `QQ overlay`, `100% fullscreen overlay`, `Studio`, `Scroll overlay`) to check contrast, alignment, icon z-order, and absence of border/clip artifacts.
3. Stop any background `Koma` tray process and run `go tool mygo build` to confirm both `Koma.exe` and `koma-setup-<version>-amd64.exe` compile cleanly.
