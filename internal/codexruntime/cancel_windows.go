//go:build windows

package codexruntime

import "os/exec"

func configureGracefulCancellation(cmd *exec.Cmd) {
	// Keep exec.CommandContext's default hard cancellation on Windows preview.
}
