//go:build !windows

package ptyowner

import (
	"os"
	"syscall"

	gopty "github.com/aymanbagabas/go-pty"
)

func validateCommandLine([]string) error { return nil }

func configureOwnerCommand(cmd *gopty.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func killOwnerProcess(process *os.Process) {
	if err := syscall.Kill(-process.Pid, syscall.SIGKILL); err != nil {
		_ = process.Kill()
	}
}
