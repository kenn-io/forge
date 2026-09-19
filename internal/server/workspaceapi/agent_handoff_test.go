package workspaceapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/workspace"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

type agentHandoffFixture struct {
	handler  *Handler
	database *db.DB
	owner    *initialMessagePTYOwner
	mux      *http.ServeMux
}

func newAgentHandoffFixture(t *testing.T, status string) agentHandoffFixture {
	t.Helper()
	dir := t.TempDir()
	database := dbtest.Open(t)
	owner := newInitialMessagePTYOwner()
	runtime := localruntime.NewManager(localruntime.Options{
		Targets: []localruntime.LaunchTarget{
			{
				Key: "codex", Label: "Codex", Kind: localruntime.LaunchTargetAgent,
				Source: "test", Command: []string{"unused"}, Available: true,
			},
			{
				Key: "shell", Label: "Shell", Kind: localruntime.LaunchTargetShell,
				Source: "test", Command: []string{"sh"}, Available: true,
			},
		},
		PtyOwnerRuntime: owner,
	})
	t.Cleanup(runtime.Shutdown)
	handler := New(Deps{
		DB: database, Workspaces: workspace.NewManager(database, filepath.Join(dir, "worktrees")),
		Runtime: runtime,
	})
	handler.agentHandoffPollInterval = 10 * time.Millisecond
	handler.agentHandoffTimeout = 2 * time.Second
	worktree := filepath.Join(dir, "workspace")
	seedReadyWorkspaceForRuntimeTokenTest(t, database, worktree)
	if status != "ready" {
		require.NoError(t, database.UpdateWorkspaceStatus(t.Context(), "ws-runtime-token", status, nil))
	}
	mux := http.NewServeMux()
	api := humago.NewWithPrefix(mux, "/api/v1", huma.DefaultConfig("agent handoff test", "1"))
	handler.Register(api)
	return agentHandoffFixture{handler: handler, database: database, owner: owner, mux: mux}
}

func (f agentHandoffFixture) request(t *testing.T, body map[string]string) *http.Request {
	t.Helper()
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	request := httptest.NewRequest(
		http.MethodPost, "/api/v1/workspaces/ws-runtime-token/runtime/agent-handoffs", bytes.NewReader(encoded),
	)
	request.Header.Set("Content-Type", "application/json")
	return request
}

// serve carries no assertions so it can run on a helper goroutine.
func (f agentHandoffFixture) serve(request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	f.mux.ServeHTTP(recorder, request)
	return recorder
}

func (f agentHandoffFixture) post(t *testing.T, body map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return f.serve(f.request(t, body))
}

func TestAgentHandoffWaitsForReadyThenLaunchesAndDeliversPrompt(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newAgentHandoffFixture(t, "creating")

	// The workspace becomes ready while the handoff is already waiting.
	readyTimer := time.AfterFunc(60*time.Millisecond, func() {
		_ = fixture.database.UpdateWorkspaceStatus(context.Background(), "ws-runtime-token", "ready", nil)
	})
	t.Cleanup(func() { readyTimer.Stop() })

	response := fixture.post(t, map[string]string{
		"target_key": "CoDeX", "message": "rebase this pull request\r\nonto main",
	})
	require.Equal(http.StatusOK, response.Code, response.Body.String())

	var body struct {
		Session struct {
			Key           string `json:"key"`
			TargetKey     string `json:"target_key"`
			Kind          string `json:"kind"`
			DisplayRegion string `json:"display_region"`
		} `json:"session"`
		InitialMessage struct {
			TargetKey    string `json:"target_key"`
			State        string `json:"state"`
			MessageBytes int    `json:"message_bytes"`
		} `json:"initial_message"`
	}
	require.NoError(json.NewDecoder(response.Body).Decode(&body))
	assert.NotEmpty(body.Session.Key)
	assert.Equal("codex", body.Session.TargetKey)
	assert.Equal("agent", body.Session.Kind)
	assert.Equal("workflow", body.Session.DisplayRegion)
	assert.Equal("codex", body.InitialMessage.TargetKey)
	assert.Equal(initialMessageDelivered, body.InitialMessage.State)
	assert.Equal(len("rebase this pull request\nonto main"), body.InitialMessage.MessageBytes)
	assert.Equal(
		"\x1b[200~rebase this pull request\nonto main\x1b[201~\r",
		string(fixture.owner.pty.written()),
	)

	sessions := fixture.handler.runtime.ListSessions("ws-runtime-token")
	require.Len(sessions, 1)
	assert.Equal(body.Session.Key, sessions[0].Key)
}

func TestAgentHandoffRetriesUntilAgentInputModeIsReady(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newAgentHandoffFixture(t, "ready")
	// The fake agent does not announce bracketed paste at start; the handoff
	// must keep retrying without writing until the mode arrives.
	fixture.owner.setEmitBracketedPaste(false)

	request := fixture.request(t, map[string]string{"target_key": "codex", "message": "triage this"})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- fixture.serve(request)
	}()

	var pty *initialMessagePTY
	require.Eventually(func() bool {
		fixture.owner.mu.Lock()
		pty = fixture.owner.pty
		fixture.owner.mu.Unlock()
		return pty != nil && len(fixture.handler.runtime.ListSessions("ws-runtime-token")) == 1
	}, time.Second, 5*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	assert.Empty(pty.written())
	pty.output <- []byte("\x1b[?2004h")

	var response *httptest.ResponseRecorder
	select {
	case response = <-done:
	case <-time.After(3 * time.Second):
		require.FailNow("handoff did not complete after input mode became ready")
	}
	require.Equal(http.StatusOK, response.Code, response.Body.String())
	assert.Equal("\x1b[200~triage this\x1b[201~\r", string(pty.written()))
}

func TestAgentHandoffRejectsInvalidInputBeforeWaiting(t *testing.T) {
	assert := assert.New(t)
	fixture := newAgentHandoffFixture(t, "creating")

	tests := []struct {
		name string
		body map[string]string
		want string
	}{
		{name: "non-agent target", body: map[string]string{"target_key": "shell", "message": "hi"},
			want: "not an available agent launch target"},
		{name: "unknown target", body: map[string]string{"target_key": "nope", "message": "hi"},
			want: "not an available agent launch target"},
		{name: "blank message", body: map[string]string{"target_key": "codex", "message": "  \n"},
			want: "must not be blank"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			started := time.Now()
			response := fixture.post(t, tc.body)
			assert.Equal(http.StatusBadRequest, response.Code, response.Body.String())
			assert.Contains(response.Body.String(), tc.want)
			assert.Less(time.Since(started), 500*time.Millisecond)
		})
	}
	assert.Empty(fixture.handler.runtime.ListSessions("ws-runtime-token"))
}

func TestAgentHandoffReportsWorkspaceSetupFailure(t *testing.T) {
	assert := assert.New(t)
	fixture := newAgentHandoffFixture(t, "creating")
	failure := "clone failed"
	require.NoError(t, fixture.database.UpdateWorkspaceStatus(t.Context(), "ws-runtime-token", "error", &failure))

	response := fixture.post(t, map[string]string{"target_key": "codex", "message": "hi"})
	assert.Equal(http.StatusConflict, response.Code, response.Body.String())
	assert.Contains(response.Body.String(), "workspace setup failed: clone failed")
	assert.Empty(fixture.handler.runtime.ListSessions("ws-runtime-token"))
}

func TestAgentHandoffTimesOutWhileWorkspaceNeverBecomesReady(t *testing.T) {
	assert := assert.New(t)
	fixture := newAgentHandoffFixture(t, "creating")
	fixture.handler.agentHandoffTimeout = 50 * time.Millisecond

	response := fixture.post(t, map[string]string{"target_key": "codex", "message": "hi"})
	assert.Equal(http.StatusServiceUnavailable, response.Code, response.Body.String())
	assert.Contains(response.Body.String(), "timed out waiting for workspace to become ready")
	assert.Empty(fixture.handler.runtime.ListSessions("ws-runtime-token"))
}

func TestAgentHandoffSurvivesClientCancellationWhileWaiting(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newAgentHandoffFixture(t, "creating")

	// The client goes away while the workspace is still provisioning; the
	// accepted handoff must still launch the agent and deliver the prompt.
	requestCtx, cancelRequest := context.WithCancel(t.Context())
	t.Cleanup(cancelRequest)
	request := fixture.request(t, map[string]string{"target_key": "codex", "message": "rebase this"}).
		WithContext(requestCtx)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- fixture.serve(request)
	}()
	// Cancel only after validation has transferred ownership to the handoff.
	require.Eventually(func() bool {
		fixture.handler.lifecycleMu.Lock()
		defer fixture.handler.lifecycleMu.Unlock()
		return fixture.handler.agentHandoffCtx != nil
	}, time.Second, 5*time.Millisecond)
	cancelRequest()
	require.NoError(fixture.database.UpdateWorkspaceStatus(context.Background(), "ws-runtime-token", "ready", nil))

	var response *httptest.ResponseRecorder
	select {
	case response = <-done:
	case <-time.After(3 * time.Second):
		require.FailNow("handoff did not finish after the client canceled")
	}
	require.Equal(http.StatusOK, response.Code, response.Body.String())
	require.NotNil(fixture.owner.pty)
	assert.Equal("\x1b[200~rebase this\x1b[201~\r", string(fixture.owner.pty.written()))
	assert.Len(fixture.handler.runtime.ListSessions("ws-runtime-token"), 1)
}

func TestAgentHandoffCancelsPromptlyOnShutdown(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newAgentHandoffFixture(t, "creating")
	fixture.handler.agentHandoffTimeout = time.Minute

	request := fixture.request(t, map[string]string{"target_key": "codex", "message": "hi"})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- fixture.serve(request)
	}()
	time.Sleep(40 * time.Millisecond)
	started := time.Now()
	fixture.handler.CancelAgentHandoffs()

	var response *httptest.ResponseRecorder
	select {
	case response = <-done:
	case <-time.After(2 * time.Second):
		require.FailNow("handoff kept waiting after shutdown cancellation")
	}
	assert.Less(time.Since(started), time.Second)
	assert.Equal(http.StatusServiceUnavailable, response.Code, response.Body.String())
	assert.Contains(response.Body.String(), "canceled by shutdown")
	assert.Empty(fixture.handler.runtime.ListSessions("ws-runtime-token"))
}

func TestAgentHandoffRefusesToWaitAfterShutdownCancellation(t *testing.T) {
	// Shutdown may cancel before any handoff has created the shared
	// context. A request arriving after that must not start a fresh wait
	// that only the later workspace shutdown could end.
	assert := assert.New(t)
	fixture := newAgentHandoffFixture(t, "creating")
	fixture.handler.agentHandoffTimeout = time.Minute
	fixture.handler.CancelAgentHandoffs()

	started := time.Now()
	response := fixture.post(t, map[string]string{"target_key": "codex", "message": "hi"})

	assert.Less(time.Since(started), time.Second)
	assert.Equal(http.StatusServiceUnavailable, response.Code, response.Body.String())
	assert.Contains(response.Body.String(), "canceled by shutdown")
	assert.Empty(fixture.handler.runtime.ListSessions("ws-runtime-token"))
}

func TestAgentHandoffReportsLaunchedSessionWhenPromptDeliveryTimesOut(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newAgentHandoffFixture(t, "ready")
	// Long enough for the launch itself to finish on a loaded CI runner;
	// only the prompt delivery is meant to hit this deadline.
	fixture.handler.agentHandoffTimeout = 2 * time.Second
	// The agent never enables bracketed paste, so nothing is ever written.
	fixture.owner.setEmitBracketedPaste(false)

	response := fixture.post(t, map[string]string{"target_key": "codex", "message": "hi"})
	require.Equal(http.StatusServiceUnavailable, response.Code, response.Body.String())
	var problem struct {
		Detail  string         `json:"detail"`
		Details map[string]any `json:"details"`
	}
	require.NoError(json.NewDecoder(response.Body).Decode(&problem))
	assert.Contains(problem.Detail, "timed out waiting for agent input")

	// The agent is still running without its prompt, and the problem says so.
	sessions := fixture.handler.runtime.ListSessions("ws-runtime-token")
	require.Len(sessions, 1)
	assert.Equal(sessions[0].Key, problem.Details["session_key"])
	assert.Equal("codex", problem.Details["target_key"])
	assert.Equal("not_delivered", problem.Details["initial_message_state"])
	assert.Empty(fixture.owner.pty.written())
}

func TestAgentHandoffDeliveryPreservesCancellationCause(t *testing.T) {
	for _, cause := range []error{context.DeadlineExceeded, context.Canceled} {
		t.Run(cause.Error(), func(t *testing.T) {
			fixture := newAgentHandoffFixture(t, "ready")
			ctx, cancel := context.WithCancelCause(t.Context())
			cancel(cause)

			_, err := fixture.handler.deliverInitialMessage(ctx, InitialMessageRequest{
				WorkspaceID: "ws-runtime-token", RuntimeSessionKey: "session-1",
				TargetKey: "codex", Message: "hi",
			})

			var problem *httpapi.ProblemError
			require.ErrorAs(t, err, &problem)
			assert.Equal(t, http.StatusServiceUnavailable, problem.Status)
			assert.Equal(t, handoffWaitError(cause, "agent input to become ready").Error(), problem.Error())
		})
	}
}
