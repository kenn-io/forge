package localruntime

import (
	"context"
	"errors"
	"fmt"
	"slices"
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

// TmuxRuntimeFixture owns runtime state and the private server across test restarts.
// ServerCommand addresses the private server, independently of runtime wrappers.
type TmuxRuntimeFixture struct {
	ServerCommand []string
	t             TmuxFixtureTest
	exitDir       string
	ownerRoot     string
}

// NewTmuxRuntimeFixture stops detached backends before its state is removed.
// serverCommand must come from testtmux.Owner.Command; pass nil for fake commands
// or when the caller owns server cleanup.
func NewTmuxRuntimeFixture(t TmuxFixtureTest, serverCommand []string) *TmuxRuntimeFixture {
	f := &TmuxRuntimeFixture{ServerCommand: serverCommand, t: t, exitDir: t.TempDir(), ownerRoot: t.TempDir()}
	if len(serverCommand) > 0 {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
			defer cancel()
			if err := f.killServerAndWait(ctx, func(ctx context.Context) (string, error) {
				out, err := procutil.CommandContext(ctx, serverCommand[0], append(slices.Clone(serverCommand[1:]), "list-sessions", "-F", "#{session_id}")...).CombinedOutput()
				if isTmuxSessionAbsent(out, err) {
					return "", nil
				}
				return string(out), err
			}); err != nil {
				t.Fatalf("cleanup private tmux server: %v", err)
			}
		})
	}
	return f
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
	if options.AgentExitDir == "" {
		options.AgentExitDir = f.exitDir
	}
	if options.TmuxOwnerMarker == "" {
		options.TmuxOwnerMarker = "kenn-forge:test-runtime-fixture"
	}
	if options.PtyOwnerRuntime == nil {
		options.PtyOwnerRuntime = ptyownerruntime.New(&ptyowner.Client{Root: f.ownerRoot, InProcess: true}, nil)
	}
	options.WrapAgentSessionsInTmux = true
	return f.NewManager(command, options)
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
