//go:build !darwin && !linux && !windows

package testtmux

import (
	"errors"
	"os"
	"strconv"
	"strings"

	"go.kenn.io/forge/internal/procutil"
)

func processStart(pid int) (string, error) {
	command := procutil.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=")
	command.Env = append(os.Environ(), "LC_ALL=C")
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	start := strings.TrimSpace(string(output))
	if start == "" {
		return "", errors.New("process start time is empty")
	}
	return start, nil
}
