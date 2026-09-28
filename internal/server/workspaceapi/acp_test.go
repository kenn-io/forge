package workspaceapi

import (
	"net"
	"net/rpc"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/ptyowner"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/workspace"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

// The handler test treats the already-running owner as its RPC boundary. SDK
// and process survival are covered by localruntime's executable fixtures.
type acpReconnectPeer struct {
	bindings atomic.Int32
	done     <-chan struct{}
}

func (p *acpReconnectPeer) Bind(_ localruntime.ACPMCPBinding, _ *struct{}) error {
	p.bindings.Add(1)
	return nil
}

func (p *acpReconnectPeer) Watch(_ localruntime.ACPWatch, reply *localruntime.ACPUpdate) error {
	select {
	case <-p.done:
		reply.Exited = true
	case <-time.After(20 * time.Millisecond):
	}
	return nil
}

func TestACPReattachesOnWorkspaceOpenNotStartup(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)
	cwd := t.TempDir()
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{ID: "workspace", Platform: "github", PlatformHost: "github.com", RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc, ItemKey: db.AdHocWorkspaceItemKey("work/chat"), GitHeadRef: "work/chat", WorkspaceBranch: "work/chat", WorktreePath: cwd, Status: "ready"}))
	require.NoError(database.UpsertWorkspaceRuntimeSession(ctx, &db.WorkspaceRuntimeSession{WorkspaceID: "workspace", SessionKey: "saved-chat", TargetKey: "chat", Kind: "acp", Scope: "session", DisplayRegion: "workflow"}))
	root := t.TempDir()
	paths, err := ptyowner.NewSessionPaths(root, "saved-chat")
	require.NoError(err)
	require.NoError(os.MkdirAll(filepath.Dir(paths.Socket), 0o700))
	if paths.SocketDir != "" {
		t.Cleanup(func() { _ = os.RemoveAll(paths.SocketDir) })
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "unix", paths.Socket)
	require.NoError(err)
	defer listener.Close()
	peer := &acpReconnectPeer{done: ctx.Done()}
	server := rpc.NewServer()
	require.NoError(server.RegisterName("ACP", peer))
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
	defer func() { _ = listener.Close(); <-accepted }()
	runtime := localruntime.NewManager(localruntime.Options{ACPSessionsDir: root})
	defer runtime.Shutdown()
	workspaces := workspace.NewManager(database, t.TempDir())
	handler := New(Deps{DB: database, Workspaces: workspaces, Runtime: runtime})
	require.NoError(handler.RestoreRuntimeSessions(ctx))
	require.NoError(handler.restoreRuntimeSessions(ctx, true))
	assert.Zero(peer.bindings.Load())
	assert.Empty(runtime.ListSessions("workspace"))
	stored, err := database.ListWorkspaceRuntimeSessions(ctx, "workspace")
	require.NoError(err)
	require.Len(stored, 1)
	result, err := handler.GetWorkspaceRuntimeService(ctx, "workspace")
	require.NoError(err)
	require.Len(result.Sessions, 1)
	assert.Equal(localruntime.SessionStatusRunning, result.Sessions[0].Status)
	assert.Equal(int32(1), peer.bindings.Load())
	_, err = handler.GetWorkspaceRuntimeService(ctx, "workspace")
	require.NoError(err)
	assert.Equal(int32(1), peer.bindings.Load(), "reopening must reuse the attachment")
}
