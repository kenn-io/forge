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

	"go.kenn.io/forge/internal/agentactivity"

	"github.com/coder/websocket"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/ptyowner"
	ptyownerruntime "go.kenn.io/forge/internal/ptyowner/runtime"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/terminalwebsocket"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/workspacetest"
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
	protocol    int
	// parked makes the owner report that it parked the chat when it exits.
	parked bool
	// bind, when set, runs inside Bind, so a test can hold a reconnect open.
	bind func()
}

func newChatOwner(generation string, messages ...string) *chatOwner {
	owner := &chatOwner{exited: make(chan struct{}), protocol: localruntime.ACPOwnerProtocol, state: localruntime.ACPState{RuntimeGeneration: generation, Connected: true}}
	for _, text := range messages {
		owner.state.Messages = append(owner.state.Messages, localruntime.ACPMessage{Role: "assistant", Text: text})
	}
	return owner
}

func (o *chatOwner) exit() { o.once.Do(func() { close(o.exited) }) }

func (o *chatOwner) Bind(localruntime.ACPMCPBinding, *struct{}) error {
	if o.bind != nil {
		o.bind()
	}
	return nil
}

func (o *chatOwner) Snapshot(_ struct{}, reply *localruntime.ACPUpdate) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	data, err := json.Marshal(o.state)
	reply.Protocol = o.protocol
	reply.State = data
	return err
}

// chatSocket opens the chat key's websocket on the daemon.
func (f *chatFixture) chatSocket(ctx context.Context) *websocket.Conn {
	f.t.Helper()
	mux := http.NewServeMux()
	f.handler.RegisterTerminal(humago.New(mux, huma.DefaultConfig("test", "1")))
	server := httptest.NewServer(mux)
	f.t.Cleanup(server.Close)
	conn, _, err := terminalwebsocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/workspaces/workspace/runtime/sessions/"+chatKey+"/terminal?protocol=acp", nil, nil) //nolint:bodyclose // upgraded response has no body
	require.NoError(f.t, err)
	f.t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "test done") })
	return conn
}

// chatFrame is any frame a chat socket sends: a full state, an update, an
// acknowledgement, or a command error.
type chatFrame struct {
	localruntime.ACPState
	Updates      *localruntime.ACPState `json:"updates"`
	Accepted     *acpAccepted           `json:"accepted"`
	CommandError string                 `json:"commandError"`
	Command      string                 `json:"command"`
	ID           string                 `json:"id"`
}

// readChatFrame reads the next frame from conn.
func readChatFrame(ctx context.Context, t *testing.T, conn *websocket.Conn) chatFrame {
	t.Helper()
	_, data, err := conn.Read(ctx)
	require.NoError(t, err)
	return decodeChat[chatFrame](t, data)
}

// A chat whose owner runs an older protocol opens from its saved session at
// once, leaving that owner alone. The send that needs the agent reports why
// the owner could not be replaced, and never reaches the old owner.
func TestChatSocketServesASavedChatAndReportsItsStartFailure(t *testing.T) {
	t.Parallel()
	f := newChatFixture(t, localruntime.Options{})
	f.saveState(`{"messages":[{"id":"m1","role":"assistant","text":"Saved conversation"}]}`)
	owner := newChatOwner("old", "Owner state")
	owner.protocol = 0
	f.serve(owner)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn := f.chatSocket(ctx)

	first := readChatFrame(ctx, t, conn)
	assert.True(t, first.Dormant)
	assert.False(t, first.Connected)
	require.Len(t, first.Messages, 1)
	assert.Equal(t, "Saved conversation", first.Messages[0].Text)

	require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte(`{"type":"prompt","mode":"send","id":"p1","text":"hello"}`)))
	var refused chatFrame
	var failed *localruntime.ACPState
	for refused.CommandError == "" || failed == nil {
		frame := readChatFrame(ctx, t, conn)
		if frame.CommandError != "" {
			refused = frame
		}
		if frame.Updates != nil && frame.Updates.Error != "" {
			failed = frame.Updates
		}
	}
	assert.Equal(t, "prompt", refused.Command)
	assert.Equal(t, "p1", refused.ID)
	const cannotReload = "this chat's agent cannot reload its session, so it stays on the previous Forge version; closing its tab stops the agent and deletes the chat"
	assert.Contains(t, refused.CommandError, cannotReload)
	assert.False(t, failed.Starting, "a failed start stays shown as starting")
	assert.Contains(t, failed.Error, cannotReload)
	_, commands := owner.received()
	assert.Empty(t, commands, "the old owner received the prompt")
}

// launchingChat is a chat whose owner is not running. Starting it runs an
// owner command that records the launch; the test then serves an owner on
// the chat's socket, as the launched owner would.
func launchingChat(t *testing.T) (f *chatFixture, launches func() int) {
	t.Helper()
	return recordingChat(t, `echo launch >> "${1%/*}/launches"`)
}

// A read of a chat whose owner is gone returns its saved transcript and starts
// nothing. A prompt sent on the socket starts the owner, shows the chat as
// starting meanwhile, delivers the prompt, and switches the socket to the
// running chat.
func TestChatSocketStartsADormantChatForAPrompt(t *testing.T) {
	t.Parallel()
	f, launches := launchingChat(t)
	f.saveState(`{"messages":[{"id":"m1","role":"assistant","text":"Saved reply"}]}`)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	status, data := f.doPath(http.MethodGet, "/workspaces/workspace/runtime?viewing=true", "")
	require.Equal(t, http.StatusOK, status, string(data))
	assert.Contains(t, string(data), `"status":"parked"`)
	conn := f.chatSocket(ctx)

	first := readChatFrame(ctx, t, conn)
	assert.True(t, first.Dormant)
	require.Len(t, first.Messages, 1)
	assert.Equal(t, "Saved reply", first.Messages[0].Text)
	assert.Equal(t, 0, launches(), "a read started the agent")

	require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte(`{"type":"prompt","mode":"send","id":"p1","text":"hello"}`)))
	for {
		frame := readChatFrame(ctx, t, conn)
		if frame.Updates != nil && frame.Updates.Starting {
			break
		}
	}
	require.Eventually(t, func() bool { return launches() == 1 }, 5*time.Second, 10*time.Millisecond)
	owner := newChatOwner("generation-2", "Saved reply")
	f.serve(owner)

	var accepted, live bool
	for !accepted || !live {
		frame := readChatFrame(ctx, t, conn)
		accepted = accepted || (frame.Accepted != nil && frame.Accepted.ID == "p1")
		live = live || frame.RuntimeGeneration == "generation-2"
	}
	_, commands := owner.received()
	require.Len(t, commands, 1)
	assert.Equal(t, "prompt", commands[0].Type)
	assert.Equal(t, "hello", commands[0].Text)
	assert.Equal(t, 1, launches())
}

// A coordinator's command to a chat whose owner is gone starts the owner and
// runs on it.
func TestWorkspaceChatCommandStartsADormantChat(t *testing.T) {
	t.Parallel()
	f, launches := launchingChat(t)
	answered := make(chan int, 1)
	go func() {
		status, _ := f.do(http.MethodPost, chatKey+"/chat/commands", `{"type":"prompt","mode":"send","id":"p1","text":"hello"}`)
		answered <- status
	}()
	require.Eventually(t, func() bool { return launches() == 1 }, 5*time.Second, 10*time.Millisecond)
	owner := newChatOwner("generation-2")
	f.serve(owner)

	select {
	case status := <-answered:
		require.Equal(t, http.StatusOK, status)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "the command did not finish after the owner started")
	}
	_, commands := owner.received()
	require.Len(t, commands, 1)
	assert.Equal(t, "hello", commands[0].Text)
	status, data := f.do(http.MethodGet, chatKey+"/chat", "")
	require.Equal(t, http.StatusOK, status, string(data))
	chat := decodeChat[WorkspaceChat](t, data)
	require.NotNil(t, chat.WorkspaceChatState)
	assert.Equal(t, "generation-2", chat.RuntimeGeneration)
	assert.False(t, chat.Dormant)
	assert.Equal(t, 1, launches())
}

func (o *chatOwner) Watch(_ localruntime.ACPWatch, reply *localruntime.ACPUpdate) error {
	<-o.exited
	reply.Exited = true
	reply.Parked = o.parked
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
	database := dbtest.Open(t)
	root := t.TempDir()
	addChatRecord(t, database, root, "workspace", chatKey)
	var handler *Handler
	options.ACPSessionsDir = root
	options.OnSessionExit = func(info localruntime.SessionInfo) { handler.HandleRuntimeSessionExit(info) }
	runtime := localruntime.NewManager(options)
	t.Cleanup(runtime.Shutdown)
	handler = New(Deps{DB: database, Workspaces: workspacetest.NewManager(t, database, t.TempDir()), Runtime: runtime})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		assert.NoError(t, handler.Shutdown(ctx))
	})
	mux := http.NewServeMux()
	handler.RegisterExecution(humago.New(mux, huma.DefaultConfig("test", "1")))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return &chatFixture{t: t, database: database, root: root, runtime: runtime, handler: handler, server: server}
}

// addChatRecord stores a ready workspace with one chat whose owner state lives
// in root.
func addChatRecord(t *testing.T, database *db.DB, root, workspaceID, key string) {
	t.Helper()
	require := require.New(t)
	ctx := t.Context()
	cwd := t.TempDir()
	branch := "work/" + workspaceID
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{ID: workspaceID, Platform: "github", PlatformHost: "github.com", RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc, ItemKey: db.AdHocWorkspaceItemKey(branch), GitHeadRef: branch, WorkspaceBranch: branch, WorktreePath: cwd, Status: "ready"}))
	require.NoError(database.UpsertWorkspaceRuntimeSession(ctx, &db.WorkspaceRuntimeSession{WorkspaceID: workspaceID, SessionKey: key, TargetKey: "chat", Label: "Coordinator task", Kind: "acp", Scope: "session", DisplayRegion: "workflow", CreatedAt: chatCreatedAt}))
	paths, err := ptyowner.NewSessionPaths(root, key)
	require.NoError(err)
	require.NoError(os.MkdirAll(paths.Dir, 0o700))
	require.NoError(os.MkdirAll(filepath.Dir(paths.Socket), 0o700))
	if paths.SocketDir != "" {
		t.Cleanup(func() { _ = os.RemoveAll(paths.SocketDir) })
	}
	config := fmt.Sprintf(`{"Root":%q,"Info":{"key":%q,"workspace_id":%q,"target_key":"chat","kind":"acp"},"Command":["agent"],"CWD":%q}`, root, key, workspaceID, cwd)
	require.NoError(os.WriteFile(filepath.Join(paths.Dir, "config.json"), []byte(config), 0o600))
}

// save writes the chat's saved session, supervised or not.
func (f *chatFixture) save(supervised bool) {
	f.t.Helper()
	state := `{}`
	if supervised {
		state = `{"supervision":{"supervisor":"coordinator","generation":1,"takenOver":false,"changedAt":"2026-10-09T12:00:00Z"}}`
	}
	f.saveState(state)
}

// saveState writes the chat's saved session with state as its ACP state.
func (f *chatFixture) saveState(state string) {
	f.t.Helper()
	f.saveKeyState(chatKey, state)
}

// saveKeyState writes the saved session of the chat key with state as its
// ACP state.
func (f *chatFixture) saveKeyState(key, state string) {
	f.t.Helper()
	paths, err := ptyowner.NewSessionPaths(f.root, key)
	require.NoError(f.t, err)
	require.NoError(f.t, os.WriteFile(filepath.Join(paths.Dir, "session.json"), []byte(`{"SessionID":"native","State":`+state+`}`), 0o600))
}

// serve runs owner on the chat's socket until the owner exits.
func (f *chatFixture) serve(owner *chatOwner) {
	f.t.Helper()
	f.serveKey(chatKey, owner)
}

// serveKey runs owner on the socket of the chat key until the owner exits.
func (f *chatFixture) serveKey(key string, owner *chatOwner) {
	f.t.Helper()
	paths, err := ptyowner.NewSessionPaths(f.root, key)
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
	return f.doPath(method, "/workspaces/workspace/runtime/sessions/"+path, body)
}

// doPath sends a request to path on the daemon.
func (f *chatFixture) doPath(method, path, body string) (int, []byte) {
	f.t.Helper()
	request, err := http.NewRequestWithContext(f.t.Context(), method, f.server.URL+path, strings.NewReader(body))
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

// keepsStoredChat checks for d that the chat's record stays stored. It polls
// on the test goroutine: assert.Never can leave its last check running after
// the test ends, when the test context the database read uses is canceled.
func (f *chatFixture) keepsStoredChat(d time.Duration, msg string) {
	f.t.Helper()
	timer := time.NewTimer(d)
	defer timer.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		require.NotEmpty(f.t, f.storedChats(), msg)
		select {
		case <-timer.C: // The absence window checks that owner exit never removes the stored chat.
			return
		case <-tick.C:
		}
	}
}

// A coordinator reads every message after its cursor, each with its
// transcript index, along with the rest of the chat's state.
func TestWorkspaceChatPagesTheTranscriptAfterACursor(t *testing.T) {
	assert := assert.New(t)
	f := newChatFixture(t, localruntime.Options{})
	f.save(true)
	owner := newChatOwner("generation-1", "m0", "m1", "m2", "m3", "m4")
	owner.state.Messages[2].Role = "user"
	owner.state.Messages[2].Turn = &localruntime.ACPTurn{StopReason: "end_turn"}
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
	assert.Equal(&localruntime.ACPTurn{StopReason: "end_turn"}, chat.Messages[0].Turn)
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

	for _, code := range []string{"supervised", "stale_generation", "stale_request", "not_owned", "person_only", "withdrawn", "sending", "already_answered", "not_pending"} {
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
	owner.unavailable, owner.refusal = false, `permission request has no option "allow"`
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
	f.keepsStoredChat(300*time.Millisecond, "the supervised chat's record was forgotten")
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
	f.keepsStoredChat(300*time.Millisecond, "the restored chat's record was forgotten")
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

// The missing-tmux prune can judge a supervised chat after its runtime saw the
// agent exit but before the exit hook kept the chat. It keeps the chat, as
// that hook would.
func TestTmuxPruneKeepsAnExitedSupervisedChat(t *testing.T) {
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

	// As if the exit hook had not run yet.
	f.handler.setRuntimeRecoveryPending(chatKey, false)
	assert.True(t, f.handler.retainMissingRuntime(stored[0]), "the prune would forget the supervised chat")
}

// A daemon that restarts attaches no chat owner until a read needs it, and
// knows nothing of chats parked before it started. The workspace still shows
// the activity the chat's owner last reported.
func TestWorkspaceAgentStateSurvivesDaemonRestartBeforeAttach(t *testing.T) {
	f := newChatFixture(t, localruntime.Options{})
	f.save(false)
	ctx := t.Context()
	summary, err := f.handler.workspaces.GetSummary(ctx, "workspace")
	require.NoError(t, err)
	require.NotNil(t, summary)
	store := agentactivity.NewStore(t.TempDir())
	require.NoError(t, store.Record(localruntime.ACPActivityAgent, "native", chatKey, summary.WorktreePath, agentactivity.StateDone))
	f.handler.agentActivity = store

	result, err := f.handler.GetWorkspaceService(ctx, "workspace")
	require.NoError(t, err)
	require.NotNil(t, result.Workspace.AgentState)
	assert.Equal(t, "done", *result.Workspace.AgentState)

	// A forgotten chat's report no longer counts.
	require.NoError(t, f.database.DeleteWorkspaceRuntimeSession(ctx, "workspace", chatKey))
	result, err = f.handler.GetWorkspaceService(ctx, "workspace")
	require.NoError(t, err)
	assert.Nil(t, result.Workspace.AgentState)
}

// A chat its owner parked has not exited. The missing-backend prune keeps its
// record from the moment the runtime drops it, before the exit hook marks it
// pending recovery, and its activity report keeps counting until it reopens.
func TestWorkspaceChatParkedByItsOwnerKeepsItsRecord(t *testing.T) {
	f := newChatFixture(t, localruntime.Options{})
	f.save(false)
	owner := newChatOwner("generation-1")
	owner.parked = true
	f.serve(owner)
	status, body := f.do(http.MethodGet, chatKey+"/chat", "")
	require.Equal(t, http.StatusOK, status, string(body))

	owner.exit()
	require.Eventually(t, f.recoveryPending, 5*time.Second, 10*time.Millisecond)
	assert.False(t, f.runtime.Exited(chatKey), "a parked chat has not exited")
	stored := f.storedChats()
	require.Len(t, stored, 1)
	assert.Contains(t, f.handler.liveAgentSessionKeys(t.Context(), "workspace"), chatKey)

	// As if the exit hook had not run yet.
	f.handler.setRuntimeRecoveryPending(chatKey, false)
	assert.True(t, f.handler.retainMissingRuntime(stored[0]), "the prune would forget the parked chat")
}

// After a daemon restart, ordinary reopening reattaches a pending chat whose
// agent exits during reattachment. The exit hook skips a chat pending
// recovery, so reopening must handle the exit itself, or the kept record loses
// its exemption from the missing-backend prune.
func TestWorkspaceChatReopenHandlesAnExitDuringReattachment(t *testing.T) {
	f := newChatFixture(t, localruntime.Options{})
	f.save(true)
	f.handler.setRuntimeRecoveryPending(chatKey, true)
	owner := newChatOwner("generation-1")
	owner.exit()
	paths, err := ptyowner.NewSessionPaths(f.root, chatKey)
	require.NoError(t, err)
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", paths.Socket)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	server := rpc.NewServer()
	require.NoError(t, server.RegisterName("ACP", owner))
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go server.ServeConn(conn)
		}
	}()

	_, _ = f.do(http.MethodGet, chatKey+"/chat", "")
	require.Eventually(t, func() bool { return f.runtime.Exited(chatKey) }, 5*time.Second, 10*time.Millisecond)

	assert.Eventually(t, f.recoveryPending, 2*time.Second, 10*time.Millisecond,
		"the kept record must stay exempt from the missing-backend prune")
	assert.Never(t, func() bool { return !f.recoveryPending() }, 200*time.Millisecond, 10*time.Millisecond)
	assert.Len(t, f.storedChats(), 1)
}

// A chat nobody supervises keeps today's lifecycle: restore refuses it while
// it runs, and its record is forgotten when its agent exits.
func TestWorkspaceChatForgetsAnUnsupervisedChatWhenItsAgentExits(t *testing.T) {
	f := newChatFixture(t, localruntime.Options{})
	f.save(false)
	owner := newChatOwner("generation-1")
	f.serve(owner)
	status, data := f.do(http.MethodGet, chatKey+"/chat", "")
	require.Equal(t, http.StatusOK, status, string(data))
	status, data = f.do(http.MethodPost, chatKey+"/chat/restore", "")
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

// A taken-over chat is a person's chat. Its coordinator can no longer
// restore it, and its record is forgotten when its agent exits.
func TestWorkspaceChatTreatsATakenOverChatAsUnsupervised(t *testing.T) {
	f := newChatFixture(t, localruntime.Options{})
	f.saveState(`{"supervision":{"supervisor":"coordinator","generation":2,"takenOver":true,"changedAt":"2026-10-09T12:00:00Z"}}`)
	owner := newChatOwner("generation-1")
	f.serve(owner)
	status, data := f.do(http.MethodGet, chatKey+"/chat", "")
	require.Equal(t, http.StatusOK, status, string(data))
	status, data = f.do(http.MethodPost, chatKey+"/chat/restore", "")
	require.Equal(t, http.StatusConflict, status, string(data))
	assert.Equal(t, "not_supervised", decodeChat[chatProblem](t, data).Details["reason"])

	f.stop(owner)
	require.Eventually(t, func() bool { return len(f.storedChats()) == 0 }, 5*time.Second, 10*time.Millisecond)
	assert.False(t, f.recoveryPending())
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

// holdSetup makes workspace setup own the workspace until the test ends.
func (f *chatFixture) holdSetup() {
	f.t.Helper()
	done, admitted := f.handler.beginWorkspaceSetup("workspace")
	require.True(f.t, admitted)
	f.t.Cleanup(func() { f.handler.finishWorkspaceSetup("workspace", done) })
}

// A chat that is already attached answers at once, even while another
// request holds the restore lock for a slow relaunch.
func TestWorkspaceChatCommandsDoNotWaitForAnotherRestore(t *testing.T) {
	f := newChatFixture(t, localruntime.Options{})
	f.save(true)
	owner := newChatOwner("generation-1")
	f.serve(owner)
	status, data := f.do(http.MethodGet, chatKey+"/chat", "")
	require.Equal(t, http.StatusOK, status, string(data))

	release := f.handler.workspaceRuntimeLocks.Lock("workspace")
	unlocked := false
	unlock := func() {
		if !unlocked {
			unlocked = true
			release()
		}
	}
	t.Cleanup(unlock)
	answered := make(chan int, 2)
	go func() {
		status, _ := f.do(http.MethodPost, chatKey+"/chat/commands", `{"type":"cancel","generation":1}`)
		answered <- status
		status, _ = f.do(http.MethodGet, chatKey+"/chat", "")
		answered <- status
	}()
	for range 2 {
		select {
		case status := <-answered:
			assert.Equal(t, http.StatusOK, status)
		case <-time.After(5 * time.Second):
			unlock()
			require.FailNow(t, "an attached chat waited for the restore lock")
		}
	}
}

// A chat that could not be attached because setup owns the workspace reads
// from its saved session, and a command that needs its agent is refused with
// the reason rather than starting a second agent.
func TestWorkspaceChatDuringSetupReadsItsSavedSession(t *testing.T) {
	f := newChatFixture(t, localruntime.Options{})
	f.saveState(`{"messages":[{"id":"m1","role":"assistant","text":"Saved reply"}]}`)
	f.holdSetup()

	status, data := f.do(http.MethodGet, chatKey+"/chat", "")
	require.Equal(t, http.StatusOK, status, string(data))
	chat := decodeChat[WorkspaceChat](t, data)
	require.NotNil(t, chat.WorkspaceChatState)
	assert.True(t, chat.Dormant)
	require.Len(t, chat.Messages, 1)
	assert.Equal(t, "Saved reply", chat.Messages[0].Text)
	status, data = f.do(http.MethodPost, chatKey+"/chat/commands", `{"type":"prompt","mode":"send","id":"p1","text":"hello"}`)
	require.Equal(t, http.StatusConflict, status, string(data))
	assert.Equal(t, "setup_in_progress", decodeChat[chatProblem](t, data).Details["reason"])
}

// A restore refused because setup owns the workspace carries a reason, like
// every other conflict a coordinator settles.
func TestWorkspaceChatRestoreDuringSetupReportsItsReason(t *testing.T) {
	f := newChatFixture(t, localruntime.Options{})
	f.save(true)
	f.holdSetup()

	status, data := f.do(http.MethodPost, chatKey+"/chat/restore", "")
	require.Equal(t, http.StatusConflict, status, string(data))
	problem := decodeChat[chatProblem](t, data)
	assert.Equal(t, "workspaceSetupInProgress", problem.Code)
	assert.Equal(t, "setup_in_progress", problem.Details["reason"])
}

// An agent's message to a chat whose owner is gone starts the owner and
// delivers the message as the coordinator's.
func TestAgentMessageStartsADormantChat(t *testing.T) {
	t.Parallel()
	f, launches := launchingChat(t)
	answered := make(chan error, 1)
	go func() {
		_, err := f.handler.SubmitAgentMessageService(t.Context(), "workspace", chatKey, "hello")
		answered <- err
	}()
	require.Eventually(t, func() bool { return launches() == 1 }, 5*time.Second, 10*time.Millisecond)
	owner := newChatOwner("generation-2")
	f.serve(owner)

	select {
	case err := <-answered:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "the message did not finish after the owner started")
	}
	_, commands := owner.received()
	require.Len(t, commands, 1)
	assert.Equal(t, "prompt", commands[0].Type)
	assert.True(t, commands[0].FromAgent)
	assert.Contains(t, commands[0].Text, "hello")
}

// cannotReloadChat is a supervised chat whose owner died while no daemon was
// attached, so nothing marks it exited, and whose agent refuses to reload it.
// launches counts the owner starts.
func cannotReloadChat(t *testing.T) (f *chatFixture, launches func() int) {
	t.Helper()
	// Each launch is recorded, then fails the way a real owner does.
	return recordingChat(t, `echo launch >> "${1%/*}/launches"; printf cannot_reload > "${1%/*}/start-error"`)
}

// recordingChat is a supervised chat whose owner is not running and whose
// owner command is script. launches counts the owner starts the script
// records.
func recordingChat(t *testing.T, script string) (f *chatFixture, launches func() int) {
	t.Helper()
	ownerCommand := []string{"/bin/sh", "-c", script, "owner"}
	f = newChatFixture(t, localruntime.Options{
		ACPOwnerCommand: ownerCommand,
		PtyOwnerRuntime: ptyownerruntime.New(&ptyowner.Client{Root: filepath.Join(t.TempDir(), "pty-owner"), InProcess: true}, nil),
	})
	f.save(true)
	paths, err := ptyowner.NewSessionPaths(f.root, chatKey)
	require.NoError(t, err)
	return f, func() int {
		data, err := os.ReadFile(filepath.Join(paths.Dir, "launches"))
		if errors.Is(err, os.ErrNotExist) {
			return 0
		}
		require.NoError(t, err)
		return strings.Count(string(data), "launch")
	}
}

func (f *chatFixture) requireCannotReloadRestore() {
	f.t.Helper()
	status, data := f.do(http.MethodPost, chatKey+"/chat/restore", "")
	require.Equal(f.t, http.StatusConflict, status, string(data))
	assert.Equal(f.t, "cannot_reload", decodeChat[chatProblem](f.t, data).Details["reason"])
}

// Reads never start an agent (maintainer decision). A chat whose agent would
// refuse to reload it reads from its saved session however often it is read;
// a command starts the agent once, after which the chat reads as exited, and
// an explicit restore tries again.
func TestWorkspaceChatStartsItsAgentOnlyForACommand(t *testing.T) {
	f, launches := cannotReloadChat(t)

	for range 3 {
		status, data := f.do(http.MethodGet, chatKey+"/chat", "")
		require.Equal(t, http.StatusOK, status, string(data))
		chat := decodeChat[WorkspaceChat](t, data)
		assert.False(t, chat.Exited)
		require.NotNil(t, chat.WorkspaceChatState)
		assert.True(t, chat.Dormant, "a chat whose agent is not running reads as dormant")
		status, data = f.doPath(http.MethodGet, "/workspaces/workspace/runtime?viewing=true", "")
		require.Equal(t, http.StatusOK, status, string(data))
	}
	assert.Equal(t, 0, launches(), "a read started the agent")

	status, data := f.do(http.MethodPost, chatKey+"/chat/commands", `{"type":"prompt","mode":"send","id":"p1","text":"hello"}`)
	require.Equal(t, http.StatusConflict, status, string(data))
	assert.Equal(t, "cannot_reload", decodeChat[chatProblem](t, data).Details["reason"])
	assert.Equal(t, 1, launches(), "the command starts the agent once")

	status, data = f.do(http.MethodGet, chatKey+"/chat", "")
	require.Equal(t, http.StatusOK, status, string(data))
	assert.Equal(t, map[string]any{"exited": true}, chatFields(t, data))
	assert.True(t, f.handler.RuntimeExited(chatKey), "the fleet inventory treats it as exited")
	assert.Equal(t, 1, launches(), "a read after the refusal started the agent")

	f.requireCannotReloadRestore()
	assert.Equal(t, 2, launches(), "an explicit restore tries again")

	status, data = f.do(http.MethodDelete, chatKey, "")
	require.Equal(t, http.StatusNoContent, status, string(data))
	assert.False(t, f.handler.chatCannotReload(chatKey), "a stopped chat leaves no mark behind")
}

// A restore starts the chat's agent once. Reconnecting the rest of the
// workspace in the same request leaves that chat to the restore.
func TestWorkspaceChatRestoreStartsTheAgentOnce(t *testing.T) {
	f, launches := cannotReloadChat(t)

	f.requireCannotReloadRestore()
	assert.Equal(t, 1, launches())
	status, data := f.do(http.MethodGet, chatKey+"/chat", "")
	require.Equal(t, http.StatusOK, status, string(data))
	assert.Equal(t, map[string]any{"exited": true}, chatFields(t, data))
	assert.Equal(t, 1, launches(), "a refused restore is remembered for later reads")
}

// A chat whose agent process runs but never answered tells the reader what
// happened and what to do; the dial details stay in the log. Another chat's
// silent agent does not change what this chat shows.
func TestChatRestoreExplainsAnUnresponsiveAgent(t *testing.T) {
	t.Parallel()
	silent := fmt.Errorf("%w: dial unix /synthetic/sock: connect: no such file or directory: %w", localruntime.ErrACPOwnerUnresponsive, context.DeadlineExceeded)
	other := errors.New("agent refused to start")

	require.EqualError(t, chatStartError(silent), acpOwnerUnresponsiveMessage)
	assert.Same(t, other, chatStartError(other))

	problem, ok := errors.AsType[*httpapi.ProblemError](restoreChatProblem(silent))
	require.True(t, ok)
	assert.Equal(t, http.StatusServiceUnavailable, problem.Status)
	assert.Equal(t, acpOwnerUnresponsiveMessage, problem.Detail)
}
