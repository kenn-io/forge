package workspaceapi

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/rpc"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/agentactivity"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/ptyowner"
	"go.kenn.io/forge/internal/server/httpapi"
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
	<-p.done
	reply.Exited = true
	return nil
}

func TestACPReattachesOnWorkspaceOpenNotStartup(t *testing.T) {
	t.Parallel()
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

// acpPromptPeer rejects prompts the way an owner with a disconnected agent
// does over the RPC.
type acpPromptPeer struct {
	acpReconnectPeer
	disconnected atomic.Bool
	prompts      atomic.Int32
}

func (p *acpPromptPeer) Command(command localruntime.ACPCommand, reply *localruntime.ACPCommandReply) error {
	if command.Type != "prompt" {
		return errors.New("unexpected command")
	}
	if p.disconnected.Load() {
		reply.Unavailable = true
		return nil
	}
	p.prompts.Add(1)
	return nil
}

func TestACPRuntimeReportsSessionsAndReleasesUnwrittenPrompt(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)
	cwd := t.TempDir()
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{ID: "workspace", Platform: "github", PlatformHost: "github.com", RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc, ItemKey: db.AdHocWorkspaceItemKey("work/chat"), GitHeadRef: "work/chat", WorkspaceBranch: "work/chat", WorktreePath: cwd, Status: "ready"}))
	require.NoError(database.UpsertWorkspaceRuntimeSession(ctx, &db.WorkspaceRuntimeSession{WorkspaceID: "workspace", SessionKey: "chat-runtime", TargetKey: "chat", Kind: "acp", Scope: "session", DisplayRegion: "workflow"}))
	root := t.TempDir()
	paths, err := ptyowner.NewSessionPaths(root, "chat-runtime")
	require.NoError(err)
	require.NoError(os.MkdirAll(filepath.Dir(paths.Socket), 0o700))
	if paths.SocketDir != "" {
		t.Cleanup(func() { _ = os.RemoveAll(paths.SocketDir) })
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "unix", paths.Socket)
	require.NoError(err)
	defer listener.Close()
	peer := &acpPromptPeer{}
	peer.done = ctx.Done()
	peer.disconnected.Store(true)
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
	activity := agentactivity.NewStore(t.TempDir())
	handler := New(Deps{DB: database, Workspaces: workspace.NewManager(database, t.TempDir()), Runtime: runtime, AgentActivity: activity})
	_, err = handler.GetWorkspaceRuntimeService(ctx, "workspace")
	require.NoError(err)
	require.NoError(activity.Record(localruntime.ACPActivityAgent, "acp-session", "chat-runtime", cwd, agentactivity.StateWorking))
	// A hook identity is not evidence for an ACP runtime.
	require.NoError(activity.HandleEvent("claude", agentactivity.HookEvent{SessionID: "spoofed", CWD: cwd, HookEventName: "UserPromptSubmit"}, "chat-runtime"))

	sessions, err := handler.ListWorkspaceAgentSessionsService(ctx, "workspace")
	require.NoError(err)
	require.Len(sessions, 1)
	assert.Equal("acp", sessions[0].Agent)
	assert.Equal("acp-session", sessions[0].SessionID)
	assert.Equal("chat", sessions[0].TargetKey)
	assert.Equal(agentactivity.StateWorking, sessions[0].State)

	request := InitialMessageRequest{WorkspaceID: "workspace", RuntimeSessionKey: "chat-runtime", TargetKey: "chat", Message: "start"}
	_, err = handler.SubmitInitialMessageService(ctx, request)
	problem, ok := errors.AsType[*httpapi.ProblemError](err)
	require.True(ok, "a rejected prompt must be a proven no-write conflict, got %v", err)
	assert.Equal(http.StatusConflict, problem.Status)
	assert.Zero(peer.prompts.Load())
	peer.disconnected.Store(false)
	status, err := handler.SubmitInitialMessageService(ctx, request)
	require.NoError(err)
	assert.Equal(initialMessageDelivered, status.State)
	assert.Equal(int32(1), peer.prompts.Load())

	// ACP prompts are protocol messages, not pasted terminal input: no size
	// limit and no control-character refusal.
	large := "review\tthis " + strings.Repeat("a", 256<<10)
	result, err := handler.SubmitAgentMessageService(ctx, "workspace", "chat-runtime", large)
	require.NoError(err)
	assert.Equal(len(large), result.MessageBytes)
	assert.Equal(int32(2), peer.prompts.Load())
}

// stallingChat is an ACP chat whose prompts wait on the agent until released.
type stallingChat struct {
	release   chan struct{}
	cancelled chan struct{}
	done      chan struct{}
}

type imageReferenceChat struct {
	stallingChat
	snapshot []byte
	history  []byte
}

func (c *imageReferenceChat) Snapshot() ([]byte, error)        { return c.snapshot, nil }
func (c *imageReferenceChat) History(int, int) ([]byte, error) { return c.history, nil }
func (c *imageReferenceChat) Subscribe() (<-chan struct{}, func()) {
	changes := make(chan struct{}, 1)
	changes <- struct{}{}
	return changes, func() {}
}

func TestACPChatServesReferencedImagesOnConnectAndReload(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	root := t.TempDir()
	image := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="320" height="120"><rect width="320" height="120" fill="blue"/></svg>`)
	path := filepath.Join(root, "screenshot image.svg")
	require.NoError(os.WriteFile(path, image, 0o600))
	input, err := json.Marshal(map[string]string{"path": path})
	require.NoError(err)
	message := localruntime.ACPMessage{Role: "tool", Text: "View Image " + path, Kind: "read", RawInput: string(input), Locations: []localruntime.ACPToolLocation{{Path: path}}, ToolContent: []localruntime.ACPToolContent{{Type: "content", Content: &localruntime.ACPContent{Type: "resource_link", URI: path, Name: "screenshot.svg"}}}}
	snapshot, err := json.Marshal(localruntime.ACPState{Messages: []localruntime.ACPMessage{message}, Connected: true})
	require.NoError(err)
	message.ToolContent[0].Content = &localruntime.ACPContent{Type: "resource_link", URI: (&url.URL{Scheme: "file", Path: path}).String(), Name: "screenshot.svg"}
	history, err := json.Marshal(map[string]localruntime.ACPHistory{"history": {Offset: 3, Messages: []localruntime.ACPMessage{message}}})
	require.NoError(err)
	chat := &imageReferenceChat{snapshot: snapshot, history: history}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serveACP(w, r, chat) }))
	defer server.Close()
	for range 2 {
		conn, _, dialErr := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http"), nil)
		require.NoError(dialErr)
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		_, data, readErr := conn.Read(ctx)
		require.NoError(readErr)
		var state localruntime.ACPState
		require.NoError(json.Unmarshal(data, &state))
		require.Len(state.Messages, 1)
		content := state.Messages[0].ToolContent[0].Content
		assert.Equal("image", content.Type)
		assert.Equal("image/svg+xml", content.MimeType)
		assert.Equal(base64.StdEncoding.EncodeToString(image), content.Data)
		assert.Equal(path, content.URI)
		assert.True(state.Connected)
		require.NoError(conn.Write(ctx, websocket.MessageText, []byte(`{"type":"history","before":4,"limit":1}`)))
		_, data, readErr = conn.Read(ctx)
		require.NoError(readErr)
		var page map[string]localruntime.ACPHistory
		require.NoError(json.Unmarshal(data, &page))
		assert.Equal(3, page["history"].Offset)
		require.Len(page["history"].Messages, 1)
		assert.Equal(base64.StdEncoding.EncodeToString(image), page["history"].Messages[0].ToolContent[0].Content.Data)
		cancel()
		require.NoError(conn.CloseNow())
	}
}

func (c *stallingChat) Snapshot() ([]byte, error)        { return []byte(`{}`), nil }
func (c *stallingChat) History(int, int) ([]byte, error) { return []byte(`{}`), nil }
func (c *stallingChat) Subscribe() (<-chan struct{}, func()) {
	return make(chan struct{}), func() {}
}

func (c *stallingChat) Command(command localruntime.ACPCommand) error {
	switch command.Type {
	case "prompt":
		<-c.release
	case "cancel":
		close(c.cancelled)
	}
	return nil
}
func (c *stallingChat) Prompt(string) error        { return nil }
func (c *stallingChat) Stop(context.Context) error { return nil }
func (c *stallingChat) Detach()                    {}
func (c *stallingChat) Done() <-chan struct{}      { return c.done }
func (c *stallingChat) ExitCode() int              { return 0 }

// A prompt waiting on the agent must not keep the chat from reading a stop.
func TestACPChatReadsStopWhileAPromptStalls(t *testing.T) {
	t.Parallel()
	chat := &stallingChat{release: make(chan struct{}), cancelled: make(chan struct{}), done: make(chan struct{})}
	defer close(chat.release)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serveACP(w, r, chat) }))
	defer server.Close()
	conn, _, err := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.CloseNow()

	require.NoError(t, conn.Write(t.Context(), websocket.MessageText, []byte(`{"type":"prompt","text":"stall"}`)))
	// However many prompts wait behind it, the reader still takes the stop.
	for range 100 {
		require.NoError(t, conn.Write(t.Context(), websocket.MessageText, []byte(`{"type":"prompt","text":"behind"}`)))
	}
	require.NoError(t, conn.Write(t.Context(), websocket.MessageText, []byte(`{"type":"cancel"}`)))
	select {
	case <-chat.cancelled:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "stop waited behind a stalled prompt")
	}
}

// historyChat records history requests and answers them with a page frame.
type historyChat struct {
	stallingChat
	requests chan [2]int
}

func (c *historyChat) History(before, limit int) ([]byte, error) {
	c.requests <- [2]int{before, limit}
	return []byte(`{"history":{"offset":3,"messages":[]}}`), nil
}

// Earlier transcript pages are answered on the connection that asked.
func TestACPChatAnswersHistoryRequests(t *testing.T) {
	t.Parallel()
	chat := &historyChat{stallingChat{release: make(chan struct{}), cancelled: make(chan struct{}), done: make(chan struct{})}, make(chan [2]int, 1)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serveACP(w, r, chat) }))
	defer server.Close()
	conn, _, err := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.CloseNow()

	require.NoError(t, conn.Write(t.Context(), websocket.MessageText, []byte(`{"type":"history","before":13,"limit":10}`)))
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	require.NoError(t, err)
	assert.JSONEq(t, `{"history":{"offset":3,"messages":[]}}`, string(data))
	assert.Equal(t, [2]int{13, 10}, <-chat.requests)
}

// failingChat refuses every command.
type failingChat struct{ stallingChat }

func (c *failingChat) Command(localruntime.ACPCommand) error { return errors.New("refused") }

// A failed command names itself so the client settles only that request.
func TestACPChatErrorsNameTheFailedCommand(t *testing.T) {
	t.Parallel()
	chat := &failingChat{stallingChat{release: make(chan struct{}), cancelled: make(chan struct{}), done: make(chan struct{})}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serveACP(w, r, chat) }))
	defer server.Close()
	conn, _, err := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.CloseNow()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte(`{"type":"prompt","text":"hello","id":"submission-1"}`)))
	_, data, err := conn.Read(ctx)
	require.NoError(t, err)
	assert.JSONEq(t, `{"commandError":"refused","command":"prompt","id":"submission-1"}`, string(data))

	require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte(`{"type":"cancel"}`)))
	_, data, err = conn.Read(ctx)
	require.NoError(t, err)
	assert.JSONEq(t, `{"commandError":"refused","command":"cancel","id":""}`, string(data))
}

// acceptingChat takes every command.
type acceptingChat struct{ stallingChat }

func (c *acceptingChat) Command(localruntime.ACPCommand) error { return nil }

// An accepted prompt is acknowledged to its sender, so a retried submission
// the host already has, which changes no state, still settles.
func TestACPChatAcknowledgesAcceptedPrompts(t *testing.T) {
	t.Parallel()
	chat := &acceptingChat{stallingChat{release: make(chan struct{}), cancelled: make(chan struct{}), done: make(chan struct{})}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serveACP(w, r, chat) }))
	defer server.Close()
	conn, _, err := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.CloseNow()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte(`{"type":"cancel"}`)))
	require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte(`{"type":"prompt","text":"hello","id":"submission-1"}`)))
	_, data, err := conn.Read(ctx)
	require.NoError(t, err)
	assert.JSONEq(t, `{"accepted":{"command":"prompt","id":"submission-1"}}`, string(data), "only the prompt is acknowledged")
}
