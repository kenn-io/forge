//go:build windows

package localruntime

import (
	"os"
	"os/exec"
)

func terminateSessionProcess(process *os.Process) error {
	return process.Kill()
}

func killSessionProcess(process *os.Process) error {
	return process.Kill()
}

func configureACPProcess(cmd *exec.Cmd) {}
