package glasswork

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestGlassSelectClickKeyboardAndEscape(t *testing.T) {
	selected := "standard"
	open := false
	pickedCount := 0
	var lastPicked string

	opts := []SelectOption[string]{
		{Value: "standard", Label: "Standard"},
		{Value: "express", Label: "Express"},
		{Value: "pickup", Label: "Pickup", Disabled: true},
		{Value: "sameday", Label: "Same day"},
	}

	view := func(c *ui.Context) {
		ui.Box(c).Fill().Background(ui.Hex("#E3E9F3")).Center().Children(func() {
			Select(c, SelectProps[string]{
				Key:      "delivery-select",
				Label:    "Delivery",
				Selected: &selected,
				Open:     &open,
				Width:    180,
				Height:   32,
				Kind:     KindLight,
				Options:  opts,
				OnSelect: func(v string) {
					pickedCount++
					lastPicked = v
				},
			})
		})
	}

	tt := ui.NewTester(view, 280, 240)
	if !tt.HasText("Standard") || tt.HasText("Express") {
		t.Fatalf("closed select should show only selected label; texts=%q", tt.Texts())
	}

	// Click trigger to open popup
	if err := tt.Click("Delivery"); err != nil {
		t.Fatalf("click trigger: %v", err)
	}
	if !open || !tt.HasText("Express") || !tt.HasText("Same day") {
		t.Fatalf("expected popup open with all options; open=%v texts=%q", open, tt.Texts())
	}

	// Advance partway to exercise the stretching liquid neck shader without panicking
	tt.Advance(45 * time.Millisecond)
	_ = tt.Image()

	// Advance to settled open state
	tt.Advance(380 * time.Millisecond)
	_ = tt.Image()

	// Click an option
	if err := tt.Click("Express"); err != nil {
		t.Fatalf("click Express: %v", err)
	}
	if selected != "express" || pickedCount != 1 || lastPicked != "express" {
		t.Fatalf("after clicking Express: selected=%q picked=%d last=%q", selected, pickedCount, lastPicked)
	}
	if open {
		t.Fatalf("expected caller Open flag to be false immediately on selection")
	}

	// Advance through reverse-close animation until fully unmounted
	tt.Advance(320 * time.Millisecond)
	if tt.HasText("Same day") {
		t.Fatalf("expected popup unmounted after close animation; texts=%q", tt.Texts())
	}
}

func TestGlassSelectKeyboardNavigationAndEscape(t *testing.T) {
	selected := "standard"
	open := false
	pickedCount := 0
	var lastPicked string

	opts := []SelectOption[string]{
		{Value: "standard", Label: "Standard"},
		{Value: "express", Label: "Express"},
		{Value: "pickup", Label: "Pickup", Disabled: true},
		{Value: "sameday", Label: "Same day"},
	}

	view := func(c *ui.Context) {
		ui.Box(c).Fill().Background(ui.Hex("#E3E9F3")).Center().Children(func() {
			Select(c, SelectProps[string]{
				Key:      "delivery-select-kbd",
				Label:    "Delivery",
				Selected: &selected,
				Open:     &open,
				Width:    180,
				Height:   32,
				Kind:     KindLight,
				Options:  opts,
				OnSelect: func(v string) {
					pickedCount++
					lastPicked = v
				},
			})
		})
	}

	tt := ui.NewTesterWith(view, 280, 240, ui.TesterOptions{
		Preferences: ui.Preferences{ReduceMotion: true, ReduceTransparency: true},
	})

	// Focus trigger first via click + close, then open with ArrowDown
	if err := tt.Click("Delivery"); err != nil {
		t.Fatalf("focus click trigger: %v", err)
	}
	tt.Key(0, ui.KeyEscape)
	if open {
		t.Fatalf("expected Escape to close select")
	}

	// Keyboard: open with ArrowDown, move past disabled Pickup to Same day, confirm with Enter
	tt.Key(0, ui.KeyDown)
	if !open {
		t.Fatalf("expected KeyDown on focused trigger to open select")
	}
	tt.Key(0, ui.KeyDown) // Express -> skips disabled Pickup -> Same day
	tt.Key(0, ui.KeyDown) // Standard -> Express -> skips Pickup -> Same day
	tt.Key(0, ui.KeyEnter)
	if selected != "sameday" || pickedCount != 1 || lastPicked != "sameday" {
		t.Fatalf("after keyboard nav skipping disabled item: selected=%q picked=%d last=%q", selected, pickedCount, lastPicked)
	}

	// Open again and dismiss with Escape without changing selection
	if err := tt.Click("Delivery"); err != nil {
		t.Fatalf("re-open trigger: %v", err)
	}
	if !open {
		t.Fatalf("expected select open before Escape")
	}
	tt.Key(0, ui.KeyEscape)
	if open {
		t.Fatalf("expected Escape to close select")
	}
	if selected != "sameday" {
		t.Fatalf("Escape should keep selected=%q", selected)
	}
	if tt.HasText("Express") {
		t.Fatalf("expected popup gone after Escape close; texts=%q", tt.Texts())
	}
}

func TestGlassSelectReduceMotionInstantOpenClose(t *testing.T) {
	selected := "a"
	open := false
	opts := []SelectOption[string]{
		{Value: "a", Label: "Alpha"},
		{Value: "b", Label: "Beta"},
	}
	view := func(c *ui.Context) {
		ui.Box(c).Fill().Center().Children(func() {
			Select(c, SelectProps[string]{
				Key:      "rm-select",
				Label:    "Pick",
				Selected: &selected,
				Open:     &open,
				Width:    140,
				Height:   28,
				Kind:     KindDark,
				Options:  opts,
			})
		})
	}
	tt := ui.NewTesterWith(view, 240, 200, ui.TesterOptions{
		Preferences: ui.Preferences{ReduceMotion: true, ReduceTransparency: true},
	})
	if err := tt.Click("Pick"); err != nil {
		t.Fatalf("click trigger: %v", err)
	}
	if !open || !tt.HasText("Beta") {
		t.Fatalf("expected instant open under ReduceMotion; open=%v texts=%q", open, tt.Texts())
	}
	if err := tt.Click("Beta"); err != nil {
		t.Fatalf("click Beta: %v", err)
	}
	if selected != "b" || open || tt.HasText("Alpha") {
		t.Fatalf("expected instant close under ReduceMotion; selected=%q open=%v texts=%q", selected, open, tt.Texts())
	}
}
