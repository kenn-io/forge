package localruntime

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/ptyowner"
	ptyownerruntime "go.kenn.io/forge/internal/ptyowner/runtime"
)

// TmuxFixtureTest supplies the lifecycle operations used by TmuxRuntimeFixture.
type TmuxFixtureTest interface {
	Context() context.Context
	TempDir() string
	Cleanup(func())
	Helper()
	Fatalf(string, ...any)
}

// TmuxRuntimeFixture owns managers and shared agent state across test restarts.
// Command is the private server command supplied by workspacetest.
type TmuxRuntimeFixture struct {
	Command    []string
	t          TmuxFixtureTest
	exitDir    string
	ownerRoot  string
	managers   []*Manager
	workspaces map[*Manager][]string
}

// NewTmuxRuntimeFixture registers final cleanup without changing Shutdown's
// detach semantics during a simulated daemon restart.
func NewTmuxRuntimeFixture(t TmuxFixtureTest) *TmuxRuntimeFixture {
	return &TmuxRuntimeFixture{t: t, exitDir: t.TempDir(), ownerRoot: t.TempDir(), workspaces: make(map[*Manager][]string)}
}

func (f *TmuxRuntimeFixture) registerCleanup() {
	t := f.t
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		for _, manager := range slices.Backward(f.managers) {
			for _, id := range f.workspaces[manager] {
				manager.StopWorkspace(ctx, id)
			}
			manager.Shutdown()
		}
	})
}

// NewManager preserves scenario options, including fake command-resolution inputs.
func (f *TmuxRuntimeFixture) NewManager(command []string, options Options, workspaceIDs ...string) *Manager {
	if len(f.managers) == 0 {
		f.registerCleanup()
	}
	options.TmuxCommand = command
	manager := NewManager(options)
	f.managers = append(f.managers, manager)
	f.workspaces[manager] = workspaceIDs
	return manager
}

// NewAgentManager wires the exit records and PTY owner used by daemon agent runs.
func (f *TmuxRuntimeFixture) NewAgentManager(command []string, options Options, workspaceIDs ...string) *Manager {
	if options.AgentExitDir == "" {
		options.AgentExitDir = f.exitDir
	}
	options.WrapAgentSessionsInTmux = true
	options.TmuxOwnerMarker = "kenn-forge:test-runtime-fixture"
	options.PtyOwnerRuntime = ptyownerruntime.New(&ptyowner.Client{Root: f.ownerRoot, InProcess: true}, nil)
	return f.NewManager(command, options, workspaceIDs...)
}

// WaitForServerGone waits for the observed server identity to disappear.
func (f *TmuxRuntimeFixture) WaitForServerGone(serverID func(context.Context) (string, error)) {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		id, err := serverID(f.t.Context())
		if err == nil && id == "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatalf("tmux server did not disappear within five seconds")
}

// WaitForClient returns the attached client PID before a test kills or detaches it.
func (f *TmuxRuntimeFixture) WaitForClient(command []string, session string) int {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out, err := procutil.CommandContext(f.t.Context(), command[0], append(command[1:], "list-clients", "-t", session, "-F", "#{client_pid}")...).Output()
		if err == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
			if err == nil {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatalf("tmux client did not attach within five seconds")
	return 0
}
