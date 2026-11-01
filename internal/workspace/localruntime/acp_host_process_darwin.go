package localruntime

import (
	"bytes"
	"encoding/binary"

	"golang.org/x/sys/unix"
)

// ACPHostsCanShareAgents reports whether a shared host can list what its
// agent runs, so binding a chat notices a replaced agent. Sharing needs it.
const ACPHostsCanShareAgents = true

// acpProcessTree lists the process at pid and its descendants, each before
// its children, from the kern.proc.all process table and each process's
// kern.procargs2, which ps reads too.
func acpProcessTree(pid int) []agentProcess {
	children := make(map[int][]int)
	if table, err := unix.SysctlKinfoProcSlice("kern.proc.all"); err == nil {
		for i := range table {
			parent := int(table[i].Eproc.Ppid)
			children[parent] = append(children[parent], int(table[i].Proc.P_pid))
		}
	}
	var processes []agentProcess
	var walk func(int)
	walk = func(pid int) {
		processes = append(processes, darwinProcess(pid))
		for _, child := range children[pid] {
			walk(child)
		}
	}
	walk(pid)
	return processes
}

// darwinProcess reads a process's kern.procargs2: a 32-bit argument count,
// the path the kernel executed (an interpreter for a script), NUL padding,
// then the arguments and the environment, each NUL-terminated.
func darwinProcess(pid int) agentProcess {
	buf, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(buf) < 4 {
		return agentProcess{}
	}
	argc := int(binary.NativeEndian.Uint32(buf))
	exe, rest, _ := bytes.Cut(buf[4:], []byte{0})
	process := agentProcess{Exe: string(exe)}
	rest = bytes.TrimLeft(rest, "\x00")
	for len(process.Args) < argc && len(rest) > 0 {
		var arg []byte
		arg, rest, _ = bytes.Cut(rest, []byte{0})
		process.Args = append(process.Args, string(arg))
	}
	return process
}
