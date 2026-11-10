//go:build darwin || linux || freebsd || openbsd || netbsd

package codexruntime

import (
	"errors"
	"os/exec"
	"syscall"
)

func ChildExitCode(err error) (int, bool) {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return 0, false
	}
	if code := exitErr.ExitCode(); code >= 0 {
		return code, true
	}
	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal()), true
	}
	return 0, false
}
