package textsafe

import (
	"strings"
	"testing"
)

func TestCleanMakesSingleLineAndRemovesTerminalControls(t *testing.T) {
	got := Clean("safe\x1b[31m\nvalue\u200b", 100)
	if strings.ContainsRune(got, '\x1b') || strings.ContainsRune(got, '\n') || strings.ContainsRune(got, '\u200b') {
		t.Fatalf("control or format character survived: %q", got)
	}
	if got != "safe [31m value" {
		t.Fatalf("unexpected cleaned value: %q", got)
	}
}

func TestCleanTruncatesByRunesAndCleanOrPreservesUnknown(t *testing.T) {
	if got := Clean("abcdef", 4); got != "abc…" {
		t.Fatalf("unexpected truncation: %q", got)
	}
	if got := Clean("你好世界", 3); got != "你好…" {
		t.Fatalf("unicode truncation must use runes: %q", got)
	}
	if got := CleanOr(" \n\t", "—", 20); got != "—" {
		t.Fatalf("blank value should use fallback: %q", got)
	}
}
