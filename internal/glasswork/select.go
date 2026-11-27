package glasswork

import (
	"math"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/shader"

	"koma/internal/icons"
)

// Physical spring presets for Liquid Glass Select & GlassPopup neck stretch,
// adapted from bilalgursen/liquid-glass (GOOEY + ATTACH + LET_GO in engine.ts / glass-popup.tsx)
// and tuned for snappy, zero-flicker Windows Direct3D 11 desktop interaction.
var (
	// SpringSelectOpen: smooth critically-damped swell as the popup emerges from its trigger (omega=15, zeta=1).
	SpringSelectOpen = ui.Animation{
		Spring: ui.SpringOptions{Mass: 1, Stiffness: 225, Damping: 30.0},
	}

	// SpringSelectClose: brisk reverse rewind back into the trigger pill (~1.5x opening pace, omega=23, zeta=1).
	SpringSelectClose = ui.Animation{
		Spring: ui.SpringOptions{Mass: 1, Stiffness: 529, Damping: 46.0},
	}

	// SpringNeckAttach: fast meniscus bridge formation as the popup separates from the trigger (omega=28, zeta=1).
	SpringNeckAttach = ui.Animation{
		Spring: ui.SpringOptions{Mass: 1, Stiffness: 784, Damping: 56.0},
	}

	// SpringNeckSnap: crisp release once the liquid neck thins past the snap threshold (omega=18, zeta=1).
	SpringNeckSnap = ui.Animation{
		Spring: ui.SpringOptions{Mass: 1, Stiffness: 324, Damping: 36.0},
	}

	// SpringSelectChevron: smooth 180-degree rotation of the trigger chevron.
	SpringSelectChevron = ui.Animation{
		Spring: ui.SpringOptions{Mass: 1, Stiffness: 256, Damping: 26.0},
	}
)

// liquidSelectPopupShader draws a unified Liquid Glass popup surface with an optional
// polynomial smooth-minimum (smin) meniscus neck bridging the gap back to its trigger pill.
// While NeckK > 0, the popup swells out of the trigger as one body of glass and gathers into
// a center-pinched neck; once NeckK relaxes to 0, it settles cleanly into Card's frosted glass
// (radial center-pulling edge refraction, soft 5.5px frost, crisp 1px inset hairline border).
var liquidSelectPopupShader = shader.MustCompile(`package main

var PanelRect vec4      // (x, y, w, h) of animated popup card in local DIPs
var AnchorRect vec4     // (x, y, w, h) of trigger pill just outside shader box in local DIPs
var PanelRadius float   // dynamic corner radius (rounder while attached by neck)
var AnchorRadius float  // trigger pill corner radius
var NeckK float         // smooth-min bridge softness in DIPs (0 when settled)
var NeckWidth float     // horizontal Gaussian gathering half-width for the liquid neck
var IsDark float        // 0 = light glass, 1 = dark glass
var Frost float         // frosted tint cover (0..1)
var BorderAlpha float   // 1px inset hairline border opacity
var BorderWidth float   // border width in DIPs
var TintColor vec4      // frosted card tint color

func roundRect(p vec2, b vec2, r float) float {
	q := abs(p) - b + vec2(r, r)
	return length(max(q, vec2(0.0, 0.0))) + min(max(q.x, q.y), 0.0) - r
}

func evalPopupDist(pos vec2, pCenter vec2, pHalf vec2, pRad float, aCenter vec2, aHalf vec2, aRad float) float {
	dPanel := roundRect(pos-pCenter, pHalf, pRad)
	if NeckK <= 0.05 {
		return dPanel
	}
	dAnchor := roundRect(pos-aCenter, aHalf, aRad)
	nx := (pos.x - aCenter.x) / max(NeckWidth, 8.0)
	kLocal := NeckK * exp(-nx*nx)
	if kLocal <= 0.02 {
		return min(dPanel, dAnchor)
	}
	h := clamp(0.5+0.5*(dAnchor-dPanel)/kLocal, 0.0, 1.0)
	return mix(dAnchor, dPanel, h) - kLocal*h*(1.0-h)
}

func Fragment(dst vec4, pos vec2) vec4 {
	pHalf := max(PanelRect.zw*0.5, vec2(1.0, 1.0))
	pCenter := PanelRect.xy + pHalf
	pRad := min(PanelRadius, min(pHalf.x, pHalf.y))

	aHalf := max(AnchorRect.zw*0.5, vec2(1.0, 1.0))
	aCenter := AnchorRect.xy + aHalf
	aRad := min(AnchorRadius, min(aHalf.x, aHalf.y))

	dist := evalPopupDist(pos, pCenter, pHalf, pRad, aCenter, aHalf, aRad)
	aa := max(1.0/pixelScale(), 0.5)
	mask := 1.0 - smoothstep(-aa, aa, dist)
	if mask <= 0.001 {
		return vec4(0.0)
	}

	// Card radial center-pulling edge refraction (matches PresetCard in glasswork.go)
	localP := pos - pCenter
	minHalf := max(min(pHalf.x, pHalf.y), 1.0)
	ratio := clamp(1.0-abs(dist)/(minHalf*1.35), 0.0, 1.0)
	kRad := pow(ratio, 9.75) * smoothstep(1.5, -0.5, dist)
	radDir := -localP / max(length(localP), 0.001)
	bendDip := radDir * kRad * (min(PanelRect.z, PanelRect.w) * 0.12)

	// Extra meniscus pull across the stretching liquid neck so the bridge bends the backdrop cleanly
	dPanel := roundRect(pos-pCenter, pHalf, pRad)
	inNeck := 0.0
	if NeckK > 0.05 && dPanel > -2.0 {
		inNeck = smoothstep(-2.0, 4.0, dPanel)
		eps := 1.5
		gx := evalPopupDist(pos+vec2(eps, 0.0), pCenter, pHalf, pRad, aCenter, aHalf, aRad) - dist
		gy := evalPopupDist(pos+vec2(0.0, eps), pCenter, pHalf, pRad, aCenter, aHalf, aRad) - dist
		ndir := normalize(vec2(gx, gy) + vec2(1e-5, 1e-5))
		bendDip += -ndir * (6.5 * inNeck * smoothstep(0.0, 10.0, -dist+4.0))
	}

	glass := backdropSample(pos + bendDip).rgb

	// Subtle vibrancy boost
	luma := dot(glass, vec3(0.2126, 0.7152, 0.0722))
	glass = max(mix(vec3(luma), glass, 1.15), vec3(0.0))

	// Blend from clearer liquid neck into Card's calm frost inside the panel
	localFrost := mix(Frost, Frost*0.58, inNeck*0.65)
	glass = mix(glass, TintColor.rgb, localFrost)

	// Subtle specular rim glint along the stretched liquid neck walls
	if inNeck > 0.01 {
		rimLine := 1.0 - smoothstep(0.0, 2.0, abs(dist+1.2))
		neckRim := 0.34
		if IsDark > 0.5 {
			neckRim = 0.24
		}
		glass += vec3(1.0) * rimLine * inNeck * neckRim
	}

	// Subtle top optical reflection & warm bottom caustic (calm Card level)
	ny := clamp(localP.y/max(pHalf.y, 1.0), -1.0, 1.0)
	topRefl := (1.0 - smoothstep(-0.1, 0.75, ny)) * 0.045
	glass *= (1.0 - topRefl)

	// Crisp 1px inset optical hairline border (faded smoothly at the trigger seam while attached)
	if BorderAlpha > 0.001 && BorderWidth > 0.0 {
		bLine := smoothstep(-BorderWidth-aa, -BorderWidth+aa, dist)
		seamFade := 1.0
		if NeckK > 0.05 {
			dAnchor := roundRect(pos-aCenter, aHalf, aRad)
			seamFade = smoothstep(0.0, 4.0, dAnchor)
		}
		glass = mix(glass, vec3(1.0), bLine*BorderAlpha*seamFade)
	}

	glass = clamp(glass, vec3(0.0), vec3(1.0))
	return vec4(glass*mask, mask)
}
`)

var (
	uSelPanelRect    = liquidSelectPopupShader.Uniform("PanelRect")
	uSelAnchorRect   = liquidSelectPopupShader.Uniform("AnchorRect")
	uSelPanelRadius  = liquidSelectPopupShader.Uniform("PanelRadius")
	uSelAnchorRadius = liquidSelectPopupShader.Uniform("AnchorRadius")
	uSelNeckK        = liquidSelectPopupShader.Uniform("NeckK")
	uSelNeckWidth    = liquidSelectPopupShader.Uniform("NeckWidth")
	uSelIsDark       = liquidSelectPopupShader.Uniform("IsDark")
	uSelFrost        = liquidSelectPopupShader.Uniform("Frost")
	uSelBorderAlpha  = liquidSelectPopupShader.Uniform("BorderAlpha")
	uSelBorderWidth  = liquidSelectPopupShader.Uniform("BorderWidth")
	uSelTintColor    = liquidSelectPopupShader.Uniform("TintColor")

	liquidSelectValuesPool = sync.Pool{
		New: func() any { return liquidSelectPopupShader.NewValues() },
	}
)

// SelectOption describes one selectable choice in a Liquid Glass Select menu.
type SelectOption[T comparable] struct {
	Value    T
	Label    string
	Badge    string // optional subtle secondary hint on the right (e.g. "~30 MB")
	Disabled bool
}

// SelectProps configures a Liquid Glass Select dropdown inspired by bilalgursen/liquid-glass GlassSelect.
type SelectProps[T comparable] struct {
	Key         string // stable unique key for MyGo identity & spring state
	Label       string // accessible name for the trigger button (RolePopUpButton)
	Placeholder string // fallback text when no option matches *Selected
	Tooltip     string // optional tooltip shown on hover
	Options     []SelectOption[T]
	Selected    *T       // bound persistent value updated immediately on selection
	Open        *bool    // optional caller-owned flag mirroring active open state (excludes closing tail)
	Width       float32  // trigger width in DIPs (default 132)
	Height      float32  // trigger height in DIPs (default 28)
	Kind        Kind     // KindLight or KindDark
	Accent      ui.Color // active check & focus accent
	AlignEnd    bool     // align popup right edge to trigger right edge (recommended for Settings rows)
	Disabled    bool
	OnSelect    func(value T) // invoked immediately when the user picks an option
}

type selectOpenKey struct{}
type selectBridgeKey struct{}
type selectChevronKey struct{}

type selectLocalState struct {
	open       bool
	closing    bool
	wasShown   bool
	openProg   float32
	bridgeProg float32
	lastExtSet bool
}

// SelectTriggerOpts returns calm liquid droplet wobble & press swell tuned for pill Select triggers.
func SelectTriggerOpts() LiquidButtonOpts {
	return LiquidButtonOpts{
		HoverScale: 1.028,
		PressScale: 0.965,
		Wobble:     0.018,
		MaxGrowPx:  3.0,
		Pull:       false,
	}
}

// Select renders a Liquid Glass dropdown built on MyGo's accessible ui.SelectBaseOpen:
//   - Trigger wears Button's clear optical lens glass (PresetLens) with droplet wobble & rotating chevron
//   - Opening swells the list out of the trigger across a smooth-minimum (smin) liquid glass neck that
//     thins and snaps, then settles into Card's frosted glass with staggered option arrival
//   - Closing rewinds smoothly back into the trigger while passing input through immediately
//   - Full keyboard support (Up/Down/Home/End/Enter/Space/Escape), disabled option skipping,
//     and ReduceMotion / ReduceTransparency accessibility compliance
func Select[T comparable](c *ui.Context, props SelectProps[T]) bool {
	if props.Selected == nil {
		return false
	}
	w := props.Width
	if w <= 0 {
		w = 132
	}
	h := props.Height
	if h <= 0 {
		h = 28
	}
	accent := props.Accent
	if accent.A == 0 {
		accent = ui.Hex("#0066E6")
		if props.Kind == KindDark {
			accent = ui.Hex("#0A84FF")
		}
	}
	inks := props.Kind.Inks()
	reduceMotion := c.Preferences().ReduceMotion

	const popupGap float32 = 8
	const panelCorner float32 = 18

	changed := false
	wrap := ui.Box(c.Key(props.Key + "-wrap")).Shrink(0)
	st := ui.Local(wrap, "glass-select-state", func() selectLocalState { return selectLocalState{} })

	// Allow caller to force-close the popup externally (e.g. closing parent sheet).
	if props.Open != nil && !*props.Open && st.lastExtSet && st.open && !st.closing {
		if reduceMotion || st.openProg <= 0.04 {
			st.open = false
			st.closing = false
			st.wasShown = false
		} else {
			st.closing = true
		}
	}

	// Detect when SelectBaseOpen closed *open (option chosen, Escape, or backdrop click)
	// and hold it mounted for the reverse liquid-retraction animation unless ReduceMotion is on.
	if !st.open && st.wasShown {
		if !st.closing && !reduceMotion && st.openProg > 0.04 {
			st.open = true
			st.closing = true
		} else {
			st.closing = false
			st.wasShown = false
		}
	}

	wrap.Children(func() {
		sel := ui.SelectBaseOpen(c.Key(props.Key), props.Selected, &st.open)

		// If the user clicks the trigger mid-close, immediately reverse and reopen from current progress!
		if st.closing && sel.Trigger.Clicked() {
			st.closing = false
			st.open = true
		}
		if props.Disabled {
			sel.Trigger.Disabled(true)
			st.open = false
			st.closing = false
		}

		isActivelyOpen := st.open && !st.closing

		// Step open/close spring progress
		openTarget := float32(0)
		openAnim := SpringSelectClose
		if isActivelyOpen {
			openTarget = 1
			openAnim = SpringSelectOpen
		}
		var openProg float32
		if reduceMotion {
			openProg = sel.Trigger.Jump(selectOpenKey{}, openTarget)
		} else {
			openProg = sel.Trigger.AnimateTo(selectOpenKey{}, openTarget, openAnim)
		}
		st.openProg = clamp32(openProg, 0, 1.05)

		// Step liquid meniscus neck bridge:
		// Opening: forms quickly while openProg < 0.44, then thins and snaps cleanly to 0.
		// Closing: briefly re-forms as the card pulls back into the trigger pill (0.12..0.56).
		bridgeTarget := float32(0)
		bridgeAnim := SpringNeckSnap
		if !reduceMotion {
			if isActivelyOpen && st.openProg > 0.01 && st.openProg < 0.44 {
				bridgeTarget = 1.0
				bridgeAnim = SpringNeckAttach
			} else if st.closing && st.openProg > 0.12 && st.openProg < 0.58 {
				bridgeTarget = 0.68
				bridgeAnim = SpringNeckAttach
			}
		}
		var bridge float32
		if reduceMotion {
			bridge = sel.Trigger.Jump(selectBridgeKey{}, 0)
		} else {
			bridge = clamp32(sel.Trigger.AnimateTo(selectBridgeKey{}, bridgeTarget, bridgeAnim), 0, 1)
		}
		st.bridgeProg = bridge

		// Finish reverse-close once the glass has returned inside the trigger
		if st.closing && st.openProg <= 0.02 {
			st.open = false
			st.closing = false
			st.wasShown = false
			st.openProg = sel.Trigger.Jump(selectOpenKey{}, 0)
			st.bridgeProg = sel.Trigger.Jump(selectBridgeKey{}, 0)
			isActivelyOpen = false
		}

		// Chevron smooth 180-degree rotation
		chevTarget := float32(0)
		if isActivelyOpen {
			chevTarget = 180
		}
		var chevDeg float32
		if reduceMotion {
			chevDeg = sel.Trigger.Jump(selectChevronKey{}, chevTarget)
		} else {
			chevDeg = sel.Trigger.AnimateTo(selectChevronKey{}, chevTarget, SpringSelectChevron)
		}

		// Resolve display label for current *Selected
		currentLabel := props.Placeholder
		for _, opt := range props.Options {
			if opt.Value == *props.Selected {
				currentLabel = opt.Label
				break
			}
		}

		// Trigger optical Liquid Glass lens material (Button's clear curved lens)
		trigSurf := Light()
		if props.Kind == KindDark {
			trigSurf = Dark()
			trigSurf.Density = 0.50
			trigSurf.Floor = 0.34
		} else {
			trigSurf.Density = 0.56
			trigSurf.Floor = 0.42
		}
		trigSurf.Blur = 3.0
		trigSurf.Shadow = true

		trig := sel.Trigger
		trig.Width(w).Height(h).Padding(0, 9, 0, 12).Gap(6).
			Radius(h * 0.5).AlignItems(ui.Center).
			Material(trigSurf).
			Label(props.Label).
			Cursor(ui.CursorPointer)
		if props.Tooltip != "" {
			trig.Tooltip(props.Tooltip)
		}
		if props.Disabled {
			trig.Opacity(0.45).Cursor(ui.CursorDefault)
		}

		// Apply liquid droplet wobble while closed; subtle meniscus sag while neck is attached
		if !isActivelyOpen && !st.closing && !props.Disabled {
			ApplyLiquidButton(trig, SelectTriggerOpts())
		} else if !reduceMotion && bridge > 0.01 {
			// Gentle 1.5px pull toward the emerging popup while the liquid neck holds
			trig.Translate(0, 1.5*bridge)
		}

		chevCol := inks.Secondary
		if isActivelyOpen || trig.Hovered() || trig.Pressed() {
			chevCol = inks.Accent
		}

		trig.Children(func() {
			ui.Text(c, currentLabel).
				FontSize(12).FontWeight(600).TextColor(inks.Primary).
				SingleLine().Grow(1)
			ui.Icon(c, icons.ChevronDown.For(isActivelyOpen || trig.Pressed())).
				Size(13, 13).Shrink(0).TextColor(chevCol).
				Rotate(chevDeg).
				ContentTransition(ui.ContentTransition{})
		})

		// Build floating Liquid Glass Popup
		panel := sel.Popup(func(panel ui.Element) {
			if props.AlignEnd {
				panel.AttachTo(trig, ui.AnchorBottomEnd, ui.AnchorTopEnd)
			}
			at, _ := panel.AttachedAnchors()
			dir := float32(1)
			if at == ui.AnchorTopStart || at == ui.AnchorTop || at == ui.AnchorTopEnd {
				dir = -1
			}

			panel.Margin(popupGap, 0, 0, 0).
				MinWidth(max(w, trig.Bounds().W)).
				Padding(5).Gap(2).
				Radius(panelCorner).
				IgnoreInput(st.closing)

			// Capture geometry and animation values for the custom gooey Liquid Glass shader
			pKind := props.Kind
			pOpen := clamp32(st.openProg, 0, 1)
			pBridge := bridge
			pDir := dir
			pClosing := st.closing
			trigW := trig.Bounds().W
			if trigW <= 1 {
				trigW = w
			}
			trigH := trig.Bounds().H
			if trigH <= 1 {
				trigH = h
			}

			panel.Draw(func(pt *ui.Painter, r ui.Rect) {
				if r.W <= 1 || r.H <= 1 || pOpen <= 0.005 {
					return
				}
				drawSelectPopupGlass(pt, r, trigW, trigH, popupGap, panelCorner, pDir, pOpen, pBridge, pKind)
			})

			// Option items: staggered arrival on open, crisp fast fade on close
			highlightFill := ui.RGBA(15, 23, 42, 0.08)
			if props.Kind == KindDark {
				highlightFill = ui.RGBA(255, 255, 255, 0.13)
			}
			listOpacity := float32(1)
			if pClosing {
				// Closing fades options out quickly ahead of the retracting glass (LIST_OUT)
				listOpacity = smoothstep32(0.45, 0.90, pOpen)
			}

			lensScope := "glass-select-hl-" + props.Key
			for i, opt := range props.Options {
				item := sel.Item(opt.Value)
				isSelected := opt.Value == *props.Selected
				isHL := item.Highlighted()

				item.Height(32).Padding(0, 10, 0, 10).Gap(8).
					Radius(12).AlignItems(ui.Center).
					Label(opt.Label)
				if opt.Disabled {
					item.Disabled(true).Opacity(0.40).Cursor(ui.CursorDefault)
				} else {
					item.Cursor(ui.CursorPointer)
				}

				// Staggered queue entrance (--glass-i): each item rises 5px and fades in cleanly
				itemAlpha := listOpacity
				var itemY float32
				if !reduceMotion && !pClosing {
					start := 0.14 + float32(min(i, 6))*0.075
					inT := smoothstep32(start, min(start+0.38, 0.98), pOpen)
					itemAlpha *= inT
					itemY = pDir * (1.0 - inT) * 5.0
				}
				if itemAlpha < 0.999 {
					item.Opacity(itemAlpha)
				}
				if math.Abs(float64(itemY)) > 0.05 {
					item.Translate(0, itemY)
				}

				item.Transition(ui.ElementTransition{Colors: true, Animation: &AnimColorFade})

				fg := inks.Primary
				weight := 500
				if isSelected {
					weight = 600
				}
				if isHL && !opt.Disabled {
					fg = inks.Primary
				}

				item.Children(func() {
					if isHL && !opt.Disabled {
						SlidingSoftLens(c, lensScope, 12, highlightFill)
					}
					ui.Text(c, opt.Label).
						FontSize(12.5).FontWeight(weight).TextColor(fg).
						SingleLine().Grow(1)
					if opt.Badge != "" && !isSelected {
						ui.Text(c, opt.Badge).
							FontSize(10.5).FontWeight(500).TextColor(inks.Secondary).
							SingleLine().Shrink(0)
					}
					if isSelected {
						ui.Icon(c, icons.CheckMark.Active).
							Size(13.5, 13.5).Shrink(0).TextColor(accent)
					}
				})
			}
		})

		// Check Trigger.Changed() AFTER sel.Popup so item.afterInput callbacks have registered
		// and executed within applyInputs() for both mouse clicks and keyboard Enter/Space!
		if trig.Changed() {
			changed = true
			if props.OnSelect != nil {
				props.OnSelect(*props.Selected)
			}
		}

		if panel.Valid() {
			st.wasShown = true
		}

		if props.Open != nil {
			*props.Open = st.open && !st.closing
			st.lastExtSet = *props.Open
		}
	})

	return changed
}

// drawSelectPopupGlass renders the stretching Liquid Glass popup card and its smooth-min meniscus neck.
func drawSelectPopupGlass(pt *ui.Painter, r ui.Rect, trigW, trigH, gap, cornerR, dir, openProg, bridge float32, kind Kind) {
	// Compute animated card rect swelling out of the trigger's near edge:
	// At openProg=0 it starts tucked near the trigger edge; at openProg=1 it occupies the exact layout rect r.
	t := clamp32(openProg, 0, 1)
	ease := smoothstep32(0, 1, t)
	curW := lerp32(min(trigW*0.92, r.W), r.W, ease)
	curH := lerp32(min(trigH*0.85, r.H), r.H, t)

	// Keep horizontal center aligned with r (or trigger center)
	curX := r.X + (r.W-curW)*0.5

	// Near edge crosses the gap early (NEAR_EDGE ~ 0.35) and leans slightly back toward the trigger while bridge holds
	neckPull := float32(4.0) * bridge
	var curY float32
	if dir > 0 {
		// Opening below trigger: trigger bottom sits at r.Y - gap
		nearEdge := lerp32(r.Y-gap, r.Y-neckPull, smoothstep32(0, 0.38, t))
		farEdge := lerp32(r.Y-gap+curH, r.Y+r.H, t)
		curY = nearEdge
		curH = max(farEdge-nearEdge, 8)
	} else {
		// Opening above trigger: trigger top sits at r.Y + r.H + gap
		nearEdge := lerp32(r.Y+r.H+gap, r.Y+r.H+neckPull, smoothstep32(0, 0.38, t))
		farEdge := lerp32(r.Y+r.H+gap-curH, r.Y, t)
		curY = farEdge
		curH = max(nearEdge-farEdge, 8)
	}

	animRect := ui.Rect{X: curX, Y: curY, W: curW, H: curH}
	// Corners start rounder while held by surface tension and relax to panelCorner as the neck releases
	dynRad := min(lerp32(cornerR, cornerR*1.45, bridge), min(curW, curH)*0.5)

	// Accessibility: ReduceTransparency renders a clean solid surface
	if pt.Preferences().ReduceTransparency {
		tint := ui.Hex("#FFFFFF")
		if kind == KindDark {
			tint = ui.Hex("#1A1B1F")
		}
		pt.Shadow(animRect, dynRad, 0, 10, 26, 0, ui.RGBA(0, 0, 0, 0.14*t))
		pt.Fill(animRect, tint.Alpha(0.96*t), dynRad)
		pt.Stroke(animRect, ui.RGBA(0, 0, 0, 0.14*t), dynRad, 1)
		return
	}

	// 1. Soft twin Card lift shadow (fades in smoothly as the popup emerges)
	shadowAlpha := smoothstep32(0.08, 0.65, t)
	if shadowAlpha > 0.01 {
		if kind == KindDark {
			pt.Shadow(animRect, dynRad, 0, 14, 34, 0, ui.RGBA(0, 0, 0, 0.24*shadowAlpha))
			pt.Shadow(animRect, dynRad, 0, 4, 12, 0, ui.RGBA(0, 0, 0, 0.16*shadowAlpha))
		} else {
			pt.Shadow(animRect, dynRad, 0, 12, 30, 0, ui.RGBA(0, 0, 0, 0.09*shadowAlpha))
			pt.Shadow(animRect, dynRad, 0, 3, 10, 0, ui.RGBA(0, 0, 0, 0.05*shadowAlpha))
		}
	}

	// 2. Shader bounding box covers the gap bridge + animated card without overlapping the trigger's label
	shaderBox := animRect
	var anchorAbs ui.Rect
	anchorX := r.X + (r.W-trigW)*0.5
	if dir > 0 {
		triggerBottom := r.Y - gap
		anchorAbs = ui.Rect{X: anchorX, Y: triggerBottom - trigH, W: trigW, H: trigH}
		if bridge > 0.01 && shaderBox.Y > triggerBottom {
			extra := shaderBox.Y - triggerBottom
			shaderBox.Y = triggerBottom
			shaderBox.H += extra
		}
	} else {
		triggerTop := r.Y + r.H + gap
		anchorAbs = ui.Rect{X: anchorX, Y: triggerTop, W: trigW, H: trigH}
		if bridge > 0.01 && shaderBox.Y+shaderBox.H < triggerTop {
			shaderBox.H = triggerTop - shaderBox.Y
		}
	}

	// Local rects inside shaderBox
	localPanel := ui.Rect{
		X: animRect.X - shaderBox.X,
		Y: animRect.Y - shaderBox.Y,
		W: animRect.W,
		H: animRect.H,
	}
	localAnchor := ui.Rect{
		X: anchorAbs.X - shaderBox.X,
		Y: anchorAbs.Y - shaderBox.Y,
		W: anchorAbs.W,
		H: anchorAbs.H,
	}

	// Neck softness in DIPs & horizontal gathering width (pinches into a curved liquid waist at center)
	neckK := float32(22.0) * bridge * smoothstep32(0.02, 0.25, t)
	neckW := max(trigW*lerp32(0.24, 0.56, bridge), trigH*0.75)

	isDark := float32(0)
	tint := ui.Hex("#FFFFFF")
	frost := float32(0.70)
	borderAlpha := float32(0.44)
	if kind == KindDark {
		isDark = 1
		tint = ui.Hex("#1A1B1F")
		frost = 0.62
		borderAlpha = 0.20
	}

	vals := liquidSelectValuesPool.Get().(*shader.Values)
	defer liquidSelectValuesPool.Put(vals)
	vals.Vec4(uSelPanelRect, localPanel.X, localPanel.Y, localPanel.W, localPanel.H)
	vals.Vec4(uSelAnchorRect, localAnchor.X, localAnchor.Y, localAnchor.W, localAnchor.H)
	vals.Float(uSelPanelRadius, dynRad)
	vals.Float(uSelAnchorRadius, trigH*0.5)
	vals.Float(uSelNeckK, neckK)
	vals.Float(uSelNeckWidth, neckW)
	vals.Float(uSelIsDark, isDark)
	vals.Float(uSelFrost, frost)
	vals.Float(uSelBorderAlpha, borderAlpha)
	vals.Float(uSelBorderWidth, 1.0)
	vals.Color(uSelTintColor, tint)

	liquidSelectPopupShader.Draw(pt, shaderBox, shader.Options{
		BackdropBlur: 5.5,
		Radius:       0,
		Values:       vals,
	})
}

func lerp32(a, b, t float32) float32 {
	return a + (b-a)*t
}

func smoothstep32(edge0, edge1, x float32) float32 {
	if edge1 <= edge0 {
		if x >= edge1 {
			return 1
		}
		return 0
	}
	t := clamp32((x-edge0)/(edge1-edge0), 0, 1)
	return t * t * (3 - 2*t)
}

// keep time import used for potential duration helpers / future extensions
var _ = time.Millisecond
