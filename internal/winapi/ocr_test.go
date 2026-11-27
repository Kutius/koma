package winapi

import (
	"errors"
	"image"
	"image/color"
	"runtime"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestCleanOCRLine(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"你 好 世 界", "你好世界"},
		{"Hello World", "Hello World"},
		{"Koma 截 图 工 具 v0.3", "Koma 截图工具 v0.3"},
		{"  第 一 行 ， 第 二 句 。 ", "第一行，第二句。"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := CleanOCRLine(tc.in); got != tc.want {
			t.Errorf("CleanOCRLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestWindowsNativeOCREndToEnd(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows native OCR test only runs on Windows")
	}

	// Render crisp high-contrast black text on white background via MyGo's SVG rasterizer.
	svg := `<svg xmlns="http://www.w3.org/2000/svg" width="520" height="160" viewBox="0 0 520 160">
		<rect width="520" height="160" fill="#ffffff"/>
		<text x="36" y="72" fill="#111111" font-family="Segoe UI, Arial, sans-serif" font-size="40" font-weight="700">KOMA 2026</text>
		<text x="36" y="128" fill="#111111" font-family="Segoe UI, Arial, sans-serif" font-size="34" font-weight="600">HELLO WORLD</text>
	</svg>`
	parsed, err := ui.ParseSVG([]byte(svg))
	if err != nil {
		t.Fatalf("ParseSVG failed: %v", err)
	}
	img := parsed.Image(520, 160, color.White)
	if img == nil {
		t.Fatalf("SVG rasterized nil image")
	}

	res, err := RecognizeText(img)
	if errors.Is(err, ErrOCRNoLanguage) {
		t.Skipf("skipping live OCR assertion (no OCR language pack installed): %v", err)
	}
	if err != nil {
		t.Fatalf("RecognizeText failed: %v", err)
	}
	upper := strings.ToUpper(res.Text)
	t.Logf("Native Windows OCR output (%d lines): %q", len(res.Lines), res.Text)
	if !strings.Contains(upper, "KOMA") && !strings.Contains(upper, "HELLO") {
		t.Fatalf("expected OCR output to contain KOMA or HELLO, got %q", res.Text)
	}
	if len(res.Lines) < 2 {
		t.Fatalf("expected at least 2 recognized lines, got %d (%q)", len(res.Lines), res.Text)
	}
}

func TestRecognizeTextRejectsTinyOrNil(t *testing.T) {
	if _, err := RecognizeText(nil); err == nil {
		t.Fatalf("expected error for nil image")
	}
	tiny := image.NewRGBA(image.Rect(0, 0, 1, 1))
	if _, err := RecognizeText(tiny); err == nil {
		t.Fatalf("expected error for 1x1 image")
	}
}
