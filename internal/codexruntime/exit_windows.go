//go:build windows

package codexruntime

import (
	"errors"
	"os/exec"
)

func ChildExitCode(err error) (int, bool) {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return 0, false
	}
	code := exitErr.ExitCode()
	return code, code >= 0
}
