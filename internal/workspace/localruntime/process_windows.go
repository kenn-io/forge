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

func configureACPProcess(cmd *exec.Cmd) {
	_, err := windows.GetConsoleCP()
	shareParentConsole(cmd, err == nil)
}

// shareParentConsole lets the agent join a console the parent already has
// (acp-owner's ConPTY), so it costs no console of its own; a console-less
// parent keeps CREATE_NO_WINDOW so no window opens.
func shareParentConsole(cmd *exec.Cmd, hasConsole bool) {
	if hasConsole {
		cmd.SysProcAttr.CreationFlags &^= windows.CREATE_NO_WINDOW
	}
}
