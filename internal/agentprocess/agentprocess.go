// Package agentprocess identifies the coding agent that ran a hook, so the
// daemon can later tell whether that agent still runs.
package agentprocess

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Identity names one process across PID reuse. A zero PID means unknown.
type Identity struct {
	PID   int
	Start string
}

// processInfo is what a platform reads about one process.
type processInfo struct {
	parent int
	name   string
	start  string
	exited bool
}

// hookShells are the shells agents run hook commands through.
var hookShells = []string{"sh", "bash", "dash", "zsh", "fish"}

// Current returns the agent that ran this hook: the parent process, or the
// parent of the one shell the agent ran the hook command through. It stops
// after one shell because the next shell up is the terminal the agent runs in,
// which outlives the agent.
func Current() Identity {
	return currentFrom(os.Getppid(), readProcess)
}

func currentFrom(pid int, read func(int) (processInfo, bool)) Identity {
	info, ok := read(pid)
	if !ok {
		return Identity{}
	}
	if slices.Contains(hookShells, strings.TrimPrefix(filepath.Base(info.name), "-")) {
		pid = info.parent
		if info, ok = read(pid); !ok {
			return Identity{}
		}
	}
	return Identity{PID: pid, Start: info.start}
}

// Gone reports whether the identified process is known to have ended. An
// unknown identity or a process that cannot be read is not known gone, so a
// caller that must not start a second agent keeps waiting.
func (id Identity) Gone() bool {
	if id.PID == 0 || id.Start == "" {
		return false
	}
	info, known, ok := readProcessState(id.PID)
	if !known {
		return false
	}
	return !ok || info.exited || info.start != id.Start
}

func readProcess(pid int) (processInfo, bool) {
	info, known, ok := readProcessState(pid)
	return info, known && ok
}
