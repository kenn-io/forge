//go:build darwin

package testtmux

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func processStart(pid int) (string, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	if int(info.Proc.P_pid) != pid {
		return "", fmt.Errorf("process %d identity is unavailable", pid)
	}
	start := info.Proc.P_starttime
	return fmt.Sprintf("%d.%06d", start.Sec, start.Usec), nil
}
