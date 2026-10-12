package localruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// AgentExit is how a tmux-backed agent's pane process ended.
type AgentExit struct {
	// Recorded reports that the pane wrapper outlived the agent and saved its
	// status. A started agent without a record had its wrapper killed too.
	Recorded bool
	Status   int
}

// Killed reports whether the agent was ended by a signal rather than
// finishing: its wrapper died with it, or it died from SIGHUP, SIGKILL, or
// SIGTERM, as on a host shutdown.
func (e AgentExit) Killed() bool {
	if !e.Recorded {
		return true
	}
	switch e.Status {
	case 128 + 1, 128 + 9, 128 + 15:
		return true
	default:
		return false
	}
}

type agentExitPaths struct {
	started string
	exited  string
}

func (m *Manager) agentExitPaths(tmuxSession string) agentExitPaths {
	base := filepath.Join(m.agentExitDir, tmuxSession)
	return agentExitPaths{started: base + ".started", exited: base + ".exit"}
}

// recordAgentExitPaneCommand runs the agent under a shell that outlives it and
// records its exit status. The shell catches INT and QUIT so an interrupt
// cannot end the pane while the agent keeps running; the agent still gets
// default handling. HUP and TERM keep their default, so a killed wrapper leaves
// no status behind.
func recordAgentExitPaneCommand(paneCommand string, paths agentExitPaths) string {
	command := strings.TrimPrefix(paneCommand, "exec ")
	exitTmp := paths.exited + ".tmp"
	return strings.Join([]string{
		"trap : INT QUIT",
		": > " + shellCommand([]string{paths.started}),
		command,
		"__kenn_forge_status=$?",
		`printf '%s\n' "$__kenn_forge_status" > ` + shellCommand([]string{exitTmp}) +
			" && mv " + shellCommand([]string{exitTmp, paths.exited}),
		`exit "$__kenn_forge_status"`,
	}, "\n")
}

// AgentExit reads how the agent in tmuxSession ended. It reports false for a
// session launched without a record, such as one started before records
// existed.
func (m *Manager) AgentExit(tmuxSession string) (AgentExit, bool) {
	if m.agentExitDir == "" || tmuxSession == "" {
		return AgentExit{}, false
	}
	paths := m.agentExitPaths(tmuxSession)
	if _, err := os.Stat(paths.started); err != nil {
		return AgentExit{}, false
	}
	data, err := os.ReadFile(paths.exited)
	if err != nil {
		return AgentExit{}, true
	}
	status, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return AgentExit{}, true
	}
	return AgentExit{Recorded: true, Status: status}, true
}

// ForgetAgentExit removes the exit record of tmuxSession.
func (m *Manager) ForgetAgentExit(tmuxSession string) {
	if m.agentExitDir == "" || tmuxSession == "" {
		return
	}
	paths := m.agentExitPaths(tmuxSession)
	for _, path := range []string{paths.started, paths.exited, paths.exited + ".tmp"} {
		_ = os.Remove(path)
	}
}

// PruneAgentExits removes records whose tmux session no longer exists, such as
// those left by agents a reboot killed.
func (m *Manager) PruneAgentExits(ctx context.Context) {
	if m.agentExitDir == "" {
		return
	}
	entries, err := os.ReadDir(m.agentExitDir)
	if err != nil {
		return
	}
	seen := make(map[string]bool)
	for _, entry := range entries {
		session, _, _ := strings.Cut(entry.Name(), ".")
		if session == "" || seen[session] {
			continue
		}
		seen[session] = true
		if err := m.requireTmuxSession(ctx, session); errors.Is(err, ErrSessionNotFound) {
			m.ForgetAgentExit(session)
		}
	}
}
