//go:build !windows

package workspaceapi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/agentactivity"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/ptyowner"
	ptyownerruntime "go.kenn.io/forge/internal/ptyowner/runtime"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/internal/workspace"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

// A shutdown can signal the agents before tmux and Forge. The session then
// ends cleanly, as if the agent finished, yet the agent was killed and must
// be kept. Killing the whole pane is the shutdown case; killing only the
// agent leaves its wrapper to record the signal.
func TestRuntimeExitKeepsAgentKilledBeforeTmux(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"pane killed", "agent killed"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require := require.New(t)
			ctx := t.Context()
			if privateTmuxOwner == nil {
				t.Skip("private tmux tests unsupported")
			}
			tmuxPath, err := exec.LookPath("tmux")
			require.NoError(err)
			tmux := privateTmuxOwner.Command(t, tmuxPath)
			// Another session keeps the server running after the agent's ends.
			require.NoError(procutil.CommandContext(ctx, tmux[0], append(tmux[1:],
				"new-session", "-d", "-s", "other", "sleep 600")...).Run())
			cwd := gitfixture.NewRepository(t, false).Dir
			database := dbtest.Open(t)
			require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
				ID: "workspace", Platform: "github", PlatformHost: "github.com",
				RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc,
				ItemKey: db.AdHocWorkspaceItemKey("work/killed"), GitHeadRef: "work/killed",
				WorkspaceBranch: "work/killed", WorktreePath: cwd, Status: "ready",
			}))
			agent := filepath.Join(t.TempDir(), "agent")
			require.NoError(os.WriteFile(agent, []byte("#!/bin/sh\necho $$ > pid\nexec sleep 600\n"), 0o755))
			var handler *Handler
			runtime := localruntime.NewManager(localruntime.Options{
				TmuxCommand:             tmux,
				WrapAgentSessionsInTmux: true,
				PtyOwnerRuntime:         ptyownerruntime.New(&ptyowner.Client{Root: t.TempDir(), InProcess: true}, nil),
				TmuxOwnerMarker:         "kenn-forge:test-killed",
				AgentExitDir:            t.TempDir(),
				Targets: []localruntime.LaunchTarget{{
					Key: "custom-worker", Kind: localruntime.LaunchTargetAgent, Available: true, Command: []string{agent},
				}, {Key: "shell", Kind: localruntime.LaunchTargetShell, Available: true, Command: tmux}},
				OnSessionExit: func(info localruntime.SessionInfo) { handler.HandleRuntimeSessionExit(info) },
			})
			t.Cleanup(func() {
				cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
				defer cancel()
				runtime.StopWorkspace(cleanupCtx, "workspace")
				runtime.Shutdown()
			})
			workspaces := workspace.NewManager(database, t.TempDir())
			workspaces.SetTmuxCommand(tmux)
			handler = New(Deps{
				DB: database, Workspaces: workspaces, Runtime: runtime,
				AgentActivity: agentactivity.NewStore(t.TempDir()),
			})
			session, err := handler.launchWorkspaceRuntimeService(ctx, "workspace", "custom-worker", "", "")
			require.NoError(err)
			var agentPID int
			require.Eventually(func() bool {
				data, err := os.ReadFile(filepath.Join(cwd, "pid"))
				if err != nil {
					return false
				}
				agentPID, err = strconv.Atoi(strings.TrimSpace(string(data)))
				return err == nil
			}, 5*time.Second, 10*time.Millisecond)
			_, err = database.RecordWorkspaceRuntimeConversation(ctx, session.Key, "claude", "conversation")
			require.NoError(err)

			if name == "pane killed" {
				out, err := procutil.CommandContext(ctx, tmux[0], append(tmux[1:],
					"list-panes", "-t", session.TmuxSession, "-F", "#{pane_pid}")...).Output()
				require.NoError(err)
				panePID, err := strconv.Atoi(strings.TrimSpace(string(out)))
				require.NoError(err)
				require.NoError(syscall.Kill(-panePID, syscall.SIGTERM))
			} else {
				require.NoError(syscall.Kill(agentPID, syscall.SIGTERM))
			}

			require.Eventually(func() bool {
				handler.runtimeRecoveryMu.Lock()
				defer handler.runtimeRecoveryMu.Unlock()
				return handler.runtimeRecoveryPending[session.Key]
			}, 5*time.Second, 10*time.Millisecond)
			rows, err := database.ListAllWorkspaceRuntimeSessions(ctx)
			require.NoError(err)
			require.Len(rows, 1, "a killed agent keeps its record")
			require.Equal("conversation", rows[0].ConversationID)
		})
	}
}
