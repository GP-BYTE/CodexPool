//go:build windows

package codexruntime

import "os/exec"

func configureOwnedProcessTree(cmd *exec.Cmd) {}

func terminateOwnedProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
