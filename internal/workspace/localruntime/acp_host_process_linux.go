package localruntime

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ACPHostsCanShareAgents reports whether a shared host can list what its
// agent runs, so binding a chat notices a replaced agent. Sharing needs it.
const ACPHostsCanShareAgents = true

// acpProcessTree lists the process at pid and its descendants, each before
// its children, from /proc.
func acpProcessTree(pid int) []agentProcess {
	var processes []agentProcess
	var walk func(int)
	walk = func(pid int) {
		proc := filepath.Join("/proc", strconv.Itoa(pid))
		var process agentProcess
		if exe, err := os.Readlink(filepath.Join(proc, "exe")); err == nil {
			process.Exe = strings.TrimSuffix(exe, " (deleted)")
		}
		if cmdline, err := os.ReadFile(filepath.Join(proc, "cmdline")); err == nil {
			process.Args = strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
		}
		processes = append(processes, process)
		children, _ := filepath.Glob(filepath.Join(proc, "task", "*", "children"))
		for _, file := range children {
			data, _ := os.ReadFile(file)
			for field := range strings.FieldsSeq(string(data)) {
				if child, err := strconv.Atoi(field); err == nil {
					walk(child)
				}
			}
		}
	}
	walk(pid)
	return processes
}
