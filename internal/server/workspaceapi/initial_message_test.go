package workspaceapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/agentactivity"
	"go.kenn.io/forge/internal/db"
	ptyownerruntime "go.kenn.io/forge/internal/ptyowner/runtime"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/workspace"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

func TestNormalizeInitialAgentMessage(t *testing.T) {
	tests := []struct {
		name      string
		message   string
		want      string
		wantBytes int
		wantErr   string
	}{
		{name: "line endings", message: "first\r\nsecond\rthird", want: "first\nsecond\nthird", wantBytes: 18},
		{name: "tab", message: "review\tthis", wantErr: "control character"},
		{name: "vertical tab", message: "review\vthis", wantErr: "control character"},
		{name: "form feed", message: "review\fthis", wantErr: "control character"},
		{name: "next line", message: "review\u0085this", wantErr: "control character"},
		{name: "maximum", message: strings.Repeat("a", 64<<10), wantBytes: 64 << 10},
		{name: "blank", message: " \n\t ", wantErr: "must not be blank"},
		{name: "invalid utf8", message: string([]byte{0xff}), wantErr: "valid UTF-8"},
		{name: "nul", message: "before\x00after", wantErr: "control character"},
		{name: "escape", message: "before\x1bafter", wantErr: "control character"},
		{name: "oversized", message: strings.Repeat("a", (64<<10)+1), wantErr: "64 KiB"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			normalized, messageBytes, err := normalizeInitialAgentMessage(tc.message)
			if tc.wantErr != "" {
				require.ErrorContains(err, tc.wantErr)
				return
			}
			require.NoError(err)
			if tc.want != "" {
				assert.Equal(tc.want, normalized)
			}
			assert.Equal(tc.wantBytes, messageBytes)
		})
	}
}

func TestInitialMessageRoutesDeliverOnceAndKeepReceiptAfterRuntimeExit(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)
	worktree := t.TempDir()
	workspaceID := "ws-initial-message"
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID: workspaceID, Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widgets",
		ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 42,
		GitHeadRef: "feature/message", WorkspaceBranch: "feature/message",
		WorktreePath: worktree, TmuxSession: "forge-initial-message", Status: "ready",
	}))

	owner := newInitialMessagePTYOwner()
	runtime := localruntime.NewManager(localruntime.Options{
		Targets: []localruntime.LaunchTarget{{
			Key: "codex", Label: "Codex", Kind: localruntime.LaunchTargetAgent,
			Source: "test", Command: []string{"unused"}, Available: true,
		}},
		PtyOwnerRuntime: owner,
	})
	t.Cleanup(runtime.Shutdown)
	session, err := runtime.Launch(ctx, workspaceID, worktree, "codex")
	require.NoError(err)
	require.NoError(database.UpsertWorkspaceRuntimeSession(ctx, &db.WorkspaceRuntimeSession{
		WorkspaceID: workspaceID, SessionKey: session.Key,
		TargetKey: "codex", Label: "Codex", Kind: "agent", Scope: "session",
		CreatedAt: session.CreatedAt,
	}))
	activity := agentactivity.NewStore(t.TempDir())
	require.NoError(activity.HandleEvent("codex", agentactivity.HookEvent{
		SessionID: "coding-session", CWD: worktree,
		HookEventName: "UserPromptSubmit",
	}, session.Key))

	handler := New(Deps{
		DB: database, Workspaces: workspace.NewManager(database, t.TempDir()),
		Runtime: runtime, AgentActivity: activity,
	})
	mux := http.NewServeMux()
	api := humago.NewWithPrefix(mux, "/api/v1", huma.DefaultConfig("initial message test", "1"))
	handler.Register(api)
	endpoint := "/api/v1/workspaces/" + workspaceID +
		"/runtime/sessions/" + session.Key + "/initial-message"
	postContext := func(requestContext context.Context, target, agent, codingSession, message string) *httptest.ResponseRecorder {
		t.Helper()
		body, marshalErr := json.Marshal(map[string]string{
			"agent": agent, "session_id": codingSession, "message": message,
		})
		require.NoError(marshalErr)
		request := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(body)).WithContext(requestContext)
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		return recorder
	}
	post := func(target, agent, codingSession, message string) *httptest.ResponseRecorder {
		t.Helper()
		return postContext(ctx, target, agent, codingSession, message)
	}

	requestContext, cancelRequest := context.WithCancel(ctx)
	owner.pty.setOnWrite(cancelRequest)
	response := postContext(requestContext, endpoint, "CoDeX", "coding-session", "review this")
	owner.pty.setOnWrite(nil)
	require.Equal(http.StatusOK, response.Code, response.Body.String())
	var receipt struct {
		Agent        string     `json:"agent"`
		SessionID    string     `json:"session_id"`
		State        string     `json:"state"`
		MessageBytes int        `json:"message_bytes"`
		DeliveredAt  *time.Time `json:"delivered_at"`
	}
	require.NoError(json.NewDecoder(response.Body).Decode(&receipt))
	assert.Equal("codex", receipt.Agent)
	assert.Equal("coding-session", receipt.SessionID)
	assert.Equal(db.AgentInitialMessageDelivered, receipt.State)
	assert.Equal(11, receipt.MessageBytes)
	require.NotNil(receipt.DeliveredAt)
	assert.Equal(time.UTC, receipt.DeliveredAt.Location())
	assert.Equal("review this\r", string(owner.pty.written()))

	response = post(endpoint, "codex", "coding-session", "review this")
	require.Equal(http.StatusOK, response.Code, response.Body.String())
	assert.Equal("review this\r", string(owner.pty.written()))

	launchSession := func(codingSession string, report bool) localruntime.SessionInfo {
		t.Helper()
		launched, launchErr := runtime.Launch(ctx, workspaceID, worktree, "codex")
		require.NoError(launchErr)
		require.NoError(database.UpsertWorkspaceRuntimeSession(ctx, &db.WorkspaceRuntimeSession{
			WorkspaceID: workspaceID, SessionKey: launched.Key,
			TargetKey: "codex", Label: "Codex", Kind: "agent", Scope: "session",
			CreatedAt: launched.CreatedAt,
		}))
		if report {
			require.NoError(activity.HandleEvent("codex", agentactivity.HookEvent{
				SessionID: codingSession, CWD: worktree,
				HookEventName: "UserPromptSubmit",
			}, launched.Key))
		}
		return launched
	}
	endpointFor := func(runtimeKey string) string {
		return "/api/v1/workspaces/" + workspaceID +
			"/runtime/sessions/" + runtimeKey + "/initial-message"
	}

	failingSession := launchSession("coding-failure", true)
	owner.pty.setWriteError(errors.New("write failed"))
	response = post(endpointFor(failingSession.Key), "codex", "coding-failure", "try once")
	require.Equal(http.StatusInternalServerError, response.Code, response.Body.String())
	failedReceipt, err := database.GetAgentInitialMessageReceipt(ctx, workspaceID, failingSession.Key)
	require.NoError(err)
	require.NotNil(failedReceipt)
	assert.Equal(db.AgentInitialMessageUncertain, failedReceipt.State)
	owner.pty.setWriteError(nil)

	inactivePasteSession := launchSession("coding-multiline", true)
	writtenBefore := owner.pty.written()
	response = post(
		endpointFor(inactivePasteSession.Key), "codex", "coding-multiline", "first\nsecond",
	)
	require.Equal(http.StatusBadRequest, response.Code, response.Body.String())
	assert.Equal(writtenBefore, owner.pty.written())
	inactiveReceipt, err := database.GetAgentInitialMessageReceipt(
		ctx, workspaceID, inactivePasteSession.Key,
	)
	require.NoError(err)
	assert.Nil(inactiveReceipt)

	unreportedSession := launchSession("coding-unreported", false)
	response = post(
		endpointFor(unreportedSession.Key), "codex", "coding-unreported", "do not send",
	)
	require.Equal(http.StatusConflict, response.Code, response.Body.String())
	unreportedReceipt, err := database.GetAgentInitialMessageReceipt(
		ctx, workspaceID, unreportedSession.Key,
	)
	require.NoError(err)
	assert.Nil(unreportedReceipt)

	require.NoError(runtime.Stop(ctx, workspaceID, session.Key))
	require.NoError(database.DeleteWorkspaceRuntimeSession(ctx, workspaceID, session.Key))
	response = post(endpoint, "codex", "different-session", "review this")
	require.Equal(http.StatusConflict, response.Code, response.Body.String())

	getRecorder := httptest.NewRecorder()
	mux.ServeHTTP(getRecorder, httptest.NewRequest(http.MethodGet, endpoint, nil))
	require.Equal(http.StatusOK, getRecorder.Code, getRecorder.Body.String())
	receipt = struct {
		Agent        string     `json:"agent"`
		SessionID    string     `json:"session_id"`
		State        string     `json:"state"`
		MessageBytes int        `json:"message_bytes"`
		DeliveredAt  *time.Time `json:"delivered_at"`
	}{}
	require.NoError(json.NewDecoder(getRecorder.Body).Decode(&receipt))
	assert.Equal("codex", receipt.Agent)
	assert.Equal("coding-session", receipt.SessionID)
	assert.Equal(db.AgentInitialMessageDelivered, receipt.State)
}

type initialMessagePTYOwner struct {
	pty     *initialMessagePTY
	started bool
}

func newInitialMessagePTYOwner() *initialMessagePTYOwner {
	return &initialMessagePTYOwner{pty: &initialMessagePTY{
		output: make(chan []byte), done: make(chan struct{}),
	}}
}

func (o *initialMessagePTYOwner) HasState(string) bool { return o.started }

func (o *initialMessagePTYOwner) Attach(context.Context, string) (ptyownerruntime.PTY, error) {
	return o.pty, nil
}

func (o *initialMessagePTYOwner) Start(
	context.Context, string, string, []string, []string,
) (ptyownerruntime.PTY, error) {
	o.started = true
	return o.pty, nil
}

func (o *initialMessagePTYOwner) Stop(context.Context, string) error {
	o.pty.Close()
	return nil
}

type initialMessagePTY struct {
	mu       sync.Mutex
	output   chan []byte
	done     chan struct{}
	writes   []byte
	writeErr error
	onWrite  func()
	once     sync.Once
}

func (p *initialMessagePTY) Output() <-chan []byte { return p.output }
func (p *initialMessagePTY) Done() <-chan struct{} { return p.done }
func (p *initialMessagePTY) ExitCode() int         { return 0 }
func (p *initialMessagePTY) Resize(int, int) error { return nil }

func (p *initialMessagePTY) Write(data []byte) error {
	p.mu.Lock()
	if p.writeErr != nil {
		p.mu.Unlock()
		return p.writeErr
	}
	p.writes = append(p.writes, data...)
	onWrite := p.onWrite
	p.mu.Unlock()
	if onWrite != nil {
		onWrite()
	}
	return nil
}

func (p *initialMessagePTY) setWriteError(err error) {
	p.mu.Lock()
	p.writeErr = err
	p.mu.Unlock()
}

func (p *initialMessagePTY) setOnWrite(onWrite func()) {
	p.mu.Lock()
	p.onWrite = onWrite
	p.mu.Unlock()
}

func (p *initialMessagePTY) written() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return bytes.Clone(p.writes)
}

func (p *initialMessagePTY) Close() {
	p.once.Do(func() {
		close(p.output)
		close(p.done)
	})
}
