//go:build darwin || linux || freebsd || openbsd || netbsd

package codexruntime

import (
	"errors"
	"os/exec"
	"syscall"
)

func configureOwnedProcessTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateOwnedProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	// The app-server runs in a dedicated process group whose id equals the
	// direct child's pid. Killing the negative pgid therefore targets only this
	// owned tree, including helpers that inherited the Profile environment.
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
