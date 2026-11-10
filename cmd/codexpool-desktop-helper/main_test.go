package main

import "testing"

func TestRunRejectsUnsafeOrIncompleteInvocationBeforeStateAccess(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"unknown"},
		{"activate"},
		{"restore"},
	} {
		if got := run(args); got != 2 {
			t.Fatalf("run(%q) = %d, want usage failure", args, got)
		}
	}
}
