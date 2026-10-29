package localruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/ptyowner"
	ptyownerruntime "go.kenn.io/forge/internal/ptyowner/runtime"
)

// TmuxFixtureTest supplies the lifecycle operations used by TmuxRuntimeFixture.
type TmuxFixtureTest interface {
	Context() context.Context
	Cleanup(func())
	Helper()
	Fatalf(string, ...any)
}

// TmuxRuntimeFixture owns runtime state and the private server across test restarts.
// ServerCommand addresses the private server, independently of runtime wrappers.
type TmuxRuntimeFixture struct {
	ServerCommand  []string
	t              TmuxFixtureTest
	root           string
	exitDir        string
	ownerRoot      string
	environmentDir string
}

// NewTmuxRuntimeFixture stops detached backends before its state is removed.
// serverCommand must come from testtmux.Owner.Command; pass nil for fake commands
// or when the caller owns server cleanup.
func NewTmuxRuntimeFixture(t TmuxFixtureTest, serverCommand []string) *TmuxRuntimeFixture {
	f := &TmuxRuntimeFixture{ServerCommand: serverCommand, t: t}
	// Cleanups run in reverse, so server shutdown precedes state removal while
	// callbacks registered after construction still observe the live server.
	t.Cleanup(f.removeState)
	if len(serverCommand) > 0 {
		t.Cleanup(f.cleanupServer)
	}
	return f
}

func (f *TmuxRuntimeFixture) cleanupServer() {
	// Owner release runs later; detached agents must exit before fixture state is removed.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(f.t.Context()), 5*time.Second)
	defer cancel()
	command := f.ServerCommand
	if err := f.killServerAndWait(ctx, func(ctx context.Context) (string, error) {
		out, err := procutil.CommandContext(ctx, command[0], append(slices.Clone(command[1:]), "list-sessions", "-F", "#{session_id}")...).CombinedOutput()
		if isTmuxSessionAbsent(out, err) {
			return "", nil
		}
		return string(out), err
	}); err != nil {
		f.t.Fatalf("cleanup private tmux server: %v", err)
	}
}

// NewManager preserves scenario options, including fake command-resolution inputs.
func (f *TmuxRuntimeFixture) NewManager(command []string, options Options) *Manager {
	options.TmuxCommand = command
	manager := NewManager(options)
	f.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(f.t.Context()), 5*time.Second)
		defer cancel()
		manager.mu.Lock()
		workspaces := make(map[string]struct{})
		for _, session := range manager.sessions {
			workspaces[session.snapshot().WorkspaceID] = struct{}{}
		}
		manager.mu.Unlock()
		for id := range workspaces {
			manager.StopWorkspace(ctx, id)
		}
		manager.Shutdown()
	})
	return manager
}

// NewAgentManager supplies daemon agent defaults while preserving explicit options.
func (f *TmuxRuntimeFixture) NewAgentManager(command []string, options Options) *Manager {
	if options.TmuxEnvironmentDir == "" {
		options.TmuxEnvironmentDir = f.stateDir(&f.environmentDir, "handoff")
	}
	if options.AgentExitDir == "" {
		options.AgentExitDir = f.stateDir(&f.exitDir, "exit")
	}
	if options.TmuxOwnerMarker == "" {
		options.TmuxOwnerMarker = "kenn-forge:test-runtime-fixture"
	}
	if options.PtyOwnerRuntime == nil {
		options.PtyOwnerRuntime = f.newPtyOwnerRuntime()
	}
	options.WrapAgentSessionsInTmux = true
	return f.NewManager(command, options)
}

func (f *TmuxRuntimeFixture) stateDir(dir *string, name string) string {
	if *dir == "" {
		*dir = f.newStateDir(name)
	}
	return *dir
}

func (f *TmuxRuntimeFixture) newStateDir(name string) string {
	dir := filepath.Join(f.stateRoot(), name)
	if err := os.Mkdir(dir, 0o700); err != nil {
		f.t.Fatalf("create fixture %s directory: %v", name, err)
	}
	return dir
}

// stateRoot allocates agent state on first use; raw managers allocate none.
func (f *TmuxRuntimeFixture) stateRoot() string {
	if f.root == "" {
		root, err := os.MkdirTemp("", "localruntime-fixture-")
		if err != nil {
			f.t.Fatalf("create fixture state root: %v", err)
		}
		f.root = root
	}
	return f.root
}

func (f *TmuxRuntimeFixture) removeState() {
	if f.root == "" {
		return
	}
	if err := os.RemoveAll(f.root); err != nil {
		f.t.Fatalf("remove fixture state: %v", err)
	}
}

// newPtyOwnerRuntime stops owners under the fixture root after its managers'
// cleanup, since Manager.Shutdown detaches them without stopping.
func (f *TmuxRuntimeFixture) newPtyOwnerRuntime() ptyownerruntime.Owner {
	client := &ptyowner.Client{Root: f.stateDir(&f.ownerRoot, "pty-owner"), InProcess: true}
	f.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(f.t.Context()), 5*time.Second)
		defer cancel()
		// Stop resolves each session directory's name back to that directory.
		entries, err := os.ReadDir(f.ownerRoot)
		for _, entry := range entries {
			if entry.IsDir() {
				err = errors.Join(err, client.Stop(ctx, entry.Name()))
			}
		}
		if err != nil {
			f.t.Fatalf("stop fixture PTY owners: %v", err)
		}
	})
	return ptyownerruntime.New(client, nil)
}

// KillServerAndWait bounds the kill and all identity probes by one deadline.
func (f *TmuxRuntimeFixture) KillServerAndWait(serverID func(context.Context) (string, error)) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.t.Context(), 5*time.Second)
	defer cancel()
	if err := f.killServerAndWait(ctx, serverID); err != nil {
		f.t.Fatalf("kill private tmux server: %v", err)
	}
}

func (f *TmuxRuntimeFixture) killServerAndWait(ctx context.Context, serverID func(context.Context) (string, error)) error {
	command := f.ServerCommand
	if len(command) == 0 {
		return errors.New("fixture has no private tmux server command")
	}
	killErr := procutil.CommandContext(ctx, command[0], append(slices.Clone(command[1:]), "kill-server")...).Run()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		id, err := serverID(ctx)
		if err == nil && id == "" {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for tmux server: %w", errors.Join(killErr, ctx.Err()))
		case <-ticker.C:
		}
	}
}
