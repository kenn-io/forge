//go:build darwin

package agentprocess

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// darwinZombie is SZOMB from sys/proc.h, which x/sys/unix does not export.
const darwinZombie = 5

// readProcessState reports the process, whether its state is known, and
// whether it exists.
func readProcessState(pid int) (processInfo, bool, bool) {
	kinfo, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(kinfo.Proc.P_pid) != pid {
		if errors.Is(unix.Kill(pid, 0), unix.ESRCH) {
			return processInfo{}, true, false
		}
		return processInfo{}, false, false
	}
	start := kinfo.Proc.P_starttime
	return processInfo{
		parent: int(kinfo.Eproc.Ppid),
		name:   unix.ByteSliceToString(kinfo.Proc.P_comm[:]),
		start:  fmt.Sprintf("%d.%06d", start.Sec, start.Usec),
		exited: kinfo.Proc.P_stat == darwinZombie,
	}, true, true
}
