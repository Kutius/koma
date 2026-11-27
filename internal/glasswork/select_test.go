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

	tt := ui.NewTester(view, 400, 320)
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
	tt.Advance(500 * time.Millisecond)
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
	tt.Advance(600 * time.Millisecond)
	if tt.HasText("Same day") {
		t.Fatalf("expected popup unmounted after close animation; texts=%q", tt.Texts())
	}

	// Keyboard: open with ArrowDown, move past disabled Pickup to Same day, confirm with Enter
	tt.Key(0, ui.KeyDown)
	if !open {
		t.Fatalf("expected KeyDown on focused trigger to open select")
	}
	tt.Key(0, ui.KeyDown) // Express -> skips disabled Pickup -> Same day
	tt.Key(0, ui.KeyEnter)
	if selected != "sameday" || pickedCount != 2 {
		t.Fatalf("after keyboard nav skipping disabled item: selected=%q picked=%d", selected, pickedCount)
	}
	tt.Advance(600 * time.Millisecond)

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
	tt.Advance(600 * time.Millisecond)
	if tt.HasText("Express") {
		t.Fatalf("expected popup gone after Escape close animation; texts=%q", tt.Texts())
	}
}
