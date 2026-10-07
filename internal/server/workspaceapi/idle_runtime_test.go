package workspaceapi

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/rpc"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/agentactivity"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/ptyowner"
	ptyownerruntime "go.kenn.io/forge/internal/ptyowner/runtime"
	"go.kenn.io/forge/internal/terminalwebsocket"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/workspace"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

func newIdleRuntimeDaemon(t *testing.T, workspaceID string, deps Deps, options localruntime.Options) (*localruntime.Manager, *Handler) {
	t.Helper()
	runtime := localruntime.NewManager(options)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		runtime.StopWorkspace(cleanupCtx, workspaceID)
		runtime.Shutdown()
	})
	deps.Runtime = runtime
	handler := New(deps)
	handler.syncIdle(t.Context())
	t.Cleanup(func() { _ = handler.Shutdown(context.Background()) })
	return runtime, handler
}

func TestIdleRuntimeStopParksAgentsAndResumesOnReopen(t *testing.T) { //nolint:paralleltest // t.Setenv writes the helper switches
	require := require.New(t)
	assert := assert.New(t)
	t.Setenv("KENN_FORGE_AGENT_SESSION_HELPER", "1")
	t.Setenv("KENN_FORGE_RESUME_AGENT_HELPER", "1")
	ctx := t.Context()
	database := dbtest.Open(t)
	worktree := filepath.Join(t.TempDir(), "worktree")
	require.NoError(os.Mkdir(worktree, 0o700))
	ws := &db.Workspace{
		ID: "ws-idle", Platform: "github", PlatformHost: "github.com", RepoOwner: "acme", RepoName: "widgets",
		ItemType: db.WorkspaceItemTypeAdHoc, ItemKey: db.AdHocWorkspaceItemKey("work/idle"), GitHeadRef: "work/idle",
		WorkspaceBranch: "work/idle", WorktreePath: worktree, Status: "ready", TmuxSession: "forge-idle-base",
		TerminalBackend: workspace.TerminalBackendPtyOwner,
	}
	require.NoError(database.InsertWorkspace(ctx, ws))
	helper := func(mode string) []string {
		return []string{os.Args[0], "-test.run=^TestWorkspaceAgentSessionHelper$", "--", mode}
	}
	client := &ptyowner.Client{Root: filepath.Join(t.TempDir(), "pty-owner"), InProcess: true, Command: helper("sleep")}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.Stop(cleanupCtx, ws.TmuxSession)
	})
	workspaces := workspace.NewManager(database, t.TempDir())
	workspaces.SetPtyOwnerClient(client)
	require.NoError(workspaces.EnsureTerminal(ctx, ws))
	activity := agentactivity.NewStore(t.TempDir())
	parkedDir := t.TempDir()
	var now atomic.Int64
	now.Store(time.Now().UnixNano())
	clock := func() time.Time { return time.Unix(0, now.Load()).UTC() }
	advance := func(d time.Duration) { now.Add(int64(d)) }
	// Each daemon gets its own runtime manager and handler over the same owners.
	daemon := func(stopAfter time.Duration) (*localruntime.Manager, *Handler) {
		return newIdleRuntimeDaemon(t, ws.ID, Deps{DB: database, Workspaces: workspaces, AgentActivity: activity, Now: clock, Config: ConfigSnapshot{IdleRuntimeStopAfter: stopAfter}}, localruntime.Options{
			Targets: []localruntime.LaunchTarget{
				{Key: "worker", Kind: localruntime.LaunchTargetAgent, Available: true, Command: []string{os.Args[0], "-test.run=^TestResumeAgentHelper$", "--"}},
				{Key: "plain_shell", Kind: localruntime.LaunchTargetPlainShell, Available: true, Command: helper("sleep")},
			},
			PtyOwnerRuntime: ptyownerruntime.New(client, nil),
			ParkedDir:       parkedDir,
		})
	}
	runtime, handler := daemon(time.Hour)

	agent, err := runtime.Launch(ctx, ws.ID, worktree, "worker")
	require.NoError(err)
	shell, err := runtime.Launch(ctx, ws.ID, worktree, "plain_shell")
	require.NoError(err)
	for _, info := range []localruntime.SessionInfo{agent, shell} {
		require.NoError(handler.recordRuntimeSession(ctx, ws.ID, info, "session"))
	}
	report := func(agentName, sessionID, hook string) {
		require.NoError(activity.HandleEvent(agentName, agentactivity.HookEvent{SessionID: sessionID, CWD: worktree, HookEventName: hook}, agent.Key))
	}
	// A resumed agent's SessionStart says it continued its conversation.
	continued := func(key, sessionID string) {
		require.NoError(activity.HandleEvent("claude", agentactivity.HookEvent{SessionID: sessionID, CWD: worktree, HookEventName: "SessionStart", Source: "resume"}, key))
	}
	// The agent helper records the arguments a resume appended.
	resumedWith := func() string {
		data, _ := os.ReadFile(filepath.Join(worktree, "args"))
		return string(data)
	}
	require.Eventually(func() bool { _, err := os.Stat(filepath.Join(worktree, "args")); return err == nil }, 5*time.Second, 10*time.Millisecond)
	report("claude", "saved-conversation", "Stop")
	stopIdle := func() { handler.idle.Load().stopIdle(ctx, time.Hour) }
	running := func(key string) bool {
		return slices.ContainsFunc(runtime.ListSessions(ws.ID), func(info localruntime.SessionInfo) bool { return info.Key == key })
	}
	untouched := func(msg string) {
		assert.Len(runtime.ListSessions(ws.ID), 2, msg)
		assert.True(client.HasState(ws.TmuxSession), msg)
	}
	view := func(viewing bool) {
		_, err := handler.getWorkspaceRuntime(ctx, &getWorkspaceRuntimeInput{ID: ws.ID, Viewing: viewing})
		require.NoError(err)
	}
	mux := http.NewServeMux()
	handler.RegisterTerminal(humago.NewWithPrefix(mux, "/ws/v1", huma.DefaultConfig("idle runtime test", "1")))
	server := httptest.NewServer(mux)
	defer server.Close()
	dial := func(path string) *websocket.Conn {
		conn, _, err := terminalwebsocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/ws/v1/workspaces/"+ws.ID+path, nil, nil) //nolint:bodyclose // the upgraded response has no body
		require.NoError(err)
		return conn
	}
	claim := []byte(`{"type":"claim_resize","cols":80,"rows":24}`)
	unused := func() bool { return handler.idle.Load().idle(ws, time.Time{}) >= time.Hour }

	advance(2 * time.Hour)
	handler.idle.Load().stopIdle(ctx, 0)
	untouched("a pass whose timeout a reload cleared stops nothing")
	view(true)
	stopIdle()
	untouched("a page view is use")

	// Typing in a runtime pane from any client is use; opening, focusing and
	// reading are not.
	advance(2 * time.Hour)
	view(false)
	agentPane := dial("/runtime/sessions/" + agent.Key + "/terminal?resize_active=1")
	require.NoError(agentPane.Write(ctx, websocket.MessageText, claim))
	assert.Never(func() bool { return !unused() }, time.Second, 10*time.Millisecond, "sockets, focus and reads are not use")
	_ = agentPane.CloseNow()
	// Input from any client counts, not only sockets: tools write through the
	// runtime manager.
	advance(2 * time.Hour)
	direct, err := runtime.AttachSession(ws.ID, agent.Key)
	require.NoError(err)
	idleBefore := handler.idle.Load().idle(ws, time.Time{})
	require.NoError(direct.Write([]byte("\x1b[I")))
	assert.Equal(idleBefore, handler.idle.Load().idle(ws, time.Time{}), "automatic focus reports leave idle time unchanged")
	require.NoError(direct.Write([]byte("y")))
	direct.Close()
	assert.False(unused(), "input written through the runtime manager is use")
	advance(2 * time.Hour)
	base := dial("/terminal")
	require.NoError(base.Write(ctx, websocket.MessageBinary, []byte("echo\n")))
	assert.Eventually(func() bool { return !unused() }, 5*time.Second, 10*time.Millisecond, "typing in the workspace terminal is use")
	_ = base.CloseNow()

	advance(2 * time.Hour)
	report("claude", "saved-conversation", "SessionStart")
	shellAttached, err := runtime.AttachSession(ws.ID, shell.Key)
	require.NoError(err)
	defer shellAttached.Close()
	stopIdle()
	assert.True(running(agent.Key), "an agent that was never prompted has nothing to resume")
	assert.False(running(shell.Key), "a shell stops")
	assert.True(shellAttached.RecoverableDetach(), "an attached pane must reconnect rather than see an exit")
	assert.False(client.HasState(shell.Key))
	require.Eventually(func() bool {
		stopIdle()
		return !client.HasState(ws.TmuxSession)
	}, 5*time.Second, 10*time.Millisecond, "an idle base terminal stops after its socket releases")
	ready, err := handler.GetWorkspaceService(ctx, ws.ID)
	require.NoError(err)
	require.Equal("ready", ready.Workspace.Status, "stopping the base terminal keeps the workspace ready")
	base = dial("/terminal")
	require.NoError(base.Write(ctx, websocket.MessageBinary, []byte("echo\n")))
	require.Eventually(func() bool { return client.HasState(ws.TmuxSession) && !unused() }, 5*time.Second, 10*time.Millisecond, "attach starts a working fresh base terminal")
	_ = base.CloseNow()

	advance(2 * time.Hour)
	report("claude", "saved-conversation", "UserPromptSubmit")
	stopIdle()
	assert.True(running(agent.Key), "a working agent keeps running")
	report("gemini", "no-resume", "Stop")
	stopIdle()
	assert.True(running(agent.Key), "an agent that cannot be resumed keeps running")
	report("claude", "saved-conversation", "PermissionRequest")
	stopIdle()
	assert.Empty(runtime.ListSessions(ws.ID), "an agent waiting on approval stops")
	assert.False(client.HasState(agent.Key))
	stored, err := database.ListAllWorkspaceRuntimeSessions(ctx)
	require.NoError(err)
	require.Len(stored, 2, "stopped rows stay")
	require.NotEmpty(activity.LiveReportsForWorkspace(worktree, []string{agent.Key}), "stopping must keep the saved conversation")

	// Nothing but a page view resumes: sockets, plain reads and tools only list.
	_, _, err = terminalwebsocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/ws/v1/workspaces/"+ws.ID+"/runtime/sessions/"+agent.Key+"/terminal?resize_active=1", nil, nil) //nolint:bodyclose // a refused upgrade has no body to drain
	require.Error(err, "a pane attaching to a stopped agent finds nothing to attach to")
	listed, err := handler.GetWorkspaceRuntimeService(ctx, ws.ID, false)
	require.NoError(err)
	assert.Empty(runtime.ListSessions(ws.ID), "only a page view resumes stopped runtimes")
	require.Len(listed.Sessions, 2)
	for _, session := range listed.Sessions {
		assert.Equal(localruntime.SessionStatusParked, session.Status, "readers see a stopped runtime as parked, not failed")
	}

	runtime.Shutdown()
	runtime, handler = daemon(time.Hour)
	require.NoError(handler.RestoreRuntimeSessions(ctx))
	assert.Empty(resumedWith(), "a daemon restart must not relaunch a stopped agent")
	assert.False(client.HasState(shell.Key), "a daemon restart must not relaunch a stopped shell")
	require.NoError(handler.restoreRuntimeSessions(ctx, true))
	assert.Empty(resumedWith(), "recovery retries must not relaunch a stopped agent")

	view(true)
	assert.True(running(agent.Key))
	assert.True(running(shell.Key), "a stopped shell starts fresh under its key")
	require.Eventually(func() bool { return resumedWith() == "--resume\nsaved-conversation" }, 5*time.Second, 10*time.Millisecond, "the agent resumes its saved conversation")
	continued(agent.Key, "saved-conversation")
	advance(2 * time.Hour)
	stopIdle()
	assert.False(running(agent.Key), "a resumed conversation stops again before it is prompted")

	// A resume that fails gives up the stop and hands the agent to recovery.
	stored, err = database.ListAllWorkspaceRuntimeSessions(ctx)
	require.NoError(err)
	i := slices.IndexFunc(stored, func(row db.WorkspaceRuntimeSession) bool { return row.SessionKey == agent.Key })
	require.GreaterOrEqual(i, 0)
	removedTarget := stored[i]
	removedTarget.TargetKey = "removed"
	require.NoError(database.UpsertWorkspaceRuntimeSession(ctx, &removedTarget))
	listed, err = handler.GetWorkspaceRuntimeService(ctx, ws.ID, true)
	require.NoError(err)
	assert.False(running(agent.Key))
	assert.False(runtime.StoppedMark(agent.Key), "a failed resume clears the stop mark")
	for _, session := range listed.Sessions {
		if session.Key == agent.Key {
			assert.Equal(localruntime.SessionStatusError, session.Status)
		}
	}
	require.NoError(os.Remove(filepath.Join(worktree, "args")))
	require.NoError(handler.restoreRuntimeSessions(ctx, true))
	assert.False(running(agent.Key), "recovery can't resume while the target is missing")
	removedTarget.TargetKey = "worker"
	require.NoError(database.UpsertWorkspaceRuntimeSession(ctx, &removedTarget))
	require.NoError(handler.restoreRuntimeSessions(ctx, true))
	assert.True(running(agent.Key), "the maintenance pass resumes an agent whose resume failed")
	require.Eventually(func() bool { return resumedWith() == "--resume\nsaved-conversation" }, 5*time.Second, 10*time.Millisecond)

	// A page reload that cancels the viewing read doesn't fail the resume.
	continued(agent.Key, "saved-conversation")
	advance(2 * time.Hour)
	stopIdle()
	require.False(running(agent.Key))
	require.False(running(shell.Key))
	require.True(runtime.StoppedMark(shell.Key))
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	stored, err = database.ListAllWorkspaceRuntimeSessions(ctx)
	require.NoError(err)
	for _, row := range stored {
		require.NoError(handler.idle.Load().Resume(cancelled, ws, row))
	}
	assert.True(running(shell.Key), "a cancelled request still resumes a stopped shell")
	assert.False(runtime.StoppedMark(shell.Key))
	assert.True(running(agent.Key), "a cancelled request still resumes a stopped agent")
	assert.False(runtime.StoppedMark(agent.Key))

	// An agent recovery resumes after its owner died with the machine stops
	// again while idle in that conversation.
	crashed, err := runtime.Launch(ctx, ws.ID, worktree, "worker")
	require.NoError(err)
	require.NoError(handler.recordRuntimeSession(ctx, ws.ID, crashed, "session"))
	require.NoError(activity.HandleEvent("claude", agentactivity.HookEvent{SessionID: "crashed-conversation", CWD: worktree, HookEventName: "Stop"}, crashed.Key))
	runtime.Shutdown()
	require.NoError(client.Stop(ctx, crashed.Key))
	runtime, handler = daemon(time.Hour)
	require.NoError(handler.RestoreRuntimeSessions(ctx))
	require.Eventually(func() bool { return resumedWith() == "--resume\ncrashed-conversation" }, 5*time.Second, 10*time.Millisecond, "recovery resumes the saved conversation")
	require.True(running(crashed.Key))
	continued(crashed.Key, "crashed-conversation")
	stopIdle() // the restarted daemon's first pass starts the clock
	advance(2 * time.Hour)
	stopIdle()
	assert.False(running(crashed.Key), "a recovered conversation stops again before it is prompted")
	assert.True(runtime.StoppedMark(crashed.Key))
	stored, err = database.ListAllWorkspaceRuntimeSessions(ctx)
	require.NoError(err)
	require.NotEmpty(stored)
	require.Empty(runtime.ListSessions(ws.ID), "parked runtimes have no live manager entry")
	for _, row := range stored {
		require.True(runtime.StoppedMark(row.SessionKey))
	}
	require.NotEmpty(activity.LiveReportsForWorkspace(worktree, []string{crashed.Key}))
	_, err = handler.DeleteWorkspace(ctx, &DeleteWorkspaceInput{ID: ws.ID, Force: true})
	require.NoError(err)
	for _, row := range stored {
		assert.False(runtime.StoppedMark(row.SessionKey), "deletion removes every stored runtime's stop mark")
		assert.Empty(activity.LiveReportsForWorkspace(worktree, []string{row.SessionKey}), "deletion removes every stored runtime's activity")
	}
}

func TestIdleRuntimeStopKeepsIdleTimeAcrossRestart(t *testing.T) { //nolint:paralleltest // t.Setenv writes the helper switch
	require := require.New(t)
	assert := assert.New(t)
	t.Setenv("KENN_FORGE_AGENT_SESSION_HELPER", "1")
	ctx := t.Context()
	database := dbtest.Open(t)
	worktree := t.TempDir()
	ws := &db.Workspace{
		ID: "ws-idle-restart", Platform: "github", PlatformHost: "github.com", RepoOwner: "acme", RepoName: "widgets",
		ItemType: db.WorkspaceItemTypeAdHoc, ItemKey: db.AdHocWorkspaceItemKey("work/idle-restart"), GitHeadRef: "work/idle-restart",
		WorkspaceBranch: "work/idle-restart", WorktreePath: worktree, Status: "ready", TerminalBackend: workspace.TerminalBackendPtyOwner,
	}
	require.NoError(database.InsertWorkspace(ctx, ws))
	shellCommand := []string{os.Args[0], "-test.run=^TestWorkspaceAgentSessionHelper$", "--", "sleep"}
	client := &ptyowner.Client{Root: filepath.Join(t.TempDir(), "pty-owner"), InProcess: true, Command: shellCommand}
	workspaces := workspace.NewManager(database, t.TempDir())
	workspaces.SetPtyOwnerClient(client)
	stateDir := t.TempDir()
	parkedDir := filepath.Join(stateDir, "parked-runtimes")
	activityFile := filepath.Join(stateDir, "idle-activity.json")
	// The workspace and its runtime are days old when idle stop first runs.
	var now atomic.Int64
	now.Store(time.Now().Add(240 * time.Hour).UnixNano())
	clock := func() time.Time { return time.Unix(0, now.Load()).UTC() }
	advance := func(d time.Duration) { now.Add(int64(d)) }
	// Each daemon runs the idle loop and stops through Handler.Shutdown, as the server does.
	daemon := func(stopAfter time.Duration) (*localruntime.Manager, *Handler) {
		runtime, handler := newIdleRuntimeDaemon(t, ws.ID, Deps{DB: database, Workspaces: workspaces, Now: clock, Config: ConfigSnapshot{IdleRuntimeStopAfter: stopAfter}}, localruntime.Options{
			Targets:         []localruntime.LaunchTarget{{Key: "plain_shell", Kind: localruntime.LaunchTargetPlainShell, Available: true, Command: shellCommand}},
			PtyOwnerRuntime: ptyownerruntime.New(client, nil),
			ParkedDir:       parkedDir,
		})
		require.True(handler.runBackground(handler.runIdle))
		return runtime, handler
	}
	runtime, handler := daemon(time.Hour)
	noState := func(msg string) {
		assert.NoDirExists(parkedDir, msg)
		assert.NoFileExists(activityFile, msg)
	}
	pending := func(key string) bool {
		handler.runtimeRecoveryMu.Lock()
		defer handler.runtimeRecoveryMu.Unlock()
		return handler.runtimeRecoveryPending[key]
	}
	// Starting with the setting off hands an agent idle stop stopped to
	// recovery and removes idle state.
	stopped := db.WorkspaceRuntimeSession{WorkspaceID: ws.ID, SessionKey: "stopped-agent", TargetKey: "worker", Kind: string(localruntime.LaunchTargetAgent), Scope: "session"}
	require.NoError(database.UpsertWorkspaceRuntimeSession(ctx, &stopped))
	require.NoError(runtime.Park(ctx, ws.ID, stopped.SessionKey))
	require.True(runtime.StoppedMark(stopped.SessionKey))
	require.NoError(handler.Shutdown(ctx))
	runtime.Shutdown()
	runtime, handler = daemon(0)
	require.NoError(handler.RestoreRuntimeSessions(ctx))
	noState("startup with the setting off removes idle state")
	assert.Nil(handler.idle.Load())
	assert.True(pending(stopped.SessionKey), "a stopped agent goes back to recovery at startup")

	shell, err := runtime.Launch(ctx, ws.ID, worktree, "plain_shell")
	require.NoError(err)
	require.NoError(handler.recordRuntimeSession(ctx, ws.ID, shell, "session"))
	view := func() {
		_, err := handler.getWorkspaceRuntime(ctx, &getWorkspaceRuntimeInput{ID: ws.ID, Viewing: true})
		require.NoError(err)
	}
	restartWith := func(stopAfter time.Duration) {
		require.NoError(handler.Shutdown(ctx))
		runtime.Shutdown()
		runtime, handler = daemon(stopAfter)
		require.NoError(handler.RestoreRuntimeSessions(ctx))
	}
	restart := func() { restartWith(time.Hour) }
	stopIdle := func() { handler.idle.Load().stopIdle(ctx, time.Hour) }

	attached, err := runtime.AttachSession(ws.ID, shell.Key)
	require.NoError(err)
	require.NoError(attached.Write([]byte("y")))
	attached.Close()
	_, err = handler.getWorkspaceRuntime(ctx, &getWorkspaceRuntimeInput{ID: ws.ID, Viewing: true})
	require.NoError(err)
	assert.Nil(handler.syncIdle(ctx))
	noState("typing and page views with the setting off record nothing")

	restart()
	stopIdle()
	assert.True(client.HasState(shell.Key), "idle time starts when the pass first sees a workspace")
	restart()
	advance(40 * time.Minute)
	stopIdle()
	assert.True(client.HasState(shell.Key))
	restart()
	advance(40 * time.Minute)
	stopIdle()
	assert.False(client.HasState(shell.Key), "restarts with no use don't reset idle time")

	view()
	require.True(client.HasState(shell.Key))
	advance(59 * time.Minute)
	view()
	restart()
	advance(2 * time.Minute)
	stopIdle()
	assert.True(client.HasState(shell.Key), "a view just before a restart pushes the stop out")
	advance(time.Hour)
	stopIdle()
	assert.False(client.HasState(shell.Key))

	handler.idle.Load().save()
	require.FileExists(activityFile)
	// Turning it off hands a stopped agent to recovery and removes idle state.
	handler.setRuntimeRecoveryPending(stopped.SessionKey, false)
	require.NoError(runtime.Park(ctx, ws.ID, stopped.SessionKey))
	require.True(runtime.StoppedMark(stopped.SessionKey))
	handler.ApplyConfig(ConfigSnapshot{})
	assert.Nil(handler.syncIdle(ctx))
	noState("turning the setting off removes idle state")
	assert.True(pending(stopped.SessionKey), "a stopped agent goes back to recovery")
}

// acpParkPeer is an ACP owner that counts park requests.
type acpParkPeer struct {
	acpReconnectPeer
	parks atomic.Int32
}

func (p *acpParkPeer) Park(_ struct{}, _ *struct{}) error {
	p.parks.Add(1)
	return nil
}

func TestIdleRuntimeStopParksACPChatAndResumesOnView(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)
	cwd := t.TempDir()
	ws := &db.Workspace{
		ID: "workspace", Platform: "github", PlatformHost: "github.com", RepoOwner: "acme", RepoName: "widget",
		ItemType: db.WorkspaceItemTypeAdHoc, ItemKey: db.AdHocWorkspaceItemKey("work/chat"), GitHeadRef: "work/chat",
		WorkspaceBranch: "work/chat", WorktreePath: cwd, Status: "ready", TerminalBackend: workspace.TerminalBackendPtyOwner,
	}
	require.NoError(database.InsertWorkspace(ctx, ws))
	const key = "idle-chat"
	require.NoError(database.UpsertWorkspaceRuntimeSession(ctx, &db.WorkspaceRuntimeSession{WorkspaceID: ws.ID, SessionKey: key, TargetKey: "chat", Kind: "acp", Scope: "session", DisplayRegion: "workflow"}))
	root := t.TempDir()
	paths, err := ptyowner.NewSessionPaths(root, key)
	require.NoError(err)
	require.NoError(os.MkdirAll(paths.Dir, 0o700))
	require.NoError(os.MkdirAll(filepath.Dir(paths.Socket), 0o700))
	if paths.SocketDir != "" {
		t.Cleanup(func() { _ = os.RemoveAll(paths.SocketDir) })
	}
	require.NoError(os.WriteFile(filepath.Join(paths.Dir, "session.json"), []byte(`{"SessionID":"saved","LoadSession":true}`), 0o600))
	peer := &acpParkPeer{done: ctx.Done()}
	server := rpc.NewServer()
	require.NoError(server.RegisterName("ACP", peer))
	// listen serves the owner's socket until the returned func closes it.
	listen := func() func() {
		listener, err := (&net.ListenConfig{}).Listen(ctx, "unix", paths.Socket)
		require.NoError(err)
		accepted := make(chan struct{})
		go func() {
			defer close(accepted)
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				go server.ServeConn(conn)
			}
		}()
		return func() { _ = listener.Close(); <-accepted }
	}
	closeOwner := listen()
	defer func() { closeOwner() }()
	// No pty-owner state exists for the chat, so its owner reads as gone once detached.
	client := &ptyowner.Client{Root: filepath.Join(t.TempDir(), "pty-owner"), InProcess: true}
	runtime := localruntime.NewManager(localruntime.Options{
		ACPSessionsDir: root, ParkedDir: t.TempDir(), PtyOwnerRuntime: ptyownerruntime.New(client, nil),
	})
	defer runtime.Shutdown()
	activity := agentactivity.NewStore(t.TempDir())
	require.NoError(activity.Record(localruntime.ACPActivityAgent, "saved", key, cwd, agentactivity.StateIdle))
	require.NoError(activity.HandleEvent("claude", agentactivity.HookEvent{SessionID: "stale-hook", CWD: cwd, HookEventName: "UserPromptSubmit"}, key))
	var now atomic.Int64
	now.Store(time.Now().UnixNano())
	clock := func() time.Time { return time.Unix(0, now.Load()).UTC() }
	workspaces := workspace.NewManager(database, t.TempDir())
	workspaces.SetPtyOwnerClient(client)
	handler := New(Deps{DB: database, Workspaces: workspaces, Runtime: runtime, AgentActivity: activity, Now: clock, Config: ConfigSnapshot{IdleRuntimeStopAfter: time.Hour}})
	handler.syncIdle(ctx)
	read := func(viewing bool) localruntime.SessionStatus {
		result, err := handler.GetWorkspaceRuntimeService(ctx, ws.ID, viewing)
		require.NoError(err)
		require.Len(result.Sessions, 1)
		return result.Sessions[0].Status
	}
	stopIdle := func() {
		now.Add(int64(2 * time.Hour))
		handler.idle.Load().stopIdle(ctx, time.Hour)
	}

	assert.Equal(localruntime.SessionStatusRunning, read(true))
	require.Equal(int32(1), peer.bindings.Load())
	stopIdle()
	assert.Equal(int32(1), peer.parks.Load(), "an idle reloadable chat parks its owner despite a stale working hook report")
	assert.Equal(localruntime.SessionStatusParked, read(false), "only a page view resumes a stopped chat")
	assert.Equal(int32(1), peer.bindings.Load())
	assert.Equal(localruntime.SessionStatusRunning, read(true))
	assert.Equal(int32(2), peer.bindings.Load(), "a page view reloads the chat")
	assert.False(runtime.StoppedMark(key))

	// A failed resume leaves the chat to the retry every read makes.
	closeOwner()
	stopIdle()
	require.True(runtime.StoppedMark(key))
	assert.NotEqual(localruntime.SessionStatusRunning, read(true))
	assert.False(runtime.StoppedMark(key), "a failed resume clears the stop mark")
	handler.runtimeRecoveryMu.Lock()
	assert.True(handler.runtimeRecoveryPending[key], "a failed resume hands the chat to recovery")
	handler.runtimeRecoveryMu.Unlock()
	closeOwner = listen()
	assert.Equal(localruntime.SessionStatusRunning, read(false), "a later read reloads the chat")
}
