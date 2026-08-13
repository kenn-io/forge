//go:build linux

package testtmux

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func processStart(pid int) (string, error) {
	content, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
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
