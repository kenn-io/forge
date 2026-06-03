package localruntime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"

	"github.com/creack/pty/v2"

	"go.kenn.io/middleman/internal/procutil"
)

type tmuxAttachLifecycle struct {
	cmd  *exec.Cmd
	ptmx *os.File
}

func (l tmuxAttachLifecycle) Detach() {
	if l.cmd != nil && l.cmd.Process != nil {
		_ = terminateSessionProcess(l.cmd.Process)
	}
	if l.ptmx != nil {
		_ = l.ptmx.Close()
	}
}

func (l tmuxAttachLifecycle) Stop(context.Context) error {
	if l.cmd != nil && l.cmd.Process != nil {
		_ = killSessionProcess(l.cmd.Process)
	}
	if l.ptmx != nil {
		_ = l.ptmx.Close()
	}
	return nil
}

func startTmuxAttachSession(
	info SessionInfo,
	command []string,
	cwd string,
	extraStripVars []string,
) (*session, error) {
	if len(command) == 0 || command[0] == "" {
		return nil, errors.New("session command is empty")
	}

	// Resolve the executable to an absolute path so workspace-relative paths
	// cannot shadow trusted commands.
	resolvedPath, err := resolveExecutable(command[0])
	if err != nil {
		return nil, err
	}
	slog.Debug(
		"runtime tmux attach resolving command",
		"workspace_id", info.WorkspaceID,
		"session_key", info.Key,
		"target_key", info.TargetKey,
		"program", resolvedPath,
		"argc", len(command),
		"cwd", cwd,
	)

	cmd := procutil.Command(resolvedPath, command[1:]...)
	cmd.Env = append(
		sessionEnvironment(os.Environ(), extraStripVars),
		"TERM=xterm-256color",
	)

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Rows: 30,
		Cols: 120,
	})
	if err != nil {
		return nil, fmt.Errorf("start tmux attach pty: %w", err)
	}
	slog.Debug(
		"runtime tmux attach pty started",
		"workspace_id", info.WorkspaceID,
		"session_key", info.Key,
		"target_key", info.TargetKey,
		"pid", cmd.Process.Pid,
	)

	info.Status = SessionStatusRunning
	s := &session{
		info:        info,
		cmd:         cmd,
		ptmx:        ptmx,
		tmuxSession: info.TmuxSession,
		lifecycle: tmuxAttachLifecycle{
			cmd:  cmd,
			ptmx: ptmx,
		},
		done:        make(chan struct{}),
		outputDone:  make(chan struct{}),
		subscribers: make(map[chan []byte]struct{}),
	}
	go s.drainOutput()
	return s, nil
}
