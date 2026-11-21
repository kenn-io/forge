//go:build linux

package agentprocess

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// readProcessState reports the process, whether its state is known, and
// whether it exists.
func readProcessState(pid int) (processInfo, bool, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if errors.Is(err, os.ErrNotExist) {
		return processInfo{}, true, false
	}
	if err != nil {
		return processInfo{}, false, false
	}
	info, err := parseProcStat(string(data))
	if err != nil {
		return processInfo{}, false, false
	}
	return info, true, true
}

// parseProcStat reads proc(5) stat fields. The command name sits in parentheses
// and may itself contain spaces and parentheses, so fields count from the last
// closing parenthesis: state, ppid, and at field 22 starttime.
func parseProcStat(stat string) (processInfo, error) {
	open := strings.IndexByte(stat, '(')
	end := strings.LastIndexByte(stat, ')')
	if open < 0 || end < open {
		return processInfo{}, errors.New("process stat has no command name")
	}
	fields := strings.Fields(stat[end+1:])
	// fields[0] is field 3 of proc(5), so starttime (22) is fields[19].
	if len(fields) < 20 {
		return processInfo{}, errors.New("process stat is too short")
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return processInfo{}, fmt.Errorf("process stat parent: %w", err)
	}
	return processInfo{
		parent: parent,
		name:   stat[open+1 : end],
		start:  fields[19],
		exited: fields[0] == "Z" || fields[0] == "X",
	}, nil
}
