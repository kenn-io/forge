package workspaceapi

import (
	"context"
	shellquote "github.com/kballard/go-shellquote"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/agentactivity"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/ptyowner"
	ptyownerruntime "go.kenn.io/forge/internal/ptyowner/runtime"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/workspace"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

func TestRestoreRuntimeSessionsResumesSavedConversationAfterTmuxLoss(t *testing.T) {
	for _, status := range []string{"ready", "creating", "error", "unavailable", "ambiguous", "fairness", "stopped"} {
		t.Run(status, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			ctx := t.Context()
			database := dbtest.Open(t)
			cwd := t.TempDir()
			require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
				ID: "workspace", Platform: "github", PlatformHost: "github.com",
				RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc,
				ItemKey: db.AdHocWorkspaceItemKey("work/resume"), GitHeadRef: "work/resume",
				WorkspaceBranch: "work/resume", WorktreePath: cwd, Status: status, TmuxSession: "forge-base",
			}))
			require.NoError(database.UpsertWorkspaceRuntimeSession(ctx, &db.WorkspaceRuntimeSession{
				WorkspaceID: "workspace", SessionKey: "saved-runtime", TargetKey: "custom-worker",
				Label: "Worker 2", Kind: "agent", Scope: "session", TmuxSession: "gone",
				DisplayRegion: "workflow",
			}))
			activity := agentactivity.NewStore(t.TempDir())
			require.NoError(activity.HandleEvent("claude", agentactivity.HookEvent{
				SessionID: "saved-conversation", CWD: cwd, HookEventName: "Stop",
			}, "saved-runtime"))
			dir := t.TempDir()
			if privateTmuxOwner == nil {
				t.Skip("private tmux tests unsupported")
			}
			tmuxPath, err := exec.LookPath("tmux")
			require.NoError(err)
			tmux := privateTmuxOwner.Command(t, tmuxPath)
			blocked := filepath.Join(dir, "blocked")
			if status == "fairness" {
				wrapper := filepath.Join(dir, "tmux-wrapper")
				require.NoError(os.WriteFile(wrapper, []byte("#!/bin/sh\nif [ \"$1\" = has-session ] && [ \"$3\" = blocked ]; then\n touch "+shellquote.Join(blocked)+"\n exec sleep 60\nfi\nexec "+shellquote.Join(tmux...)+" \"$@\"\n"), 0o755))
				tmux = []string{wrapper}
			}
			agent := filepath.Join(dir, "agent")
			require.NoError(os.WriteFile(agent, []byte(`#!/bin/sh
[ "$#" = 4 ] && [ "$1" = --model ] && [ "$2" = model-a ] && [ "$3" = --resume ] && [ "$4" = saved-conversation ] || exit 42
printf '%s\n' "$@" > args
exec sleep 60
`), 0o755))
			runtime := localruntime.NewManager(localruntime.Options{
				TmuxCommand:             tmux,
				WrapAgentSessionsInTmux: true,
				PtyOwnerRuntime:         ptyownerruntime.New(&ptyowner.Client{Root: t.TempDir(), InProcess: true}, nil),
				TmuxOwnerMarker:         "kenn-forge:test-resume",
				Targets:                 []localruntime.LaunchTarget{{Key: "custom-worker", Kind: localruntime.LaunchTargetAgent, Available: true, Command: []string{agent, "--model", "model-a"}}, {Key: "shell", Kind: localruntime.LaunchTargetShell, Available: true, Command: tmux}},
			})
			t.Cleanup(func() {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				runtime.StopWorkspace(cleanupCtx, "workspace")
				runtime.Shutdown()
			})
			workspaces := workspace.NewManager(database, t.TempDir())
			workspaces.SetTmuxCommand(tmux)
			handler := New(Deps{DB: database, Workspaces: workspaces, Runtime: runtime, AgentActivity: activity})
			if status == "fairness" {
				require.NoError(database.UpdateWorkspaceStatus(ctx, "workspace", "ready", nil))
				require.NoError(database.UpsertWorkspaceRuntimeSession(ctx, &db.WorkspaceRuntimeSession{
					WorkspaceID: "workspace", SessionKey: "aaa-blocked", TargetKey: "custom-worker",
					Label: "Blocked", Kind: "agent", Scope: "session", TmuxSession: "blocked",
				}))
				handler.setRuntimeRecoveryPending("aaa-blocked", true)
				handler.setRuntimeRecoveryPending("saved-runtime", true)
				attemptCtx, cancel := context.WithCancel(ctx)
				defer cancel()
				attempt := make(chan error, 1)
				go func() { attempt <- handler.restoreRuntimeSessions(attemptCtx, true) }()
				require.Eventually(func() bool { _, err := os.Stat(blocked); return err == nil }, 5*time.Second, 10*time.Millisecond)
				cancel()
				select {
				case <-attempt:
				case <-time.After(5 * time.Second):
					require.FailNow("blocked recovery did not honor cancellation")
				}
				handler.runWorkspaceTmuxPrune(ctx)
				// The next pass must reach the healthy session before retrying
				// the blocked first record. Remove the fixture before full restore.
				require.NoError(workspaces.ForgetRuntimeSession(ctx, "workspace", "aaa-blocked"))
				handler.setRuntimeRecoveryPending("aaa-blocked", false)
			}
			if status == "unavailable" || status == "ambiguous" || status == "stopped" {
				require.NoError(database.UpdateWorkspaceStatus(ctx, "workspace", "ready", nil))
				targets := runtime.LaunchTargets()
				targets = append(targets, localruntime.LaunchTarget{Key: "shell", Kind: localruntime.LaunchTargetShell, Available: true, Command: tmux})
				if status == "unavailable" || status == "stopped" {
					targets[0].Available = false
				} else {
					targets[0].Command = append(targets[0].Command, "--", "original prompt")
				}
				runtime.UpdateTargets(targets)
				require.NoError(handler.RestoreRuntimeSessions(ctx))
				handler.runWorkspaceTmuxPrune(ctx)
				retained, err := database.ListAllWorkspaceRuntimeSessions(ctx)
				require.NoError(err)
				require.Len(retained, 1, "failed recovery must survive periodic pruning")
				require.Len(activity.LiveReportsForWorkspace(cwd, []string{"saved-runtime"}), 1)
				assert.NoFileExists(filepath.Join(cwd, "args"))
				if status == "stopped" {
					_, err := handler.stopWorkspaceRuntimeSession(ctx, &stopWorkspaceRuntimeSessionInput{ID: "workspace", SessionKey: "saved-runtime"})
					require.NoError(err)
					targets[0].Available = true
					runtime.UpdateTargets(targets)
					handler.runWorkspaceTmuxPrune(ctx)
					retained, err := database.ListAllWorkspaceRuntimeSessions(ctx)
					require.NoError(err)
					assert.Empty(retained)
					assert.Empty(runtime.ListSessions("workspace"))
					assert.NoFileExists(filepath.Join(cwd, "args"))
					return
				}
				targets[0].Available = true
				targets[0].Command = []string{agent, "--model", "model-a"}
				runtime.UpdateTargets(targets)
			}
			if status == "unavailable" || status == "ambiguous" {
				done, start := handler.beginWorkspaceSetup("workspace")
				require.True(start)
				handler.runWorkspaceTmuxPrune(ctx)
				assert.NoFileExists(filepath.Join(cwd, "args"), "recovery must wait for active setup")
				handler.finishWorkspaceSetup("workspace", done)
				handler.runWorkspaceTmuxPrune(ctx)
			} else {
				require.NoError(handler.RestoreRuntimeSessions(ctx))
			}
			require.Eventually(func() bool { _, err := os.Stat(filepath.Join(cwd, "args")); return err == nil }, 5*time.Second, 10*time.Millisecond)
			args, err := os.ReadFile(filepath.Join(cwd, "args"))
			require.NoError(err)
			assert.Equal("--model\nmodel-a\n--resume\nsaved-conversation\n", string(args))
			sessions := runtime.ListSessions("workspace")
			require.Len(sessions, 1)
			assert.Equal("saved-runtime", sessions[0].Key)
			assert.Equal("Worker 2", sessions[0].Label)
			stored, err := database.ListAllWorkspaceRuntimeSessions(ctx)
			require.NoError(err)
			require.Len(stored, 1)
			assert.Equal("workflow", stored[0].DisplayRegion)
			require.NotEmpty(stored[0].TmuxSession)
			assert.Equal(sessions[0].TmuxSession, stored[0].TmuxSession)
			assert.NotEqual("gone", stored[0].TmuxSession)
			owner, err := procutil.CommandContext(ctx, tmux[0], append(tmux[1:], "show-options", "-qv", "-t", stored[0].TmuxSession, "@forge_owner")...).Output()
			require.NoError(err)
			assert.Equal("kenn-forge:test-resume\n", string(owner))
			assert.Len(activity.LiveReportsForWorkspace(cwd, []string{"saved-runtime"}), 1)
			require.NoError(handler.RestoreRuntimeSessions(ctx))
			assert.Len(runtime.ListSessions("workspace"), 1)
		})
	}
}
