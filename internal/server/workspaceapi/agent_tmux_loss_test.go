package workspaceapi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	shellquote "github.com/kballard/go-shellquote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/agentactivity"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/ptyowner"
	ptyownerruntime "go.kenn.io/forge/internal/ptyowner/runtime"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/internal/testutil/workspacetest"
	"go.kenn.io/forge/internal/workspace"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

// A host shutdown can kill the tmux server, or Forge's tmux client before it,
// while Forge still runs. The agent then reads as exited and reports
// SessionEnd, yet its conversation is the one to resume. An agent that ends on
// its own stays forgotten, even when it was the server's last session and tmux
// exits with it.
func TestRuntimeExitKeepsAgentAfterTmuxServerLoss(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"server killed", "client killed first", "client killed then finished",
		"client detached", "client detached then finished", "agent exits",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require := require.New(t)
			assert := assert.New(t)
			ctx := t.Context()
			if privateTmuxOwner == nil {
				t.Skip("private tmux tests unsupported")
			}
			tmuxPath, err := exec.LookPath("tmux")
			require.NoError(err)
			tmux := privateTmuxOwner.Command(t, tmuxPath)
			cwd := gitfixture.NewRepository(t, false).Dir
			database := dbtest.Open(t)
			require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
				ID: "workspace", Platform: "github", PlatformHost: "github.com",
				RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc,
				ItemKey: db.AdHocWorkspaceItemKey("work/loss"), GitHeadRef: "work/loss",
				WorkspaceBranch: "work/loss", WorktreePath: cwd, Status: "ready",
			}))
			agent := filepath.Join(t.TempDir(), "agent")
			require.NoError(os.WriteFile(agent, []byte(`#!/bin/sh
printf '%s\n' "$@" > args.tmp && mv args.tmp args
while [ ! -e quit ]; do sleep 0.05; done
rm -f quit args
`), 0o755))
			activity := agentactivity.NewStore(t.TempDir())
			agentExits := t.TempDir()
			// startForge builds a daemon's runtime and handler over the shared
			// database and activity store, as each Forge start does.
			startForge := func() (*Handler, *localruntime.Manager, *workspace.Manager) {
				var handler *Handler
				runtime := localruntime.NewManager(localruntime.Options{
					TmuxCommand:             tmux,
					WrapAgentSessionsInTmux: true,
					PtyOwnerRuntime:         ptyownerruntime.New(&ptyowner.Client{Root: t.TempDir(), InProcess: true}, nil),
					TmuxOwnerMarker:         "kenn-forge:test-loss",
					AgentExitDir:            agentExits,
					Targets: []localruntime.LaunchTarget{{
						Key: "custom-worker", Kind: localruntime.LaunchTargetAgent, Available: true,
						Command: []string{agent, "--model", "model-a"},
					}, {Key: "shell", Kind: localruntime.LaunchTargetShell, Available: true, Command: tmux}},
					OnSessionExit: func(info localruntime.SessionInfo) { handler.HandleRuntimeSessionExit(info) },
				})
				t.Cleanup(func() {
					cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
					defer cancel()
					runtime.StopWorkspace(cleanupCtx, "workspace")
					runtime.Shutdown()
				})
				workspaces := workspacetest.NewManager(t, database, t.TempDir())
				workspaces.SetTmuxCommand(tmux)
				handler = New(Deps{DB: database, Workspaces: workspaces, Runtime: runtime, AgentActivity: activity})
				return handler, runtime, workspaces
			}
			handler, runtime, workspaces := startForge()

			session, err := handler.launchWorkspaceRuntimeService(ctx, "workspace", "custom-worker", "", "", nil)
			require.NoError(err)
			require.Eventually(func() bool { _, err := os.Stat(filepath.Join(cwd, "args")); return err == nil },
				5*time.Second, 10*time.Millisecond)
			// Hooks arrive through the daemon, which saves the conversation.
			hook := func(event string) {
				_, err := handler.receiveAgentHook(ctx, &receiveAgentHookInput{
					Agent: "claude", RuntimeSessionKey: session.Key,
					Body: agentactivity.HookEvent{SessionID: "conversation", CWD: cwd, HookEventName: event},
				})
				require.NoError(err)
			}
			hook("Stop")
			stored := func() []db.WorkspaceRuntimeSession {
				rows, err := database.ListAllWorkspaceRuntimeSessions(ctx)
				require.NoError(err)
				return rows
			}
			require.Len(stored(), 1)
			require.NotEmpty(stored()[0].TmuxSession, "the agent must run under tmux")
			require.Equal("conversation", stored()[0].ConversationID)
			// A nested agent working in another worktree inherits the runtime
			// key; its conversation is not this terminal's.
			_, err = handler.receiveAgentHook(ctx, &receiveAgentHookInput{
				Agent: "claude", RuntimeSessionKey: session.Key,
				Body: agentactivity.HookEvent{SessionID: "nested", CWD: t.TempDir(), HookEventName: "Stop"},
			})
			require.NoError(err)
			require.Equal("conversation", stored()[0].ConversationID, "a foreign workspace keeps the saved conversation")
			liveReports := func() []agentactivity.Report {
				return activity.LiveReportsForWorkspace(cwd, []string{session.Key})
			}

			if name == "agent exits" {
				require.NoError(os.WriteFile(filepath.Join(cwd, "quit"), nil, 0o600))
				require.Eventually(func() bool { return len(stored()) == 0 }, 5*time.Second, 10*time.Millisecond)
				require.Eventually(func() bool { return len(liveReports()) == 0 },
					5*time.Second, 10*time.Millisecond)
				return
			}

			pending := func() bool {
				handler.runtimeRecoveryMu.Lock()
				defer handler.runtimeRecoveryMu.Unlock()
				return handler.runtimeRecoveryPending[session.Key]
			}
			if name == "client detached" || name == "client detached then finished" {
				// A detached client exits 0 while the agent keeps running.
				require.Eventually(func() bool {
					return procutil.CommandContext(ctx, tmux[0], append(tmux[1:],
						"detach-client", "-s", stored()[0].TmuxSession)...).Run() == nil
				}, 5*time.Second, 10*time.Millisecond)
				require.Eventually(pending, 5*time.Second, 10*time.Millisecond)
				require.Len(stored(), 1, "a detached client must not forget the agent")
				_, recorded := runtime.AgentExit(stored()[0].TmuxSession)
				require.True(recorded, "the still-running agent keeps its exit record")
			}
			if name == "client killed first" || name == "client killed then finished" {
				// Launch returns before Forge's client attaches, so wait for it.
				var pid int
				require.Eventually(func() bool {
					out, err := procutil.CommandContext(ctx, tmux[0], append(tmux[1:],
						"list-clients", "-t", stored()[0].TmuxSession, "-F", "#{client_pid}")...).Output()
					if err != nil {
						return false
					}
					pid, err = strconv.Atoi(strings.TrimSpace(string(out)))
					return err == nil
				}, 5*time.Second, 10*time.Millisecond)
				client, err := os.FindProcess(pid)
				require.NoError(err)
				require.NoError(client.Kill())
				require.Eventually(pending, 5*time.Second, 10*time.Millisecond)
				require.Len(stored(), 1, "a lost tmux client must not forget the agent")
				require.Equal("conversation", stored()[0].ConversationID)
				_, recorded := runtime.AgentExit(stored()[0].TmuxSession)
				require.True(recorded, "the still-running agent keeps its exit record")
			}
			if name == "client killed then finished" || name == "client detached then finished" {
				// The agent finishes while Forge no longer watches it; the
				// next start must not resume a conversation that is over.
				tmuxSession := stored()[0].TmuxSession
				require.NoError(os.WriteFile(filepath.Join(cwd, "quit"), nil, 0o600))
				require.Eventually(func() bool {
					return procutil.CommandContext(ctx, tmux[0], append(tmux[1:],
						"has-session", "-t", tmuxSession)...).Run() != nil
				}, 5*time.Second, 10*time.Millisecond)
				runtime.Shutdown()
				restarted, restartedRuntime, _ := startForge()
				require.NoError(restarted.RestoreRuntimeSessions(ctx))
				require.Empty(stored(), "a finished agent is forgotten")
				require.Empty(restartedRuntime.ListSessions("workspace"))
				assert.NoFileExists(filepath.Join(cwd, "args"), "the finished conversation is not resumed")
				return
			}
			// kill-server can exit non-zero when its own client loses the
			// server mid-command, so check the outcome rather than the status.
			killErr := procutil.CommandContext(ctx, tmux[0], append(tmux[1:], "kill-server")...).Run()
			// The kill client can return before the server finishes shutting down.
			require.Eventually(func() bool {
				server, err := workspaces.TmuxServerID(ctx)
				return err == nil && server == ""
			}, 5*time.Second, 10*time.Millisecond, "kill-server: %v", killErr)
			require.Eventually(pending, 5*time.Second, 10*time.Millisecond)
			// The dying agent reports SessionEnd while Forge still runs, which
			// deletes its activity report but not its saved conversation.
			hook("SessionEnd")
			assert.Empty(liveReports())
			handler.runWorkspaceTmuxPrune(ctx)
			require.Len(stored(), 1, "a lost tmux server must not forget the agent")
			assert.Equal("conversation", stored()[0].ConversationID)

			// This process saw the agent exit, so it waits for the next start.
			require.NoError(os.Remove(filepath.Join(cwd, "args")))
			require.NoError(handler.restoreRuntimeSessions(ctx, true))
			assert.NoFileExists(filepath.Join(cwd, "args"))
			runtime.Shutdown()

			restarted, restartedRuntime, _ := startForge()
			require.NoError(restarted.RestoreRuntimeSessions(ctx))
			require.Eventually(func() bool {
				args, err := os.ReadFile(filepath.Join(cwd, "args"))
				return err == nil && string(args) == "--model\nmodel-a\n--resume\nconversation\n"
			}, 5*time.Second, 10*time.Millisecond)
			live := restartedRuntime.ListSessions("workspace")
			require.Len(live, 1)
			assert.Equal(session.Key, live[0].Key)
		})
	}
}

// A tmux client also exits non-zero when its session vanished before it
// attached, as when a launch fails at once. With the server it ran on still
// running, that session ended rather than being lost, so the agent is
// forgotten. If Forge never learned which server the agent ran on, it cannot
// rule out a replacement and keeps the agent.
func TestRuntimeExitForgetsAgentWhoseSessionEndedOnLiveServer(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"server known", "server unknown"} {
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
			require.NoError(procutil.CommandContext(ctx, tmux[0], append(tmux[1:],
				"new-session", "-d", "-s", "other", "sleep 600")...).Run())
			cwd := t.TempDir()
			database := dbtest.Open(t)
			require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
				ID: "workspace", Platform: "github", PlatformHost: "github.com",
				RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc,
				ItemKey: db.AdHocWorkspaceItemKey("work/ended"), GitHeadRef: "work/ended",
				WorkspaceBranch: "work/ended", WorktreePath: cwd, Status: "ready",
			}))
			require.NoError(database.UpsertWorkspaceRuntimeSession(ctx, &db.WorkspaceRuntimeSession{
				WorkspaceID: "workspace", SessionKey: "ended-runtime", TargetKey: "custom-worker",
				Label: "Worker", Kind: "agent", Scope: "session", TmuxSession: "forge-ended",
			}))
			rows, err := database.ListAllWorkspaceRuntimeSessions(ctx)
			require.NoError(err)
			require.Len(rows, 1)
			activity := agentactivity.NewStore(t.TempDir())
			workspaces := workspacetest.NewManager(t, database, t.TempDir())
			workspaces.SetTmuxCommand(tmux)
			handler := New(Deps{DB: database, Workspaces: workspaces, AgentActivity: activity})
			// A server that does not hold the agent's session, such as a
			// replacement, is never recorded.
			handler.noteTmuxServer(ctx, "ended-runtime", "forge-ended")
			if name == "server known" {
				// Forge learns the server while the session runs; the session
				// then ends on that same server.
				require.NoError(procutil.CommandContext(ctx, tmux[0], append(tmux[1:],
					"new-session", "-d", "-s", "forge-ended", "sleep 600")...).Run())
				handler.noteTmuxServer(ctx, "ended-runtime", "forge-ended")
				require.NoError(procutil.CommandContext(ctx, tmux[0], append(tmux[1:],
					"kill-session", "-t", "forge-ended")...).Run())
			}

			exitCode := 1
			handler.HandleRuntimeSessionExit(localruntime.SessionInfo{
				Key: "ended-runtime", WorkspaceID: "workspace", TargetKey: "custom-worker",
				Kind: localruntime.LaunchTargetAgent, TmuxSession: "forge-ended",
				CreatedAt: rows[0].CreatedAt, ExitCode: &exitCode,
			})

			handler.runtimeRecoveryMu.Lock()
			pending := handler.runtimeRecoveryPending["ended-runtime"]
			handler.runtimeRecoveryMu.Unlock()
			if name == "server unknown" {
				require.True(pending)
				rows, err := database.ListAllWorkspaceRuntimeSessions(ctx)
				require.NoError(err)
				require.Len(rows, 1, "an unknown server keeps the agent")
				return
			}
			require.False(pending)
			require.Eventually(func() bool {
				rows, err := database.ListAllWorkspaceRuntimeSessions(ctx)
				return err == nil && len(rows) == 0
			}, 5*time.Second, 10*time.Millisecond)
		})
	}
}

// An agent can exit before its launch finishes recording it, and before its
// exit callback runs. Post-launch cleanup must then keep an agent that lost
// its tmux server, as the exit handler would, and still forget one that ended.
func TestPostLaunchCleanupKeepsAgentLostBeforeItsRecord(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"server killed", "agent exits"} {
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
			cwd := gitfixture.NewRepository(t, false).Dir
			database := dbtest.Open(t)
			require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
				ID: "workspace", Platform: "github", PlatformHost: "github.com",
				RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc,
				ItemKey: db.AdHocWorkspaceItemKey("work/record"), GitHeadRef: "work/record",
				WorkspaceBranch: "work/record", WorktreePath: cwd, Status: "ready",
			}))
			agent := filepath.Join(t.TempDir(), "agent")
			require.NoError(os.WriteFile(agent, []byte(`#!/bin/sh
: > started
while [ ! -e quit ]; do sleep 0.05; done
rm -f quit started
`), 0o755))
			runtime := localruntime.NewManager(localruntime.Options{
				TmuxCommand:             tmux,
				WrapAgentSessionsInTmux: true,
				PtyOwnerRuntime:         ptyownerruntime.New(&ptyowner.Client{Root: t.TempDir(), InProcess: true}, nil),
				TmuxOwnerMarker:         "kenn-forge:test-record",
				AgentExitDir:            t.TempDir(),
				Targets: []localruntime.LaunchTarget{{
					Key: "custom-worker", Kind: localruntime.LaunchTargetAgent, Available: true,
					Command: []string{agent},
				}, {Key: "shell", Kind: localruntime.LaunchTargetShell, Available: true, Command: tmux}},
				// The exit callback has not run yet when cleanup checks.
				OnSessionExit: func(localruntime.SessionInfo) {},
			})
			t.Cleanup(func() {
				cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
				defer cancel()
				runtime.StopWorkspace(cleanupCtx, "workspace")
				runtime.Shutdown()
			})
			workspaces := workspacetest.NewManager(t, database, t.TempDir())
			workspaces.SetTmuxCommand(tmux)
			handler := New(Deps{
				DB: database, Workspaces: workspaces, Runtime: runtime,
				AgentActivity: agentactivity.NewStore(t.TempDir()),
			})

			session, err := runtime.LaunchWithInitialMessage(ctx, "workspace", cwd, "custom-worker", "", nil)
			require.NoError(err)
			require.Eventually(func() bool { _, err := os.Stat(filepath.Join(cwd, "started")); return err == nil },
				5*time.Second, 10*time.Millisecond)
			if name == "agent exits" {
				require.NoError(os.WriteFile(filepath.Join(cwd, "quit"), nil, 0o600))
			} else {
				_ = procutil.CommandContext(ctx, tmux[0], append(tmux[1:], "kill-server")...).Run()
			}
			require.Eventually(func() bool { return runtime.Exited(session.Key) }, 5*time.Second, 10*time.Millisecond)

			// The record and the agent's first hook land after the exit.
			require.NoError(handler.recordRuntimeSession(ctx, "workspace", session, "session"))
			_, err = database.RecordWorkspaceRuntimeConversation(ctx, session.Key, "claude", "conversation")
			require.NoError(err)
			handler.forgetRecordedRuntimeSessionIfExited(ctx, session)

			rows, err := database.ListAllWorkspaceRuntimeSessions(ctx)
			require.NoError(err)
			handler.runtimeRecoveryMu.Lock()
			pending := handler.runtimeRecoveryPending[session.Key]
			handler.runtimeRecoveryMu.Unlock()
			if name == "agent exits" {
				require.Empty(rows, "an agent that ended is forgotten")
				require.False(pending)
				return
			}
			require.Len(rows, 1, "a lost agent keeps its record")
			require.Equal("conversation", rows[0].ConversationID)
			require.True(pending)
		})
	}
}

// When tmux dies while Forge keeps running, every base terminal goes with it.
// Once another terminal starts a new server, cleanup must not mistake those
// missing bases for individual exits and mark the workspaces errored, whether
// or not a workspace had an agent kept for recovery.
func TestTmuxServerLossKeepsWorkspacesReady(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	if privateTmuxOwner == nil {
		t.Skip("private tmux tests unsupported")
	}
	tmuxPath, err := exec.LookPath("tmux")
	require.NoError(err)
	tmux := privateTmuxOwner.Command(t, tmuxPath)
	database := dbtest.Open(t)
	for _, id := range []string{"0000000000000a01", "0000000000000a02", "0000000000000a03"} {
		require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
			ID: id, Platform: "github", PlatformHost: "github.com",
			RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc,
			ItemKey: db.AdHocWorkspaceItemKey("work/" + id), GitHeadRef: "work/" + id,
			WorkspaceBranch: "work/" + id, WorktreePath: gitfixture.NewRepository(t, false).Dir,
			Status: "ready", TmuxSession: "forge-" + id,
		}))
	}
	agent := filepath.Join(t.TempDir(), "agent")
	require.NoError(os.WriteFile(agent, []byte("#!/bin/sh\nexec sleep 600\n"), 0o755))
	var handler *Handler
	runtime := localruntime.NewManager(localruntime.Options{
		TmuxCommand:             tmux,
		WrapAgentSessionsInTmux: true,
		PtyOwnerRuntime:         ptyownerruntime.New(&ptyowner.Client{Root: t.TempDir(), InProcess: true}, nil),
		TmuxOwnerMarker:         "kenn-forge:test-ready",
		AgentExitDir:            t.TempDir(),
		Targets: []localruntime.LaunchTarget{{
			Key: "custom-worker", Kind: localruntime.LaunchTargetAgent, Available: true, Command: []string{agent},
		}, {Key: "shell", Kind: localruntime.LaunchTargetShell, Available: true, Command: tmux}},
		OnSessionExit: func(info localruntime.SessionInfo) { handler.HandleRuntimeSessionExit(info) },
	})
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		runtime.StopWorkspace(cleanupCtx, "0000000000000a01")
		runtime.Shutdown()
	})
	workspaces := workspacetest.NewManager(t, database, t.TempDir())
	workspaces.SetTmuxCommand(tmux)
	handler = New(Deps{
		DB: database, Workspaces: workspaces, Runtime: runtime,
		AgentActivity: agentactivity.NewStore(t.TempDir()),
	})
	require.NoError(handler.RestoreRuntimeSessions(ctx))
	session, err := handler.launchWorkspaceRuntimeService(ctx, "0000000000000a01", "custom-worker", "", "", nil)
	require.NoError(err)
	_, err = database.RecordWorkspaceRuntimeConversation(ctx, session.Key, "claude", "conversation")
	require.NoError(err)
	baseExists := func(id string) bool {
		return procutil.CommandContext(ctx, tmux[0], append(tmux[1:], "has-session", "-t", "forge-"+id)...).Run() == nil
	}
	for _, id := range []string{"0000000000000a01", "0000000000000a02", "0000000000000a03"} {
		require.True(baseExists(id))
	}

	_ = procutil.CommandContext(ctx, tmux[0], append(tmux[1:], "kill-server")...).Run()
	require.Eventually(func() bool {
		handler.runtimeRecoveryMu.Lock()
		defer handler.runtimeRecoveryMu.Unlock()
		return handler.runtimeRecoveryPending[session.Key]
	}, 5*time.Second, 10*time.Millisecond)
	// Another terminal starts a new tmux server.
	require.NoError(procutil.CommandContext(ctx, tmux[0], append(tmux[1:],
		"new-session", "-d", "-s", "other", "sleep 600")...).Run())

	// A deletion holds the third workspace; recovery must not recreate its
	// terminal behind it.
	deleting, admitted := handler.beginWorkspaceSetup("0000000000000a03")
	require.True(admitted)
	handler.runWorkspaceTmuxPrune(ctx)
	assert.False(baseExists("0000000000000a03"), "recovery waits for the deletion")
	rows, err := database.ListAllWorkspaceRuntimeSessions(ctx)
	require.NoError(err)
	require.Len(rows, 1, "the kept agent waits for the next start")

	// The kept agent stays visible as unavailable, and Stop forgets it.
	listed := func() []string {
		sessions, err := handler.workspaceRuntimeSessions(ctx, "0000000000000a01")
		require.NoError(err)
		keys := make([]string, 0, len(sessions))
		for _, s := range sessions {
			keys = append(keys, s.Key)
		}
		return keys
	}
	assert.Contains(listed(), session.Key)
	_, err = handler.stopWorkspaceRuntimeSession(ctx, &stopWorkspaceRuntimeSessionInput{
		ID: "0000000000000a01", SessionKey: session.Key,
	})
	require.NoError(err)
	rows, err = database.ListAllWorkspaceRuntimeSessions(ctx)
	require.NoError(err)
	assert.Empty(rows)
	assert.NotContains(listed(), session.Key)

	// The deletion is refused. With no kept agent left, the unfinished restore
	// still holds the prune, and the next pass restores the skipped terminal.
	handler.finishWorkspaceSetup("0000000000000a03", deleting)
	handler.runWorkspaceTmuxPrune(ctx)
	assert.True(baseExists("0000000000000a03"), "the next pass restores the skipped terminal")
	for _, id := range []string{"0000000000000a01", "0000000000000a02", "0000000000000a03"} {
		ws, err := database.GetWorkspace(ctx, id)
		require.NoError(err)
		assert.Equal("ready", ws.Status, "workspace %s", id)
		assert.True(baseExists(id), "base terminal %s restored", id)
	}
}

// Another terminal, or recovery restoring base terminals, can start a new tmux
// server before a lost agent's exit is handled. The agent's session is then
// missing from a running server, yet it was lost with the old server and must
// be kept.
func TestRuntimeExitKeepsAgentWhenTmuxServerIsReplaced(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	ctx := t.Context()
	if privateTmuxOwner == nil {
		t.Skip("private tmux tests unsupported")
	}
	tmuxPath, err := exec.LookPath("tmux")
	require.NoError(err)
	tmux := privateTmuxOwner.Command(t, tmuxPath)
	cwd := gitfixture.NewRepository(t, false).Dir
	database := dbtest.Open(t)
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID: "workspace", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc,
		ItemKey: db.AdHocWorkspaceItemKey("work/replaced"), GitHeadRef: "work/replaced",
		WorkspaceBranch: "work/replaced", WorktreePath: cwd, Status: "ready",
	}))
	agent := filepath.Join(t.TempDir(), "agent")
	require.NoError(os.WriteFile(agent, []byte("#!/bin/sh\n: > started\nexec sleep 600\n"), 0o755))
	var handler *Handler
	replaced := make(chan struct{})
	runtime := localruntime.NewManager(localruntime.Options{
		TmuxCommand:             tmux,
		WrapAgentSessionsInTmux: true,
		PtyOwnerRuntime:         ptyownerruntime.New(&ptyowner.Client{Root: t.TempDir(), InProcess: true}, nil),
		TmuxOwnerMarker:         "kenn-forge:test-replaced",
		AgentExitDir:            t.TempDir(),
		Targets: []localruntime.LaunchTarget{{
			Key: "custom-worker", Kind: localruntime.LaunchTargetAgent, Available: true, Command: []string{agent},
		}, {Key: "shell", Kind: localruntime.LaunchTargetShell, Available: true, Command: tmux}},
		// Hold the exit until a replacement server runs.
		OnSessionExit: func(info localruntime.SessionInfo) {
			<-replaced
			handler.HandleRuntimeSessionExit(info)
		},
	})
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		runtime.StopWorkspace(cleanupCtx, "workspace")
		runtime.Shutdown()
	})
	workspaces := workspacetest.NewManager(t, database, t.TempDir())
	workspaces.SetTmuxCommand(tmux)
	handler = New(Deps{
		DB: database, Workspaces: workspaces, Runtime: runtime,
		AgentActivity: agentactivity.NewStore(t.TempDir()),
	})
	session, err := handler.launchWorkspaceRuntimeService(ctx, "workspace", "custom-worker", "", "", nil)
	require.NoError(err)
	require.Eventually(func() bool { _, err := os.Stat(filepath.Join(cwd, "started")); return err == nil },
		5*time.Second, 10*time.Millisecond)
	_, err = database.RecordWorkspaceRuntimeConversation(ctx, session.Key, "claude", "conversation")
	require.NoError(err)
	original, err := workspaces.TmuxServerID(ctx)
	require.NoError(err)

	_ = procutil.CommandContext(ctx, tmux[0], append(tmux[1:], "kill-server")...).Run()
	require.Eventually(func() bool {
		return procutil.CommandContext(ctx, tmux[0], append(tmux[1:],
			"new-session", "-d", "-s", "other", "sleep 600")...).Run() == nil
	}, 5*time.Second, 50*time.Millisecond)
	current, err := workspaces.TmuxServerID(ctx)
	require.NoError(err)
	require.NotEqual(original, current, "a replacement server runs")
	close(replaced)

	require.Eventually(func() bool {
		handler.runtimeRecoveryMu.Lock()
		defer handler.runtimeRecoveryMu.Unlock()
		return handler.runtimeRecoveryPending[session.Key]
	}, 5*time.Second, 10*time.Millisecond)
	rows, err := database.ListAllWorkspaceRuntimeSessions(ctx)
	require.NoError(err)
	require.Len(rows, 1, "an agent lost with the replaced server keeps its record")
	require.Equal("conversation", rows[0].ConversationID)
	// Post-launch cleanup can ask about the same exit; it gets the same answer.
	exited, ok := runtime.ExitedSession(session.Key)
	require.True(ok)
	require.True(handler.keepAgentAfterTmuxServerLoss(exited))
}

// An agent's first hook can arrive before its runtime row is recorded, and
// SessionEnd can delete its report before another hook. Recording the row
// saves the conversation that hook reported.
func TestRecordedAgentKeepsConversationReportedBeforeItsRow(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	ctx := t.Context()
	cwd := t.TempDir()
	database := dbtest.Open(t)
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID: "workspace", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc,
		ItemKey: db.AdHocWorkspaceItemKey("work/early"), GitHeadRef: "work/early",
		WorkspaceBranch: "work/early", WorktreePath: cwd, Status: "ready",
	}))
	activity := agentactivity.NewStore(t.TempDir())
	handler := New(Deps{
		DB: database, Workspaces: workspacetest.NewManager(t, database, t.TempDir()), AgentActivity: activity,
	})
	hook := func(event string) {
		_, err := handler.receiveAgentHook(ctx, &receiveAgentHookInput{
			Agent: "claude", RuntimeSessionKey: "early-runtime",
			Body: agentactivity.HookEvent{SessionID: "conversation", CWD: cwd, HookEventName: event},
		})
		require.NoError(err)
	}

	hook("SessionStart")
	require.NoError(handler.recordRuntimeSession(ctx, "workspace", localruntime.SessionInfo{
		Key: "early-runtime", WorkspaceID: "workspace", TargetKey: "claude", Label: "Claude",
		Kind: localruntime.LaunchTargetAgent, CreatedAt: time.Now().UTC(),
	}, "session"))
	hook("SessionEnd")

	require.Empty(activity.LiveReportsForWorkspace(cwd, []string{"early-runtime"}))
	rows, err := database.ListAllWorkspaceRuntimeSessions(ctx)
	require.NoError(err)
	require.Len(rows, 1)
	require.Equal("conversation", rows[0].ConversationID)
}

// Restoring base terminals after a tmux loss works through the workspaces
// still outstanding. A busy workspace, a failed terminal start, or an
// exhausted budget stays outstanding, and later passes skip finished ones.
func TestRestoreTerminalsAfterServerLossTracksUnfinishedWorkspaces(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	if privateTmuxOwner == nil {
		t.Skip("private tmux tests unsupported")
	}
	tmuxPath, err := exec.LookPath("tmux")
	require.NoError(err)
	tmux := privateTmuxOwner.Command(t, tmuxPath)
	database := dbtest.Open(t)
	for _, id := range []string{"0000000000000b01", "0000000000000b02"} {
		require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
			ID: id, Platform: "github", PlatformHost: "github.com",
			RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc,
			ItemKey: db.AdHocWorkspaceItemKey("work/" + id), GitHeadRef: "work/" + id,
			WorkspaceBranch: "work/" + id, WorktreePath: t.TempDir(),
			Status: "ready", TmuxSession: "forge-" + id,
		}))
	}
	workspaces := workspacetest.NewManager(t, database, t.TempDir())
	workspaces.SetTmuxCommand(tmux)
	handler := New(Deps{DB: database, Workspaces: workspaces})
	baseExists := func(id string) bool {
		return procutil.CommandContext(ctx, tmux[0], append(tmux[1:], "has-session", "-t", "forge-"+id)...).Run() == nil
	}
	all := map[string]bool{"0000000000000b01": true, "0000000000000b02": true}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	assert.Equal(all, handler.restoreTerminalsAfterServerLoss(cancelled, nil, all), "an exhausted budget finishes nothing")

	busy, admitted := handler.beginWorkspaceSetup("0000000000000b02")
	require.True(admitted)
	left := handler.restoreTerminalsAfterServerLoss(ctx, nil, all)
	assert.Equal(map[string]bool{"0000000000000b02": true}, left, "a busy workspace stays outstanding")
	assert.True(baseExists("0000000000000b01"))
	handler.finishWorkspaceSetup("0000000000000b02", busy)

	// A later pass works only on what is left.
	require.NoError(procutil.CommandContext(ctx, tmux[0], append(tmux[1:],
		"kill-session", "-t", "forge-0000000000000b01")...).Run())
	assert.Empty(handler.restoreTerminalsAfterServerLoss(ctx, nil, left))
	assert.True(baseExists("0000000000000b02"))
	assert.False(baseExists("0000000000000b01"), "a finished workspace is not revisited")

	// A terminal that fails to start stays outstanding.
	failing := filepath.Join(t.TempDir(), "tmux")
	require.NoError(os.WriteFile(failing, []byte("#!/bin/sh\necho \"can't find session: x\" >&2\nexit 1\n"), 0o755))
	broken := workspacetest.NewManager(t, database, t.TempDir())
	broken.SetTmuxCommand([]string{failing})
	brokenHandler := New(Deps{DB: database, Workspaces: broken})
	one := map[string]bool{"0000000000000b01": true}
	assert.Equal(one, brokenHandler.restoreTerminalsAfterServerLoss(ctx, nil, one))

	// A workspace list that cannot be read holds the prune until a later pass
	// reads it, even with no kept agent left to start the work again.
	restoring := func() bool {
		handler.runtimeRecoveryMu.Lock()
		defer handler.runtimeRecoveryMu.Unlock()
		return handler.baseRestoreListPending || len(handler.baseRestoreRemaining) > 0
	}
	// Keeping an agent after a loss asks for every base terminal.
	handler.runtimeRecoveryMu.Lock()
	handler.baseRestoreListPending = true
	handler.runtimeRecoveryMu.Unlock()
	handler.restoreBaseTerminalsAfterLoss(cancelled, nil)
	assert.True(restoring(), "an unread list holds the prune")
	handler.restoreBaseTerminalsAfterLoss(ctx, nil)
	assert.False(restoring(), "a later pass reads the list and finishes")
	assert.True(baseExists("0000000000000b01"), "the retried pass restores every terminal")
}

// A workspace whose terminal start keeps exhausting the budget must not
// starve the others: each pass starts after the one the previous pass reached.
func TestRestoreTerminalsAfterServerLossRotatesPastAStuckWorkspace(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	ctx := t.Context()
	if privateTmuxOwner == nil {
		t.Skip("private tmux tests unsupported")
	}
	tmuxPath, err := exec.LookPath("tmux")
	require.NoError(err)
	tmux := privateTmuxOwner.Command(t, tmuxPath)
	// Creating the first workspace's terminal hangs; it marks when it starts.
	stuck := filepath.Join(t.TempDir(), "stuck")
	wrapper := filepath.Join(t.TempDir(), "tmux")
	require.NoError(os.WriteFile(wrapper, []byte("#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = forge-0000000000000c01 ] && [ \"$1\" != has-session ]; then : > "+shellquote.Join(stuck)+"; exec sleep 60; fi\ndone\nexec "+shellquote.Join(tmux...)+" \"$@\"\n"), 0o755))
	database := dbtest.Open(t)
	for _, id := range []string{"0000000000000c01", "0000000000000c02"} {
		require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
			ID: id, Platform: "github", PlatformHost: "github.com",
			RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc,
			ItemKey: db.AdHocWorkspaceItemKey("work/" + id), GitHeadRef: "work/" + id,
			WorkspaceBranch: "work/" + id, WorktreePath: t.TempDir(),
			Status: "ready", TmuxSession: "forge-" + id,
		}))
	}
	workspaces := workspacetest.NewManager(t, database, t.TempDir())
	workspaces.SetTmuxCommand([]string{wrapper})
	handler := New(Deps{DB: database, Workspaces: workspaces})
	// A pass runs out of budget once it reaches the stuck workspace.
	pass := func(left map[string]bool) map[string]bool {
		passCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		result := make(chan map[string]bool, 1)
		go func() { result <- handler.restoreTerminalsAfterServerLoss(passCtx, nil, left) }()
		require.Eventually(func() bool { _, err := os.Stat(stuck); return err == nil }, 5*time.Second, 10*time.Millisecond)
		require.NoError(os.Remove(stuck))
		cancel()
		return <-result
	}

	left := pass(map[string]bool{"0000000000000c01": true, "0000000000000c02": true})
	require.Len(left, 2, "the first pass spends its budget on the stuck workspace")
	left = pass(left)
	require.Equal(map[string]bool{"0000000000000c01": true}, left, "the next pass reaches the other workspace")
	require.NoError(procutil.CommandContext(ctx, tmux[0], append(tmux[1:],
		"has-session", "-t", "forge-0000000000000c02")...).Run())
}

// Base terminals startup could not restore stay outstanding: the prune waits,
// and the periodic restore finishes them once tmux works again.
func TestStartupKeepsUnrestoredTerminalsOutstanding(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	if privateTmuxOwner == nil {
		t.Skip("private tmux tests unsupported")
	}
	tmuxPath, err := exec.LookPath("tmux")
	require.NoError(err)
	tmux := privateTmuxOwner.Command(t, tmuxPath)
	// Creating the first workspace's terminal fails while broken exists.
	broken := filepath.Join(t.TempDir(), "broken")
	require.NoError(os.WriteFile(broken, nil, 0o600))
	wrapper := filepath.Join(t.TempDir(), "tmux")
	require.NoError(os.WriteFile(wrapper, []byte("#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = forge-0000000000000d01 ] && [ \"$1\" != has-session ] && [ -e "+shellquote.Join(broken)+" ]; then echo broken >&2; exit 1; fi\ndone\nexec "+shellquote.Join(tmux...)+" \"$@\"\n"), 0o755))
	database := dbtest.Open(t)
	for _, id := range []string{"0000000000000d01", "0000000000000d02"} {
		require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
			ID: id, Platform: "github", PlatformHost: "github.com",
			RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc,
			ItemKey: db.AdHocWorkspaceItemKey("work/" + id), GitHeadRef: "work/" + id,
			WorkspaceBranch: "work/" + id, WorktreePath: t.TempDir(),
			Status: "ready", TmuxSession: "forge-" + id,
		}))
	}
	runtime := localruntime.NewManager(localruntime.Options{
		TmuxCommand:     []string{wrapper},
		PtyOwnerRuntime: ptyownerruntime.New(&ptyowner.Client{Root: t.TempDir(), InProcess: true}, nil),
	})
	t.Cleanup(runtime.Shutdown)
	workspaces := workspacetest.NewManager(t, database, t.TempDir())
	workspaces.SetTmuxCommand([]string{wrapper})
	handler := New(Deps{DB: database, Workspaces: workspaces, Runtime: runtime})
	baseExists := func(id string) bool {
		return procutil.CommandContext(ctx, tmux[0], append(tmux[1:], "has-session", "-t", "forge-"+id)...).Run() == nil
	}

	require.NoError(handler.RestoreRuntimeSessions(ctx))
	require.True(baseExists("0000000000000d02"))
	require.False(baseExists("0000000000000d01"))
	handler.runWorkspaceTmuxPrune(ctx)
	first, err := database.GetWorkspace(ctx, "0000000000000d01")
	require.NoError(err)
	assert.Equal("ready", first.Status, "the prune waits for the unrestored terminal")

	require.NoError(os.Remove(broken))
	handler.runWorkspaceTmuxPrune(ctx)
	assert.True(baseExists("0000000000000d01"), "the periodic restore finishes it")
	first, err = database.GetWorkspace(ctx, "0000000000000d01")
	require.NoError(err)
	assert.Equal("ready", first.Status)
}

// Stopping the kept agent before any periodic pass must not cancel the base
// terminal restore its tmux loss started.
func TestTmuxServerLossRestoresTerminalsAfterKeptAgentStops(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	if privateTmuxOwner == nil {
		t.Skip("private tmux tests unsupported")
	}
	tmuxPath, err := exec.LookPath("tmux")
	require.NoError(err)
	tmux := privateTmuxOwner.Command(t, tmuxPath)
	database := dbtest.Open(t)
	for _, id := range []string{"0000000000000e01", "0000000000000e02"} {
		require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
			ID: id, Platform: "github", PlatformHost: "github.com",
			RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc,
			ItemKey: db.AdHocWorkspaceItemKey("work/" + id), GitHeadRef: "work/" + id,
			WorkspaceBranch: "work/" + id, WorktreePath: gitfixture.NewRepository(t, false).Dir,
			Status: "ready", TmuxSession: "forge-" + id,
		}))
	}
	agent := filepath.Join(t.TempDir(), "agent")
	require.NoError(os.WriteFile(agent, []byte("#!/bin/sh\nexec sleep 600\n"), 0o755))
	var handler *Handler
	runtime := localruntime.NewManager(localruntime.Options{
		TmuxCommand:             tmux,
		WrapAgentSessionsInTmux: true,
		PtyOwnerRuntime:         ptyownerruntime.New(&ptyowner.Client{Root: t.TempDir(), InProcess: true}, nil),
		TmuxOwnerMarker:         "kenn-forge:test-stop-first",
		AgentExitDir:            t.TempDir(),
		Targets: []localruntime.LaunchTarget{{
			Key: "custom-worker", Kind: localruntime.LaunchTargetAgent, Available: true, Command: []string{agent},
		}, {Key: "shell", Kind: localruntime.LaunchTargetShell, Available: true, Command: tmux}},
		OnSessionExit: func(info localruntime.SessionInfo) { handler.HandleRuntimeSessionExit(info) },
	})
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		runtime.StopWorkspace(cleanupCtx, "0000000000000e01")
		runtime.Shutdown()
	})
	workspaces := workspacetest.NewManager(t, database, t.TempDir())
	workspaces.SetTmuxCommand(tmux)
	handler = New(Deps{
		DB: database, Workspaces: workspaces, Runtime: runtime,
		AgentActivity: agentactivity.NewStore(t.TempDir()),
	})
	require.NoError(handler.RestoreRuntimeSessions(ctx))
	session, err := handler.launchWorkspaceRuntimeService(ctx, "0000000000000e01", "custom-worker", "", "", nil)
	require.NoError(err)
	baseExists := func(id string) bool {
		return procutil.CommandContext(ctx, tmux[0], append(tmux[1:], "has-session", "-t", "forge-"+id)...).Run() == nil
	}

	_ = procutil.CommandContext(ctx, tmux[0], append(tmux[1:], "kill-server")...).Run()
	require.Eventually(func() bool {
		handler.runtimeRecoveryMu.Lock()
		defer handler.runtimeRecoveryMu.Unlock()
		return handler.runtimeRecoveryPending[session.Key]
	}, 5*time.Second, 10*time.Millisecond)
	_, err = handler.stopWorkspaceRuntimeSession(ctx, &stopWorkspaceRuntimeSessionInput{
		ID: "0000000000000e01", SessionKey: session.Key,
	})
	require.NoError(err)
	require.NoError(procutil.CommandContext(ctx, tmux[0], append(tmux[1:],
		"new-session", "-d", "-s", "other", "sleep 600")...).Run())

	handler.runWorkspaceTmuxPrune(ctx)

	for _, id := range []string{"0000000000000e01", "0000000000000e02"} {
		ws, err := database.GetWorkspace(ctx, id)
		require.NoError(err)
		assert.Equal("ready", ws.Status, "workspace %s", id)
		assert.True(baseExists(id), "base terminal %s restored", id)
	}
}

// A base terminal whose start keeps hanging gets only its share of a recovery
// pass, so a pending agent still resumes, whether it runs in a healthy
// workspace or in the stuck terminal's own.
func TestStuckTerminalRestoreLeavesTimeForAgents(t *testing.T) {
	t.Parallel()
	if privateTmuxOwner == nil {
		t.Skip("private tmux tests unsupported")
	}
	for name, agentWorkspace := range map[string]string{
		"other workspace": "0000000000000f02",
		"same workspace":  "0000000000000f01",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			testStuckTerminalRestoreLeavesTimeForAgents(t, agentWorkspace)
		})
	}
}

func testStuckTerminalRestoreLeavesTimeForAgents(t *testing.T, agentWorkspace string) {
	require := require.New(t)
	ctx := t.Context()
	tmuxPath, err := exec.LookPath("tmux")
	require.NoError(err)
	tmux := privateTmuxOwner.Command(t, tmuxPath)
	wrapper := filepath.Join(t.TempDir(), "tmux")
	require.NoError(os.WriteFile(wrapper, []byte("#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = forge-0000000000000f01 ] && [ \"$1\" != has-session ]; then exec sleep 60; fi\ndone\nexec "+shellquote.Join(tmux...)+" \"$@\"\n"), 0o755))
	database := dbtest.Open(t)
	cwd := gitfixture.NewRepository(t, false).Dir
	for _, id := range []string{"0000000000000f01", "0000000000000f02"} {
		worktree := t.TempDir()
		if id == agentWorkspace {
			worktree = cwd
		}
		require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
			ID: id, Platform: "github", PlatformHost: "github.com",
			RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc,
			ItemKey: db.AdHocWorkspaceItemKey("work/" + id), GitHeadRef: "work/" + id,
			WorkspaceBranch: "work/" + id, WorktreePath: worktree,
			Status: "ready", TmuxSession: "forge-" + id,
		}))
	}
	require.NoError(database.UpsertWorkspaceRuntimeSession(ctx, &db.WorkspaceRuntimeSession{
		WorkspaceID: agentWorkspace, SessionKey: "saved-runtime", TargetKey: "custom-worker",
		Label: "Worker", Kind: "agent", Scope: "session", TmuxSession: "gone",
	}))
	_, err = database.RecordWorkspaceRuntimeConversation(ctx, "saved-runtime", "claude", "conversation")
	require.NoError(err)
	agent := filepath.Join(t.TempDir(), "agent")
	require.NoError(os.WriteFile(agent, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > args.tmp && mv args.tmp args\nexec sleep 600\n"), 0o755))
	runtime := localruntime.NewManager(localruntime.Options{
		TmuxCommand:             []string{wrapper},
		WrapAgentSessionsInTmux: true,
		PtyOwnerRuntime:         ptyownerruntime.New(&ptyowner.Client{Root: t.TempDir(), InProcess: true}, nil),
		TmuxOwnerMarker:         "kenn-forge:test-stuck",
		Targets: []localruntime.LaunchTarget{{
			Key: "custom-worker", Kind: localruntime.LaunchTargetAgent, Available: true, Command: []string{agent},
		}, {Key: "shell", Kind: localruntime.LaunchTargetShell, Available: true, Command: []string{wrapper}}},
	})
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		runtime.StopWorkspace(cleanupCtx, agentWorkspace)
		runtime.Shutdown()
	})
	workspaces := workspacetest.NewManager(t, database, t.TempDir())
	workspaces.SetTmuxCommand([]string{wrapper})
	handler := New(Deps{DB: database, Workspaces: workspaces, Runtime: runtime})
	handler.baseRestoreBudget = 200 * time.Millisecond
	handler.setRuntimeRecoveryPending("saved-runtime", true)
	handler.runtimeRecoveryMu.Lock()
	handler.baseRestoreRemaining = map[string]bool{"0000000000000f01": true}
	handler.runtimeRecoveryMu.Unlock()

	go func() { _ = handler.restoreRuntimeSessions(ctx, true) }()

	require.Eventually(func() bool {
		args, err := os.ReadFile(filepath.Join(cwd, "args"))
		return err == nil && strings.Contains(string(args), "conversation")
	}, 5*time.Second, 10*time.Millisecond, "the pending agent resumes despite the stuck terminal")
}

// A tmux server can die, and a replacement start, between the prune's check
// for outstanding terminal restores and its tmux listing. Judging the lost
// agent then reveals the loss, so the prune leaves the workspaces whose base
// terminals went with the old server ready for recovery to restore.
func TestTmuxPruneLeavesWorkspacesReadyWhenTheServerIsReplaced(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"server killed", "client detached first"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			testTmuxPruneLeavesWorkspacesReadyWhenTheServerIsReplaced(t, name == "client detached first")
		})
	}
}

func testTmuxPruneLeavesWorkspacesReadyWhenTheServerIsReplaced(t *testing.T, detachClient bool) {
	require := require.New(t)
	ctx := t.Context()
	if privateTmuxOwner == nil {
		t.Skip("private tmux tests unsupported")
	}
	tmuxPath, err := exec.LookPath("tmux")
	require.NoError(err)
	tmux := privateTmuxOwner.Command(t, tmuxPath)
	cwd := gitfixture.NewRepository(t, false).Dir
	database := dbtest.Open(t)
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID: "0000000000000e01", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc,
		ItemKey: db.AdHocWorkspaceItemKey("work/replaced"), GitHeadRef: "work/replaced",
		WorkspaceBranch: "work/replaced", WorktreePath: cwd, Status: "ready",
		TmuxSession: "forge-0000000000000e01",
	}))
	require.NoError(procutil.CommandContext(ctx, tmux[0], append(tmux[1:],
		"new-session", "-d", "-s", "forge-0000000000000e01", "sleep 600")...).Run())
	agent := filepath.Join(t.TempDir(), "agent")
	require.NoError(os.WriteFile(agent, []byte("#!/bin/sh\n: > started\nexec sleep 600\n"), 0o755))
	var handler *Handler
	release := make(chan struct{})
	exitHandled := make(chan struct{})
	runtime := localruntime.NewManager(localruntime.Options{
		TmuxCommand:             tmux,
		WrapAgentSessionsInTmux: true,
		PtyOwnerRuntime:         ptyownerruntime.New(&ptyowner.Client{Root: t.TempDir(), InProcess: true}, nil),
		TmuxOwnerMarker:         "kenn-forge:test-replaced",
		AgentExitDir:            t.TempDir(),
		Targets: []localruntime.LaunchTarget{{
			Key: "custom-worker", Kind: localruntime.LaunchTargetAgent, Available: true, Command: []string{agent},
		}, {Key: "shell", Kind: localruntime.LaunchTargetShell, Available: true, Command: tmux}},
		OnSessionExit: func(info localruntime.SessionInfo) {
			<-release
			handler.HandleRuntimeSessionExit(info)
			close(exitHandled)
		},
	})
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		runtime.StopWorkspace(cleanupCtx, "0000000000000e01")
		runtime.Shutdown()
	})
	workspaces := workspacetest.NewManager(t, database, t.TempDir())
	workspaces.SetTmuxCommand(tmux)
	handler = New(Deps{DB: database, Workspaces: workspaces, Runtime: runtime})
	session, err := handler.launchWorkspaceRuntimeService(ctx, "0000000000000e01", "custom-worker", "", "", nil)
	require.NoError(err)
	require.Eventually(func() bool { _, err := os.Stat(filepath.Join(cwd, "started")); return err == nil },
		5*time.Second, 10*time.Millisecond)

	// The server dies and another terminal starts a replacement before the
	// exit handler has run.
	if detachClient {
		require.Eventually(func() bool {
			out, err := procutil.CommandContext(ctx, tmux[0], append(tmux[1:],
				"list-clients", "-t", session.TmuxSession, "-F", "#{client_pid}")...).Output()
			return err == nil && strings.TrimSpace(string(out)) != ""
		}, 5*time.Second, 10*time.Millisecond)
		require.NoError(procutil.CommandContext(ctx, tmux[0], append(tmux[1:],
			"detach-client", "-s", session.TmuxSession)...).Run())
		require.Eventually(func() bool { return runtime.Exited(session.Key) }, 5*time.Second, 10*time.Millisecond)
	}
	_ = procutil.CommandContext(ctx, tmux[0], append(tmux[1:], "kill-server")...).Run()
	require.Eventually(func() bool {
		server, err := workspaces.TmuxServerID(ctx)
		return err == nil && server == "" && runtime.Exited(session.Key)
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(procutil.CommandContext(ctx, tmux[0], append(tmux[1:],
		"new-session", "-d", "-s", "other", "sleep 600")...).Run())
	handler.runWorkspaceTmuxPrune(ctx)
	close(release)
	<-exitHandled

	ws, err := database.GetWorkspace(ctx, "0000000000000e01")
	require.NoError(err)
	require.Equal("ready", ws.Status, "the replaced server's base terminal is restored, not errored")
	rows, err := database.ListAllWorkspaceRuntimeSessions(ctx)
	require.NoError(err)
	require.Len(rows, 1, "the lost agent is kept for recovery")
}

// Startup restores the base terminal of a workspace that is not ready only
// for its retained agents. When that restore fails but the agents resume,
// later passes still restore the terminal rather than dropping it.
func TestOutstandingTerminalOfRetainedWorkspaceIsRestoredAfterItsAgentResumes(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	ctx := t.Context()
	if privateTmuxOwner == nil {
		t.Skip("private tmux tests unsupported")
	}
	tmuxPath, err := exec.LookPath("tmux")
	require.NoError(err)
	tmux := privateTmuxOwner.Command(t, tmuxPath)
	// The base terminal cannot start while the fail file exists.
	fail := filepath.Join(t.TempDir(), "fail")
	require.NoError(os.WriteFile(fail, nil, 0o600))
	wrapper := filepath.Join(t.TempDir(), "tmux")
	require.NoError(os.WriteFile(wrapper, []byte("#!/bin/sh\nfor a in \"$@\"; do\n"+
		"  if [ \"$a\" = forge-0000000000000d01 ] && [ \"$1\" = new-session ] && [ -e "+
		shellquote.Join(fail)+" ]; then exit 1; fi\ndone\nexec "+shellquote.Join(tmux...)+" \"$@\"\n"), 0o755))
	cwd := gitfixture.NewRepository(t, false).Dir
	database := dbtest.Open(t)
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID: "0000000000000d01", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc,
		ItemKey: db.AdHocWorkspaceItemKey("work/retained"), GitHeadRef: "work/retained",
		WorkspaceBranch: "work/retained", WorktreePath: cwd, Status: "error",
		TmuxSession: "forge-0000000000000d01",
	}))
	require.NoError(database.UpsertWorkspaceRuntimeSession(ctx, &db.WorkspaceRuntimeSession{
		WorkspaceID: "0000000000000d01", SessionKey: "saved-runtime", TargetKey: "custom-worker",
		Label: "Worker", Kind: "agent", Scope: "session", TmuxSession: "gone",
	}))
	_, err = database.RecordWorkspaceRuntimeConversation(ctx, "saved-runtime", "claude", "conversation")
	require.NoError(err)
	agent := filepath.Join(t.TempDir(), "agent")
	require.NoError(os.WriteFile(agent, []byte("#!/bin/sh\n: > started\nexec sleep 600\n"), 0o755))
	runtime := localruntime.NewManager(localruntime.Options{
		TmuxCommand:             []string{wrapper},
		WrapAgentSessionsInTmux: true,
		PtyOwnerRuntime:         ptyownerruntime.New(&ptyowner.Client{Root: t.TempDir(), InProcess: true}, nil),
		TmuxOwnerMarker:         "kenn-forge:test-retained",
		Targets: []localruntime.LaunchTarget{{
			Key: "custom-worker", Kind: localruntime.LaunchTargetAgent, Available: true, Command: []string{agent},
		}, {Key: "shell", Kind: localruntime.LaunchTargetShell, Available: true, Command: []string{wrapper}}},
	})
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		runtime.StopWorkspace(cleanupCtx, "0000000000000d01")
		runtime.Shutdown()
	})
	workspaces := workspacetest.NewManager(t, database, t.TempDir())
	workspaces.SetTmuxCommand([]string{wrapper})
	handler := New(Deps{DB: database, Workspaces: workspaces, Runtime: runtime})
	require.NoError(handler.RestoreRuntimeSessions(ctx))
	require.Eventually(func() bool { _, err := os.Stat(filepath.Join(cwd, "started")); return err == nil },
		5*time.Second, 10*time.Millisecond, "the agent resumes")
	require.True(handler.restoringBaseTerminals(), "the failed terminal stays outstanding")

	require.NoError(os.Remove(fail))
	require.NoError(handler.restoreRuntimeSessions(ctx, true))

	require.NoError(procutil.CommandContext(ctx, tmux[0], append(tmux[1:],
		"has-session", "-t", "=forge-0000000000000d01")...).Run(), "the base terminal is restored")
	require.False(handler.restoringBaseTerminals())
}

// A deleted workspace never admits recovery again, so an outstanding terminal
// restore for it must end rather than hold the missing-tmux prune forever.
func TestDeletedWorkspaceEndsItsOutstandingTerminalRestore(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID: "0000000000000b01", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc,
		ItemKey: db.AdHocWorkspaceItemKey("work/deleted"), GitHeadRef: "work/deleted",
		WorkspaceBranch: "work/deleted", WorktreePath: t.TempDir(), Status: "ready",
		TmuxSession: "forge-0000000000000b01",
	}))
	runtime := localruntime.NewManager(localruntime.Options{})
	t.Cleanup(runtime.Shutdown)
	handler := New(Deps{DB: database, Workspaces: workspacetest.NewManager(t, database, t.TempDir()), Runtime: runtime})
	handler.runtimeRecoveryMu.Lock()
	handler.baseRestoreRemaining = map[string]bool{"0000000000000b01": true}
	handler.runtimeRecoveryMu.Unlock()
	handler.markWorkspaceDeleting("0000000000000b01")
	handler.finishWorkspaceDeleting("0000000000000b01", true)

	require.NoError(handler.restoreRuntimeSessions(ctx, true))

	require.False(handler.restoringBaseTerminals(), "the deleted workspace's terminal is no longer outstanding")
}
