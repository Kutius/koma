package app

import (
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"koma/internal/canvas"
)

func TestScrollBarWindowHoverTooltipNoFlicker(t *testing.T) {
	cases := []struct {
		name      string
		selDip    ui.Rect
		dispW     int
		dispH     int
		wantBelow bool // expected tooltip side relative to the toolbar
	}{
		{
			name:      "below_selection",
			selDip:    ui.Rect{X: 200, Y: 100, W: 600, H: 500},
			dispW:     1920,
			dispH:     1080,
			wantBelow: true,
		},
		{
			name:      "near_bottom_flips_above_selection",
			selDip:    ui.Rect{X: 200, Y: 400, W: 600, H: 600},
			dispW:     1920,
			dispH:     1080,
			wantBelow: false,
		},
		{
			name:      "near_top_below_selection",
			selDip:    ui.Rect{X: 200, Y: 10, W: 600, H: 300},
			dispW:     1920,
			dispH:     1080,
			wantBelow: true,
		},
		{
			name:      "fullscreen_docked_inside_bottom",
			selDip:    ui.Rect{X: 0, Y: 0, W: 1920, H: 1080},
			dispW:     1920,
			dispH:     1080,
			wantBelow: false,
		},
	}

	first := GenerateSampleCanvas()
	reproDir := os.Getenv("KOMA_REPRO_DIR")

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := New()
			disp := mygo.Display{
				Bounds:      mygo.Rectangle{Width: tc.dispW, Height: tc.dispH},
				ScaleFactor: 1,
				Primary:     true,
			}
			s := newScrollSession(a, ScrollCaptureParams{
				Display:    disp,
				SelDip:     tc.selDip,
				PhysRect:   image.Rect(int(tc.selDip.X), int(tc.selDip.Y), int(tc.selDip.X+tc.selDip.W), int(tc.selDip.Y+tc.selDip.H)),
				FirstFrame: first,
			}, first)

			barWinR := computeScrollBarWindowRect(s.selDip, s.barCardR, float32(tc.dispW), float32(tc.dispH))
			s.barWinOffX, s.barWinOffY = barWinR.X, barWinR.Y

			// Verify barWin stays inside screen and never intrudes into selDip when outside it.
			if barWinR.X < 0 || barWinR.Y < 0 || barWinR.X+barWinR.W > float32(tc.dispW)+0.5 || barWinR.Y+barWinR.H > float32(tc.dispH)+0.5 {
				t.Fatalf("barWinR %+v outside display %dx%d", barWinR, tc.dispW, tc.dispH)
			}
			if s.barCardR.Y >= s.selDip.Y+s.selDip.H && barWinR.Y < s.selDip.Y+s.selDip.H+1.5 {
				t.Fatalf("barWinR %+v intrudes upward into selDip %+v", barWinR, s.selDip)
			}
			if s.barCardR.Y+s.barCardR.H <= s.selDip.Y && barWinR.Y+barWinR.H > s.selDip.Y-1.5 {
				t.Fatalf("barWinR %+v intrudes downward into selDip %+v", barWinR, s.selDip)
			}
			if s.showPrev {
				ovX := barWinR.X < s.prevCardR.X+s.prevCardR.W && barWinR.X+barWinR.W > s.prevCardR.X
				ovY := barWinR.Y < s.prevCardR.Y+s.prevCardR.H && barWinR.Y+barWinR.H > s.prevCardR.Y
				if ovX && ovY {
					t.Fatalf("barWinR %+v overlaps prevCardR %+v", barWinR, s.prevCardR)
				}
			}

			tt := ui.NewTester(s.barView, int(barWinR.W), int(barWinR.H))
			barMidY := (s.barCardR.Y - s.barWinOffY) + s.barCardR.H*0.5

			// Hover "重置" past tooltip delay, then sweep across "编辑", "贴图", "完成".
			r1, ok := tt.Find("重置")
			if !ok {
				t.Fatalf("could not find 重置 button")
			}
			tt.Move(r1.X+r1.W/2, r1.Y+r1.H/2)
			tt.Advance(650 * time.Millisecond)

			targets := []struct {
				btn string
				tip string
			}{
				{"重置", "重置长截图"},
				{"编辑", "在工作台编辑长截图"},
				{"贴图", "贴图到桌面"},
				{"完成", "完成并复制到剪贴板"},
			}
			for _, tgt := range targets {
				br, ok := tt.Find(tgt.btn)
				if !ok {
					t.Fatalf("could not find %q button", tgt.btn)
				}
				tt.Move(br.X+br.W/2, br.Y+br.H/2)
				var firstY float32
				var seen bool
				for frame := 0; frame < 10; frame++ {
					tipR, ok := tt.Find(tgt.tip)
					if !ok {
						t.Fatalf("%s frame %d: tooltip %q not visible (texts=%q)", tgt.btn, frame, tgt.tip, tt.Texts())
					}
					if tipR.H < 14 {
						t.Fatalf("%s frame %d: tooltip %q squashed to H=%.1f (rect=%+v)", tgt.btn, frame, tgt.tip, tipR.H, tipR)
					}
					if tipR.Y < 0 || tipR.Y+tipR.H > barWinR.H {
						t.Fatalf("%s frame %d: tooltip %q clipped outside barWin H=%.1f (rect=%+v)", tgt.btn, frame, tgt.tip, barWinR.H, tipR)
					}
					isBelow := tipR.Y+tipR.H*0.5 > barMidY
					if isBelow != tc.wantBelow {
						t.Fatalf("%s frame %d: tooltip side below=%v, want %v (barMidY=%.1f rect=%+v)", tgt.btn, frame, isBelow, tc.wantBelow, barMidY, tipR)
					}
					if !seen {
						firstY = tipR.Y
						seen = true
					} else if diff := tipR.Y - firstY; diff < -3 || diff > 3 {
						t.Fatalf("%s frame %d: tooltip jumped vertically from %.1f to %.1f (rect=%+v)", tgt.btn, frame, firstY, tipR.Y, tipR)
					}
					if reproDir != "" && tgt.btn == "编辑" && frame == 2 {
						_ = os.MkdirAll(reproDir, 0o755)
						if b, err := canvas.EncodePNG(tt.Image()); err == nil {
							_ = os.WriteFile(filepath.Join(reproDir, "fixed_"+tc.name+".png"), b, 0o644)
						}
					}
					tt.Advance(16 * time.Millisecond)
				}
			}
		})
	}
}
