//go:build !windows

package agent

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// configureCommandCancellation places each operation in a process group so
// cancellation stops the shell and its descendants, not only the parent.
func configureCommandCancellation(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
