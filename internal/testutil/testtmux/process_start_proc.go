package testtmux

import (
	"errors"
	"fmt"
	"os"
)

func readProcessStatFromProc(
	pid int,
	readFile func(string) ([]byte, error),
	probeProcess func(int) error,
) ([]byte, error) {
	content, err := readFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) &&
			errors.Is(probeProcess(pid), errProcessAbsent) {
			return nil, fmt.Errorf("%w: process %d", errProcessAbsent, pid)
		}
		return nil, err
	}
	return content, nil
}
