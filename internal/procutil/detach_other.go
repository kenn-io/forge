//go:build !windows

package procutil

import (
	"os/exec"
	"syscall"
)

// DetachCommand starts cmd in its own session, so it outlives its parent and
// the parent's terminal.
func DetachCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
