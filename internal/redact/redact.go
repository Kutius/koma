package redact

import (
	"image"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"koma/internal/winapi"
)

// Category classifies a detected sensitive span.
type Category string

const (
	CategorySecret   Category = "密钥"
	CategoryPhone    Category = "手机号"
	CategoryEmail    Category = "邮箱"
	CategoryIDCard   Category = "身份证"
	CategoryBankCard Category = "银行卡"
	CategoryIP       Category = "IP"
	CategoryKeyword  Category = "关键词"
)

// Finding describes one sensitive region detected in image pixel coordinates.
type Finding struct {
	Category Category
	Text     string
	Rect     image.Rectangle
}

var (
	// Explicit high-signal API tokens & secrets.
	reAPIKey = regexp.MustCompile(`(?i)\b(?:sk-(?:proj-|ant-|live-|test-|or-v1-)?[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{24,}|glpat-[A-Za-z0-9_-]{16,}|xox[baprs]-[A-Za-z0-9-]{16,}|AKIA[0-9A-Z]{16}|ASIA[0-9A-Z]{16}|LTAI[A-Za-z0-9]{12,}|AKID[A-Za-z0-9]{20,}|AIza[0-9A-Za-z_-]{35})\b`)
	// JWT (3 base64url segments starting with eyJ).
	reJWT = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)
	// Authorization Bearer token (group 1 = secret value only).
	reBearer = regexp.MustCompile(`(?i)\bBearer\s+([A-Za-z0-9._~+/=-]{12,})`)
	// URL credentials scheme://user:pass@host (group 1 = user:pass).
	reURLCred = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]{1,12}://([^\s/@:]+:[^\s/@]+)@`)
	// PEM private key header.
	rePrivKey = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)
	// Contextual key-value secrets: mask only the value (group 1) so labels remain readable.
	reKVSecret = regexp.MustCompile("(?i)(?:密码|口令|密钥|私钥|令牌|订单号|运单号|银行卡号?|身份证号?|password|passwd|pwd|api[_ -]?key|app[_ -]?secret|client[_ -]?secret|access[_ -]?key|secret[_ -]?key|access[_ -]?token|refresh[_ -]?token|private[_ -]?key|token|secret|cookie)\\s*[:：=]\\s*[\"'`]?([^\\s\"'`,;；，]{4,})[\"'`]?")

	// Email addresses.
	reEmail = regexp.MustCompile(`\b[A-Za-z0-9._%+\-]{1,64}@[A-Za-z0-9.\-]{1,128}\.[A-Za-z]{2,12}\b`)

	// Chinese mainland mobile numbers (supports optional +86 and space/hyphen separators).
	reCNPhone = regexp.MustCompile(`(?:(?:\+?86[\s\-]?)?1[3-9]\d[\s\-]?\d{4}[\s\-]?\d{4})`)

	// 18-digit Chinese national ID (validated with GB 11643-1999 checksum).
	reCNID = regexp.MustCompile(`\b[1-9]\d{5}(?:19|20)\d{2}(?:0[1-9]|1[0-2])(?:0[1-9]|[12]\d|3[01])\d{3}[\dXx]\b`)

	// 16-19 digit payment card candidates (validated with Luhn + BIN prefix).
	reBankCard = regexp.MustCompile(`\b[3-6]\d{3}(?:[\s\-]?\d{4}){3}(?:[\s\-]?\d{1,3})?\b`)

	// IPv4 addresses (version strings like v1.2.3.4 are filtered out).
	reIPv4 = regexp.MustCompile(`\b(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(?:\.(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}\b`)
)

// ParseKeywords splits a user-configured keyword string (comma, Chinese comma,
// enumeration comma, semicolon, or newline separated) into normalized terms.
func ParseKeywords(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', '，', '、', ';', '；', '\n', '\r':
			return true
		default:
			return false
		}
	})
	seen := make(map[string]struct{}, len(parts))
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t == "" {
			continue
		}
		key := strings.ToLower(t)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, t)
	}
	return out
}

type runeBox struct {
	x0, x1 float64
	y0, y1 int
}

type textLine struct {
	text  string
	runes []rune
	boxes []runeBox
	y0    int
	y1    int
}

// Detect scans OCR words (grouped into visual reading lines) for sensitive
// information and custom keywords, returning merged pixel bounding boxes.
func Detect(words []winapi.OCRWord, imgBounds image.Rectangle, customKeywords []string) []Finding {
	if len(words) == 0 || imgBounds.Empty() {
		return nil
	}

	lines := groupWordsIntoLines(words)
	var rawFindings []Finding

	for _, ln := range lines {
		if len(ln.runes) == 0 {
			continue
		}
		spans := detectSpansInLine(ln.text, customKeywords)
		for _, sp := range spans {
			rect, ok := runeSpanToRect(ln, sp.startRune, sp.endRune, imgBounds)
			if !ok {
				continue
			}
			rawFindings = append(rawFindings, Finding{
				Category: sp.cat,
				Text:     string(ln.runes[sp.startRune:sp.endRune]),
				Rect:     rect,
			})
		}
	}

	return mergeFindings(rawFindings, imgBounds)
}

// FindWordAt locates a tight word/token rectangle under pixel point (px, py)
// for one-click "Point-to-Redact" after OCR has run. If the OCR word is a wide
// multi-token line (as produced by PP-OCRv6), it narrows the hit to the exact
// space/separator-delimited token around (px, py).
func FindWordAt(words []winapi.OCRWord, px, py float32) (winapi.OCRWord, bool) {
	if len(words) == 0 {
		return winapi.OCRWord{}, false
	}
	const tol float32 = 4.0
	bestIdx := -1
	bestArea := math.MaxInt
	for i, w := range words {
		if w.Rect.Empty() || strings.TrimSpace(w.Text) == "" {
			continue
		}
		if px >= float32(w.Rect.Min.X)-tol && px <= float32(w.Rect.Max.X)+tol &&
			py >= float32(w.Rect.Min.Y)-tol && py <= float32(w.Rect.Max.Y)+tol {
			area := w.Rect.Dx() * w.Rect.Dy()
			if area < bestArea {
				bestArea = area
				bestIdx = i
			}
		}
	}
	if bestIdx < 0 {
		return winapi.OCRWord{}, false
	}
	w := words[bestIdx]
	return narrowWordTokenAt(w, float64(px)), true
}

func narrowWordTokenAt(w winapi.OCRWord, px float64) winapi.OCRWord {
	// Zero-alloc fast path: interactive tokens produced by BuildInteractiveTokens
	// are already split on spaces/colons/equals, so avoid []rune allocation on every
	// 60-144Hz pointer-move frame.
	hasBreak := false
	for _, r := range w.Text {
		if unicode.IsSpace(r) || r == ':' || r == '：' || r == '=' {
			hasBreak = true
			break
		}
	}
	if !hasBreak {
		return w
	}

	runes := []rune(w.Text)
	if len(runes) <= 2 {
		return w
	}

	boxes := interpolateRuneBoxes(runes, w.Rect)
	hitIdx := -1
	for i, b := range boxes {
		if px >= b.x0 && px <= b.x1 {
			hitIdx = i
			break
		}
	}
	if hitIdx < 0 {
		if px < boxes[0].x0 {
			hitIdx = 0
		} else {
			hitIdx = len(boxes) - 1
		}
	}

	isSep := func(r rune) bool {
		return unicode.IsSpace(r) || r == ':' || r == '：' || r == '=' || r == ',' || r == '，' || r == ';' || r == '；'
	}
	if isSep(runes[hitIdx]) {
		return w
	}
	lo, hi := hitIdx, hitIdx+1
	for lo > 0 && !isSep(runes[lo-1]) {
		lo--
	}
	for hi < len(runes) && !isSep(runes[hi]) {
		hi++
	}
	x0 := int(math.Floor(boxes[lo].x0))
	x1 := int(math.Ceil(boxes[hi-1].x1))
	if x1 <= x0 {
		return w
	}
	return winapi.OCRWord{
		Text: string(runes[lo:hi]),
		Rect: image.Rect(x0, w.Rect.Min.Y, x1, w.Rect.Max.Y),
	}
}

type lineSpan struct {
	cat       Category
	startRune int
	endRune   int
}

func detectSpansInLine(line string, customKeywords []string) []lineSpan {
	var spans []lineSpan
	addBytes := func(cat Category, b0, b1 int) {
		if b0 < 0 || b1 <= b0 || b1 > len(line) {
			return
		}
		r0 := len([]rune(line[:b0]))
		r1 := r0 + len([]rune(line[b0:b1]))
		if r1 > r0 {
			spans = append(spans, lineSpan{cat: cat, startRune: r0, endRune: r1})
		}
	}

	for _, loc := range reAPIKey.FindAllStringIndex(line, -1) {
		addBytes(CategorySecret, loc[0], loc[1])
	}
	for _, loc := range reJWT.FindAllStringIndex(line, -1) {
		addBytes(CategorySecret, loc[0], loc[1])
	}
	for _, m := range reBearer.FindAllStringSubmatchIndex(line, -1) {
		if len(m) >= 4 {
			addBytes(CategorySecret, m[2], m[3])
		}
	}
	for _, m := range reURLCred.FindAllStringSubmatchIndex(line, -1) {
		if len(m) >= 4 {
			addBytes(CategorySecret, m[2], m[3])
		}
	}
	for _, loc := range rePrivKey.FindAllStringIndex(line, -1) {
		addBytes(CategorySecret, loc[0], loc[1])
	}
	for _, m := range reKVSecret.FindAllStringSubmatchIndex(line, -1) {
		if len(m) >= 4 {
			val := line[m[2]:m[3]]
			// Skip trivial placeholder values like "true", "false", "null", "none".
			lower := strings.ToLower(val)
			if lower == "true" || lower == "false" || lower == "null" || lower == "none" || lower == "required" {
				continue
			}
			addBytes(CategorySecret, m[2], m[3])
		}
	}
	for _, loc := range reEmail.FindAllStringIndex(line, -1) {
		addBytes(CategoryEmail, loc[0], loc[1])
	}
	for _, loc := range reCNPhone.FindAllStringIndex(line, -1) {
		if isDigitBoundary(line, loc[0], loc[1]) {
			addBytes(CategoryPhone, loc[0], loc[1])
		}
	}
	for _, loc := range reCNID.FindAllStringIndex(line, -1) {
		if validCNIDChecksum(line[loc[0]:loc[1]]) {
			addBytes(CategoryIDCard, loc[0], loc[1])
		}
	}
	for _, loc := range reBankCard.FindAllStringIndex(line, -1) {
		if isDigitBoundary(line, loc[0], loc[1]) && validLuhnDigits(line[loc[0]:loc[1]]) {
			addBytes(CategoryBankCard, loc[0], loc[1])
		}
	}
	for _, loc := range reIPv4.FindAllStringIndex(line, -1) {
		ip := line[loc[0]:loc[1]]
		if ip == "0.0.0.0" {
			continue
		}
		// Avoid false positives on software version tags like "v1.2.3.4" or "V2.0.1.0".
		if loc[0] > 0 && (line[loc[0]-1] == 'v' || line[loc[0]-1] == 'V') {
			continue
		}
		addBytes(CategoryIP, loc[0], loc[1])
	}

	if len(customKeywords) > 0 {
		lowerLine := strings.ToLower(line)
		lineRunes := []rune(lowerLine)
		for _, kw := range customKeywords {
			kwRunes := []rune(strings.ToLower(strings.TrimSpace(kw)))
			if len(kwRunes) == 0 || len(kwRunes) > len(lineRunes) {
				continue
			}
			for i := 0; i <= len(lineRunes)-len(kwRunes); i++ {
				matched := true
				for j := 0; j < len(kwRunes); j++ {
					if lineRunes[i+j] != kwRunes[j] {
						matched = false
						break
					}
				}
				if matched {
					spans = append(spans, lineSpan{cat: CategoryKeyword, startRune: i, endRune: i + len(kwRunes)})
					i += len(kwRunes) - 1
				}
			}
		}
	}

	return spans
}

func isDigitBoundary(s string, b0, b1 int) bool {
	if b0 > 0 && s[b0-1] >= '0' && s[b0-1] <= '9' {
		return false
	}
	if b1 < len(s) && s[b1] >= '0' && s[b1] <= '9' {
		return false
	}
	return true
}

func validCNIDChecksum(id string) bool {
	if len(id) != 18 {
		return false
	}
	weights := [17]int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	checkMap := [11]byte{'1', '0', 'X', '9', '8', '7', '6', '5', '4', '3', '2'}
	sum := 0
	for i := 0; i < 17; i++ {
		c := id[i]
		if c < '0' || c > '9' {
			return false
		}
		sum += int(c-'0') * weights[i]
	}
	last := id[17]
	if last == 'x' {
		last = 'X'
	}
	return last == checkMap[sum%11]
}

func validLuhnDigits(s string) bool {
	var digits []int
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			digits = append(digits, int(s[i]-'0'))
		}
	}
	if len(digits) < 16 || len(digits) > 19 {
		return false
	}
	sum := 0
	alt := false
	for i := len(digits) - 1; i >= 0; i-- {
		d := digits[i]
		if alt {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		alt = !alt
	}
	return sum%10 == 0
}

func groupWordsIntoLines(words []winapi.OCRWord) []textLine {
	valid := make([]winapi.OCRWord, 0, len(words))
	for _, w := range words {
		if strings.TrimSpace(w.Text) != "" && !w.Rect.Empty() {
			valid = append(valid, w)
		}
	}
	if len(valid) == 0 {
		return nil
	}

	sort.Slice(valid, func(i, j int) bool {
		ci := valid[i].Rect.Min.Y + valid[i].Rect.Max.Y
		cj := valid[j].Rect.Min.Y + valid[j].Rect.Max.Y
		if ci != cj {
			return ci < cj
		}
		return valid[i].Rect.Min.X < valid[j].Rect.Min.X
	})

	type rowBucket struct {
		y0, y1 int
		words  []winapi.OCRWord
	}
	var buckets []rowBucket
	for _, w := range valid {
		placed := false
		for bi := range buckets {
			ov := min(buckets[bi].y1, w.Rect.Max.Y) - max(buckets[bi].y0, w.Rect.Min.Y)
			minH := min(buckets[bi].y1-buckets[bi].y0, w.Rect.Dy())
			if minH > 0 && float64(ov)/float64(minH) >= 0.45 {
				buckets[bi].words = append(buckets[bi].words, w)
				buckets[bi].y0 = min(buckets[bi].y0, w.Rect.Min.Y)
				buckets[bi].y1 = max(buckets[bi].y1, w.Rect.Max.Y)
				placed = true
				break
			}
		}
		if !placed {
			buckets = append(buckets, rowBucket{y0: w.Rect.Min.Y, y1: w.Rect.Max.Y, words: []winapi.OCRWord{w}})
		}
	}

	sort.Slice(buckets, func(i, j int) bool { return buckets[i].y0 < buckets[j].y0 })

	out := make([]textLine, 0, len(buckets))
	for _, bk := range buckets {
		sort.Slice(bk.words, func(i, j int) bool { return bk.words[i].Rect.Min.X < bk.words[j].Rect.Min.X })
		var runes []rune
		var boxes []runeBox
		for wi, w := range bk.words {
			wr := []rune(w.Text)
			if len(wr) == 0 {
				continue
			}
			if wi > 0 && len(runes) > 0 {
				prev := bk.words[wi-1]
				gap := w.Rect.Min.X - prev.Rect.Max.X
				avgH := float64(prev.Rect.Dy()+w.Rect.Dy()) * 0.5
				if shouldJoinWithSpace(runes[len(runes)-1], wr[0], gap, avgH) {
					gx0 := float64(prev.Rect.Max.X)
					gx1 := float64(w.Rect.Min.X)
					if gx1 < gx0 {
						gx1 = gx0
					}
					runes = append(runes, ' ')
					boxes = append(boxes, runeBox{x0: gx0, x1: gx1, y0: bk.y0, y1: bk.y1})
				}
			}
			wBoxes := interpolateRuneBoxes(wr, w.Rect)
			runes = append(runes, wr...)
			boxes = append(boxes, wBoxes...)
		}
		out = append(out, textLine{
			text:  string(runes),
			runes: runes,
			boxes: boxes,
			y0:    bk.y0,
			y1:    bk.y1,
		})
	}
	return out
}

func shouldJoinWithSpace(prevLast, nextFirst rune, gapPx int, avgH float64) bool {
	if float64(gapPx) > avgH*0.28 {
		return true
	}
	if unicode.In(prevLast, unicode.Han) && unicode.In(nextFirst, unicode.Han) {
		return false
	}
	// Avoid breaking contiguous emails / tokens / paths split at punctuation by WinRT OCR.
	if nextFirst == '@' || prevLast == '@' || nextFirst == '.' || prevLast == '.' ||
		nextFirst == '-' || prevLast == '-' || nextFirst == '_' || prevLast == '_' ||
		nextFirst == '/' || prevLast == '/' || nextFirst == ':' || prevLast == ':' {
		return float64(gapPx) > avgH*0.45
	}
	return float64(gapPx) > avgH*0.18
}

func runeVisualWeight(r rune) float64 {
	if unicode.IsSpace(r) {
		return 0.36
	}
	if r >= 0x2E80 && r <= 0x9FFF || r >= 0xF900 && r <= 0xFAFF || r >= 0xFF00 && r <= 0xFFEF {
		return 1.0
	}
	switch r {
	case 'i', 'l', 'I', '!', '|', '.', ',', ':', ';', '\'', '`':
		return 0.34
	case 'm', 'w', 'M', 'W', '@', '%':
		return 0.78
	default:
		return 0.56
	}
}

func interpolateRuneBoxes(runes []rune, rect image.Rectangle) []runeBox {
	n := len(runes)
	boxes := make([]runeBox, n)
	if n == 0 {
		return boxes
	}
	totalW := 0.0
	weights := make([]float64, n)
	for i, r := range runes {
		w := runeVisualWeight(r)
		weights[i] = w
		totalW += w
	}
	if totalW <= 0 {
		totalW = float64(n)
	}
	rx0 := float64(rect.Min.X)
	rw := float64(rect.Dx())
	acc := 0.0
	for i := 0; i < n; i++ {
		x0 := rx0 + rw*(acc/totalW)
		acc += weights[i]
		x1 := rx0 + rw*(acc/totalW)
		boxes[i] = runeBox{x0: x0, x1: x1, y0: rect.Min.Y, y1: rect.Max.Y}
	}
	return boxes
}

func runeSpanToRect(ln textLine, r0, r1 int, bounds image.Rectangle) (image.Rectangle, bool) {
	if r0 < 0 || r1 <= r0 || r1 > len(ln.boxes) {
		return image.Rectangle{}, false
	}
	minX := ln.boxes[r0].x0
	maxX := ln.boxes[r0].x1
	minY := ln.boxes[r0].y0
	maxY := ln.boxes[r0].y1
	for i := r0 + 1; i < r1; i++ {
		b := ln.boxes[i]
		if b.x0 < minX {
			minX = b.x0
		}
		if b.x1 > maxX {
			maxX = b.x1
		}
		if b.y0 < minY {
			minY = b.y0
		}
		if b.y1 > maxY {
			maxY = b.y1
		}
	}

	// Add comfortable breathing padding so anti-aliased glyph ascenders/descenders never peek out.
	h := max(1, maxY-minY)
	padX := max(4, h/5)
	padY := max(2, h/8)
	rc := image.Rect(
		int(math.Floor(minX))-padX,
		minY-padY,
		int(math.Ceil(maxX))+padX,
		maxY+padY,
	).Intersect(bounds)
	if rc.Dx() < 4 || rc.Dy() < 4 {
		return image.Rectangle{}, false
	}
	return rc, true
}

func mergeFindings(in []Finding, bounds image.Rectangle) []Finding {
	if len(in) <= 1 {
		return in
	}
	sort.Slice(in, func(i, j int) bool {
		if in[i].Rect.Min.Y != in[j].Rect.Min.Y {
			return in[i].Rect.Min.Y < in[j].Rect.Min.Y
		}
		return in[i].Rect.Min.X < in[j].Rect.Min.X
	})

	var merged []Finding
	for _, f := range in {
		combined := false
		for i := range merged {
			a := merged[i].Rect
			b := f.Rect
			// Merge overlapping or nearly touching boxes on the same line.
			expA := image.Rect(a.Min.X-3, a.Min.Y-2, a.Max.X+3, a.Max.Y+2)
			if expA.Overlaps(b) {
				merged[i].Rect = a.Union(b).Intersect(bounds)
				combined = true
				break
			}
		}
		if !combined {
			merged = append(merged, f)
		}
	}
	return merged
}
