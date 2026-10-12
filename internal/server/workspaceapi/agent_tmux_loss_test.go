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

	"github.com/stretchr/testify/assert"
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

// A host shutdown can kill the tmux server, or Forge's tmux client before it,
// while Forge still runs. The agent then reads as exited and reports
// SessionEnd, yet its conversation is the one to resume. An agent that ends on
// its own stays forgotten, even when it was the server's last session and tmux
// exits with it.
func TestRuntimeExitKeepsAgentAfterTmuxServerLoss(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"server killed", "client killed first", "agent exits"} {
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
			// startForge builds a daemon's runtime and handler over the shared
			// database and activity store, as each Forge start does.
			startForge := func() (*Handler, *localruntime.Manager, *workspace.Manager) {
				var handler *Handler
				runtime := localruntime.NewManager(localruntime.Options{
					TmuxCommand:             tmux,
					WrapAgentSessionsInTmux: true,
					PtyOwnerRuntime:         ptyownerruntime.New(&ptyowner.Client{Root: t.TempDir(), InProcess: true}, nil),
					TmuxOwnerMarker:         "kenn-forge:test-loss",
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
				workspaces := workspace.NewManager(database, t.TempDir())
				workspaces.SetTmuxCommand(tmux)
				handler = New(Deps{DB: database, Workspaces: workspaces, Runtime: runtime, AgentActivity: activity})
				return handler, runtime, workspaces
			}
			handler, runtime, workspaces := startForge()

			session, err := handler.launchWorkspaceRuntimeService(ctx, "workspace", "custom-worker", "", "")
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
			if name == "client killed first" {
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
			}
			// kill-server can exit non-zero when its own client loses the
			// server mid-command, so check the outcome rather than the status.
			killErr := procutil.CommandContext(ctx, tmux[0], append(tmux[1:], "kill-server")...).Run()
			server, err := workspaces.TmuxServerID(ctx)
			require.NoError(err)
			require.Empty(server, "kill-server: %v", killErr)
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
			workspaces := workspace.NewManager(database, t.TempDir())
			workspaces.SetTmuxCommand(tmux)
			handler := New(Deps{DB: database, Workspaces: workspaces, AgentActivity: activity})
			if name == "server known" {
				handler.noteTmuxServer(ctx, "ended-runtime", "forge-ended")
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
			workspaces := workspace.NewManager(database, t.TempDir())
			workspaces.SetTmuxCommand(tmux)
			handler := New(Deps{
				DB: database, Workspaces: workspaces, Runtime: runtime,
				AgentActivity: agentactivity.NewStore(t.TempDir()),
			})

			session, err := runtime.LaunchWithInitialMessage(ctx, "workspace", cwd, "custom-worker", "")
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
	workspaces := workspace.NewManager(database, t.TempDir())
	workspaces.SetTmuxCommand(tmux)
	handler = New(Deps{
		DB: database, Workspaces: workspaces, Runtime: runtime,
		AgentActivity: agentactivity.NewStore(t.TempDir()),
	})
	require.NoError(handler.RestoreRuntimeSessions(ctx))
	session, err := handler.launchWorkspaceRuntimeService(ctx, "0000000000000a01", "custom-worker", "", "")
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
	// The deletion is refused. The skipped workspace stays ready, and the next
	// pass restores its terminal.
	handler.finishWorkspaceSetup("0000000000000a03", deleting)
	skipped, err := database.GetWorkspace(ctx, "0000000000000a03")
	require.NoError(err)
	assert.Equal("ready", skipped.Status, "an unfinished restore holds the prune")
	handler.runWorkspaceTmuxPrune(ctx)
	assert.True(baseExists("0000000000000a03"), "the next pass restores the skipped terminal")

	for _, id := range []string{"0000000000000a01", "0000000000000a02", "0000000000000a03"} {
		ws, err := database.GetWorkspace(ctx, id)
		require.NoError(err)
		assert.Equal("ready", ws.Status, "workspace %s", id)
		assert.True(baseExists(id), "base terminal %s restored", id)
	}
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
	workspaces := workspace.NewManager(database, t.TempDir())
	workspaces.SetTmuxCommand(tmux)
	handler = New(Deps{
		DB: database, Workspaces: workspaces, Runtime: runtime,
		AgentActivity: agentactivity.NewStore(t.TempDir()),
	})
	session, err := handler.launchWorkspaceRuntimeService(ctx, "workspace", "custom-worker", "", "")
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
