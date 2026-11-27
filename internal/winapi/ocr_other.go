//go:build !windows

package winapi

import (
	"errors"
	"image"
	"strings"
	"unicode"
)

// ErrOCRNoLanguage indicates Windows has no OCR language pack installed.
var ErrOCRNoLanguage = errors.New("OCR is only supported on Windows")

// OCRWord represents a single recognized text token/segment with its bounding
// box in the original input image's pixel coordinate space.
type OCRWord struct {
	Text string
	Rect image.Rectangle
}

// OCRResult holds recognized text lines and optional per-word bounding boxes.
type OCRResult struct {
	Text  string
	Lines []string
	Words []OCRWord
}

// RecognizeText is a non-Windows compile stub.
func RecognizeText(img *image.RGBA) (OCRResult, error) {
	return OCRResult{}, ErrOCRNoLanguage
}

// CleanOCRLine removes spurious spaces between CJK characters.
func CleanOCRLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	runes := []rune(s)
	out := make([]rune, 0, len(runes))
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == ' ' && len(out) > 0 && i+1 < len(runes) {
			prev := out[len(out)-1]
			next := runes[i+1]
			if isCJKOrPunct(prev) && isCJKOrPunct(next) {
				continue
			}
		}
		out = append(out, r)
	}
	return string(out)
}

func isCJKOrPunct(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana) ||
		(r >= 0x3000 && r <= 0x303F) ||
		(r >= 0xFF00 && r <= 0xFFEF)
}
