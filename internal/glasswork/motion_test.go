package glasswork

import (
	"math"
	"testing"
	"time"
)

func TestLiquidSpringPresetsValid(t *testing.T) {
	springs := []struct {
		name      string
		anim      time.Duration
		p100      float64
		noBounce  bool
		max90AtMs time.Duration // must reach >= 90% of target by this time (Apple Fluid Interfaces responsiveness)
	}{
		{"SpringSnap", SpringSnap.Spring.SettleTime(), SpringSnap.Spring.Progress(100 * time.Millisecond), true, 125 * time.Millisecond},
		{"SpringPress", SpringPress.Spring.SettleTime(), SpringPress.Spring.Progress(100 * time.Millisecond), false, 120 * time.Millisecond},
		{"SpringHoverX", SpringHoverX.Spring.SettleTime(), SpringHoverX.Spring.Progress(100 * time.Millisecond), false, 120 * time.Millisecond},
		{"SpringHoverY", SpringHoverY.Spring.SettleTime(), SpringHoverY.Spring.Progress(100 * time.Millisecond), false, 150 * time.Millisecond},
		{"SpringSlide", SpringSlide.Spring.SettleTime(), SpringSlide.Spring.Progress(100 * time.Millisecond), false, 130 * time.Millisecond},
		{"SpringToolbar", SpringToolbar.Spring.SettleTime(), SpringToolbar.Spring.Progress(100 * time.Millisecond), true, 140 * time.Millisecond},
		{"SpringPanel", SpringPanel.Spring.SettleTime(), SpringPanel.Spring.Progress(100 * time.Millisecond), true, 165 * time.Millisecond},
		{"SpringCursor", SpringCursor.Spring.SettleTime(), SpringCursor.Spring.Progress(100 * time.Millisecond), true, 125 * time.Millisecond},
		{"SpringGlowRise", SpringGlowRise.Spring.SettleTime(), SpringGlowRise.Spring.Progress(100 * time.Millisecond), false, 100 * time.Millisecond},
		{"SpringGlowFade", SpringGlowFade.Spring.SettleTime(), SpringGlowFade.Spring.Progress(100 * time.Millisecond), true, 190 * time.Millisecond},
		{"SpringGlowSpread", SpringGlowSpread.Spring.SettleTime(), SpringGlowSpread.Spring.Progress(100 * time.Millisecond), true, 165 * time.Millisecond},
		{"SpringPullFollow", SpringPullFollow.Spring.SettleTime(), SpringPullFollow.Spring.Progress(100 * time.Millisecond), true, 80 * time.Millisecond},
		{"SpringPullRelease", SpringPullRelease.Spring.SettleTime(), SpringPullRelease.Spring.Progress(100 * time.Millisecond), false, 165 * time.Millisecond},
		{"SpringContent", SpringContent.Spring.SettleTime(), SpringContent.Spring.Progress(100 * time.Millisecond), true, 150 * time.Millisecond},
		{"SpringSelectOpen", SpringSelectOpen.Spring.SettleTime(), SpringSelectOpen.Spring.Progress(100 * time.Millisecond), true, 170 * time.Millisecond},
		{"SpringSelectClose", SpringSelectClose.Spring.SettleTime(), SpringSelectClose.Spring.Progress(100 * time.Millisecond), true, 115 * time.Millisecond},
		{"SpringNeckAttach", SpringNeckAttach.Spring.SettleTime(), SpringNeckAttach.Spring.Progress(100 * time.Millisecond), true, 100 * time.Millisecond},
		{"SpringNeckSnap", SpringNeckSnap.Spring.SettleTime(), SpringNeckSnap.Spring.Progress(100 * time.Millisecond), true, 145 * time.Millisecond},
		{"SpringSelectChevron", SpringSelectChevron.Spring.SettleTime(), SpringSelectChevron.Spring.Progress(100 * time.Millisecond), false, 130 * time.Millisecond},
	}

	springOpts := map[string]struct {
		progress func(time.Duration) float64
	}{
		"SpringSnap":          {SpringSnap.Spring.Progress},
		"SpringPress":         {SpringPress.Spring.Progress},
		"SpringHoverX":        {SpringHoverX.Spring.Progress},
		"SpringHoverY":        {SpringHoverY.Spring.Progress},
		"SpringSlide":         {SpringSlide.Spring.Progress},
		"SpringToolbar":       {SpringToolbar.Spring.Progress},
		"SpringPanel":         {SpringPanel.Spring.Progress},
		"SpringCursor":        {SpringCursor.Spring.Progress},
		"SpringGlowRise":      {SpringGlowRise.Spring.Progress},
		"SpringGlowFade":      {SpringGlowFade.Spring.Progress},
		"SpringGlowSpread":    {SpringGlowSpread.Spring.Progress},
		"SpringPullFollow":    {SpringPullFollow.Spring.Progress},
		"SpringPullRelease":   {SpringPullRelease.Spring.Progress},
		"SpringContent":       {SpringContent.Spring.Progress},
		"SpringSelectOpen":    {SpringSelectOpen.Spring.Progress},
		"SpringSelectClose":   {SpringSelectClose.Spring.Progress},
		"SpringNeckAttach":    {SpringNeckAttach.Spring.Progress},
		"SpringNeckSnap":      {SpringNeckSnap.Spring.Progress},
		"SpringSelectChevron": {SpringSelectChevron.Spring.Progress},
	}

	for _, s := range springs {
		if s.anim <= 0 {
			t.Errorf("%s expected positive settle time, got %v", s.name, s.anim)
		}
		if math.IsNaN(s.p100) || math.IsInf(s.p100, 0) {
			t.Errorf("%s progress is invalid: %v", s.name, s.p100)
		}
		progFn := springOpts[s.name].progress
		if got := progFn(s.max90AtMs); got < 0.90 {
			t.Errorf("%s too sluggish: expected progress >= 0.90 by %v, got %.3f (settle=%v)", s.name, s.max90AtMs, got, s.anim)
		}
		if s.noBounce {
			for ms := 10; ms <= 400; ms += 10 {
				if p := progFn(time.Duration(ms) * time.Millisecond); p > 1.001 {
					t.Errorf("%s is critically damped and must not overshoot: got %.4f at %dms", s.name, p, ms)
					break
				}
			}
		}
	}

	// Verify out-of-phase hover springs actually diverge mid-flight (droplet wobble!)
	// while settling briskly without lingering oscillation.
	px := SpringHoverX.Spring.Progress(80 * time.Millisecond)
	py := SpringHoverY.Spring.Progress(80 * time.Millisecond)
	if math.Abs(px-py) < 0.05 {
		t.Errorf("expected out-of-phase hover X/Y springs to diverge at 80ms, got px=%.3f py=%.3f", px, py)
	}
	if stY := SpringHoverY.Spring.SettleTime(); stY > 750*time.Millisecond {
		t.Errorf("SpringHoverY should settle crisply (<750ms), got %v", stY)
	}

	// Asymmetric exit faster than enter on Select dropdown.
	if SpringSelectClose.Spring.SettleTime() >= SpringSelectOpen.Spring.SettleTime() {
		t.Errorf("SpringSelectClose (%v) should be faster than SpringSelectOpen (%v)",
			SpringSelectClose.Spring.SettleTime(), SpringSelectOpen.Spring.SettleTime())
	}
}

func TestRubberBandPullAndStretch(t *testing.T) {
	if got := rubberBandPull(0, 10); got != 0 {
		t.Fatalf("expected 0 at rest, got %v", got)
	}
	pSmall := rubberBandPull(5, 10)
	pBig := rubberBandPull(500, 10)
	if pSmall <= 0 || pSmall >= pBig || pBig >= 10 {
		t.Fatalf("expected monotonic bounded rubberband: small=%v big=%v", pSmall, pBig)
	}
	if math.Abs(float64(rubberBandPull(-20, 10)+rubberBandPull(20, 10))) > 1e-5 {
		t.Fatalf("expected odd symmetry in rubberBandPull")
	}

	// Horizontal pull should translate right, stretch X > 1, and thin Y < 1 (liquid volume conservation).
	tx, ty, sx, sy := computePullStretch(60, 0, 140, 44)
	if tx <= 0 || ty != 0 {
		t.Fatalf("unexpected translation for horizontal pull: tx=%v ty=%v", tx, ty)
	}
	if sx <= 1.0 || sy >= 1.0 {
		t.Fatalf("expected stretch along X and thinning along Y, got sx=%v sy=%v", sx, sy)
	}
}

func TestTransitionsNeverScaleFromZero(t *testing.T) {
	pt := PanelTransition(0, 10)
	if pt.Enter == nil || pt.Enter.Scale < 0.9 {
		t.Fatalf("panel transition should enter from >= 0.9 scale, got %+v", pt.Enter)
	}
	if pt.ExitDuration <= 0 || pt.ExitDuration > 110*time.Millisecond {
		t.Fatalf("panel exit should be crisp (<=110ms), got %v", pt.ExitDuration)
	}

	tt := ToolbarAppearTransition(6)
	if tt.ExitDuration <= 0 || tt.ExitDuration > 90*time.Millisecond {
		t.Fatalf("toolbar exit should be ultra-crisp (<=90ms), got %v", tt.ExitDuration)
	}

	mt := MorphBarTransition(8)
	if mt.ExitDuration <= 0 || mt.ExitDuration > 100*time.Millisecond {
		t.Fatalf("morph bar exit should be crisp (<=100ms), got %v", mt.ExitDuration)
	}

	st := StaggeredEnterTransition(20)
	if st.Animation == nil || st.Animation.Delay > 100*time.Millisecond {
		t.Fatalf("stagger delay should be brisk and capped (<=100ms), got %v", st.Animation.Delay)
	}
}
