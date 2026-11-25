package app

import (
	"image"
	"runtime"
	"testing"
	"weak"

	"github.com/egoist/mygo/ui"

	"koma/internal/canvas"
	"koma/internal/winapi"
)

func TestEditorNoDuplicateRasterImgWithoutRasterAnnotations(t *testing.T) {
	a := New()
	ed := NewEditorState(a)

	img := image.NewRGBA(image.Rect(0, 0, 320, 200))
	ed.loadOwnedImage(img, nil, "test")

	if ed.baseImg != img {
		t.Fatalf("expected loadOwnedImage to adopt img without extra CloneRGBA")
	}
	if ed.rasterImg != nil {
		t.Fatalf("expected rasterImg == nil when no raster annotations exist")
	}
	if ed.rasterBmp == nil {
		t.Fatalf("expected rasterBmp to be initialized directly from baseImg")
	}
	bmpPtr := ed.rasterBmp

	// Adding a raster annotation creates rasterImg and reuses the same *ui.Bitmap via Update().
	ed.annotations = []canvas.Annotation{
		{
			ID:        1,
			Kind:      canvas.ToolPixelate,
			Start:     canvas.PointF{X: 10, Y: 10},
			End:       canvas.PointF{X: 100, Y: 100},
			BlockSize: 10,
		},
	}
	ed.rasterDirty = true
	ed.rebuildCaches()

	if ed.rasterImg == nil {
		t.Fatalf("expected rasterImg != nil when pixelate annotation exists")
	}
	if ed.rasterBmp != bmpPtr {
		t.Fatalf("expected rasterBmp to be updated in place rather than allocating a new GPU texture")
	}

	// Removing raster annotations releases rasterImg and keeps the same bitmap.
	rasterWeak := weak.Make(ed.rasterImg)
	ed.annotations = nil
	ed.rasterDirty = true
	ed.rebuildCaches()
	if ed.rasterImg != nil {
		t.Fatalf("expected rasterImg == nil after clearing raster annotations")
	}
	if ed.rasterBmp != bmpPtr {
		t.Fatalf("expected rasterBmp to stay reused after reverting to baseImg")
	}

	runtime.GC()
	runtime.GC()
	if rasterWeak.Value() != nil {
		t.Fatalf("expected old rasterImg to be garbage-collected after removing raster annotations")
	}
	runtime.KeepAlive(ed)
}

func TestEditorParkAndRestoreForTrayReleasesUncompressedPixels(t *testing.T) {
	a := New()
	ed := NewEditorState(a)

	img := image.NewRGBA(image.Rect(0, 0, 400, 300))
	img.Pix[0], img.Pix[1], img.Pix[2], img.Pix[3] = 42, 128, 210, 255
	ed.loadOwnedImage(img, nil, "park-test")
	ed.annotations = []canvas.Annotation{
		{ID: 1, Kind: canvas.ToolArrow, Start: canvas.PointF{X: 10, Y: 10}, End: canvas.PointF{X: 80, Y: 80}, Color: ui.Hex("#007AFF"), StrokeWidth: 4},
	}
	ed.pushUndo()

	weakBase := weak.Make(ed.baseImg)
	weakBmp := weak.Make(ed.rasterBmp)
	img = nil

	ed.ParkForTray()
	if ed.baseImg != nil || ed.rasterImg != nil || ed.rasterBmp != nil {
		t.Fatalf("expected ParkForTray to drop baseImg, rasterImg, and rasterBmp")
	}
	if len(ed.basePNG) == 0 {
		t.Fatalf("expected ParkForTray to keep compressed PNG bytes for transparent restore")
	}

	runtime.GC()
	runtime.GC()
	if weakBase.Value() != nil || weakBmp.Value() != nil {
		t.Fatalf("expected parked baseImg and rasterBmp to be garbage-collected")
	}

	// ExportComposite still works while parked (without keeping baseImg resident).
	if comp := ed.ExportComposite(); comp == nil || comp.Bounds().Dx() != 400 || comp.Bounds().Dy() != 300 {
		t.Fatalf("expected ExportComposite to work while parked, got %v", comp)
	}
	if ed.baseImg != nil {
		t.Fatalf("expected ExportComposite while parked not to leave baseImg resident")
	}

	// Reopening Studio restores baseImg and keeps annotations intact.
	ed.RestoreFromTray()
	if ed.baseImg == nil || ed.baseImg.Bounds().Dx() != 400 || ed.baseImg.Bounds().Dy() != 300 {
		t.Fatalf("expected RestoreFromTray to restore 400x300 baseImg")
	}
	if ed.baseImg.Pix[0] != 42 || ed.baseImg.Pix[1] != 128 || ed.baseImg.Pix[2] != 210 {
		t.Fatalf("restored pixel mismatch: got (%d,%d,%d)", ed.baseImg.Pix[0], ed.baseImg.Pix[1], ed.baseImg.Pix[2])
	}
	if len(ed.annotations) != 1 {
		t.Fatalf("expected annotations to survive ParkForTray/RestoreFromTray, got %d", len(ed.annotations))
	}
	runtime.KeepAlive(ed)
}

func TestOverlayReleaseBuffersFreesCaptureAndLoupe(t *testing.T) {
	a := New()
	img := GenerateSampleCanvas()
	ov := NewTestOverlayView(a, img, canvas.ToolPixelate)
	tt := ui.NewTester(ov.View, 860, 520)
	tt.Move(300, 200)
	tt.Frame()

	weakImg := weak.Make(img)
	weakBmp := weak.Make(ov.pane.bmp)
	img = nil

	ov.pane.releaseBuffers()
	tt = nil

	runtime.GC()
	runtime.GC()
	if weakImg.Value() != nil || weakBmp.Value() != nil {
		t.Fatalf("expected overlay capture image and bitmap to be garbage-collected after releaseBuffers")
	}
	runtime.KeepAlive(ov)
}

func TestHistoryByteBudgetEvictsOldEntries(t *testing.T) {
	a := New()
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	// Each fake payload is 9 MiB: budget is 24 MiB, so at most 2 entries fit.
	big1 := make([]byte, 9<<20)
	big2 := make([]byte, 9<<20)
	big3 := make([]byte, 9<<20)
	weakOld := weak.Make(&big1[0])

	a.AddHistoryEntryWithPNG(img, big1, "first")
	big1 = nil
	a.AddHistoryEntryWithPNG(img, big2, "second")
	big2 = nil
	a.AddHistoryEntryWithPNG(img, big3, "third")
	big3 = nil

	a.mu.Lock()
	n := len(a.history)
	a.mu.Unlock()
	if n != 2 {
		t.Fatalf("expected history trimmed to 2 entries under 24 MiB cap, got %d", n)
	}
	runtime.GC()
	runtime.GC()
	if weakOld.Value() != nil {
		t.Fatalf("expected evicted history PNG buffer to be garbage-collected")
	}
	runtime.KeepAlive(a)
}

func TestWinAPICallbackReuse(t *testing.T) {
	// Calling ListMonitors and ListVisibleWindows repeatedly must reuse a single
	// static Win32 callback slot rather than leaking syscall.NewCallback entries.
	for i := 0; i < 20; i++ {
		_ = winapi.ListMonitors()
		_ = winapi.ListVisibleWindows()
	}
	winapi.TrimWorkingSet()
}
