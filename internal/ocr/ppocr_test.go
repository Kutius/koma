package ocr

import (
	"context"
	"image/color"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestNormalizeEngineAndDisplayName(t *testing.T) {
	if got := NormalizeEngine("ppocrv6"); got != EnginePPOCRv6 {
		t.Fatalf("NormalizeEngine(ppocrv6) = %q, want %q", got, EnginePPOCRv6)
	}
	if got := NormalizeEngine("  PPOCRV6 "); got != EnginePPOCRv6 {
		t.Fatalf("NormalizeEngine(PPOCRV6) = %q, want %q", got, EnginePPOCRv6)
	}
	if got := NormalizeEngine("unknown"); got != EngineSystem {
		t.Fatalf("NormalizeEngine(unknown) = %q, want %q", got, EngineSystem)
	}
	if got := EnginePPOCRv6.DisplayName(); got != "PP-OCRv6" {
		t.Fatalf("EnginePPOCRv6.DisplayName() = %q", got)
	}
	if got := EngineSystem.DisplayName(); got != "系统原生" {
		t.Fatalf("EngineSystem.DisplayName() = %q", got)
	}
}

func TestParseCharacterListAndDecodeCTC(t *testing.T) {
	chars := parseCharacterList("K\no\nm\na\n截\n图\n")
	// Index 0 = blank, 1..6 = characters, 7 = trailing space
	if len(chars) != 8 {
		t.Fatalf("len(chars) = %d, want 8 (%v)", len(chars), chars)
	}
	if chars[0] != "" || chars[1] != "K" || chars[6] != "图" || chars[7] != " " {
		t.Fatalf("unexpected char table: %#v", chars)
	}

	// Simulate 7 timesteps x 8 classes: K K blank o m m a
	T, C := 7, len(chars)
	logits := make([]float32, T*C)
	seq := []int{1, 1, 0, 2, 3, 3, 4}
	for ti, cls := range seq {
		logits[ti*C+cls] = 0.96
	}
	txt, score := decodeCTC(logits, T, C, chars)
	if txt != "Koma" {
		t.Fatalf("decodeCTC text = %q, want %q", txt, "Koma")
	}
	if score < 0.9 {
		t.Fatalf("decodeCTC score = %.3f, want >= 0.9", score)
	}
}

func TestExtractTextBoxesAndAssembleReadingLines(t *testing.T) {
	// Synthesize a 64x64 DBNet probability map with two horizontal text lines.
	const w, h = 64, 64
	prob := make([]float32, w*h)
	for y := 10; y < 18; y++ {
		for x := 6; x < 30; x++ {
			prob[y*w+x] = 0.92
		}
		for x := 38; x < 58; x++ {
			prob[y*w+x] = 0.90
		}
	}
	for y := 36; y < 44; y++ {
		for x := 8; x < 54; x++ {
			prob[y*w+x] = 0.95
		}
	}

	boxes := extractTextBoxes(prob, w, h, 320, 320)
	if len(boxes) != 3 {
		t.Fatalf("extractTextBoxes returned %d boxes, want 3: %+v", len(boxes), boxes)
	}

	lines := assembleReadingLines([]textBoxWithText{
		{box: textBox{X0: 190, Y0: 50, X1: 290, Y1: 90}, text: "2026"},
		{box: textBox{X0: 40, Y0: 180, X1: 270, Y1: 220}, text: "一帧清光"},
		{box: textBox{X0: 30, Y0: 50, X1: 150, Y1: 90}, text: "Koma"},
	})
	if len(lines) != 2 {
		t.Fatalf("assembleReadingLines returned %d lines: %#v", len(lines), lines)
	}
	if lines[0] != "Koma 2026" || lines[1] != "一帧清光" {
		t.Fatalf("unexpected reading order: %#v", lines)
	}
}

func TestInstalledAndUninstallLifecycle(t *testing.T) {
	dir := t.TempDir()
	if Installed(dir) {
		t.Fatalf("empty dir should not report Installed")
	}
	stub := make([]byte, 2048)
	for _, name := range []string{detModelFile, recModelFile, ortDLLFile, ortProvFile} {
		if err := os.WriteFile(filepath.Join(dir, name), stub, 0o644); err != nil {
			t.Fatalf("write stub %s: %v", name, err)
		}
	}
	if !Installed(dir) {
		t.Fatalf("expected Installed(dir) == true after writing required files")
	}
	if got := DiskUsageBytes(dir); got <= 0 {
		t.Fatalf("expected DiskUsageBytes > 0, got %d", got)
	}
	if err := Uninstall(dir); err != nil {
		t.Fatalf("Uninstall failed: %v", err)
	}
	if Installed(dir) {
		t.Fatalf("expected Installed(dir) == false after Uninstall")
	}
}

// TestLivePPOCRv6DownloadAndRecognize runs a real end-to-end download + ONNX Runtime
// inference check on light & dark Chinese/English images when KOMA_TEST_LIVE_PPOCR=1.
func TestLivePPOCRv6DownloadAndRecognize(t *testing.T) {
	if os.Getenv("KOMA_TEST_LIVE_PPOCR") != "1" {
		t.Skip("set KOMA_TEST_LIVE_PPOCR=1 to run live PP-OCRv6 download and inference test")
	}
	if runtime.GOOS != "windows" {
		t.Skip("PP-OCRv6 Windows DLL test only runs on Windows")
	}

	dir := os.Getenv("KOMA_TEST_OCR_DIR")
	if dir == "" {
		dir = t.TempDir()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	dlStart := time.Now()
	if err := DownloadPPOCRv6(ctx, dir, func(p DownloadProgress) {
		t.Logf("download: %s (%.1f%%)", p.Stage, p.Percent)
	}); err != nil {
		t.Fatalf("DownloadPPOCRv6 failed: %v", err)
	}
	t.Logf("PP-OCRv6 ready in %v (%.1f MB on disk)", time.Since(dlStart), float64(DiskUsageBytes(dir))/(1024*1024))

	if !Installed(dir) {
		t.Fatalf("Installed(%q) returned false after DownloadPPOCRv6", dir)
	}

	eng := NewPPOCREngine(dir)
	defer eng.Release()

	// Test 1: Light mode mixed Chinese + English + numbers
	lightSVG := `<svg xmlns="http://www.w3.org/2000/svg" width="640" height="200" viewBox="0 0 640 200">
		<rect width="640" height="200" fill="#ffffff"/>
		<text x="36" y="76" fill="#111827" font-family="Microsoft YaHei, Segoe UI, sans-serif" font-size="36" font-weight="700">Koma 截图工具 v0.3</text>
		<text x="36" y="142" fill="#1f2937" font-family="Microsoft YaHei, Segoe UI, sans-serif" font-size="32" font-weight="500">一帧清光，恰如其分 2026</text>
	</svg>`
	lightParsed, err := ui.ParseSVG([]byte(lightSVG))
	if err != nil {
		t.Fatalf("ParseSVG light failed: %v", err)
	}
	lightImg := lightParsed.Image(640, 200, color.White)

	t0 := time.Now()
	resLight, err := eng.Recognize(lightImg)
	if err != nil {
		t.Fatalf("PP-OCRv6 light Recognize failed: %v", err)
	}
	t.Logf("PP-OCRv6 Light (%v, %d lines): %q", time.Since(t0), len(resLight.Lines), resLight.Text)
	if !strings.Contains(resLight.Text, "Koma") || !strings.Contains(resLight.Text, "截图") {
		t.Fatalf("expected light OCR to recognize Koma and 截图, got %q", resLight.Text)
	}

	// Test 2: Dark mode IDE-style code & Chinese text (warm session latency check)
	darkSVG := `<svg xmlns="http://www.w3.org/2000/svg" width="640" height="200" viewBox="0 0 640 200">
		<rect width="640" height="200" fill="#1e1e2e"/>
		<text x="36" y="76" fill="#f8fafc" font-family="Microsoft YaHei, Consolas, sans-serif" font-size="32" font-weight="600">本地高精度文字识别</text>
		<text x="36" y="142" fill="#93c5fd" font-family="Consolas, Segoe UI, monospace" font-size="28" font-weight="600">func main() { fmt.Println("OK") }</text>
	</svg>`
	darkParsed, err := ui.ParseSVG([]byte(darkSVG))
	if err != nil {
		t.Fatalf("ParseSVG dark failed: %v", err)
	}
	darkImg := darkParsed.Image(640, 200, color.RGBA{0x1e, 0x1e, 0x2e, 0xff})

	t1 := time.Now()
	resDark, err := eng.Recognize(darkImg)
	if err != nil {
		t.Fatalf("PP-OCRv6 dark Recognize failed: %v", err)
	}
	t.Logf("PP-OCRv6 Dark (%v, %d lines): %q", time.Since(t1), len(resDark.Lines), resDark.Text)
	if !strings.Contains(resDark.Text, "文字识别") && !strings.Contains(resDark.Text, "高精度") {
		t.Fatalf("expected dark OCR to recognize Chinese text, got %q", resDark.Text)
	}
}
