package redact

import (
	"image"
	"image/color"
	"testing"

	"koma/internal/winapi"
)

func TestDetectSensitiveCategories(t *testing.T) {
	bounds := image.Rect(0, 0, 1200, 400)
	words := []winapi.OCRWord{
		// Line 1: split phone number + email
		{Text: "手机:", Rect: image.Rect(20, 20, 70, 42)},
		{Text: "138", Rect: image.Rect(78, 20, 114, 42)},
		{Text: "0013", Rect: image.Rect(122, 20, 170, 42)},
		{Text: "8000", Rect: image.Rect(178, 20, 226, 42)},
		{Text: "邮箱:", Rect: image.Rect(280, 20, 330, 42)},
		{Text: "alice.wu@koma.dev", Rect: image.Rect(340, 20, 520, 42)},

		// Line 2: Bearer token & OpenAI-style key (label should stay unmasked)
		{Text: "Authorization: Bearer sk-proj-9a8b7c6d5e4f3a2b1c0d9e8f", Rect: image.Rect(20, 70, 620, 94)},

		// Line 3: Valid CN ID vs invalid checksum + IPv4 vs version number
		{Text: "身份证 11010519491231002X", Rect: image.Rect(20, 120, 340, 144)},
		{Text: "假号 110105194912310029", Rect: image.Rect(360, 120, 620, 144)},
		{Text: "v1.2.3.4", Rect: image.Rect(640, 120, 720, 144)},
		{Text: "192.168.10.42", Rect: image.Rect(740, 120, 880, 144)},

		// Line 4: Password key-value + custom keyword
		{Text: "密码：MyS3cret!Pass", Rect: image.Rect(20, 170, 260, 194)},
		{Text: "欢迎来到 星辰科技 控制台", Rect: image.Rect(300, 170, 620, 194)},

		// Line 5: Valid Luhn bank card vs non-Luhn long number
		{Text: "6222 0202 0000 1234 567", Rect: image.Rect(20, 220, 300, 244)},
		{Text: "订单号: ORD-20261127001", Rect: image.Rect(340, 220, 600, 244)},
	}

	findings := Detect(words, bounds, ParseKeywords("星辰科技, internal.corp"))
	if len(findings) < 7 {
		t.Fatalf("expected at least 7 sensitive findings, got %d: %+v", len(findings), findings)
	}

	hasCat := func(c Category) bool {
		for _, f := range findings {
			if f.Category == c {
				return true
			}
		}
		return false
	}
	for _, want := range []Category{CategoryPhone, CategoryEmail, CategorySecret, CategoryIDCard, CategoryIP, CategoryKeyword} {
		if !hasCat(want) {
			t.Errorf("missing expected category %q in findings: %+v", want, findings)
		}
	}

	// Ensure version string "v1.2.3.4" (x=640..720) and invalid ID (x=360..620) were NOT falsely redacted.
	for _, f := range findings {
		if f.Rect.Min.Y >= 110 && f.Rect.Max.Y <= 155 {
			if f.Rect.Overlaps(image.Rect(645, 122, 715, 142)) {
				t.Errorf("version string v1.2.3.4 should not be redacted: %+v", f)
			}
			if f.Rect.Overlaps(image.Rect(420, 122, 610, 142)) {
				t.Errorf("invalid ID checksum should not be redacted: %+v", f)
			}
		}
	}

	// Ensure "密码：" label stays visible (mask starts to the right of "密码：").
	for _, f := range findings {
		if f.Rect.Min.Y >= 160 && f.Rect.Max.Y <= 205 && f.Rect.Min.X < 280 {
			if f.Rect.Min.X <= 30 {
				t.Errorf("expected key-value redaction to preserve '密码：' label, got rect %v", f.Rect)
			}
		}
	}
}

func TestFindWordAtPointToRedact(t *testing.T) {
	words := []winapi.OCRWord{
		{Text: "联系人: 张三丰 13800138000", Rect: image.Rect(100, 50, 420, 74)},
		{Text: "普通按钮", Rect: image.Rect(100, 100, 180, 124)},
	}
	// Clicking around "张三丰" inside the wide PP-OCRv6 line should narrow to "张三丰".
	w, ok := FindWordAt(words, 200, 62)
	if !ok {
		t.Fatalf("expected FindWordAt to hit word")
	}
	if w.Text != "张三丰" {
		t.Fatalf("expected narrowed token '张三丰', got %q (%v)", w.Text, w.Rect)
	}
	if w.Rect.Dx() >= 250 {
		t.Fatalf("expected narrowed rect much tighter than whole line, got %v", w.Rect)
	}

	// Clicking empty canvas returns false.
	if _, ok := FindWordAt(words, 20, 20); ok {
		t.Fatalf("expected miss on empty area")
	}
}

func TestParseKeywords(t *testing.T) {
	got := ParseKeywords(" 张三, 李四，星辰科技、foo.internal;\n张三 ")
	want := []string{"张三", "李四", "星辰科技", "foo.internal"}
	if len(got) != len(want) {
		t.Fatalf("ParseKeywords = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ParseKeywords[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBuildInteractiveTokensSemanticsAndAtomicEntities(t *testing.T) {
	words := []winapi.OCRWord{
		// 1. Glued Chinese label + value & CJK↔Latin/Digit transitions
		{Text: "姓名张三丰", Rect: image.Rect(20, 20, 170, 44)},
		{Text: "手机13800138000", Rect: image.Rect(200, 20, 380, 44)},
		{Text: "收件人Alice", Rect: image.Rect(400, 20, 520, 44)},

		// 2. Quoted & bracketed atomic entities split into fragments (simulating WinRT or punct splits)
		{Text: `("alice.wu`, Rect: image.Rect(20, 70, 130, 94)},
		{Text: `@koma.dev")`, Rect: image.Rect(131, 70, 250, 94)},
		{Text: "[192.168.10.42]", Rect: image.Rect(280, 70, 430, 94)},
	}

	tokens, lines := BuildInteractiveTokens(words, nil)
	if len(lines) != 2 {
		t.Fatalf("expected 2 reading lines, got %d: %+v", len(lines), lines)
	}

	gotTexts := make(map[string]bool, len(tokens))
	for _, tk := range tokens {
		gotTexts[tk.Text] = true
	}
	for _, want := range []string{"姓名", "张三丰", "手机", "13800138000", "收件人", "Alice", "alice.wu@koma.dev", "192.168.10.42"} {
		if !gotTexts[want] {
			t.Errorf("missing expected interactive token %q; got tokens: %+v", want, tokens)
		}
	}

	// Verify whole-line lookup works for Alt+Click mode.
	ln, ok := FindLineAt(lines, 250, 32)
	if !ok || ln.Rect.Dx() < 400 {
		t.Fatalf("expected FindLineAt to return wide first line, got ok=%v ln=%+v", ok, ln)
	}
}

func TestTightenRectToInkTrimsPaddingSafely(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 200, 80))
	// White background
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 250, 250, 250, 255
	}
	// Draw crisp dark glyph block inside [40..140, 30..48] while OCR box has generous padding [28..154, 20..58]
	for y := 30; y < 48; y++ {
		for x := 40; x < 140; x++ {
			img.SetRGBA(x, y, color.RGBA{20, 25, 35, 255})
		}
	}
	loose := image.Rect(28, 20, 154, 58)
	tight := TightenRectToInk(img, loose)
	if tight.Min.Y < 26 || tight.Max.Y > 52 {
		t.Fatalf("expected vertical margin trimmed close to [30..48], got %v", tight)
	}
	if tight.Min.X < 36 || tight.Max.X > 144 {
		t.Fatalf("expected horizontal margin trimmed close to [40..140], got %v", tight)
	}
}

func BenchmarkFindWordAt_HoverHotPath(b *testing.B) {
	words := make([]winapi.OCRWord, 0, 120)
	for row := 0; row < 12; row++ {
		for col := 0; col < 10; col++ {
			words = append(words, winapi.OCRWord{
				Text: "token",
				Rect: image.Rect(20+col*90, 20+row*32, 95+col*90, 44+row*32),
			})
		}
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = FindWordAt(words, 480, 190)
	}
}
