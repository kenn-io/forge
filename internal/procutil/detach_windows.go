//go:build windows

package procutil

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// DetachCommand starts cmd outside its parent's console and process group,
// so it outlives its parent.
func DetachCommand(cmd *exec.Cmd) {
	ConfigureBackgroundCommand(cmd)
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS
}
