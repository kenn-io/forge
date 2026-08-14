//go:build darwin

package testtmux

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func processStart(pid int) (string, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return unavailableDarwinProcessStart(pid, err)
	}
	if int(info.Proc.P_pid) != pid {
		return unavailableDarwinProcessStart(
			pid, fmt.Errorf("sysctl returned process %d", info.Proc.P_pid),
		)
	}
	start := info.Proc.P_starttime
	return fmt.Sprintf("%d.%06d", start.Sec, start.Usec), nil
}

func unavailableDarwinProcessStart(pid int, identityErr error) (string, error) {
	if err := unix.Kill(pid, 0); errors.Is(err, unix.ESRCH) {
		return "", fmt.Errorf("%w: process %d", errProcessAbsent, pid)
	}
	return "", fmt.Errorf("read process %d identity: %w", pid, identityErr)
}
