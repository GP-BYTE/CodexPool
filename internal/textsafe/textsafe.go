package textsafe

import (
	"strings"
	"unicode"
)

// Clean returns single-line terminal-safe text with a rune-count bound.
// Structured JSON output should keep the original value and rely on JSON escaping.
func Clean(value string, maxRunes int) string {
	if maxRunes <= 0 {
		maxRunes = 256
	}
	cleaned := strings.Map(func(r rune) rune {
		if unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp) {
			return ' '
		}
		return r
	}, value)
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	runes := []rune(cleaned)
	if len(runes) <= maxRunes {
		return cleaned
	}
	if maxRunes == 1 {
		return "…"
	}
	return string(runes[:maxRunes-1]) + "…"
}

func CleanOr(value, fallback string, maxRunes int) string {
	value = Clean(value, maxRunes)
	if value == "" {
		return fallback
	}
	return value
}
