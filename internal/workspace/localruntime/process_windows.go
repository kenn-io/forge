//go:build windows

package localruntime

import (
	"context"
	"os"
	"os/exec"
	"strconv"

	"golang.org/x/sys/windows"

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

// configureACPProcess lets the agent join a console the parent already has,
// so it costs no console of its own. That is acp-owner's ConPTY for workspace
// agents and the daemon's console, if any, for the settings test handshake. A
// console-less parent keeps CREATE_NO_WINDOW so no window opens.
func configureACPProcess(cmd *exec.Cmd) {
	if _, err := windows.GetConsoleCP(); err == nil {
		cmd.SysProcAttr.CreationFlags &^= windows.CREATE_NO_WINDOW
	}
}
