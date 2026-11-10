//go:build darwin || linux || freebsd || openbsd || netbsd

package codexruntime

import (
	"errors"
	"os"
	"os/exec"
	"time"
)

func configureGracefulCancellation(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := cmd.Process.Signal(os.Interrupt)
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second
}
