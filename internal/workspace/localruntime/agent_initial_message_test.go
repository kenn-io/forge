package localruntime

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	shellquote "github.com/kballard/go-shellquote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/testtmux"
)

func TestClaudeInitialMessageWaitsForTrustWithRealCLI(t *testing.T) {
	if !testtmux.Supported() {
		t.Skip("requires private tmux")
	}
	claude, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude unavailable")
	}
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux unavailable")
	}
	require := require.New(t)
	dir := t.TempDir()
	// Keep onboarding and authentication out of this check; the workspace is
	// deliberately untrusted. /help runs locally without a model request.
	require.NoError(os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(
		`{"hasCompletedOnboarding":true,"theme":"dark","customApiKeyResponses":{"approved":["test-key"],"rejected":[]}}`,
	), 0o600))
	wrapper := filepath.Join(dir, "claude")
	command := []string{
		"env", "CLAUDE_CONFIG_DIR=" + dir, "ANTHROPIC_API_KEY=test-key",
		"ANTHROPIC_BASE_URL=http://127.0.0.1:1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", claude,
	}
	require.NoError(os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+shellquote.Join(command...)+" \"$@\"\n"), 0o700))
	tmuxCommand := privateTmuxOwner.Command(t, tmux)
	mgr := NewManager(Options{
		TmuxCommand: tmuxCommand, WrapAgentSessionsInTmux: true,
		Targets: []LaunchTarget{{
			Key: "reviewer", Kind: LaunchTargetAgent, Available: true,
			Command: []string{
				wrapper, "--dangerously-skip-permissions", "--tools", "", "--strict-mcp-config",
				"--settings", `{"skipDangerousModePermissionPrompt":true}`,
			},
		}, {Key: string(LaunchTargetShell), Kind: LaunchTargetShell, Available: true}},
	})
	t.Cleanup(mgr.Shutdown)
	info, err := mgr.LaunchWithInitialMessage(t.Context(), "ws-1", dir, "reviewer", "/help")
	require.NoError(err)
	if !info.InitialMessageProvided {
		require.Eventually(func() bool {
			return mgr.SubmitInitialMessage(t.Context(), "ws-1", info.Key, "/help") == nil
		}, 10*time.Second, 100*time.Millisecond)
	}
	capture := append(slices.Clone(tmuxCommand[1:]), "capture-pane", "-p", "-S", "-80", "-t", info.TmuxSession)
	require.Eventually(func() bool {
		out, err := exec.CommandContext(t.Context(), tmuxCommand[0], capture...).CombinedOutput()
		return err == nil && strings.Contains(string(out), "Yes, I trust this folder")
	}, 10*time.Second, 100*time.Millisecond, "the initial prompt must leave the trust decision to the user")
	attachment, err := mgr.AttachSession("ws-1", info.Key)
	require.NoError(err)
	defer attachment.Close()
	require.Eventually(func() bool {
		out, err := exec.CommandContext(t.Context(), tmuxCommand[0], capture...).CombinedOutput()
		if err == nil && strings.Contains(string(out), "❯ Yes, I trust this folder") {
			return true
		}
		_ = attachment.Write([]byte("\x1b[B"))
		return false
	}, 10*time.Second, 100*time.Millisecond)
	require.NoError(attachment.Write([]byte("\r")))
	require.EventuallyWithT(func(c *assert.CollectT) {
		out, err := exec.CommandContext(t.Context(), tmuxCommand[0], capture...).CombinedOutput()
		assert.NoError(c, err)
		assert.Contains(c, string(out), "For more help:")
	}, 10*time.Second, 100*time.Millisecond, "Claude must run the queued prompt after the trust decision")
}
