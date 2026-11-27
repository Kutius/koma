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

var (
	// Atomic compound tokens that must stay intact as a single clickable box
	// even though they contain punctuation like @, ., -, /, or :.
	reAtomicURL = regexp.MustCompile(`(?i)\bhttps?://[^\s"'` + "`" + `<>()\[\]{}，。；、]+`)
)

// Common Chinese field label prefixes that frequently glue directly onto values
// without a colon or space in OCR output (e.g. "姓名张三丰", "收货地址北京市...").
// Ordered longest-first so "收货地址" matches before "地址".
var cnFieldPrefixes = []string{
	"身份证号码", "银行卡号", "收货地址", "联系地址", "联系电话", "手机号码", "电子邮箱",
	"身份证号", "微信号码", "登录账号", "公司名称", "企业名称", "订单编号", "快递单号",
	"收件人", "寄件人", "联系人", "用户名", "微信号", "手机号", "订单号", "运单号",
	"身份证", "银行卡", "开户行", "密码", "口令", "密钥", "姓名", "昵称", "称呼",
	"电话", "手机", "邮箱", "地址", "账号", "账户", "公司", "机构", "部门", "卡号",
}

// BuildInteractiveTokens normalizes raw OCR words (from PP-OCRv6 CTC alignment
// or Windows native OCR) into:
//   - tokens: clean, semantically split clickable word/phrase boxes (atomic
//     sensitive entities kept whole, quotes/brackets stripped, CJK/Latin/digit
//     boundaries split, common Chinese label prefixes separated, ink-tightened).
//   - lines:  full reading-line boxes (also ink-tightened) for Alt+Click whole-line redaction.
func BuildInteractiveTokens(words []winapi.OCRWord, img *image.RGBA) (tokens []winapi.OCRWord, lines []winapi.OCRWord) {
	if len(words) == 0 {
		return nil, nil
	}
	var bounds image.Rectangle
	if img != nil {
		bounds = img.Bounds()
	}

	grouped := groupWordsIntoLines(words)
	for _, ln := range grouped {
		if len(ln.runes) == 0 || len(ln.boxes) == 0 {
			continue
		}

		// 1. Build whole-line interactive box (used by Alt+Click / Alt+Hover).
		if lineRect, ok := rawRuneSpanRect(ln, 0, len(ln.runes), bounds); ok {
			if img != nil {
				lineRect = TightenRectToInk(img, lineRect)
			}
			lines = append(lines, winapi.OCRWord{
				Text: strings.TrimSpace(ln.text),
				Rect: lineRect,
			})
		}

		// 2. Segment line into clean interactive tokens.
		spans := segmentLineIntoTokens(ln)
		for _, sp := range spans {
			rc, ok := rawRuneSpanRect(ln, sp[0], sp[1], bounds)
			if !ok {
				continue
			}
			txt := strings.TrimSpace(string(ln.runes[sp[0]:sp[1]]))
			if txt == "" {
				continue
			}
			if img != nil {
				rc = TightenRectToInk(img, rc)
			}
			tokens = append(tokens, winapi.OCRWord{
				Text: txt,
				Rect: rc,
			})
		}
	}

	return tokens, lines
}

// FindLineAt locates the reading-line box under (px, py) for Alt+Click whole-line redaction.
func FindLineAt(lines []winapi.OCRWord, px, py float32) (winapi.OCRWord, bool) {
	if len(lines) == 0 {
		return winapi.OCRWord{}, false
	}
	const tol float32 = 5.0
	bestIdx := -1
	bestDist := float32(math.MaxFloat32)
	for i, ln := range lines {
		if ln.Rect.Empty() {
			continue
		}
		if px >= float32(ln.Rect.Min.X)-tol && px <= float32(ln.Rect.Max.X)+tol &&
			py >= float32(ln.Rect.Min.Y)-tol && py <= float32(ln.Rect.Max.Y)+tol {
			cy := float32(ln.Rect.Min.Y+ln.Rect.Max.Y) * 0.5
			d := float32(math.Abs(float64(py - cy)))
			if d < bestDist {
				bestDist = d
				bestIdx = i
			}
		}
	}
	if bestIdx < 0 {
		return winapi.OCRWord{}, false
	}
	return lines[bestIdx], true
}

func segmentLineIntoTokens(ln textLine) [][2]int {
	n := len(ln.runes)
	if n == 0 {
		return nil
	}

	// Step A: Find atomic compound entities (emails, URLs, API keys, JWTs, phones,
	// IDs, bank cards, IPs) so internal punctuation never splits them apart.
	atomic := findAtomicEntitySpans(ln.text, n)

	// Step B: Walk non-atomic gaps and split them cleanly at separators, brackets,
	// quotes, CJK↔Latin/Digit transitions, and Chinese label prefixes.
	var out [][2]int
	cursor := 0
	for _, a := range atomic {
		if a[0] > cursor {
			out = append(out, splitNormalSubspan(ln, cursor, a[0])...)
		}
		out = append(out, a)
		cursor = a[1]
	}
	if cursor < n {
		out = append(out, splitNormalSubspan(ln, cursor, n)...)
	}
	return out
}

func findAtomicEntitySpans(line string, totalRunes int) [][2]int {
	var raw [][2]int
	addBytes := func(b0, b1 int) {
		if b0 < 0 || b1 <= b0 || b1 > len(line) {
			return
		}
		r0 := len([]rune(line[:b0]))
		r1 := r0 + len([]rune(line[b0:b1]))
		if r1 > r0 && r1 <= totalRunes {
			raw = append(raw, [2]int{r0, r1})
		}
	}

	for _, loc := range reAtomicURL.FindAllStringIndex(line, -1) {
		addBytes(loc[0], loc[1])
	}
	for _, loc := range reAPIKey.FindAllStringIndex(line, -1) {
		addBytes(loc[0], loc[1])
	}
	for _, loc := range reJWT.FindAllStringIndex(line, -1) {
		addBytes(loc[0], loc[1])
	}
	for _, loc := range reEmail.FindAllStringIndex(line, -1) {
		addBytes(loc[0], loc[1])
	}
	for _, loc := range reCNPhone.FindAllStringIndex(line, -1) {
		if isDigitBoundary(line, loc[0], loc[1]) {
			addBytes(loc[0], loc[1])
		}
	}
	for _, loc := range reCNID.FindAllStringIndex(line, -1) {
		if validCNIDChecksum(line[loc[0]:loc[1]]) {
			addBytes(loc[0], loc[1])
		}
	}
	for _, loc := range reBankCard.FindAllStringIndex(line, -1) {
		if isDigitBoundary(line, loc[0], loc[1]) && validLuhnDigits(line[loc[0]:loc[1]]) {
			addBytes(loc[0], loc[1])
		}
	}
	for _, loc := range reIPv4.FindAllStringIndex(line, -1) {
		addBytes(loc[0], loc[1])
	}

	if len(raw) <= 1 {
		return raw
	}
	// Prefer longer matches when overlapping; return sorted disjoint spans.
	sort.Slice(raw, func(i, j int) bool {
		if raw[i][0] != raw[j][0] {
			return raw[i][0] < raw[j][0]
		}
		return (raw[i][1] - raw[i][0]) > (raw[j][1] - raw[j][0])
	})
	var disjoint [][2]int
	for _, sp := range raw {
		if len(disjoint) == 0 || sp[0] >= disjoint[len(disjoint)-1][1] {
			disjoint = append(disjoint, sp)
		}
	}
	return disjoint
}

func splitNormalSubspan(ln textLine, start, end int) [][2]int {
	var chunks [][2]int
	i := start
	for i < end {
		// Skip spaces and hard delimiter/wrapper punctuation so brackets & quotes are never included.
		for i < end && isHardTokenDelim(ln.runes[i]) {
			i++
		}
		if i >= end {
			break
		}
		j := i + 1
		for j < end && !isHardTokenDelim(ln.runes[j]) {
			// Also split if there is a clear visual gap between two adjacent characters.
			if j < len(ln.boxes) && j-1 >= 0 {
				h := float64(max(1, ln.y1-ln.y0))
				if ln.boxes[j].x0-ln.boxes[j-1].x1 > math.Max(5.0, h*0.32) {
					break
				}
			}
			j++
		}
		chunks = append(chunks, splitSemanticRun(ln.runes, i, j)...)
		i = j
	}
	return chunks
}

func isHardTokenDelim(r rune) bool {
	if unicode.IsSpace(r) {
		return true
	}
	switch r {
	case ':', '：', '=', ',', '，', '、', ';', '；', '|', '｜',
		'(', ')', '（', '）', '[', ']', '【', '】', '{', '}', '「', '」', '『', '』',
		'<', '>', '《', '》', '"', '\'', '“', '”', '‘', '’', '`',
		'/', '\\', '。', '！', '!', '？', '?':
		return true
	}
	return false
}

type charClass int

const (
	clsOther charClass = iota
	clsCJK
	clsLatinNum
)

func classifyTokenRune(r rune) charClass {
	if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana) {
		return clsCJK
	}
	if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.' || r == '+' || r == '#' || r == '@' {
		return clsLatinNum
	}
	return clsOther
}

// splitSemanticRun splits a delimiter-free run at:
//  1. CJK ↔ Latin/Digit boundaries (e.g. "收件人Alice" -> "收件人", "Alice"; "手机138..." -> "手机", "138...")
//  2. Known Chinese field label prefixes glued directly to a Chinese value (e.g. "姓名张三丰" -> "姓名", "张三丰")
func splitSemanticRun(runes []rune, start, end int) [][2]int {
	if end <= start {
		return nil
	}
	var segs [][2]int
	segStart := start
	prevCls := classifyTokenRune(runes[start])

	for i := start + 1; i < end; i++ {
		cls := classifyTokenRune(runes[i])
		if (prevCls == clsCJK && cls == clsLatinNum) || (prevCls == clsLatinNum && cls == clsCJK) {
			segs = append(segs, [2]int{segStart, i})
			segStart = i
		}
		prevCls = cls
	}
	segs = append(segs, [2]int{segStart, end})

	// Second pass: if a pure CJK segment starts with a known Chinese field label
	// prefix followed by at least 2 more characters (e.g. "姓名张三丰" or "地址北京市朝阳区"),
	// split label and value cleanly.
	var out [][2]int
	for _, s := range segs {
		a, b := trimOuterPunct(runes, s[0], s[1])
		if b <= a {
			continue
		}
		if b-a >= 4 && allCJK(runes[a:b]) {
			if cut := matchCNFieldPrefix(runes[a:b]); cut > 0 && (b-a)-cut >= 2 {
				out = append(out, [2]int{a, a + cut}, [2]int{a + cut, b})
				continue
			}
		}
		out = append(out, [2]int{a, b})
	}
	return out
}

func trimOuterPunct(runes []rune, a, b int) (int, int) {
	isStrip := func(r rune) bool {
		return r == '.' || r == '-' || r == '_' || r == '~' || r == '·' || r == '•'
	}
	for a < b && isStrip(runes[a]) {
		a++
	}
	for b > a && isStrip(runes[b-1]) {
		b--
	}
	return a, b
}

func allCJK(rs []rune) bool {
	for _, r := range rs {
		if !unicode.In(r, unicode.Han) {
			return false
		}
	}
	return len(rs) > 0
}

func matchCNFieldPrefix(rs []rune) int {
	str := string(rs)
	for _, p := range cnFieldPrefixes {
		if strings.HasPrefix(str, p) {
			return len([]rune(p))
		}
	}
	return 0
}

func rawRuneSpanRect(ln textLine, r0, r1 int, bounds image.Rectangle) (image.Rectangle, bool) {
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
	rc := image.Rect(int(math.Floor(minX)), minY, int(math.Ceil(maxX)), maxY)
	if !bounds.Empty() {
		rc = rc.Intersect(bounds)
	}
	if rc.Dx() < 2 || rc.Dy() < 2 {
		return image.Rectangle{}, false
	}
	return rc, true
}
