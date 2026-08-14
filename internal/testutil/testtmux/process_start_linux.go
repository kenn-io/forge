//go:build linux

package testtmux

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func processStart(pid int) (string, error) {
	content, err := readProcessStatFromProc(pid, os.ReadFile, func(pid int) error {
		err := unix.Kill(pid, 0)
		if errors.Is(err, unix.ESRCH) {
			return errProcessAbsent
		}
		return err
	})
	if err != nil {
		return "", err
	}
	closingName := strings.LastIndexByte(string(content), ')')
	if closingName < 0 {
		return "", errors.New("process stat has no command terminator")
	}
	fields := strings.Fields(string(content[closingName+1:]))
	if len(fields) <= 19 {
		return "", errors.New("process stat has no start time")
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return "", fmt.Errorf("parse process start time: %w", err)
	}
	return fields[19], nil
}
