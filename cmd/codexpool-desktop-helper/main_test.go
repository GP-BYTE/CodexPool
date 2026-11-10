package main

import (
	"runtime"
	"testing"
)

func TestRunRejectsUnsafeOrIncompleteInvocationBeforeStateAccess(t *testing.T) {
	want := 2
	if runtime.GOOS != "darwin" {
		want = 4 // The macOS-only helper rejects other platforms first.
	}
	for _, args := range [][]string{
		nil,
		{"unknown"},
		{"activate"},
		{"restore"},
	} {
		if got := run(args); got != want {
			t.Fatalf("run(%q) = %d, want safe early rejection %d", args, got, want)
		}
	}
}
