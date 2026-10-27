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
	var command []string
	var session string
	t.Run("agent", func(t *testing.T) {
		fixture := NewTmuxRuntimeFixture(t, owner, tmuxPath)
		command = fixture.Command
		// Register this before the runtime so it observes runtime cleanup while
		// the private server still exists. The extra session keeps it alive.
		require.NoError(t, procutil.CommandContext(t.Context(), command[0], append(command[1:], "new-session", "-d", "-s", "keeper", "sleep 600")...).Run())
		t.Cleanup(func() {
			require.NoError(t, procutil.Command(command[0], append(command[1:], "has-session", "-t", "keeper")...).Run())
			require.Error(t, procutil.Command(command[0], append(command[1:], "has-session", "-t", session)...).Run())
		})
		manager := fixture.NewAgentManager(command, localruntime.Options{
			Targets: []localruntime.LaunchTarget{{Key: "agent", Kind: localruntime.LaunchTargetAgent, Available: true, Command: []string{"/bin/sh", "-c", "exec sleep 600"}}, {Key: "shell", Kind: localruntime.LaunchTargetShell, Available: true, Command: command}},
		}, "workspace")
		info, err := manager.Launch(t.Context(), "workspace", t.TempDir(), "agent")
		require.NoError(t, err)
		session = info.TmuxSession
		require.NotEmpty(t, session)
		require.NoError(t, procutil.CommandContext(t.Context(), command[0], append(command[1:], "has-session", "-t", session)...).Run())
	})
	require.Error(t, procutil.Command(command[0], append(command[1:], "has-session", "-t", session)...).Run())
}
