package glasswork

import (
	"math"
	"testing"
	"time"
)

func TestLiquidSpringPresetsValid(t *testing.T) {
	springs := []struct {
		name string
		opt  time.Duration
		p    float64
	}{
		{"SpringPress", SpringPress.Spring.SettleTime(), SpringPress.Spring.Progress(100 * time.Millisecond)},
		{"SpringHoverX", SpringHoverX.Spring.SettleTime(), SpringHoverX.Spring.Progress(100 * time.Millisecond)},
		{"SpringHoverY", SpringHoverY.Spring.SettleTime(), SpringHoverY.Spring.Progress(100 * time.Millisecond)},
		{"SpringSlide", SpringSlide.Spring.SettleTime(), SpringSlide.Spring.Progress(100 * time.Millisecond)},
		{"SpringPanel", SpringPanel.Spring.SettleTime(), SpringPanel.Spring.Progress(100 * time.Millisecond)},
		{"SpringGlowRise", SpringGlowRise.Spring.SettleTime(), SpringGlowRise.Spring.Progress(100 * time.Millisecond)},
		{"SpringGlowFade", SpringGlowFade.Spring.SettleTime(), SpringGlowFade.Spring.Progress(100 * time.Millisecond)},
		{"SpringPullFollow", SpringPullFollow.Spring.SettleTime(), SpringPullFollow.Spring.Progress(100 * time.Millisecond)},
		{"SpringPullRelease", SpringPullRelease.Spring.SettleTime(), SpringPullRelease.Spring.Progress(100 * time.Millisecond)},
	}
	for _, s := range springs {
		if s.opt <= 0 {
			t.Errorf("%s expected positive settle time, got %v", s.name, s.opt)
		}
		if math.IsNaN(s.p) || math.IsInf(s.p, 0) {
			t.Errorf("%s progress is invalid: %v", s.name, s.p)
		}
	}

	// Verify out-of-phase hover springs actually diverge mid-flight (droplet wobble!)
	px := SpringHoverX.Spring.Progress(120 * time.Millisecond)
	py := SpringHoverY.Spring.Progress(120 * time.Millisecond)
	if math.Abs(px-py) < 0.05 {
		t.Errorf("expected out-of-phase hover X/Y springs to diverge at 120ms, got px=%.3f py=%.3f", px, py)
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
	if pt.ExitDuration <= 0 || pt.ExitDuration >= 200*time.Millisecond {
		t.Fatalf("panel exit should be snappy (<200ms), got %v", pt.ExitDuration)
	}

	st := StaggeredEnterTransition(20)
	if st.Animation == nil || st.Animation.Delay > 200*time.Millisecond {
		t.Fatalf("stagger delay should be capped for long lists, got %v", st.Animation.Delay)
	}
}
