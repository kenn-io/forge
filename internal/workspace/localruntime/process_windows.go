//go:build windows

package localruntime

import (
	"context"
	"os"
	"os/exec"
	"strconv"

	"go.kenn.io/forge/internal/procutil"
)

func terminateSessionProcess(process *os.Process) error {
	return process.Kill()
}

func killSessionProcess(process *os.Process) error {
	cmd := procutil.Command(
		"taskkill", "/T", "/F", "/PID", strconv.Itoa(process.Pid),
	)
	if err := procutil.Run(context.Background(), cmd, "stop ACP process tree"); err != nil {
		return process.Kill()
	}
	return nil
}

func configureACPProcess(cmd *exec.Cmd) {}
