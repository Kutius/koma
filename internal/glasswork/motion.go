package glasswork

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"
)

// Physical spring presets ported directly from bilalgursen/liquid-glass (engine.ts & physics.ts).
// Conversion from angular frequency (omega, rad/s) & damping ratio (zeta) with Mass=1:
//   Stiffness = omega^2,  Damping = 2 * zeta * omega
var (
	// SpringPress: press & release liquid bounce (omega=16, zeta=0.6).
	SpringPress = ui.Animation{
		Spring: ui.SpringOptions{Mass: 1, Stiffness: 256, Damping: 19.2},
	}

	// SpringHoverX & SpringHoverY: out-of-phase hover swell springs (omega=17/zeta=0.42 vs omega=12/zeta=0.34).
	// Width and height overshoot out of phase so the glass wobbles subtly like a liquid droplet.
	SpringHoverX = ui.Animation{
		Spring: ui.SpringOptions{Mass: 1, Stiffness: 289, Damping: 14.28},
	}
	SpringHoverY = ui.Animation{
		Spring: ui.SpringOptions{Mass: 1, Stiffness: 144, Damping: 8.16},
	}

	// SpringSlide: sliding liquid glass selection lens gliding across toolbar tools & chips (omega=14, zeta=0.75).
	SpringSlide = ui.Animation{
		Spring: ui.SpringOptions{Mass: 1, Stiffness: 196, Damping: 21.0},
		Oklab:  true,
	}

	// SpringPanel: critically damped calm spring for floating toolbars, drawers & sheets (zeta=1.0, zero overshoot/flicker).
	SpringPanel = ui.Animation{
		Spring: ui.SpringOptions{Mass: 1, Stiffness: 400, Damping: 40.0},
		Oklab:  true,
	}

	// SpringCursor: inertial spring for cursor-leaning rim light (never snaps harshly to raw pointer).
	SpringCursor = ui.Animation{
		Spring: ui.SpringOptions{Mass: 1, Stiffness: 225, Damping: 24.0},
	}

	// SpringGlowRise & SpringGlowFade: asymmetric internal press light from bilalgursen LIGHT:
	// rises crisply with a subtle luminous pop (240ms, bounce 0.2), sinks back calmly (420ms, no bounce).
	SpringGlowRise = ui.Animation{
		Spring:    ui.SpringOptions{Duration: 240 * time.Millisecond, Bounce: 0.2},
		NonMotion: true,
	}
	SpringGlowFade = ui.Animation{
		Spring:    ui.SpringOptions{Duration: 420 * time.Millisecond, Bounce: 0},
		NonMotion: true,
	}
	SpringGlowSpread = ui.Animation{
		Spring:    ui.SpringOptions{Mass: 1, Stiffness: 144, Damping: 24.0},
		NonMotion: true,
	}

	// SpringPullFollow & SpringPullRelease: gel-like pointer pull rubberband & bouncy rebound (PULL in engine.ts).
	SpringPullFollow = ui.Animation{
		Spring: ui.SpringOptions{Duration: 180 * time.Millisecond, Bounce: 0},
	}
	SpringPullRelease = ui.Animation{
		Spring: ui.SpringOptions{Duration: 500 * time.Millisecond, Bounce: 0.42},
	}

	// AnimColorFade: silky Oklab color crossfade for hover/active highlights.
	AnimColorFade = ui.Animation{
		Duration:  150 * time.Millisecond,
		Ease:      ui.EaseOut,
		Oklab:     true,
		NonMotion: true,
	}

	// ContentNumeric: SwiftUI-style rolling digits for zoom %, sliders, and counters.
	ContentNumeric = ui.ContentTransition{
		Numeric: true,
	}

	// ContentNumericDown: downward rolling digits for countdowns.
	ContentNumericDown = ui.ContentTransition{
		Numeric: true,
		Down:    true,
	}
)

// LensMatch identifies a shared sliding liquid-glass selection pill within a scope.
type LensMatch struct {
	Scope string
}

// LiquidButtonOpts tunes liquid droplet wobble and gel-pull stretch on interactive glass controls.
type LiquidButtonOpts struct {
	// HoverScale is the subtle uniform hover swell (default 1.035, capped by MaxGrowPx).
	HoverScale float32
	// PressScale is the tactile scale while held (e.g. 1.055 for liquid swell CTAs, 0.94 for compact icon buttons).
	PressScale float32
	// Wobble adds bilalgursen's out-of-phase X/Y droplet wobble on hover (default 0.025).
	Wobble float32
	// MaxGrowPx caps total pixel expansion so wide buttons never swell excessively (bilalgursen MAX_GROW_PX = 6).
	MaxGrowPx float32
	// Pull enables bilalgursen's rubberband gel-pull & volume-preserving stretch when held and dragged.
	Pull bool
}

// PrimaryButtonOpts returns the signature bilalgursen GlassButton feel:
// out-of-phase droplet hover wobble + gentle liquid swell on press + gel-pull stretch when dragged.
func PrimaryButtonOpts() LiquidButtonOpts {
	return LiquidButtonOpts{
		HoverScale: 1.035,
		PressScale: 1.055,
		Wobble:     0.028,
		MaxGrowPx:  5.5,
		Pull:       true,
	}
}

// CompactButtonOpts returns a crisp, responsive feel for toolbar icon buttons & pills:
// subtle droplet hover wobble + immediate tactile press compression (Emil Kowalski scale 0.94).
func CompactButtonOpts() LiquidButtonOpts {
	return LiquidButtonOpts{
		HoverScale: 1.03,
		PressScale: 0.93,
		Wobble:     0.018,
		MaxGrowPx:  2.5,
		Pull:       false,
	}
}

// CardHoverOpts returns calm physical lift & subtle liquid wobble for interactive thumbnail cards.
func CardHoverOpts() LiquidButtonOpts {
	return LiquidButtonOpts{
		HoverScale: 1.022,
		PressScale: 0.975,
		Wobble:     0.014,
		MaxGrowPx:  4.5,
		Pull:       false,
	}
}

type pullAnchorState struct {
	wasPressed bool
	downX      float32
	downY      float32
}

type liquidKeyX struct{}
type liquidKeyY struct{}
type liquidKeyScale struct{}
type liquidPullX struct{}
type liquidPullY struct{}
type liquidStretchX struct{}
type liquidStretchY struct{}

// computePullStretch translates pointer drag delta into rubberbanded displacement and
// volume-preserving gel stretch (stretches along pull, thins by 1/sqrt(stretch) across).
func computePullStretch(dx, dy, boxW, boxH float32) (tx, ty, sx, sy float32) {
	if boxW < 8 {
		boxW = 8
	}
	if boxH < 8 {
		boxH = 8
	}
	longer := max(boxW, boxH)
	var limit, stretchMax float32
	if longer <= 72 {
		limit = 4.0 + 0.15*longer
		stretchMax = 0.18
	} else {
		limit = min(float32(10.0), 3.0+0.04*longer)
		stretchMax = 0.11
	}

	tx = rubberBandPull(dx, limit)
	ty = rubberBandPull(dy, limit)

	// Volume-preserving anisotropic stretch: pulls along drag axis, thins perpendicularly.
	ex := min(stretchMax, float32(math.Abs(float64(tx)))/boxW*0.55)
	ey := min(stretchMax, float32(math.Abs(float64(ty)))/boxH*0.55)

	sx = (1.0 + ex) / float32(math.Sqrt(float64(1.0+ey)))
	sy = (1.0 + ey) / float32(math.Sqrt(float64(1.0+ex)))
	return tx, ty, sx, sy
}

// rubberBandPull matches bilalgursen engine.ts rubberBand(offset, limit, 0.55):
// follows almost 1:1 for tiny drags, stiffens progressively, and asymptotes at ±limit.
func rubberBandPull(offset, limit float32) float32 {
	if limit <= 0 || offset == 0 {
		return 0
	}
	sign := float32(1)
	a := offset
	if a < 0 {
		sign = -1
		a = -a
	}
	const resistance = 0.55
	return sign * (a * limit * resistance) / (limit + resistance*a)
}

// ApplyLiquidButton attaches bilalgursen/liquid-glass physical motion to any MyGo element:
//   - Out-of-phase X/Y hover swell springs (wobbles softly like a liquid droplet)
//   - Press spring response (instant on pointer-down, bouncy on release)
//   - Optional gel-pull rubberband translation + volume-preserving anisotropic stretch
//
// Safe with .Clicked() — reads pointer state without stealing clicks or installing DragWindow.
func ApplyLiquidButton(e ui.Element, opts LiquidButtonOpts) {
	if opts.MaxGrowPx <= 0 {
		opts.MaxGrowPx = 5.0
	}
	b := e.Bounds()
	// Skip frame-0 transforms before layout has measured the box (prevents mount flicker).
	if b.W <= 1 || b.H <= 1 {
		return
	}
	w := max(float32(24), b.W)
	h := max(float32(20), b.H)

	// Cap growth so wide buttons don't over-expand horizontally (MAX_GROW_PX from GlassLayer).
	maxScaleW := 1.0 + (2.0*opts.MaxGrowPx)/w
	maxScaleH := 1.0 + (2.0*opts.MaxGrowPx)/h
	maxUniform := min(maxScaleW, maxScaleH)

	hovered := e.Hovered()
	pressed := e.Pressed()

	hovTarget := float32(0)
	if hovered {
		hovTarget = 1
	}

	// Out-of-phase hover springs: X settles faster (omega=17) than Y (omega=12),
	// creating the subtle organic liquid droplet wobble on enter & exit.
	wx := e.AnimateTo(liquidKeyX{}, hovTarget, SpringHoverX)
	wy := e.AnimateTo(liquidKeyY{}, hovTarget, SpringHoverY)

	baseTarget := float32(1.0)
	if pressed {
		if opts.PressScale > 1.0 {
			baseTarget = min(opts.PressScale, maxUniform)
		} else if opts.PressScale > 0 {
			baseTarget = opts.PressScale
		}
	} else if hovered && opts.HoverScale > 0 {
		baseTarget = min(opts.HoverScale, maxUniform)
	}
	baseScale := e.AnimateTo(liquidKeyScale{}, baseTarget, SpringPress)

	// Anisotropic droplet wobble around the steady hover state:
	// as wx and wy oscillate out of phase, (wx - wy) briefly stretches X vs Y, then settles to 1.0!
	wobbleX := float32(1.0) + opts.Wobble*(wx-hovTarget)
	wobbleY := float32(1.0) + opts.Wobble*(wy-hovTarget)

	sx := baseScale * clamp32(wobbleX, 0.94, 1.06)
	sy := baseScale * clamp32(wobbleY, 0.94, 1.06)
	var tx, ty float32

	if opts.Pull {
		anchor := ui.Local(e, "liquid-pull-anchor", func() pullAnchorState { return pullAnchorState{} })
		px, py, hasPt := e.PointerPosition()
		var goalTX, goalTY float32
		goalSX, goalSY := float32(1), float32(1)
		anim := SpringPullRelease

		if pressed && hasPt {
			if !anchor.wasPressed {
				anchor.wasPressed = true
				anchor.downX = px
				anchor.downY = py
			}
			goalTX, goalTY, goalSX, goalSY = computePullStretch(px-anchor.downX, py-anchor.downY, w, h)
			anim = SpringPullFollow
		} else {
			anchor.wasPressed = false
		}

		tx = e.AnimateTo(liquidPullX{}, goalTX, anim)
		ty = e.AnimateTo(liquidPullY{}, goalTY, anim)
		pullSX := e.AnimateTo(liquidStretchX{}, goalSX, anim)
		pullSY := e.AnimateTo(liquidStretchY{}, goalSY, anim)
		sx *= pullSX
		sy *= pullSY
	}

	if math.Abs(float64(sx-1)) > 0.0005 || math.Abs(float64(sy-1)) > 0.0005 {
		e.Scale(sx, sy)
	}
	if math.Abs(float64(tx)) > 0.05 || math.Abs(float64(ty)) > 0.05 {
		e.Translate(tx, ty)
	}
}

// PanelTransition returns an asymmetric, flicker-free enter/exit transition for
// floating liquid-glass toolbars, drawers, sheets, and popovers.
// Deliberately avoids element-level Blur on BackdropBlur shader surfaces so D3D11
// never flickers between offscreen blur and live backdrop sampling.
func PanelTransition(enterX, enterY float32) ui.ElementTransition {
	anim := SpringPanel
	return ui.ElementTransition{
		Animation:    &anim,
		ExitDuration: 130 * time.Millisecond,
		ExitEase:     ui.EaseOut,
		Enter: &ui.Motion{
			X:     enterX,
			Y:     enterY,
			Scale: 0.97,
		},
		Exit: &ui.Motion{
			X:     enterX * 0.5,
			Y:     enterY * 0.5,
			Scale: 0.98,
		},
	}
}

// ToolbarAppearTransition is a rock-solid, zero-flicker fade+rise entrance for
// capture overlay floating toolbars (avoids scaling parent bounds so child buttons
// never re-measure or flicker on mount).
func ToolbarAppearTransition(enterY float32) ui.ElementTransition {
	anim := SpringPanel
	return ui.ElementTransition{
		Animation:    &anim,
		ExitDuration: 110 * time.Millisecond,
		ExitEase:     ui.EaseOut,
		Enter: &ui.Motion{
			Y: enterY,
		},
		Exit: &ui.Motion{
			Y: enterY * 0.5,
		},
	}
}

// MorphBarTransition smoothly positions and reveals a floating liquid glass secondary bar
// when contextual tool options change, without cramping/wrapping text or D3D11 blur flicker.
func MorphBarTransition(enterY float32) ui.ElementTransition {
	anim := SpringPanel
	return ui.ElementTransition{
		Position:     true,
		Animation:    &anim,
		ExitDuration: 120 * time.Millisecond,
		ExitEase:     ui.EaseOut,
		Enter: &ui.Motion{
			Y: enterY,
		},
		Exit: &ui.Motion{
			Y: enterY * 0.5,
		},
	}
}

// SlidingLens renders a pass-through active liquid glass pill that physically glides
// between sibling options in the same scope using MyGo's shared-element Match + SpringSlide.
// Stays in-layer (Fly: false) so it glides cleanly BEHIND the active button icon/label!
func SlidingLens(c *ui.Context, scope string, radius float32, accent ui.Color) {
	anim := SpringSlide
	surf := Tinted(accent)
	surf.Shadow = false
	ui.Box(c).
		Absolute().Fill().PassThrough().ZIndex(-1).
		Radius(radius).
		Material(surf).
		Transition(ui.ElementTransition{
			Animation: &anim,
			Match:     LensMatch{Scope: scope},
		})
}

// SlidingSoftLens renders a subtle translucent active highlight pill that glides between
// compact options (e.g. stroke widths, font sizes) without overpowering the content.
func SlidingSoftLens(c *ui.Context, scope string, radius float32, fill ui.Color) {
	SlidingAccentLens(c, scope, radius, fill, ui.Color{})
}

// SlidingAccentLens renders a translucent tinted active pill with an optional crisp optical
// hairline that glides behind sibling icons/chips without obscuring them or needing white glyphs.
func SlidingAccentLens(c *ui.Context, scope string, radius float32, fill, border ui.Color) {
	anim := SpringSlide
	b := ui.Box(c).
		Absolute().Fill().PassThrough().ZIndex(-1).
		Radius(radius).
		Background(fill)
	if border.A > 0 {
		b.Border(1, border)
	}
	b.Transition(ui.ElementTransition{
		Animation: &anim,
		Match:     LensMatch{Scope: scope},
	})
}

// SlidingRingLens renders a shared active selection ring that glides smoothly between color swatches or presets.
func SlidingRingLens(c *ui.Context, scope string, radius float32, stroke ui.Color) {
	anim := SpringSlide
	ui.Box(c).
		Absolute().Fill().PassThrough().ZIndex(-1).
		Radius(radius).
		Border(2, stroke).
		Transition(ui.ElementTransition{
			Animation: &anim,
			Match:     LensMatch{Scope: scope},
		})
}

// StaggeredEnterTransition returns a calm cascading entrance for list/history cards (30ms stagger, capped).
func StaggeredEnterTransition(index int) ui.ElementTransition {
	if index > 6 {
		index = 6
	}
	anim := SpringPanel
	anim.Delay = time.Duration(index) * 28 * time.Millisecond
	return ui.ElementTransition{
		Animation: &anim,
		ExitDelay: -1,
		Enter: &ui.Motion{
			Y:     10,
			Scale: 0.97,
		},
	}
}
