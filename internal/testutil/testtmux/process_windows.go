//go:build windows

package testtmux

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"go.kenn.io/forge/internal/procutil"
)

type tmuxProcess struct {
	pid     int
	command string
	start   string
}

func tmuxRootBase() string {
	return os.TempDir()
}

func validateDirectoryOwner(fs.FileInfo) error {
	return nil
}

func processStart(pid int) (string, error) {
	command := procutil.Command(
		"powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
		fmt.Sprintf(
			"(Get-Process -Id %d -ErrorAction Stop).StartTime.ToUniversalTime().Ticks",
			pid,
		),
	)
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	start := strings.TrimSpace(string(output))
	if _, err := strconv.ParseInt(start, 10, 64); err != nil {
		return "", fmt.Errorf("parse process start time: %w", err)
	}
	return start, nil
}

func tmuxProcesses() ([]tmuxProcess, error) {
	return nil, nil
}

func processStillMatches(
	pid int,
	expectedStart string,
	lookupStart func(int) (string, error),
) bool {
	actualStart, err := lookupStart(pid)
	return err == nil && actualStart == expectedStart
}

func stopProcess(int, string) error {
	return errors.New("direct tmux process recovery is unavailable on Windows")
}
