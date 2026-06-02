//go:build !windows

package localruntime

import (
	"errors"
	"os"
	"syscall"
)

func terminateSessionProcess(process *os.Process) error {
	if err := syscall.Kill(-process.Pid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return process.Signal(syscall.SIGTERM)
	}
	return nil
}

func killSessionProcess(process *os.Process) error {
	// pty.StartWithSize sets Setsid, so the launched process is a
	// session/pgid leader. Send SIGKILL to -pid to reach every
	// descendant in the group; otherwise an agent's detached children
	// would outlive the session. Fall back to single-process kill if
	// the group call fails.
	if err := syscall.Kill(-process.Pid, syscall.SIGKILL); err != nil {
		return process.Kill()
	}
	return nil
}
