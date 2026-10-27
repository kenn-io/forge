package workspacetest

import (
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/testutil/testtmux"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

func TestMain(m *testing.M) {
	if code, ok := testtmux.CommandWrapperExitCode(); ok {
		os.Exit(code)
	}
	os.Exit(m.Run())
}

func TestTmuxRuntimeFixtureStopsAgentsBeforeServerCleanup(t *testing.T) {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil || !testtmux.Supported() {
		t.Skip("requires tmux on a supported host")
	}
	owner, err := testtmux.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Cleanup()) })
	command := owner.Command(t, tmuxPath)
	t.Run("detached agent", func(t *testing.T) {
		var session string
		// This runs after fixture cleanup while the parent still owns the server.
		t.Cleanup(func() {
			require.Error(t, procutil.Command(command[0], append(command[1:], "has-session", "-t", session)...).Run())
		})
		fixture := localruntime.NewTmuxRuntimeFixture(t, command)
		manager := fixture.NewAgentManager(command, localruntime.Options{
			Targets: []localruntime.LaunchTarget{{Key: "agent", Kind: localruntime.LaunchTargetAgent, Available: true, Command: []string{"/bin/sh", "-c", "exec sleep 600"}}, {Key: "shell", Kind: localruntime.LaunchTargetShell, Available: true, Command: command}},
		})
		info, err := manager.Launch(t.Context(), "workspace-b", t.TempDir(), "agent")
		require.NoError(t, err)
		session = info.TmuxSession
		require.NotEmpty(t, session)
		manager.Shutdown()
		require.NoError(t, procutil.CommandContext(t.Context(), command[0], append(command[1:], "has-session", "-t", session)...).Run())
	})
}
