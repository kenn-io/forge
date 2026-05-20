//go:build windows

package ptyowner

import (
	"os/exec"
	"syscall"

	"github.com/wesm/middleman/internal/procutil"
)

const (
	windowsDetachedProcess       = 0x00000008
	windowsCreateNewProcessGroup = 0x00000200
)

func detachCommand(cmd *exec.Cmd) {
	procutil.ConfigureBackgroundCommand(cmd)
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windowsCreateNewProcessGroup |
		windowsDetachedProcess
}
