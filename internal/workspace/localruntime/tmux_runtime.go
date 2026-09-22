package localruntime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/creack/pty/v2"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/procutil"
)

// tmuxAgentInputReady checks the application's terminal, not the attach
// client's modes. Tmux advertises bracketed paste even before the agent
// starts; input sent then can be echoed, line-buffered, and lost at startup.
func (m *Manager) tmuxAgentInputReady(ctx context.Context, sessionName string) (bool, error) {
	command := slices.Clone(m.tmuxCommand)
	if len(command) == 0 {
		if target, err := m.target(string(LaunchTargetShell)); err == nil {
			command = slices.Clone(target.Command)
		}
	}
	if len(command) == 0 {
		command = config.DefaultTmuxCommand()
	}
	command, err := resolveTmuxCommand(command)
	if err != nil {
		return false, err
	}
	launcher := tmuxLauncher{Pane: tmuxPaneEnvironment{
		clientEnv: TmuxClientEnvironment(os.Environ(), m.currentStripEnvVars()),
	}}
	output, err := launcher.output(ctx, append(command,
		"display-message", "-p", "-t", sessionName, "#{pane_tty}"))
	if err != nil {
		return false, fmt.Errorf("find agent terminal: %w", err)
	}
	tty, err := os.Open(strings.TrimSpace(string(output)))
	if err != nil {
		return false, fmt.Errorf("open agent terminal: %w", err)
	}
	defer tty.Close()
	// stty uses the same read-only invocation on macOS and Linux.
	cmd := procutil.CommandContext(ctx, "stty", "-a")
	cmd.Stdin = tty
	var modes strings.Builder
	cmd.Stdout = &modes
	if err := procutil.Run(ctx, cmd, "agent terminal input mode"); err != nil {
		return false, fmt.Errorf("read agent terminal mode: %w", err)
	}
	flags := strings.Fields(strings.ReplaceAll(modes.String(), ";", " "))
	return slices.Contains(flags, "-icanon") && slices.Contains(flags, "-echo"), nil
}

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
	// tmux clients get only the non-secret allowlist; the attach also
	// passes -E, so nothing from this environment can be copied into
	// the session environment.
	cmd.Env = append(
		TmuxClientEnvironment(os.Environ(), extraStripVars),
		"TERM=xterm-256color",
	)

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Rows: 30,
		Cols: 120,
		// A retained tmux attach starts before a browser is connected. Seed
		// nonzero cell geometry for tmux versions that do not query the
		// terminal after a character resize. Later resizes preserve this
		// per-cell size while updating the full PTY pixel dimensions.
		X: 120 * 8,
		Y: 30 * 16,
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
