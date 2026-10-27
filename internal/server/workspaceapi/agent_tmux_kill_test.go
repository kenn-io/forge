//go:build !windows

package workspaceapi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	shellquote "github.com/kballard/go-shellquote"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/agentactivity"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/ptyowner"
	ptyownerruntime "go.kenn.io/forge/internal/ptyowner/runtime"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/internal/testutil/workspacetest"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

// A shutdown can signal the agents before tmux and Forge. The session then
// ends cleanly, as if the agent finished, yet the agent was killed and must
// be kept. Killing the whole pane is the shutdown case; killing only the
// agent leaves its wrapper to record the signal.
func TestRuntimeExitKeepsAgentKilledBeforeTmux(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"pane killed", "agent killed", "client and pane killed"} {
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
			// "client and pane killed" holds the exit until both are dead.
			release := make(chan struct{})
			if name != "client and pane killed" {
				close(release)
			}
			runtime := localruntime.NewManager(localruntime.Options{
				TmuxCommand:             tmux,
				WrapAgentSessionsInTmux: true,
				PtyOwnerRuntime:         ptyownerruntime.New(&ptyowner.Client{Root: t.TempDir(), InProcess: true}, nil),
				TmuxOwnerMarker:         "kenn-forge:test-killed",
				AgentExitDir:            t.TempDir(),
				Targets: []localruntime.LaunchTarget{{
					Key: "custom-worker", Kind: localruntime.LaunchTargetAgent, Available: true, Command: []string{agent},
				}, {Key: "shell", Kind: localruntime.LaunchTargetShell, Available: true, Command: tmux}},
				OnSessionExit: func(info localruntime.SessionInfo) {
					<-release
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

			tmuxPID := func(args ...string) int {
				var pid int
				require.Eventually(func() bool {
					out, err := procutil.CommandContext(ctx, tmux[0], append(tmux[1:], args...)...).Output()
					if err != nil {
						return false
					}
					pid, err = strconv.Atoi(strings.TrimSpace(string(out)))
					return err == nil
				}, 5*time.Second, 10*time.Millisecond)
				return pid
			}
			killPane := func() {
				panePID := tmuxPID("list-panes", "-t", session.TmuxSession, "-F", "#{pane_pid}")
				require.NoError(syscall.Kill(-panePID, syscall.SIGTERM))
			}
			switch name {
			case "pane killed":
				killPane()
			case "agent killed":
				require.NoError(syscall.Kill(agentPID, syscall.SIGTERM))
			default:
				clientPID := tmuxPID("list-clients", "-t", session.TmuxSession, "-F", "#{client_pid}")
				require.NoError(syscall.Kill(clientPID, syscall.SIGKILL))
				killPane()
				require.Eventually(func() bool {
					return procutil.CommandContext(ctx, tmux[0], append(tmux[1:],
						"has-session", "-t", session.TmuxSession)...).Run() != nil
				}, 5*time.Second, 10*time.Millisecond)
				close(release)
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

// A resumed agent reuses its tmux name. Its new wrapper clears the exit its
// previous run recorded, so that stale status is never read as the new run's.
func TestResumedAgentClearsPreviousExitRecord(t *testing.T) {
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
		TmuxOwnerMarker:         "kenn-forge:test-resume-record",
		AgentExitDir:            t.TempDir(),
		Targets: []localruntime.LaunchTarget{{
			Key: "custom-worker", Kind: localruntime.LaunchTargetAgent, Available: true, Command: []string{agent},
		}, {Key: "shell", Kind: localruntime.LaunchTargetShell, Available: true, Command: tmux}},
		OnSessionExit: func(localruntime.SessionInfo) {},
	})
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		runtime.StopWorkspace(cleanupCtx, "workspace")
		runtime.Shutdown()
	})
	started := func() bool { _, err := os.Stat(filepath.Join(cwd, "started")); return err == nil }

	first, err := runtime.LaunchWithInitialMessage(ctx, "workspace", cwd, "custom-worker", "", nil)
	require.NoError(err)
	require.Eventually(started, 5*time.Second, 10*time.Millisecond)
	require.NoError(os.WriteFile(filepath.Join(cwd, "quit"), nil, 0o600))
	require.Eventually(func() bool { return runtime.Exited(first.Key) }, 5*time.Second, 10*time.Millisecond)
	exit, recorded := runtime.AgentExit(first.TmuxSession)
	require.True(recorded)
	require.True(exit.Recorded, "the finished run left its status")
	require.Equal(0, exit.Status)

	resumed, err := runtime.Resume(ctx, localruntime.RestoredRuntimeSession{
		WorkspaceID: "workspace", SessionKey: first.Key, TargetKey: "custom-worker", Label: first.Label,
		Kind: localruntime.LaunchTargetAgent, CWD: cwd, CreatedAt: first.CreatedAt,
	}, "claude", "conversation")
	require.NoError(err)
	require.Equal(first.TmuxSession, resumed.TmuxSession, "resume reuses the tmux name")
	require.Eventually(started, 5*time.Second, 10*time.Millisecond)
	exit, recorded = runtime.AgentExit(resumed.TmuxSession)
	require.True(recorded)
	require.False(exit.Recorded, "the new run starts without the old status")
}

// An agent can finish after its tmux client was killed but before Forge
// handles that client's exit. Its recorded normal exit means the conversation
// is over, even when tmux exited with it as its last session.
func TestRuntimeExitForgetsAgentThatFinishedBeforeItsExitWasHandled(t *testing.T) {
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
		ItemKey: db.AdHocWorkspaceItemKey("work/finished"), GitHeadRef: "work/finished",
		WorkspaceBranch: "work/finished", WorktreePath: cwd, Status: "ready",
	}))
	agent := filepath.Join(t.TempDir(), "agent")
	require.NoError(os.WriteFile(agent, []byte("#!/bin/sh\n: > started\nwhile [ ! -e quit ]; do sleep 0.05; done\n"), 0o755))
	var handler *Handler
	release := make(chan struct{})
	runtime := localruntime.NewManager(localruntime.Options{
		TmuxCommand:             tmux,
		WrapAgentSessionsInTmux: true,
		PtyOwnerRuntime:         ptyownerruntime.New(&ptyowner.Client{Root: t.TempDir(), InProcess: true}, nil),
		TmuxOwnerMarker:         "kenn-forge:test-finished",
		AgentExitDir:            t.TempDir(),
		Targets: []localruntime.LaunchTarget{{
			Key: "custom-worker", Kind: localruntime.LaunchTargetAgent, Available: true, Command: []string{agent},
		}, {Key: "shell", Kind: localruntime.LaunchTargetShell, Available: true, Command: tmux}},
		OnSessionExit: func(info localruntime.SessionInfo) {
			<-release
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

	var clientPID int
	require.Eventually(func() bool {
		out, err := procutil.CommandContext(ctx, tmux[0], append(tmux[1:],
			"list-clients", "-t", session.TmuxSession, "-F", "#{client_pid}")...).Output()
		if err != nil {
			return false
		}
		clientPID, err = strconv.Atoi(strings.TrimSpace(string(out)))
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(syscall.Kill(clientPID, syscall.SIGKILL))
	// The agent finishes; as tmux's only session, tmux exits with it.
	require.NoError(os.WriteFile(filepath.Join(cwd, "quit"), nil, 0o600))
	require.Eventually(func() bool {
		exit, recorded := runtime.AgentExit(session.TmuxSession)
		return recorded && exit.Recorded
	}, 5*time.Second, 10*time.Millisecond)
	close(release)

	require.Eventually(func() bool {
		rows, err := database.ListAllWorkspaceRuntimeSessions(ctx)
		return err == nil && len(rows) == 0
	}, 5*time.Second, 10*time.Millisecond, "a finished agent is forgotten")
	handler.runtimeRecoveryMu.Lock()
	defer handler.runtimeRecoveryMu.Unlock()
	require.False(handler.runtimeRecoveryPending[session.Key])
}

// Startup pruning must not delete the exit record of a gone session that a
// stored runtime still names: that runtime's exit may still be handled.
func TestStartupPruneKeepsRecordsOfStoredRuntimes(t *testing.T) {
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
	cwd := gitfixture.NewRepository(t, false).Dir
	database := dbtest.Open(t)
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID: "workspace", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc,
		ItemKey: db.AdHocWorkspaceItemKey("work/prune"), GitHeadRef: "work/prune",
		WorkspaceBranch: "work/prune", WorktreePath: cwd, Status: "ready",
	}))
	agent := filepath.Join(t.TempDir(), "agent")
	require.NoError(os.WriteFile(agent, []byte("#!/bin/sh\n: > started\nexec sleep 600\n"), 0o755))
	runtime := localruntime.NewManager(localruntime.Options{
		TmuxCommand:             tmux,
		WrapAgentSessionsInTmux: true,
		PtyOwnerRuntime:         ptyownerruntime.New(&ptyowner.Client{Root: t.TempDir(), InProcess: true}, nil),
		TmuxOwnerMarker:         "kenn-forge:test-prune",
		AgentExitDir:            t.TempDir(),
		Targets: []localruntime.LaunchTarget{{
			Key: "custom-worker", Kind: localruntime.LaunchTargetAgent, Available: true, Command: []string{agent},
		}, {Key: "shell", Kind: localruntime.LaunchTargetShell, Available: true, Command: tmux}},
		// The exit is still being handled when startup prunes.
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
	handler := New(Deps{DB: database, Workspaces: workspaces, Runtime: runtime})
	session, err := handler.launchWorkspaceRuntimeService(ctx, "workspace", "custom-worker", "", "", nil)
	require.NoError(err)
	require.Eventually(func() bool { _, err := os.Stat(filepath.Join(cwd, "started")); return err == nil },
		5*time.Second, 10*time.Millisecond)
	out, err := procutil.CommandContext(ctx, tmux[0], append(tmux[1:],
		"list-panes", "-t", session.TmuxSession, "-F", "#{pane_pid}")...).Output()
	require.NoError(err)
	panePID, err := strconv.Atoi(strings.TrimSpace(string(out)))
	require.NoError(err)
	require.NoError(syscall.Kill(-panePID, syscall.SIGTERM))
	require.Eventually(func() bool { return runtime.Exited(session.Key) }, 5*time.Second, 10*time.Millisecond)

	handler.pruneAgentExits(ctx)

	_, recorded := runtime.AgentExit(session.TmuxSession)
	require.True(recorded, "a stored runtime keeps its exit record")
}

// A finished agent's exit record outlives a cleanup that never deletes its
// row, as when Forge shuts down first, so the next start forgets the agent
// rather than resuming its finished conversation.
func TestFinishedAgentStaysFinishedWhenCleanupIsInterrupted(t *testing.T) {
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
		ItemKey: db.AdHocWorkspaceItemKey("work/interrupted"), GitHeadRef: "work/interrupted",
		WorkspaceBranch: "work/interrupted", WorktreePath: cwd, Status: "ready",
	}))
	agent := filepath.Join(t.TempDir(), "agent")
	require.NoError(os.WriteFile(agent, []byte(
		"#!/bin/sh\necho run >> runs\nwhile [ ! -e quit ]; do sleep 0.05; done\n"), 0o755))
	exitDir := t.TempDir()
	exitHandled := make(chan struct{})
	startForge := func(onExit func(*Handler, localruntime.SessionInfo)) (*Handler, *localruntime.Manager) {
		var handler *Handler
		runtime := localruntime.NewManager(localruntime.Options{
			TmuxCommand:             tmux,
			WrapAgentSessionsInTmux: true,
			PtyOwnerRuntime:         ptyownerruntime.New(&ptyowner.Client{Root: t.TempDir(), InProcess: true}, nil),
			TmuxOwnerMarker:         "kenn-forge:test-interrupted",
			AgentExitDir:            exitDir,
			Targets: []localruntime.LaunchTarget{{
				Key: "custom-worker", Kind: localruntime.LaunchTargetAgent, Available: true, Command: []string{agent},
			}, {Key: "shell", Kind: localruntime.LaunchTargetShell, Available: true, Command: tmux}},
			OnSessionExit: func(info localruntime.SessionInfo) {
				if onExit != nil {
					onExit(handler, info)
				}
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
		handler = New(Deps{DB: database, Workspaces: workspaces, Runtime: runtime})
		return handler, runtime
	}
	handler, runtime := startForge(func(handler *Handler, info localruntime.SessionInfo) {
		// Forge is shutting down, so the row cleanup never runs.
		handler.stopBackground()
		handler.HandleRuntimeSessionExit(info)
		close(exitHandled)
	})
	session, err := handler.launchWorkspaceRuntimeService(ctx, "workspace", "custom-worker", "", "", nil)
	require.NoError(err)
	require.Eventually(func() bool { _, err := os.Stat(filepath.Join(cwd, "runs")); return err == nil },
		5*time.Second, 10*time.Millisecond)
	_, err = database.RecordWorkspaceRuntimeConversation(ctx, session.Key, "claude", "conversation")
	require.NoError(err)
	require.NoError(os.WriteFile(filepath.Join(cwd, "quit"), nil, 0o600))
	<-exitHandled
	rows, err := database.ListAllWorkspaceRuntimeSessions(ctx)
	require.NoError(err)
	require.Len(rows, 1, "the interrupted cleanup leaves the row")
	runtime.Shutdown()

	restarted, restartedRuntime := startForge(nil)
	require.NoError(restarted.RestoreRuntimeSessions(ctx))

	rows, err = database.ListAllWorkspaceRuntimeSessions(ctx)
	require.NoError(err)
	require.Empty(rows, "the finished agent is forgotten")
	require.Empty(restartedRuntime.ListSessions("workspace"))
	runs, err := os.ReadFile(filepath.Join(cwd, "runs"))
	require.NoError(err)
	require.Equal("run\n", string(runs), "the finished conversation is not resumed")
}

// Startup reattaches to a surviving agent while recovery is still pending, and
// the exit handler leaves a pending runtime alone. An agent killed during
// that window must still be kept once its reattachment finishes, or the
// missing-tmux prune forgets it.
func TestReattachKeepsAgentKilledWhileRecoveryWasPending(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	ctx := t.Context()
	if privateTmuxOwner == nil {
		t.Skip("private tmux tests unsupported")
	}
	tmuxPath, err := exec.LookPath("tmux")
	require.NoError(err)
	tmux := privateTmuxOwner.Command(t, tmuxPath)
	// Another session keeps the server running, as base terminals would.
	require.NoError(procutil.CommandContext(ctx, tmux[0], append(tmux[1:],
		"new-session", "-d", "-s", "other", "sleep 600")...).Run())
	cwd := gitfixture.NewRepository(t, false).Dir
	database := dbtest.Open(t)
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID: "workspace", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc,
		ItemKey: db.AdHocWorkspaceItemKey("work/reattach"), GitHeadRef: "work/reattach",
		WorkspaceBranch: "work/reattach", WorktreePath: cwd, Status: "ready",
	}))
	agent := filepath.Join(t.TempDir(), "agent")
	require.NoError(os.WriteFile(agent, []byte("#!/bin/sh\n: > started\nexec sleep 600\n"), 0o755))
	var handler *Handler
	exitHandled := make(chan struct{})
	runtime := localruntime.NewManager(localruntime.Options{
		TmuxCommand:             tmux,
		WrapAgentSessionsInTmux: true,
		PtyOwnerRuntime:         ptyownerruntime.New(&ptyowner.Client{Root: t.TempDir(), InProcess: true}, nil),
		TmuxOwnerMarker:         "kenn-forge:test-reattach",
		AgentExitDir:            t.TempDir(),
		Targets: []localruntime.LaunchTarget{{
			Key: "custom-worker", Kind: localruntime.LaunchTargetAgent, Available: true, Command: []string{agent},
		}, {Key: "shell", Kind: localruntime.LaunchTargetShell, Available: true, Command: tmux}},
		OnSessionExit: func(info localruntime.SessionInfo) {
			handler.HandleRuntimeSessionExit(info)
			close(exitHandled)
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
	handler = New(Deps{DB: database, Workspaces: workspaces, Runtime: runtime})
	session, err := handler.launchWorkspaceRuntimeService(ctx, "workspace", "custom-worker", "", "", nil)
	require.NoError(err)
	require.Eventually(func() bool { _, err := os.Stat(filepath.Join(cwd, "started")); return err == nil },
		5*time.Second, 10*time.Millisecond)
	_, err = database.RecordWorkspaceRuntimeConversation(ctx, session.Key, "claude", "conversation")
	require.NoError(err)
	rows, err := database.ListAllWorkspaceRuntimeSessions(ctx)
	require.NoError(err)
	require.Len(rows, 1)

	// The agent is killed while startup still has its recovery pending.
	handler.setRuntimeRecoveryPending(session.Key, true)
	out, err := procutil.CommandContext(ctx, tmux[0], append(tmux[1:],
		"list-panes", "-t", session.TmuxSession, "-F", "#{pane_pid}")...).Output()
	require.NoError(err)
	panePID, err := strconv.Atoi(strings.TrimSpace(string(out)))
	require.NoError(err)
	require.NoError(syscall.Kill(-panePID, syscall.SIGTERM))
	<-exitHandled

	handler.finishReattachedRuntime(ctx, rows[0])
	handler.runWorkspaceTmuxPrune(ctx)

	rows, err = database.ListAllWorkspaceRuntimeSessions(ctx)
	require.NoError(err)
	require.Len(rows, 1, "the killed agent is kept for recovery")
	handler.runtimeRecoveryMu.Lock()
	defer handler.runtimeRecoveryMu.Unlock()
	require.True(handler.runtimeRecoveryPending[session.Key])
}

// The missing-tmux prune lists tmux and then deletes rows whose session is
// gone. An agent killed during a pass must be judged on the evidence at
// deletion time: kept once its exit handler kept it, and kept by its kill
// record when that handler has not yet run.
func TestTmuxPruneKeepsAgentKilledDuringThePass(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"exit handled during the pass", "exit not yet handled"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			testTmuxPruneKeepsAgentKilledDuringThePass(t, name == "exit handled during the pass")
		})
	}
}

func testTmuxPruneKeepsAgentKilledDuringThePass(t *testing.T, handledDuringPass bool) {
	require := require.New(t)
	ctx := t.Context()
	if privateTmuxOwner == nil {
		t.Skip("private tmux tests unsupported")
	}
	tmuxPath, err := exec.LookPath("tmux")
	require.NoError(err)
	tmux := privateTmuxOwner.Command(t, tmuxPath)
	// Another session keeps the server running, as base terminals would.
	require.NoError(procutil.CommandContext(ctx, tmux[0], append(tmux[1:],
		"new-session", "-d", "-s", "other", "sleep 600")...).Run())
	// The prune's tmux listing waits while a pause file exists.
	control := t.TempDir()
	wrapper := filepath.Join(t.TempDir(), "tmux")
	require.NoError(os.WriteFile(wrapper, []byte("#!/bin/sh\nfor a in \"$@\"; do\n"+
		"  if [ \"$a\" = list-sessions ] && [ -e "+shellquote.Join(filepath.Join(control, "pause"))+" ]; then\n"+
		"    : > "+shellquote.Join(filepath.Join(control, "paused"))+"\n"+
		"    while [ ! -e "+shellquote.Join(filepath.Join(control, "go"))+" ]; do sleep 0.02; done\n"+
		"  fi\ndone\nexec "+shellquote.Join(tmux...)+" \"$@\"\n"), 0o755))
	cwd := gitfixture.NewRepository(t, false).Dir
	database := dbtest.Open(t)
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID: "workspace", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc,
		ItemKey: db.AdHocWorkspaceItemKey("work/prune-pass"), GitHeadRef: "work/prune-pass",
		WorkspaceBranch: "work/prune-pass", WorktreePath: cwd, Status: "ready",
	}))
	agent := filepath.Join(t.TempDir(), "agent")
	require.NoError(os.WriteFile(agent, []byte("#!/bin/sh\n: > started\nexec sleep 600\n"), 0o755))
	var handler *Handler
	release := make(chan struct{})
	exitHandled := make(chan struct{})
	runtime := localruntime.NewManager(localruntime.Options{
		TmuxCommand:             tmux,
		WrapAgentSessionsInTmux: true,
		PtyOwnerRuntime:         ptyownerruntime.New(&ptyowner.Client{Root: t.TempDir(), InProcess: true}, nil),
		TmuxOwnerMarker:         "kenn-forge:test-prune-pass",
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
		runtime.StopWorkspace(cleanupCtx, "workspace")
		runtime.Shutdown()
	})
	workspaces := workspacetest.NewManager(t, database, t.TempDir())
	workspaces.SetTmuxCommand([]string{wrapper})
	handler = New(Deps{DB: database, Workspaces: workspaces, Runtime: runtime})
	session, err := handler.launchWorkspaceRuntimeService(ctx, "workspace", "custom-worker", "", "", nil)
	require.NoError(err)
	require.Eventually(func() bool { _, err := os.Stat(filepath.Join(cwd, "started")); return err == nil },
		5*time.Second, 10*time.Millisecond)
	_, err = database.RecordWorkspaceRuntimeConversation(ctx, session.Key, "claude", "conversation")
	require.NoError(err)
	out, err := procutil.CommandContext(ctx, tmux[0], append(tmux[1:],
		"list-panes", "-t", session.TmuxSession, "-F", "#{pane_pid}")...).Output()
	require.NoError(err)
	panePID, err := strconv.Atoi(strings.TrimSpace(string(out)))
	require.NoError(err)
	killAgent := func() {
		require.NoError(syscall.Kill(-panePID, syscall.SIGTERM))
		require.Eventually(func() bool { return runtime.Exited(session.Key) },
			5*time.Second, 10*time.Millisecond)
	}

	if handledDuringPass {
		require.NoError(os.WriteFile(filepath.Join(control, "pause"), nil, 0o600))
		pruned := make(chan struct{})
		go func() {
			handler.runWorkspaceTmuxPrune(ctx)
			close(pruned)
		}()
		require.Eventually(func() bool { _, err := os.Stat(filepath.Join(control, "paused")); return err == nil },
			5*time.Second, 10*time.Millisecond)
		killAgent()
		close(release)
		<-exitHandled
		require.NoError(os.WriteFile(filepath.Join(control, "go"), nil, 0o600))
		<-pruned
	} else {
		killAgent()
		handler.runWorkspaceTmuxPrune(ctx)
		close(release)
		<-exitHandled
	}

	rows, err := database.ListAllWorkspaceRuntimeSessions(ctx)
	require.NoError(err)
	require.Len(rows, 1, "the killed agent is kept for recovery")
	require.Equal("conversation", rows[0].ConversationID)
	handler.runtimeRecoveryMu.Lock()
	defer handler.runtimeRecoveryMu.Unlock()
	require.True(handler.runtimeRecoveryPending[session.Key])
}

// An agent without tmux leaves no exit record, so its row must be gone once
// its exit is handled, even when Forge is shutting down. Otherwise the next
// start finds its saved conversation and resumes a finished agent.
func TestFinishedPtyOwnerAgentIsForgottenDuringShutdown(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	ctx := t.Context()
	cwd := gitfixture.NewRepository(t, false).Dir
	database := dbtest.Open(t)
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID: "workspace", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc,
		ItemKey: db.AdHocWorkspaceItemKey("work/pty-finished"), GitHeadRef: "work/pty-finished",
		WorkspaceBranch: "work/pty-finished", WorktreePath: cwd, Status: "ready",
	}))
	agent := filepath.Join(t.TempDir(), "agent")
	require.NoError(os.WriteFile(agent, []byte(
		"#!/bin/sh\necho run >> runs\nwhile [ ! -e quit ]; do sleep 0.05; done\n"), 0o755))
	ownerRoot := t.TempDir()
	exitHandled := make(chan struct{})
	startForge := func(onExit func(*Handler, localruntime.SessionInfo)) (*Handler, *localruntime.Manager) {
		var handler *Handler
		runtime := localruntime.NewManager(localruntime.Options{
			PtyOwnerRuntime: ptyownerruntime.New(&ptyowner.Client{Root: ownerRoot, InProcess: true}, nil),
			Targets: []localruntime.LaunchTarget{{
				Key: "custom-worker", Kind: localruntime.LaunchTargetAgent, Available: true, Command: []string{agent},
			}},
			OnSessionExit: func(info localruntime.SessionInfo) {
				if onExit != nil {
					onExit(handler, info)
				}
			},
		})
		t.Cleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
			defer cancel()
			runtime.StopWorkspace(cleanupCtx, "workspace")
			runtime.Shutdown()
		})
		handler = New(Deps{DB: database, Workspaces: workspacetest.NewManager(t, database, t.TempDir()), Runtime: runtime})
		return handler, runtime
	}
	handler, runtime := startForge(func(handler *Handler, info localruntime.SessionInfo) {
		handler.stopBackground()
		handler.HandleRuntimeSessionExit(info)
		close(exitHandled)
	})
	session, err := handler.launchWorkspaceRuntimeService(ctx, "workspace", "custom-worker", "", "", nil)
	require.NoError(err)
	require.Empty(session.TmuxSession, "the agent runs without tmux")
	require.Eventually(func() bool { _, err := os.Stat(filepath.Join(cwd, "runs")); return err == nil },
		5*time.Second, 10*time.Millisecond)
	_, err = database.RecordWorkspaceRuntimeConversation(ctx, session.Key, "claude", "conversation")
	require.NoError(err)
	require.NoError(os.WriteFile(filepath.Join(cwd, "quit"), nil, 0o600))
	<-exitHandled
	rows, err := database.ListAllWorkspaceRuntimeSessions(ctx)
	require.NoError(err)
	require.Empty(rows, "the finished agent's row is deleted while Forge shuts down")
	runtime.Shutdown()

	restarted, restartedRuntime := startForge(nil)
	require.NoError(restarted.RestoreRuntimeSessions(ctx))

	require.Empty(restartedRuntime.ListSessions("workspace"))
	runs, err := os.ReadFile(filepath.Join(cwd, "runs"))
	require.NoError(err)
	require.Equal("run\n", string(runs), "the finished conversation is not resumed")
}

// An agent can catch a termination signal, report SessionEnd with reason
// "other", and exit 0 while its pane wrapper survives. That clean status is a
// kill the agent handled, so the agent is kept; an exit the user chose is
// still a finish.
func TestRuntimeExitKeepsAgentThatHandledATerminationSignal(t *testing.T) {
	t.Parallel()
	for name, reason := range map[string]string{"terminated": "other", "exited by the user": "prompt_input_exit"} {
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
				ItemKey: db.AdHocWorkspaceItemKey("work/graceful"), GitHeadRef: "work/graceful",
				WorkspaceBranch: "work/graceful", WorktreePath: cwd, Status: "ready",
			}))
			agent := filepath.Join(t.TempDir(), "agent")
			require.NoError(os.WriteFile(agent, []byte(
				"#!/bin/sh\ntrap 'exit 0' TERM\necho $$ > pid\nwhile :; do sleep 0.05; done\n"), 0o755))
			var handler *Handler
			var runtime *localruntime.Manager
			// Exit handling spends the record once it forgets the agent, so
			// read it before the handler runs.
			type exitRecord struct {
				exit     localruntime.AgentExit
				recorded bool
			}
			exitRecords := make(chan exitRecord, 1)
			runtime = localruntime.NewManager(localruntime.Options{
				TmuxCommand:             tmux,
				WrapAgentSessionsInTmux: true,
				PtyOwnerRuntime:         ptyownerruntime.New(&ptyowner.Client{Root: t.TempDir(), InProcess: true}, nil),
				TmuxOwnerMarker:         "kenn-forge:test-graceful",
				AgentExitDir:            t.TempDir(),
				Targets: []localruntime.LaunchTarget{{
					Key: "custom-worker", Kind: localruntime.LaunchTargetAgent, Available: true, Command: []string{agent},
				}, {Key: "shell", Kind: localruntime.LaunchTargetShell, Available: true, Command: tmux}},
				OnSessionExit: func(info localruntime.SessionInfo) {
					exit, recorded := runtime.AgentExit(info.TmuxSession)
					exitRecords <- exitRecord{exit: exit, recorded: recorded}
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
			var pid int
			require.Eventually(func() bool {
				data, err := os.ReadFile(filepath.Join(cwd, "pid"))
				if err != nil {
					return false
				}
				pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
				return err == nil
			}, 5*time.Second, 10*time.Millisecond)
			hook := func(event agentactivity.HookEvent) {
				event.SessionID = "conversation"
				event.CWD = cwd
				_, err := handler.receiveAgentHook(ctx, &receiveAgentHookInput{
					Agent: "claude", RuntimeSessionKey: session.Key, Body: event,
				})
				require.NoError(err)
			}
			hook(agentactivity.HookEvent{HookEventName: "SessionStart", Source: "startup"})
			hook(agentactivity.HookEvent{HookEventName: "SessionEnd", Reason: reason})

			// Only the agent is signaled; it exits 0 and its wrapper records that.
			require.NoError(syscall.Kill(pid, syscall.SIGTERM))
			record := <-exitRecords
			require.True(record.recorded)
			require.Equal(localruntime.AgentExit{Recorded: true, Status: 0}, record.exit)

			rows := func() int {
				stored, err := database.ListAllWorkspaceRuntimeSessions(ctx)
				require.NoError(err)
				return len(stored)
			}
			if reason == "other" {
				require.Eventually(func() bool {
					handler.runtimeRecoveryMu.Lock()
					defer handler.runtimeRecoveryMu.Unlock()
					return handler.runtimeRecoveryPending[session.Key]
				}, 5*time.Second, 10*time.Millisecond)
				require.Equal(1, rows(), "the terminated agent is kept for recovery")
				return
			}
			require.Eventually(func() bool { return rows() == 0 }, 5*time.Second, 10*time.Millisecond,
				"the agent the user exited is forgotten")
		})
	}
}

// At startup, a kept agent whose session is gone with a clean exit record
// finished while unwatched, unless its conversation ended from a termination
// signal it handled.
func TestAgentFinishedUnwatchedHonorsTermination(t *testing.T) {
	t.Parallel()
	exitDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(exitDir, "forge-agent.started"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(exitDir, "forge-agent.exit"), []byte("0\n"), 0o600))
	runtime := localruntime.NewManager(localruntime.Options{AgentExitDir: exitDir})
	t.Cleanup(runtime.Shutdown)
	handler := New(Deps{Runtime: runtime})
	session := db.WorkspaceRuntimeSession{TmuxSession: "forge-agent", ConversationID: "conversation"}

	require.True(t, handler.agentFinishedUnwatched(session))
	session.ConversationEndReason = "other"
	require.False(t, handler.agentFinishedUnwatched(session), "a handled termination is not a finish")
}

const runtimeStartHelperArg = "record-runtime-start"

// TestRuntimeStartHelper records when it started, in nanoseconds, in its
// working directory, then waits like a running agent. Go keeps the time
// portable where date lacks %N.
func TestRuntimeStartHelper(t *testing.T) { //nolint:paralleltest // subprocess entry point
	if !slices.Contains(os.Args, runtimeStartHelperArg) {
		return
	}
	started := strconv.FormatInt(time.Now().UnixNano(), 10)
	if os.WriteFile("started.tmp", []byte(started), 0o600) != nil || os.Rename("started.tmp", "started") != nil {
		os.Exit(2)
	}
	time.Sleep(time.Hour) //nolint:kennlint // keeps the helper subprocess alive until the runtime stops its tmux session
}

// A runtime's start is taken before its tmux pane starts, so an agent's first
// hook is never older than the runtime it reports for, even when tmux returns
// slowly after starting the pane.
func TestRuntimeStartPrecedesItsAgent(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	ctx := t.Context()
	if privateTmuxOwner == nil {
		t.Skip("private tmux tests unsupported")
	}
	tmuxPath, err := exec.LookPath("tmux")
	require.NoError(err)
	tmux := privateTmuxOwner.Command(t, tmuxPath)
	// new-session returns only after the pane's agent has started.
	wrapper := filepath.Join(t.TempDir(), "tmux")
	require.NoError(os.WriteFile(wrapper, []byte("#!/bin/sh\nfor a in \"$@\"; do\n"+
		"  if [ \"$a\" = new-session ]; then "+shellquote.Join(tmux...)+" \"$@\"; s=$?; sleep 0.3; exit $s; fi\n"+
		"done\nexec "+shellquote.Join(tmux...)+" \"$@\"\n"), 0o755))
	cwd := gitfixture.NewRepository(t, false).Dir
	database := dbtest.Open(t)
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID: "workspace", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc,
		ItemKey: db.AdHocWorkspaceItemKey("work/start"), GitHeadRef: "work/start",
		WorkspaceBranch: "work/start", WorktreePath: cwd, Status: "ready",
	}))
	runtime := localruntime.NewManager(localruntime.Options{
		TmuxCommand:             []string{wrapper},
		WrapAgentSessionsInTmux: true,
		PtyOwnerRuntime:         ptyownerruntime.New(&ptyowner.Client{Root: t.TempDir(), InProcess: true}, nil),
		TmuxOwnerMarker:         "kenn-forge:test-start",
		Targets: []localruntime.LaunchTarget{{
			Key: "custom-worker", Kind: localruntime.LaunchTargetAgent, Available: true,
			Command: []string{os.Args[0], "-test.run=^TestRuntimeStartHelper$", "--", runtimeStartHelperArg},
		}, {Key: "shell", Kind: localruntime.LaunchTargetShell, Available: true, Command: []string{wrapper}}},
	})
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		runtime.StopWorkspace(cleanupCtx, "workspace")
		runtime.Shutdown()
	})
	workspaces := workspacetest.NewManager(t, database, t.TempDir())
	workspaces.SetTmuxCommand([]string{wrapper})
	handler := New(Deps{DB: database, Workspaces: workspaces, Runtime: runtime})

	session, err := handler.launchWorkspaceRuntimeService(ctx, "workspace", "custom-worker", "", "", nil)
	require.NoError(err)

	var started int64
	require.Eventually(func() bool {
		data, err := os.ReadFile(filepath.Join(cwd, "started"))
		if err != nil {
			return false
		}
		started, err = strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)
	require.False(time.Unix(0, started).Before(session.CreatedAt),
		"the agent started at %s, before its runtime's start %s", time.Unix(0, started).UTC(), session.CreatedAt)
}
