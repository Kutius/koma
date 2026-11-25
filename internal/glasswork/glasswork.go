package glasswork

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/shader"
)

// Physical Liquid Glass GPU backdrop shader synthesized directly from
// bilalgursen/liquid-glass (engine.ts + glass-card.tsx + glass-layer.tsx)
// and kubai087/glasswork (adapt.js legibility):
//   - Curved optical dome bevel height field: h = sqrt(1 - (1 - t)^2), t = (-d/bevel)^(1/1.4)
//   - 3D surface normal from height gradient + grazing-angle Fresnel (pow(1 - n.z, 6))
//   - Dual optical modes:
//       * Lens / Pill (Mode=0): curved meniscus refraction + chromatic split + studio rim light
//         + neighbor rim mirroring + 3-band gamma-correct inner meniscus shading + warm bottom caustic
//       * Card / Drawer (Mode=1): radial center-pulling edge refraction + frosted glass + crisp rim border
//       * Tinted Prominent Glass (Mode=2): vivid accent glass with edgeClear thinning + full 3D dome optics
//   - Interactive internal press glow (L^(1/(1+glow)) + soft white press light) + cursor-leaning rim light
var liquidGlassShader = shader.MustCompile(`package main

var Radius float
var Mode float          // 0 = clear lens/pill, 1 = frosted card/drawer, 2 = tinted prominent glass
var IsDark float        // 0 = light glass, 1 = dark glass
var Frost float         // tint cover amount (0..1)
var Refract float       // refraction displacement in design units
var BendCurve float     // bend falloff exponent (1.6 lens, 9.75 card)
var EdgeBand float      // bend reach fraction of bevel (0.35 default)
var Dispersion float    // chromatic split amount
var Saturation float    // vibrancy boost
var RimStrength float   // rim light intensity (0.55)
var InnerStrength float // 3-band meniscus inner shading (0.10)
var TopRefl float       // top optical reflection (0.30)
var Caustic float       // warm bottom caustic (0.08)
var MirrorAmt float     // rim mirroring of surrounding colors (0..1)
var BorderAlpha float   // crisp inset border line opacity
var BorderWidth float   // border line width in DIPs
var TintColor vec4      // tint or prominent accent color
var LightPos vec2       // spring-smoothed cursor position in local DIPs from center
var LightMix float      // 0..1 smooth hover presence for localized cursor rim glint
var PressPos vec2       // press position in local DIPs from center
var PressAmt float      // 0..1 interactive internal press glow
var PressSpread float   // 0..1 spreading halo radius as press glow blooms & fades

func roundRect(p vec2, b vec2, r float) float {
	q := abs(p) - b + vec2(r, r)
	return length(max(q, vec2(0.0, 0.0))) + min(max(q.x, q.y), 0.0) - r
}

func dim1(k float) float {
	return pow(clamp(k, 0.0, 1.0), 1.0/2.2)
}

func dim3(k vec3) vec3 {
	return pow(clamp(k, vec3(0.0), vec3(1.0)), vec3(1.0/2.2))
}

func domeHeight(p vec2, b vec2, r float, bevel float) float {
	d := roundRect(p, b, r)
	t := pow(clamp(-d/max(bevel, 1.0), 0.0, 1.0), 1.0/1.4)
	return sqrt(max(0.0, 1.0-(1.0-t)*(1.0-t)))
}

func Fragment(dst vec4, pos vec2) vec4 {
	sz := rectSize()
	hb := sz * 0.5
	localDip := pos - hb

	// Map to bilalgursen/liquid-glass 120-unit optical design space (anchored on short side,
	// capped so tall drawers/panels keep refined bezel proportions rather than giant domes).
	refH := clamp(min(sz.x, sz.y), 24.0, 72.0)
	unit := refH / 120.0
	local := localDip / unit
	halfU := hb / unit
	rU := min(Radius/unit, min(halfU.x, halfU.y)-0.5)
	if rU < 0.0 {
		rU = 0.0
	}

	dist := roundRect(local, halfU, rU)
	aa := max(1.0/(unit*pixelScale()), 0.5)
	mask := 1.0 - smoothstep(-aa, aa, dist)
	if mask <= 0.001 {
		return vec4(0.0)
	}

	// Bevel width in design units: 42% of 120 = 50.4 for pills, bounded by corner radius for cards.
	bevel := 50.4
	if rU < halfU.y*0.85 {
		bevel = min(bevel, max(12.0, rU*1.2))
	}

	// Curved dome height field + 3D surface normal (bilalgursen engine.ts lines 1108-1114).
	eps := 1.25
	h := domeHeight(local, halfU, rU, bevel)
	hx1 := domeHeight(local+vec2(eps, 0.0), halfU, rU, bevel)
	hy1 := domeHeight(local+vec2(0.0, eps), halfU, rU, bevel)
	gradH := vec2(hx1-h, hy1-h) / eps
	normal := normalize(vec3(-gradH.x*42.0, -gradH.y*42.0, 1.0))
	depth := clamp(1.0-h, 0.0, 1.0)

	// Outward shape direction from SDF gradient.
	gx := roundRect(local+vec2(eps, 0.0), halfU, rU) - dist
	gy := roundRect(local+vec2(0.0, eps), halfU, rU) - dist
	dir := normalize(vec2(gx, gy) + vec2(1e-5, 1e-5))

	edge := 1.0 - smoothstep(0.0, bevel*EdgeBand, -dist)
	ny := local.y / max(halfU.y, 1.0)
	nx := local.x / max(halfU.x, 1.0)
	ly := local.y

	var glass vec3
	if Mode > 1.5 {
		// Mode 2: Tinted prominent glass (GlassButton material="tinted"):
		// Vivid accent color in the center, thinning slightly toward the curved meniscus rim.
		bg := backdropSample(pos - dir*Refract*pow(edge, BendCurve)*unit*0.45).rgb
		glass = mix(bg, TintColor.rgb, 1.0-edge*0.22)
	} else if Mode > 0.5 {
		// Mode 1: Frosted Card / Drawer (glass-card.tsx):
		// Radial center-pulling edge bend fading as (1 - |d|/(bendDepth * minHalf))^bendCurve.
		minHalf := min(halfU.x, halfU.y)
		ratio := clamp(1.0-abs(dist)/max(minHalf*1.35, 1.0), 0.0, 1.0)
		kRad := pow(ratio, BendCurve) * smoothstep(1.5/unit, -0.5/unit, dist)
		radDir := -local / max(length(local), 0.001)
		bendDip := radDir * kRad * (min(sz.x, sz.y) * 0.14)
		glass = backdropSample(pos + bendDip).rgb
		if Saturation != 1.0 {
			luma := dot(glass, vec3(0.2126, 0.7152, 0.0722))
			glass = max(mix(vec3(luma), glass, Saturation), vec3(0.0))
		}
		glass = mix(glass, TintColor.rgb, Frost)
	} else {
		// Mode 0: Clear Optical Lens / Floating Toolbar Pill (bilalgursen GlassLayer clear):
		// Slight center magnification + curved meniscus inward refraction + subtle chromatic split.
		bendDip := -dir * (Refract * pow(edge, BendCurve) * unit)
		split := Dispersion * 0.35
		if split > 0.001 && edge > 0.01 {
			cR := backdropSample(pos + bendDip*(1.0+split)).r
			cG := backdropSample(pos + bendDip).g
			cB := backdropSample(pos + bendDip*(1.0-split)).b
			glass = vec3(cR, cG, cB)
		} else {
			glass = backdropSample(pos + bendDip).rgb
		}
		if Saturation != 1.0 {
			luma := dot(glass, vec3(0.2126, 0.7152, 0.0722))
			glass = max(mix(vec3(luma), glass, Saturation), vec3(0.0))
		}
	}

	// Top atmospheric sky reflection (bilalgursen engine.ts lines 1160-1163).
	if TopRefl > 0.001 {
		top := (1.0-smoothstep(-0.05, 0.75, ny))*(1.0-nx*0.12)*TopRefl*0.6 + 0.02*(TopRefl/0.30)
		topDim := dim3(vec3(1.0) - vec3(1.0, 1.0, 0.72)*top)
		glass = glass * topDim
	}

	// Clear lens subtle tint veil (keeps glass bright on light theme, smoky on dark theme).
	if Mode < 0.5 {
		glass = mix(glass, TintColor.rgb, Frost)
	}

	// 3-band gamma-correct optical inner meniscus shading (bilalgursen lines 1197-1209).
	up := clamp(-dir.y, 0.0, 1.0)
	right := clamp(dir.x, 0.0, 1.0)
	left := clamp(-dir.x, 0.0, 1.0)
	down := clamp(dir.y, 0.0, 1.0)

	if InnerStrength > 0.001 {
		innerBand := smoothstep(bevel*-0.19, bevel*-0.024, dist)
		glass *= dim1(1.0 - innerBand*(up+left*0.6+right*0.3+down*0.15)*InnerStrength*2.2)

		deepBand := smoothstep(bevel*-0.73, bevel*-0.13, dist)
		glass *= dim1(1.0 - deepBand*left*InnerStrength*2.0)

		ringU := (dist + bevel*0.45) / max(bevel*0.26, 1.0)
		ring := exp(-ringU * ringU)
		glass *= dim1(1.0 - ring*(right*0.9+up*0.5+left*0.4+down*0.3)*InnerStrength*1.5)
	}

	// Optical rim light + 3D Fresnel + real surrounding color mirroring (bilalgursen engine.ts lines 1205-1257).
	if RimStrength > 0.001 {
		fresnel := pow(clamp(1.0-normal.z, 0.0, 1.0), 6.0)
		devPxU := 1.0 / (unit * pixelScale())
		rimW := max(devPxU*0.95, 0.85)
		rimLine := 1.0 - smoothstep(0.0, rimW*1.35, abs(dist+rimW*1.5))

		// Exact bilalgursen studio lighting: curved left/right end glints + subtle bottom bounce,
		// NO flat white stripe along the top edge!
		fall := exp(-(ly / 30.0) * (ly / 30.0))
		studio := right*right*fall*0.90 + down*down*0.20 + left*left*0.55

		// Localized specular rim sheen near the hovered cursor (never flashes the whole top edge!).
		cursorSpot := 0.0
		if LightMix > 0.001 {
			cReach := clamp(min(sz.x, sz.y)*1.75, 52.0, 120.0)
			cDist := length(localDip-LightPos) / cReach
			cursorSpot = exp(-cDist*cDist*1.6) * LightMix * 0.26
		}
		rimDir := studio + cursorSpot
		front := right
		back := left

		// Mirror colorful surroundings outside the rim into the curved bevel crescent!
		rimCol := vec3(1.0)
		if MirrorAmt > 0.01 && Mode < 0.5 {
			mirrorDepth := min(halfU.x, halfU.y) * 0.58
			into := clamp(-dist/max(mirrorDepth, 1.0), 0.0, 1.0)
			reachDip := clamp(sz.y*0.48, 18.0, 56.0)
			lookDip := reachDip * (1.0 - into)
			rimAtDip := pos - dir*(dist*unit)
			seen1 := backdropSample(rimAtDip + dir*(lookDip*0.45)).rgb
			seen2 := backdropSample(rimAtDip + dir*(lookDip*0.85)).rgb
			seen := seen1*0.55 + seen2*0.45

			seenMax := max(max(seen.r, seen.g), seen.b)
			bounce := mix(vec3(1.0), seen/max(seenMax, 0.001), smoothstep(0.10, 0.55, seenMax))
			rimCol = mix(vec3(1.0), bounce, down*0.55)
			rimCol = mix(rimCol, bounce*0.65, back*0.80)

			// Subtle neighbor color reflection band along the inner curved meniscus
			band := pow(1.0-into, 1.35) * 0.42 * smoothstep(0.0, rimW*1.8, -dist)
			mirror := min(max(rimLine*0.65, band), 1.0) * MirrorAmt
			lumaS := dot(seen, vec3(0.2126, 0.7152, 0.0722))
			chromaS := seenMax - min(min(seen.r, seen.g), seen.b)
			if chromaS > 0.06 {
				glass = mix(glass, max(glass, seen*1.08), mirror*clamp(chromaS*1.8, 0.0, 0.65))
			} else if lumaS > 0.75 {
				glass += vec3(0.08) * mirror
			}
		}

		// Attenuate rim slightly in light mode so white highlight stays crisp without blowing out.
		rimScale := RimStrength
		if IsDark > 0.5 {
			rimScale = RimStrength * 0.78
		}
		glass += rimCol * (rimLine*rimDir*rimScale + fresnel*front*0.045)
	}

	// Warm internal caustic gathering along the bottom curved meniscus (bilalgursen line 1280).
	if Caustic > 0.001 {
		glass += vec3(1.0, 0.92, 0.86) * smoothstep(0.72, 0.98, depth) * down * Caustic
	}

	// Interactive internal press glow (bilalgursen GLOW + LIGHT: starts focused at press point,
	// spreads smoothly outward as it decays, and brightens luminance cleanly while preserving hue!).
	if PressAmt > 0.004 {
		baseSpan := clamp(max(sz.x, sz.y)*0.90, 36.0, 180.0)
		span := baseSpan * (0.48 + 0.62*clamp(PressSpread, 0.0, 1.0))
		pd := length(localDip-PressPos) / max(span, 1.0)
		glowHere := PressAmt * 1.55 * exp(-pd*pd*2.0)
		glassL := max(dot(glass, vec3(0.2126, 0.7152, 0.0722)), 0.001)
		glass *= pow(glassL, 1.0/(1.0+glowHere)) / glassL

		whiteStrength := 0.28
		if IsDark > 0.5 {
			whiteStrength = 0.16
		}
		whiteGlow := PressAmt * whiteStrength * max(1.0-pd, 0.0)
		glass = mix(glass, vec3(1.0), clamp(whiteGlow, 0.0, 0.55))
	}

	// Crisp inset optical border line (especially refined on GlassCard & light capsules).
	if BorderAlpha > 0.001 && BorderWidth > 0.0 {
		bwU := BorderWidth / unit
		bLine := smoothstep(-bwU-aa, -bwU+aa, dist)
		borderCol := vec3(1.0)
		glass = mix(glass, borderCol, bLine*BorderAlpha)
	}

	glass = clamp(glass, vec3(0.0), vec3(1.0))
	return vec4(glass*mask, mask)
}
`)

var (
	uRadius        = liquidGlassShader.Uniform("Radius")
	uMode          = liquidGlassShader.Uniform("Mode")
	uIsDark        = liquidGlassShader.Uniform("IsDark")
	uFrost         = liquidGlassShader.Uniform("Frost")
	uRefract       = liquidGlassShader.Uniform("Refract")
	uBendCurve     = liquidGlassShader.Uniform("BendCurve")
	uEdgeBand      = liquidGlassShader.Uniform("EdgeBand")
	uDispersion    = liquidGlassShader.Uniform("Dispersion")
	uSaturation    = liquidGlassShader.Uniform("Saturation")
	uRimStrength   = liquidGlassShader.Uniform("RimStrength")
	uInnerStrength = liquidGlassShader.Uniform("InnerStrength")
	uTopRefl       = liquidGlassShader.Uniform("TopRefl")
	uCaustic       = liquidGlassShader.Uniform("Caustic")
	uMirrorAmt     = liquidGlassShader.Uniform("MirrorAmt")
	uBorderAlpha   = liquidGlassShader.Uniform("BorderAlpha")
	uBorderWidth   = liquidGlassShader.Uniform("BorderWidth")
	uTintColor     = liquidGlassShader.Uniform("TintColor")
	uLightPos      = liquidGlassShader.Uniform("LightPos")
	uLightMix      = liquidGlassShader.Uniform("LightMix")
	uPressPos      = liquidGlassShader.Uniform("PressPos")
	uPressAmt      = liquidGlassShader.Uniform("PressAmt")
	uPressSpread   = liquidGlassShader.Uniform("PressSpread")
)

// Preset distinguishes interactive optical pills/toolbars, frosted content cards, and tinted CTA glass.
type Preset uint8

const (
	PresetLens   Preset = iota // Clear curved optical lens (floating toolbars, capsules, pills)
	PresetCard                 // Frosted radial-bend content panel (history drawer, shortcut sheet)
	PresetTinted               // Vivid tinted liquid glass (primary Apple Blue buttons)
)

// Surface configures the physical Liquid Glass material for a MyGo UI element.
type Surface struct {
	Kind       Kind
	Preset     Preset
	Tint       ui.Color
	Density    float32
	Keep       float32
	Floor      float32
	Blur       float32
	Shadow     bool
	CursorLite bool // lean rim highlight toward hovered cursor
}

type pressAnimKey struct{}
type pressSpreadKey struct{}
type hoverAnimKey struct{}
type lightXAnimKey struct{}
type lightYAnimKey struct{}

type pressSpotState struct {
	x, y       float32
	hasSpot    bool
	wasPressed bool
	wasHovered bool
	lastLX     float32
	lastLY     float32
	hasLightPt bool
}

// interactiveSurface carries per-frame press glow & cursor light state bound to an element.
type interactiveSurface struct {
	Surface
	pressAmt    float32
	pressSpread float32
	pressX      float32
	pressY      float32
	lightX      float32
	lightY      float32
	lightMix    float32
}

// Light creates a crystal-clear optical liquid glass lens for light themes (dark ink).
func Light() Surface {
	return Surface{
		Kind:       KindLight,
		Preset:     PresetLens,
		Density:    0.34,
		Keep:       1.0,
		Blur:       3.2,
		Shadow:     true,
		CursorLite: true,
	}
}

// Dark creates a crystal-clear smoky optical liquid glass lens for dark themes (light ink).
func Dark() Surface {
	return Surface{
		Kind:       KindDark,
		Preset:     PresetLens,
		Density:    0.34,
		Keep:       1.0,
		Blur:       3.2,
		Shadow:     true,
		CursorLite: true,
	}
}

// Card creates a bilalgursen GlassCard-style frosted radial-refraction panel for drawers & sheets.
func Card(kind Kind) Surface {
	s := Light()
	if kind == KindDark {
		s = Dark()
	}
	s.Preset = PresetCard
	s.Blur = 5.5
	s.Shadow = true
	s.CursorLite = false
	return s
}

// FromAdaptation constructs a crystal optical lens tuned from adapt.js background sampling.
func FromAdaptation(a Adaptation) Surface {
	s := Light()
	if a.Kind == KindDark {
		s = Dark()
	}
	s.Density = a.Density
	s.Keep = a.Keep
	s.Floor = a.Floor
	return s
}

// Tinted creates a bilalgursen tinted prominent glass button with full 3D dome optics & press glow.
func Tinted(c ui.Color) Surface {
	return Surface{
		Kind:       KindDark,
		Preset:     PresetTinted,
		Tint:       c,
		Blur:       2.5,
		Shadow:     true,
		CursorLite: true,
	}
}

// BuildMaterial tracks pointer press and hover for bilalgursen's asymmetric internal press glow
// (fast luminous rise, slow calm fade, anchored where press landed, spreading outward) and
// spring-smoothed cursor-leaning optical rim light.
func (s Surface) BuildMaterial(e ui.Element) ui.Material {
	if !s.CursorLite && s.Preset == PresetCard {
		return s
	}
	isPressed := e.Pressed()
	isHovered := e.Hovered()

	pressTarget := float32(0)
	pressAnim := SpringGlowFade
	if isPressed {
		pressTarget = 1
		pressAnim = SpringGlowRise
	}
	pAmt := e.AnimateTo(pressAnimKey{}, pressTarget, pressAnim)

	// Spread grows smoothly from focused press point outward as the glow blooms & fades.
	spreadTarget := float32(0)
	if isPressed || pAmt > 0.01 {
		spreadTarget = 1
	}
	pSpread := e.AnimateTo(pressSpreadKey{}, spreadTarget, SpringGlowSpread)

	hoverTarget := float32(0)
	if isHovered {
		hoverTarget = 1
	}
	hAmt := e.AnimateTo(hoverAnimKey{}, hoverTarget, AnimColorFade)

	px, py, hasPt := e.PointerPosition()
	spot := ui.Local(e, "glass-press-spot", func() pressSpotState { return pressSpotState{} })
	if isPressed && !spot.wasPressed {
		spot.wasPressed = true
		if hasPt {
			spot.x, spot.y, spot.hasSpot = px, py, true
		}
	} else if !isPressed {
		spot.wasPressed = false
	}

	b := e.Bounds()
	cx, cy := b.W*0.5, b.H*0.5
	anchorX, anchorY := cx, cy
	if spot.hasSpot {
		anchorX, anchorY = spot.x, spot.y
	}

	// Localized cursor rim glint: jump immediately to cursor entry point on hover enter
	// (while LightMix is still 0) so the highlight never sweeps across the top edge,
	// and keep it parked at the last pointer position while LightMix fades out on leave.
	var lx, ly float32
	if isHovered && hasPt {
		spot.lastLX, spot.lastLY, spot.hasLightPt = px, py, true
		if !spot.wasHovered {
			lx = e.Jump(lightXAnimKey{}, px)
			ly = e.Jump(lightYAnimKey{}, py)
		} else {
			lx = e.AnimateTo(lightXAnimKey{}, px, SpringCursor)
			ly = e.AnimateTo(lightYAnimKey{}, py, SpringCursor)
		}
	} else if spot.hasLightPt {
		lx = e.AnimateTo(lightXAnimKey{}, spot.lastLX, SpringCursor)
		ly = e.AnimateTo(lightYAnimKey{}, spot.lastLY, SpringCursor)
	} else {
		lx, ly = cx, cy
	}
	spot.wasHovered = isHovered

	return interactiveSurface{
		Surface:     s,
		pressAmt:    clamp32(pAmt, 0, 1.25),
		pressSpread: clamp32(pSpread, 0, 1),
		pressX:      anchorX,
		pressY:      anchorY,
		lightX:      lx,
		lightY:      ly,
		lightMix:    hAmt,
	}
}

func (s Surface) NativeGlass() (ui.NativeGlassSpec, bool) { return ui.NativeGlassSpec{}, false }
func (interactiveSurface) NativeGlass() (ui.NativeGlassSpec, bool) {
	return ui.NativeGlassSpec{}, false
}

func (s Surface) String() string {
	k := "light"
	if s.Kind == KindDark {
		k = "dark"
	}
	return fmt.Sprintf("liquid-glass(%s)", k)
}

// Paint draws a physical Liquid Glass surface inside Painter.Draw callbacks.
func Paint(p *ui.Painter, box ui.Rect, radius float32, s Surface) {
	s.PaintMaterial(p, box, [4]float32{radius, radius, radius, radius})
}

func (s Surface) PaintMaterial(p *ui.Painter, box ui.Rect, radii [4]float32) {
	paintOptical(p, box, radii, interactiveSurface{Surface: s})
}

func (is interactiveSurface) PaintMaterial(p *ui.Painter, box ui.Rect, radii [4]float32) {
	paintOptical(p, box, radii, is)
}

func paintOptical(p *ui.Painter, box ui.Rect, radii [4]float32, is interactiveSurface) {
	if box.W <= 0 || box.H <= 0 {
		return
	}
	s := is.Surface
	rad := max(max(radii[0], radii[1]), max(radii[2], radii[3]))
	rad = min(rad, min(box.W, box.H)*0.5)

	var (
		mode        float32
		isDark      float32
		tintRGB     ui.Color
		frost       float32
		refract     float32
		bendCurve   float32
		edgeBand    float32
		dispersion  float32
		saturation  float32
		rimStrength float32
		innerStr    float32
		topRefl     float32
		caustic     float32
		mirrorAmt   float32
		borderAlpha float32
		borderW     float32
		blur        float32
	)

	if s.Kind == KindDark {
		isDark = 1.0
		tintRGB = ui.Hex("#1A1B1F")
	} else {
		isDark = 0.0
		tintRGB = ui.Hex("#FFFFFF")
	}

	switch s.Preset {
	case PresetTinted:
		// Faithful to bilalgursen GlassButton material="tinted": no flat white border ring!
		mode = 2.0
		tintRGB = s.Tint
		if tintRGB.A == 0 {
			tintRGB = ui.Hex("#007AFF")
		}
		frost = 0.0
		refract = 20.0
		bendCurve = 1.6
		edgeBand = 0.35
		dispersion = 0.0
		saturation = 1.0
		rimStrength = 0.55
		innerStr = 0.10
		topRefl = 0.28
		caustic = 0.08
		mirrorAmt = 0.0
		borderAlpha = 0.0
		borderW = 0.0
		blur = 2.2

	case PresetCard:
		// Faithful to bilalgursen glass-card.tsx CARD_OPTICS:
		// Radial bend, 5.5px frost, calm tint, subtle 1px hairline border (and NO stacked studio rim light).
		mode = 1.0
		if s.Kind == KindDark {
			frost = 0.58
			borderAlpha = 0.18
		} else {
			frost = 0.68
			borderAlpha = 0.42
		}
		refract = 18.0
		bendCurve = 9.75
		edgeBand = 0.35
		dispersion = 0.0
		saturation = 1.15
		rimStrength = 0.0
		innerStr = 0.04
		topRefl = 0.08
		caustic = 0.03
		mirrorAmt = 0.0
		borderW = 1.0
		blur = 5.5

	default:
		// PresetLens: bilalgursen GlassLayer clear optics — pure optical meniscus,
		// ZERO flat white border ring! Separation comes purely from curved refraction,
		// 3-band inner meniscus shading, directional end glints, and soft ambient shadow.
		mode = 0.0
		if s.Kind == KindDark {
			frost = clamp32(max(s.Floor, 0.22+0.20*s.Density), 0.18, 0.72)
			saturation = 1.32
		} else {
			frost = clamp32(max(s.Floor, 0.14+0.20*s.Density), 0.12, 0.72)
			saturation = 1.38
		}
		borderAlpha = 0.0
		borderW = 0.0
		refract = 28.0
		bendCurve = 1.6
		edgeBand = 0.38
		dispersion = 0.20
		rimStrength = 0.52
		innerStr = 0.11
		topRefl = 0.28
		caustic = 0.08
		mirrorAmt = 0.85
		blur = s.Blur
		if blur <= 0 {
			blur = 3.2
		}
	}

	// Accessibility: ReduceTransparency renders a clean solid surface
	if p.Preferences().ReduceTransparency {
		if s.Shadow {
			p.Shadow(box, rad, 0, 8, 22, 0, ui.RGBA(0, 0, 0, 0.14))
		}
		p.Fill(box, tintRGB.Alpha(0.96), rad)
		p.Stroke(box, ui.RGBA(0, 0, 0, 0.14), rad, 1)
		return
	}

	// 1. Bilalgursen twin-layer soft optical drop shadow (tight contact + ambient float)
	if s.Shadow {
		if s.Preset == PresetCard {
			p.Shadow(box, rad, 0, 14, 34, 0, ui.RGBA(0, 0, 0, 0.09))
			p.Shadow(box, rad, 0, 24, 52, 0, ui.RGBA(0, 0, 0, 0.08))
		} else if s.Preset == PresetTinted {
			p.Shadow(box, rad, 0, 6, 18, 0, tintRGB.Alpha(0.32))
			p.Shadow(box, rad, 0, 2, 6, 0, ui.RGBA(0, 0, 0, 0.14))
		} else if s.Kind == KindDark {
			p.Shadow(box, rad, 0, 8, 24, 0, ui.RGBA(0, 0, 0, 0.30))
			p.Shadow(box, rad, 0, 2, 6, 0, ui.RGBA(0, 0, 0, 0.18))
		} else {
			p.Shadow(box, rad, 0, 8, 24, 0, ui.RGBA(0, 0, 0, 0.10))
			p.Shadow(box, rad, 0, 2, 6, 0, ui.RGBA(0, 0, 0, 0.06))
		}
	}

	// Resolve local cursor light & press coordinates relative to box center
	lightLocalX := is.lightX - box.W*0.5
	lightLocalY := is.lightY - box.H*0.5
	pressLocalX := is.pressX - box.W*0.5
	pressLocalY := is.pressY - box.H*0.5

	// 2. Render physical Liquid Glass GPU shader!
	vals := liquidGlassShader.NewValues()
	vals.Float(uRadius, rad)
	vals.Float(uMode, mode)
	vals.Float(uIsDark, isDark)
	vals.Float(uFrost, frost)
	vals.Float(uRefract, refract)
	vals.Float(uBendCurve, bendCurve)
	vals.Float(uEdgeBand, edgeBand)
	vals.Float(uDispersion, dispersion)
	vals.Float(uSaturation, saturation)
	vals.Float(uRimStrength, rimStrength)
	vals.Float(uInnerStrength, innerStr)
	vals.Float(uTopRefl, topRefl)
	vals.Float(uCaustic, caustic)
	vals.Float(uMirrorAmt, mirrorAmt)
	vals.Float(uBorderAlpha, borderAlpha)
	vals.Float(uBorderWidth, borderW)
	vals.Color(uTintColor, tintRGB)
	vals.Vec2(uLightPos, lightLocalX, lightLocalY)
	vals.Float(uLightMix, is.lightMix)
	vals.Vec2(uPressPos, pressLocalX, pressLocalY)
	vals.Float(uPressAmt, is.pressAmt)
	vals.Float(uPressSpread, is.pressSpread)

	liquidGlassShader.Draw(p, box, shader.Options{
		BackdropBlur: blur,
		Radius:       rad,
		Values:       vals,
	})
}

func clamp32(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
