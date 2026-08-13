//go:build !windows

package testtmux

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.kenn.io/forge/internal/procutil"
)

type tmuxProcess struct {
	pid     int
	command string
	start   string
}

func tmuxRootBase() string {
	return "/tmp"
}

func validateDirectoryOwner(info fs.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("directory owner is unavailable")
	}
	if int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("owner UID %d does not match %d", stat.Uid, os.Getuid())
	}
	return nil
}

func tmuxProcesses() ([]tmuxProcess, error) {
	command := procutil.Command("ps", "-axo", "pid=,uid=,command=")
	command.Env = append(os.Environ(), "LC_ALL=C")
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("list tmux processes: %w", err)
	}
	var processes []tmuxProcess
	for line := range strings.SplitSeq(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		uid, uidErr := strconv.Atoi(fields[1])
		if pidErr != nil || uidErr != nil || uid != os.Getuid() {
			continue
		}
		processCommand := strings.Join(fields[2:], " ")
		executable := strings.TrimPrefix(filepathBase(fields[2]), "-")
		_, titledServer := tmuxServerTitleSocket(processCommand)
		if executable != "tmux" && !titledServer {
			continue
		}
		start, startErr := processStart(pid)
		if startErr != nil {
			continue
		}
		processes = append(processes, tmuxProcess{
			pid:     pid,
			command: processCommand,
			start:   start,
		})
	}
	return processes, nil
}

func filepathBase(path string) string {
	if index := strings.LastIndexByte(path, '/'); index >= 0 {
		return path[index+1:]
	}
	return path
}

func processStillMatches(
	pid int,
	expectedStart string,
	lookupStart func(int) (string, error),
) bool {
	actualStart, err := lookupStart(pid)
	return err == nil && actualStart == expectedStart
}

func stopProcess(pid int, expectedStart string) error {
	if !processStillMatches(pid, expectedStart, processStart) {
		return nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find stale tmux process %d: %w", pid, err)
	}
	if err := process.Signal(syscall.SIGTERM); err != nil &&
		!errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("terminate stale tmux process %d: %w", pid, err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !processStillMatches(pid, expectedStart, processStart) {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !processStillMatches(pid, expectedStart, processStart) {
		return nil
	}
	if err := process.Signal(syscall.SIGKILL); err != nil &&
		!errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("kill stale tmux process %d: %w", pid, err)
	}
	return nil
}
