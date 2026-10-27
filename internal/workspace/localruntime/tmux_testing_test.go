package localruntime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	ptyownerruntime "go.kenn.io/forge/internal/ptyowner/runtime"
)

func TestTmuxRuntimeFixtureRejectsKillWithoutPrivateServer(t *testing.T) {
	fixture := NewTmuxRuntimeFixture(t, nil)

	err := fixture.killServerAndWait(t.Context(), nil)

	require.EqualError(t, err, "fixture has no private tmux server command")
}

func TestTmuxRuntimeFixtureStopsDetachedPtyOwnerAgent(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	// Register TempDir cleanup first so the owner remains observable after fixture cleanup.
	cwd := t.TempDir()
	var owner ptyownerruntime.Owner
	var sessionKey string
	t.Cleanup(func() {
		if owner == nil || sessionKey == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer cancel()
		// Reap the real backend only after checking what fixture cleanup did.
		defer func() { require.NoError(t, owner.Stop(ctx, sessionKey)) }()
		require.Eventually(t, func() bool { return owner.Gone(ctx, sessionKey) }, 5*time.Second, 10*time.Millisecond,
			"fixture cleanup left the detached PTY-owner agent running")
	})
	fixture := NewTmuxRuntimeFixture(t, nil)
	manager := fixture.NewAgentManager([]string{"/missing/fixture-tmux"}, Options{
		Targets: []LaunchTarget{helperTarget("agent", "sleep")},
	})
	owner = manager.ptyOwnerRuntime
	info, err := manager.Launch(t.Context(), "ws:alpha", cwd, "agent")
	require.NoError(t, err)
	sessionKey = info.Key
	require.Empty(t, info.TmuxSession, "the agent must use the PTY-owner fallback")
	require.False(t, owner.Gone(t.Context(), sessionKey))

	manager.Shutdown()

	require.Empty(t, manager.ListSessions("ws:alpha"))
	require.False(t, owner.Gone(t.Context(), sessionKey), "shutdown must detach the durable owner")
}
