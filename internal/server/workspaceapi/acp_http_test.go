package workspaceapi

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/rpc"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/ptyowner"
	ptyownerruntime "go.kenn.io/forge/internal/ptyowner/runtime"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/workspace"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

// chatOwner stands in for a running ACP owner process at the RPC boundary.
// Agent behavior behind the owner is covered by localruntime's fixtures.
type chatOwner struct {
	exited chan struct{}
	once   sync.Once

	mu          sync.Mutex
	state       localruntime.ACPState
	pages       []localruntime.ACPPageRequest
	commands    []localruntime.ACPCommand
	code        string
	unavailable bool
	refusal     string
}

func newChatOwner(generation string, messages ...string) *chatOwner {
	owner := &chatOwner{exited: make(chan struct{}), state: localruntime.ACPState{RuntimeGeneration: generation, Connected: true}}
	for _, text := range messages {
		owner.state.Messages = append(owner.state.Messages, localruntime.ACPMessage{Role: "assistant", Text: text})
	}
	return owner
}

func (o *chatOwner) exit() { o.once.Do(func() { close(o.exited) }) }

func (o *chatOwner) Bind(localruntime.ACPMCPBinding, *struct{}) error { return nil }

func (o *chatOwner) Watch(_ localruntime.ACPWatch, reply *localruntime.ACPUpdate) error {
	<-o.exited
	reply.Exited = true
	return nil
}

func (o *chatOwner) Page(request localruntime.ACPPageRequest, reply *[]byte) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.pages = append(o.pages, request)
	state := o.state
	state.MessageCount = len(state.Messages)
	state.MessageOffset = min(request.After, len(state.Messages))
	state.Messages = state.Messages[state.MessageOffset:min(state.MessageOffset+request.Limit, len(state.Messages))]
	data, err := json.Marshal(state)
	*reply = data
	return err
}

func (o *chatOwner) Command(command localruntime.ACPCommand, reply *localruntime.ACPCommandReply) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.commands = append(o.commands, command)
	if o.refusal != "" {
		return errors.New(o.refusal)
	}
	reply.Unavailable = o.unavailable
	reply.Code = o.code
	return nil
}

func (o *chatOwner) received() ([]localruntime.ACPPageRequest, []localruntime.ACPCommand) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.pages), slices.Clone(o.commands)
}

const chatKey = "chat-runtime"

var chatCreatedAt = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

// chatFixture is a daemon with one stored chat whose owner state lives in
// root, served over HTTP the way execution workers and the daemon serve it.
type chatFixture struct {
	t        *testing.T
	database *db.DB
	root     string
	runtime  *localruntime.Manager
	handler  *Handler
	server   *httptest.Server
}

func newChatFixture(t *testing.T, options localruntime.Options) *chatFixture {
	t.Helper()
	require := require.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)
	cwd := t.TempDir()
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{ID: "workspace", Platform: "github", PlatformHost: "github.com", RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc, ItemKey: db.AdHocWorkspaceItemKey("work/chat"), GitHeadRef: "work/chat", WorkspaceBranch: "work/chat", WorktreePath: cwd, Status: "ready"}))
	require.NoError(database.UpsertWorkspaceRuntimeSession(ctx, &db.WorkspaceRuntimeSession{WorkspaceID: "workspace", SessionKey: chatKey, TargetKey: "chat", Label: "Coordinator task", Kind: "acp", Scope: "session", DisplayRegion: "workflow", CreatedAt: chatCreatedAt}))
	root := t.TempDir()
	paths, err := ptyowner.NewSessionPaths(root, chatKey)
	require.NoError(err)
	require.NoError(os.MkdirAll(paths.Dir, 0o700))
	require.NoError(os.MkdirAll(filepath.Dir(paths.Socket), 0o700))
	if paths.SocketDir != "" {
		t.Cleanup(func() { _ = os.RemoveAll(paths.SocketDir) })
	}
	config := fmt.Sprintf(`{"Root":%q,"Info":{"key":%q,"workspace_id":"workspace","target_key":"chat","kind":"acp"},"Command":["agent"],"CWD":%q}`, root, chatKey, cwd)
	require.NoError(os.WriteFile(filepath.Join(paths.Dir, "config.json"), []byte(config), 0o600))
	var handler *Handler
	options.ACPSessionsDir = root
	options.OnSessionExit = func(info localruntime.SessionInfo) { handler.HandleRuntimeSessionExit(info) }
	runtime := localruntime.NewManager(options)
	t.Cleanup(runtime.Shutdown)
	handler = New(Deps{DB: database, Workspaces: workspace.NewManager(database, t.TempDir()), Runtime: runtime})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		assert.NoError(t, handler.Shutdown(ctx))
	})
	mux := http.NewServeMux()
	handler.RegisterExecution(humago.New(mux, huma.DefaultConfig("test", "1")))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return &chatFixture{t: t, database: database, root: root, runtime: runtime, handler: handler, server: server}
}

// save writes the chat's saved session, supervised or not.
func (f *chatFixture) save(supervised bool) {
	f.t.Helper()
	state := `{}`
	if supervised {
		state = `{"supervision":{"supervisor":"coordinator","generation":1,"takenOver":false,"changedAt":"2026-10-09T12:00:00Z"}}`
	}
	paths, err := ptyowner.NewSessionPaths(f.root, chatKey)
	require.NoError(f.t, err)
	require.NoError(f.t, os.WriteFile(filepath.Join(paths.Dir, "session.json"), []byte(`{"SessionID":"native","State":`+state+`}`), 0o600))
}

// serve runs owner on the chat's socket until the owner exits.
func (f *chatFixture) serve(owner *chatOwner) {
	f.t.Helper()
	paths, err := ptyowner.NewSessionPaths(f.root, chatKey)
	require.NoError(f.t, err)
	listener, err := (&net.ListenConfig{}).Listen(f.t.Context(), "unix", paths.Socket)
	require.NoError(f.t, err)
	server := rpc.NewServer()
	require.NoError(f.t, server.RegisterName("ACP", owner))
	go func() {
		<-owner.exited
		_ = listener.Close()
	}()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go server.ServeConn(conn)
		}
	}()
	f.t.Cleanup(owner.exit)
}

// stop makes owner exit the way an agent exiting on its own does.
func (f *chatFixture) stop(owner *chatOwner) {
	f.t.Helper()
	owner.exit()
	require.Eventually(f.t, func() bool { return f.runtime.Exited(chatKey) }, 5*time.Second, 10*time.Millisecond)
}

func (f *chatFixture) do(method, path, body string) (int, []byte) {
	f.t.Helper()
	request, err := http.NewRequestWithContext(f.t.Context(), method, f.server.URL+"/workspaces/workspace/runtime/sessions/"+path, strings.NewReader(body))
	require.NoError(f.t, err)
	request.Header.Set("Content-Type", "application/json")
	response, err := f.server.Client().Do(request)
	require.NoError(f.t, err)
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	require.NoError(f.t, err)
	return response.StatusCode, data
}

// chatProblem is the part of a problem response a coordinator branches on.
type chatProblem struct {
	Status  int            `json:"status"`
	Code    string         `json:"code"`
	Details map[string]any `json:"details"`
}

func decodeChat[T any](t *testing.T, data []byte) T {
	t.Helper()
	var value T
	require.NoError(t, json.Unmarshal(data, &value), string(data))
	return value
}

// chatFields is a response body's fields other than its schema link.
func chatFields(t *testing.T, data []byte) map[string]any {
	t.Helper()
	fields := decodeChat[map[string]any](t, data)
	delete(fields, "$schema")
	return fields
}

// recoveryPending reports the flag that keeps the missing-backend prune away
// from a stored record.
func (f *chatFixture) recoveryPending() bool {
	f.handler.runtimeRecoveryMu.Lock()
	defer f.handler.runtimeRecoveryMu.Unlock()
	return f.handler.runtimeRecoveryPending[chatKey]
}

func (f *chatFixture) storedChats() []db.WorkspaceRuntimeSession {
	f.t.Helper()
	stored, err := f.database.ListWorkspaceRuntimeSessions(f.t.Context(), "workspace")
	require.NoError(f.t, err)
	return stored
}

// A coordinator reads every message after its cursor, each with its
// transcript index, along with the rest of the chat's state.
func TestWorkspaceChatPagesTheTranscriptAfterACursor(t *testing.T) {
	assert := assert.New(t)
	f := newChatFixture(t, localruntime.Options{})
	f.save(true)
	owner := newChatOwner("generation-1", "m0", "m1", "m2", "m3", "m4")
	f.serve(owner)

	status, data := f.do(http.MethodGet, chatKey+"/chat?after=2&limit=2", "")
	require.Equal(t, http.StatusOK, status, string(data))
	chat := decodeChat[WorkspaceChat](t, data)
	assert.False(chat.Exited)
	require.NotNil(t, chat.WorkspaceChatState)
	assert.Equal(5, chat.MessageCount)
	assert.Equal("generation-1", chat.RuntimeGeneration)
	assert.True(chat.Connected)
	require.Len(t, chat.Messages, 2)
	assert.Equal(2, chat.Messages[0].Index)
	assert.Equal("m2", chat.Messages[0].Text)
	assert.Equal(3, chat.Messages[1].Index)
	assert.Equal("m3", chat.Messages[1].Text)

	status, data = f.do(http.MethodGet, chatKey+"/chat", "")
	require.Equal(t, http.StatusOK, status, string(data))
	require.Len(t, decodeChat[WorkspaceChat](t, data).Messages, 5)
	pages, _ := owner.received()
	assert.Equal([]localruntime.ACPPageRequest{{After: 2, Limit: 2}, {After: 0, Limit: 100}}, pages)

	for _, query := range []string{"after=-1", "limit=0", "limit=501"} {
		status, data = f.do(http.MethodGet, chatKey+"/chat?"+query, "")
		assert.Equal(http.StatusUnprocessableEntity, status, "%s: %s", query, data)
	}
	pages, _ = owner.received()
	assert.Len(pages, 2, "a rejected cursor must not reach the owner")
	status, data = f.do(http.MethodGet, "unknown/chat", "")
	assert.Equal(http.StatusNotFound, status, string(data))
}

// Commands report each supervision conflict by its code, so a coordinator
// can tell a stale claim from a busy agent without reading prose.
func TestWorkspaceChatCommandsReportConflictsByCode(t *testing.T) {
	assert := assert.New(t)
	f := newChatFixture(t, localruntime.Options{})
	f.save(true)
	owner := newChatOwner("generation-1")
	f.serve(owner)
	prompt := `{"type":"prompt","mode":"send","text":"hello","id":"submission-1","generation":1,"allowanceMillis":60000}`

	status, data := f.do(http.MethodPost, chatKey+"/chat/commands", prompt)
	require.Equal(t, http.StatusOK, status, string(data))
	assert.Equal(map[string]any{"accepted": true}, chatFields(t, data))
	_, commands := owner.received()
	require.Len(t, commands, 1)
	assert.Equal(localruntime.ACPCommand{Type: "prompt", Mode: "send", Text: "hello", ID: "submission-1", Generation: 1, AllowanceMillis: 60000}, commands[0])

	for _, code := range []string{"busy", "supervised", "stale_generation", "uncertain", "stale_request", "queue_pending"} {
		owner.mu.Lock()
		owner.code = code
		owner.mu.Unlock()
		status, data = f.do(http.MethodPost, chatKey+"/chat/commands", prompt)
		require.Equal(t, http.StatusConflict, status, "%s: %s", code, data)
		problem := decodeChat[chatProblem](t, data)
		assert.Equal("conflict", problem.Code)
		assert.Equal(code, problem.Details["reason"])
	}

	owner.mu.Lock()
	owner.code, owner.unavailable = "", true
	owner.mu.Unlock()
	status, data = f.do(http.MethodPost, chatKey+"/chat/commands", prompt)
	assert.Equal(http.StatusServiceUnavailable, status, string(data))

	owner.mu.Lock()
	owner.unavailable, owner.refusal = false, "permission option is no longer pending"
	owner.mu.Unlock()
	status, data = f.do(http.MethodPost, chatKey+"/chat/commands", `{"type":"permission","id":"generation-1-p1","optionId":"allow"}`)
	require.Equal(t, http.StatusBadRequest, status, string(data))
	assert.Equal("badRequest", decodeChat[chatProblem](t, data).Code)
}

// Elicitation answers carry their form values as a JSON object.
func TestWorkspaceChatCommandsCarryElicitationContent(t *testing.T) {
	f := newChatFixture(t, localruntime.Options{})
	f.save(true)
	owner := newChatOwner("generation-1")
	f.serve(owner)

	status, data := f.do(http.MethodPost, chatKey+"/chat/commands", `{"type":"elicitation","id":"generation-1-p2","action":"accept","content":{"name":"widget","count":2},"generation":1}`)
	require.Equal(t, http.StatusOK, status, string(data))
	_, commands := owner.received()
	require.Len(t, commands, 1)
	assert.Equal(t, "accept", commands[0].Action)
	assert.JSONEq(t, `{"name":"widget","count":2}`, string(commands[0].Content))
}

// Only chat input reaches the owner; a websocket-only or unknown command type
// is refused before it is sent.
func TestWorkspaceChatCommandsRefuseOtherTypes(t *testing.T) {
	f := newChatFixture(t, localruntime.Options{})
	f.save(true)
	owner := newChatOwner("generation-1")
	f.serve(owner)

	for _, body := range []string{`{"type":"history"}`, `{"type":"heartbeat"}`, `{"type":"launch"}`, `{}`, `{"type":"cancel","before":5,"limit":10}`} {
		status, data := f.do(http.MethodPost, chatKey+"/chat/commands", body)
		assert.Contains(t, []int{http.StatusBadRequest, http.StatusUnprocessableEntity}, status, "%s: %s", body, data)
	}
	_, commands := owner.received()
	assert.Empty(t, commands)
	status, data := f.do(http.MethodPost, "unknown/chat/commands", `{"type":"cancel"}`)
	assert.Equal(t, http.StatusNotFound, status, string(data))
}

// A supervised chat outlives its agent: the daemon keeps its stored record,
// reports it as exited, and restores it in a new agent process on request.
func TestWorkspaceChatRestoresASupervisedChatAfterItsAgentExits(t *testing.T) {
	assert := assert.New(t)
	f := newChatFixture(t, localruntime.Options{})
	f.save(true)
	first := newChatOwner("generation-1", "m0")
	f.serve(first)
	status, data := f.do(http.MethodGet, chatKey+"/chat", "")
	require.Equal(t, http.StatusOK, status, string(data))

	f.stop(first)
	assert.Never(func() bool { return len(f.storedChats()) == 0 }, 300*time.Millisecond, 10*time.Millisecond, "the supervised chat's record was forgotten")
	status, data = f.do(http.MethodGet, chatKey+"/chat", "")
	require.Equal(t, http.StatusOK, status, string(data))
	assert.Equal(map[string]any{"exited": true}, chatFields(t, data))
	status, data = f.do(http.MethodPost, chatKey+"/chat/commands", `{"type":"cancel"}`)
	assert.Equal(http.StatusServiceUnavailable, status, string(data))

	second := newChatOwner("generation-2", "m0")
	f.serve(second)
	status, data = f.do(http.MethodPost, chatKey+"/chat/restore", "")
	require.Equal(t, http.StatusOK, status, string(data))
	status, data = f.do(http.MethodGet, chatKey+"/chat", "")
	require.Equal(t, http.StatusOK, status, string(data))
	chat := decodeChat[WorkspaceChat](t, data)
	assert.False(chat.Exited)
	require.NotNil(t, chat.WorkspaceChatState)
	assert.Equal("generation-2", chat.RuntimeGeneration)
	stored := f.storedChats()
	require.Len(t, stored, 1)
	assert.Equal("Coordinator task", stored[0].Label)
	assert.True(chatCreatedAt.Equal(stored[0].CreatedAt))
	assert.Equal("workflow", stored[0].DisplayRegion)

	// The restored chat is again an ordinary running chat: when its agent
	// exits, it is kept for another restore.
	f.stop(second)
	assert.Never(func() bool { return len(f.storedChats()) == 0 }, 300*time.Millisecond, 10*time.Millisecond)
}

// A restored agent can exit before the restore request finishes. That exit is
// still handled like any other, so the kept record stays exempt from the
// missing-backend prune.
func TestWorkspaceChatRecordsAnExitDuringRestore(t *testing.T) {
	f := newChatFixture(t, localruntime.Options{})
	f.save(true)
	first := newChatOwner("generation-1")
	f.serve(first)
	status, data := f.do(http.MethodGet, chatKey+"/chat", "")
	require.Equal(t, http.StatusOK, status, string(data))
	f.stop(first)
	require.Eventually(t, f.recoveryPending, 5*time.Second, 10*time.Millisecond, "the first exit was not handled")

	second := newChatOwner("generation-2")
	second.exit()
	paths, err := ptyowner.NewSessionPaths(f.root, chatKey)
	require.NoError(t, err)
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", paths.Socket)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	server := rpc.NewServer()
	require.NoError(t, server.RegisterName("ACP", second))
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go server.ServeConn(conn)
		}
	}()

	status, data = f.do(http.MethodPost, chatKey+"/chat/restore", "")
	require.Equal(t, http.StatusOK, status, string(data))
	require.Eventually(t, func() bool { return f.runtime.Exited(chatKey) }, 5*time.Second, 10*time.Millisecond)
	assert.Eventually(t, f.recoveryPending, 2*time.Second, 10*time.Millisecond, "the kept record must stay exempt from the missing-backend prune")
	assert.Len(t, f.storedChats(), 1)
}

// The interleaving the restore route cannot see: the restored agent exits and
// the exit hook runs, skipping the chat, while recovery is still pending.
// Ending the restore must then handle the exit itself.
func TestWorkspaceChatRestoreHandlesAnExitTheHookSkipped(t *testing.T) {
	f := newChatFixture(t, localruntime.Options{})
	f.save(true)
	owner := newChatOwner("generation-1")
	f.serve(owner)
	status, data := f.do(http.MethodGet, chatKey+"/chat", "")
	require.Equal(t, http.StatusOK, status, string(data))
	f.stop(owner)
	require.Eventually(t, f.recoveryPending, 5*time.Second, 10*time.Millisecond)
	stored := f.storedChats()
	require.Len(t, stored, 1)

	f.handler.finishChatRestore(stored[0])
	assert.True(t, f.recoveryPending(), "the exited chat lost its exemption from the missing-backend prune")
	assert.Len(t, f.storedChats(), 1)
}

// A chat nobody supervises keeps today's lifecycle: restore refuses it while
// it runs, and its record is forgotten when its agent exits.
func TestWorkspaceChatForgetsAnUnsupervisedChatWhenItsAgentExits(t *testing.T) {
	f := newChatFixture(t, localruntime.Options{})
	f.save(false)
	owner := newChatOwner("generation-1")
	f.serve(owner)
	status, data := f.do(http.MethodPost, chatKey+"/chat/restore", "")
	require.Equal(t, http.StatusConflict, status, string(data))
	problem := decodeChat[chatProblem](t, data)
	assert.Equal(t, "conflict", problem.Code)
	assert.Equal(t, "not_supervised", problem.Details["reason"])

	f.stop(owner)
	require.Eventually(t, func() bool { return len(f.storedChats()) == 0 }, 5*time.Second, 10*time.Millisecond)
	status, data = f.do(http.MethodPost, chatKey+"/chat/restore", "")
	assert.Equal(t, http.StatusNotFound, status, string(data))
	status, data = f.do(http.MethodGet, chatKey+"/chat", "")
	assert.Equal(t, http.StatusNotFound, status, string(data))
}

// An agent that cannot reload the saved session is reported by code, and the
// chat stays restorable once the agent can.
func TestWorkspaceChatRestoreReportsAnAgentThatCannotReload(t *testing.T) {
	// The owner records the failure the way a real owner does before exiting.
	ownerCommand := []string{"/bin/sh", "-c", `printf cannot_reload > "${1%/*}/start-error"`, "owner"}
	f := newChatFixture(t, localruntime.Options{
		ACPOwnerCommand: ownerCommand,
		PtyOwnerRuntime: ptyownerruntime.New(&ptyowner.Client{Root: filepath.Join(t.TempDir(), "pty-owner"), InProcess: true}, nil),
	})
	f.save(true)
	owner := newChatOwner("generation-1")
	f.serve(owner)
	status, data := f.do(http.MethodGet, chatKey+"/chat", "")
	require.Equal(t, http.StatusOK, status, string(data))
	f.stop(owner)

	status, data = f.do(http.MethodPost, chatKey+"/chat/restore", "")
	require.Equal(t, http.StatusConflict, status, string(data))
	problem := decodeChat[chatProblem](t, data)
	assert.Equal(t, "conflict", problem.Code)
	assert.Equal(t, "cannot_reload", problem.Details["reason"])
	assert.Len(t, f.storedChats(), 1)
	status, data = f.do(http.MethodGet, chatKey+"/chat", "")
	require.Equal(t, http.StatusOK, status, string(data))
	assert.Equal(t, map[string]any{"exited": true}, chatFields(t, data))
}
