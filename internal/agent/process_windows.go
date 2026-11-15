//go:build windows

package agent

import "os/exec"

// Windows currently cancels the direct child process. Job-object based process
// tree termination can be added when Windows execution is a supported target.
func configureCommandCancellation(command *exec.Cmd) {}
