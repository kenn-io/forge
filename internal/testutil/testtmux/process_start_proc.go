package testtmux

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func readProcessStatFromProc(
	pid int,
	readFile func(string) ([]byte, error),
	probeProcess func(int) error,
) ([]byte, error) {
	content, err := readFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		// The kernel reports ESRCH from the read itself when the task exits
		// between opening and reading its stat file; that is a definitive
		// absence, so no liveness probe is needed.
		if errors.Is(err, syscall.ESRCH) {
			return nil, fmt.Errorf("%w: process %d", errProcessAbsent, pid)
		}
		if errors.Is(err, os.ErrNotExist) &&
			errors.Is(probeProcess(pid), errProcessAbsent) {
			return nil, fmt.Errorf("%w: process %d", errProcessAbsent, pid)
		}
		return nil, err
	}
	return content, nil
}
