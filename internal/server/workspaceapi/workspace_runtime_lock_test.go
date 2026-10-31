package workspaceapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/ptyowner"
	ptyownerruntime "go.kenn.io/forge/internal/ptyowner/runtime"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/workspacetest"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

// A chat reconnect that hangs in one workspace holds up only that workspace:
// another workspace's runtime read still reconnects its own chat and answers,
// while a stop in the hung workspace waits for the reconnect to finish.
func TestSlowChatReconnectHoldsUpOnlyItsWorkspace(t *testing.T) {
	t.Parallel()
	f := newChatFixture(t, localruntime.Options{})
	addChatRecord(t, f.database, f.root, "other", "other-chat")
	require.NoError(t, f.database.UpsertWorkspaceRuntimeSession(t.Context(), &db.WorkspaceRuntimeSession{
		WorkspaceID: "workspace", SessionKey: "stored-shell", TargetKey: "plain_shell", Label: "Shell",
		Kind: "plain_shell", Scope: "session", DisplayRegion: "terminal", CreatedAt: chatCreatedAt,
	}))
	f.save(false)
	f.saveKeyState("other-chat", `{}`)
	entered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	slow := newChatOwner("generation-1")
	slow.bind = func() {
		enterOnce.Do(func() { close(entered) })
		<-release
	}
	f.serve(slow)
	f.serveKey("other-chat", newChatOwner("generation-1"))
	releaseReconnect := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseReconnect)

	slowRead := make(chan int, 1)
	go func() {
		status, _ := f.doPath(http.MethodGet, "/workspaces/workspace/runtime", "")
		slowRead <- status
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "the workspace read never reached its chat reconnect")
	}

	otherRead := make(chan []byte, 1)
	go func() {
		status, data := f.doPath(http.MethodGet, "/workspaces/other/runtime", "")
		assert.Equal(t, http.StatusOK, status, string(data))
		otherRead <- data
	}()
	select {
	case data := <-otherRead:
		sessions := decodeChat[workspaceRuntimeResponse](t, data).Sessions
		i := slices.IndexFunc(sessions, func(s localruntime.SessionInfo) bool { return s.Key == "other-chat" })
		require.GreaterOrEqual(t, i, 0, string(data))
		assert.Equal(t, localruntime.SessionStatusRunning, sessions[i].Status, "the other workspace reconnected its chat")
	case <-time.After(5 * time.Second):
		releaseReconnect()
		require.FailNow(t, "another workspace's runtime read waited for the hung reconnect")
	}

	stopped := make(chan int, 1)
	go func() {
		status, _ := f.doPath(http.MethodDelete, "/workspaces/workspace/runtime/sessions/stored-shell", "")
		stopped <- status
	}()
	select {
	case status := <-stopped:
		require.FailNow(t, "a stop ran during its workspace's chat reconnect", "status %d", status)
	case <-time.After(200 * time.Millisecond): //nolint:kennlint // deadlinetest: shows the event does not happen; the hung reconnect holds its workspace's runtime lock until releaseReconnect
	}
	releaseReconnect()
	for name, answered := range map[string]chan int{"read": slowRead, "stop": stopped} {
		select {
		case status := <-answered:
			assert.Less(t, status, 300, name)
		case <-time.After(5 * time.Second):
			require.FailNow(t, name+" did not finish after the reconnect")
		}
	}
	stored, err := f.database.ListWorkspaceRuntimeSessions(t.Context(), "workspace")
	require.NoError(t, err)
	assert.False(t, slices.ContainsFunc(stored, func(row db.WorkspaceRuntimeSession) bool { return row.SessionKey == "stored-shell" }),
		"the stop forgot its row")
}

// Recovery lists every stored runtime before it takes each workspace's lock.
// A runtime stopped after the listing stays stopped, while the rest resume.
func TestRecoveryLeavesARuntimeStoppedAfterItsListingStopped(t *testing.T) { //nolint:paralleltest // t.Setenv writes KENN_FORGE_RESUME_AGENT_HELPER
	t.Setenv("KENN_FORGE_RESUME_AGENT_HELPER", "1")
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)
	cwd := t.TempDir()
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID: "workspace", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc,
		ItemKey: db.AdHocWorkspaceItemKey("work/resume"), GitHeadRef: "work/resume",
		WorkspaceBranch: "work/resume", WorktreePath: cwd, Status: "ready",
	}))
	for _, key := range []string{"kept-runtime", "stopped-runtime"} {
		require.NoError(database.UpsertWorkspaceRuntimeSession(ctx, &db.WorkspaceRuntimeSession{
			WorkspaceID: "workspace", SessionKey: key, TargetKey: "custom-worker",
			Label: "Worker", Kind: "agent", Scope: "session",
		}))
		_, err := database.RecordWorkspaceRuntimeConversation(ctx, key, "claude", "conversation-"+key)
		require.NoError(err)
	}
	runtime := localruntime.NewManager(localruntime.Options{
		PtyOwnerRuntime: ptyownerruntime.New(&ptyowner.Client{Root: t.TempDir(), InProcess: true}, nil),
		Targets: []localruntime.LaunchTarget{{Key: "custom-worker", Kind: localruntime.LaunchTargetAgent, Available: true, Command: []string{
			os.Args[0], "-test.run=^TestResumeAgentHelper$", "--",
		}}},
	})
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		runtime.StopWorkspace(cleanupCtx, "workspace")
		runtime.Shutdown()
	})
	handler := New(Deps{DB: database, Workspaces: workspacetest.NewManager(t, database, t.TempDir()), Runtime: runtime})
	listed, err := database.ListAllWorkspaceRuntimeSessions(ctx)
	require.NoError(err)
	require.Len(listed, 2)

	_, err = handler.stopWorkspaceRuntimeSession(ctx, &stopWorkspaceRuntimeSessionInput{ID: "workspace", SessionKey: "stopped-runtime"})
	require.NoError(err)
	for _, row := range listed {
		require.NoError(handler.restoreStoredRuntimeSession(ctx, row, false))
	}

	require.Eventually(func() bool {
		_, err := os.Stat(filepath.Join(cwd, "args-kept-runtime"))
		return err == nil
	}, 10*time.Second, 10*time.Millisecond, "the runtime nobody stopped resumed")
	keys := make([]string, 0, 1)
	for _, session := range runtime.ListSessions("workspace") {
		keys = append(keys, session.Key)
	}
	assert.Equal([]string{"kept-runtime"}, keys)
	assert.NoFileExists(filepath.Join(cwd, "args-stopped-runtime"))
	stored, err := database.ListAllWorkspaceRuntimeSessions(ctx)
	require.NoError(err)
	require.Len(stored, 1)
	assert.Equal("kept-runtime", stored[0].SessionKey)
}
