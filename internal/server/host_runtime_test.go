package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/db"
)

func TestHostRuntimeCommandSessionLifecycle(t *testing.T) {
	requirePTYAvailable(t)
	require := require.New(t)
	assert := Assert.New(t)

	srv, _, _, _ := setupProjectWorktreeCommandSessionTestWithRecord(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	cwd := t.TempDir()
	body := mustMarshal(t, map[string]any{
		"session_key": "surface:host:console:console:root",
		"command":     []string{"/bin/sh", "-lc", "exec sleep 60"},
		"env":         map[string]string{"CUSTOM_SESSION_VAR": "custom-value"},
		"label":       "Console",
		"cwd":         cwd,
	})
	resp := httpDo(t, ts, http.MethodPost, "/api/v1/runtime/sessions", body)
	require.Equal(http.StatusOK, resp.StatusCode)
	var session map[string]any
	require.NoError(json.NewDecoder(resp.Body).Decode(&session))
	resp.Body.Close()
	assert.Equal("surface:host:console:console:root", session["key"])
	assert.Equal("Console", session["label"])
	assert.Equal("command", session["kind"])
	tmuxSession, _ := session["tmux_session"].(string)
	require.NotEmpty(tmuxSession)

	// Re-ensure returns the live session instead of launching again.
	resp = httpDo(t, ts, http.MethodPost, "/api/v1/runtime/sessions", body)
	require.Equal(http.StatusOK, resp.StatusCode)
	var second map[string]any
	require.NoError(json.NewDecoder(resp.Body).Decode(&second))
	resp.Body.Close()
	assert.Equal(session["key"], second["key"])
	assert.Equal(tmuxSession, second["tmux_session"])

	resp = httpDo(t, ts, http.MethodGet, "/api/v1/runtime/sessions", nil)
	require.Equal(http.StatusOK, resp.StatusCode)
	var listBody struct {
		Sessions []map[string]any `json:"sessions"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&listBody))
	resp.Body.Close()
	require.Len(listBody.Sessions, 1)
	assert.Equal("Console", listBody.Sessions[0]["label"])

	resp = httpDo(t, ts, http.MethodGet,
		"/api/v1/runtime/sessions/surface:host:console:console:root/attach-spec",
		nil,
	)
	require.Equal(http.StatusOK, resp.StatusCode)
	var spec map[string]any
	require.NoError(json.NewDecoder(resp.Body).Decode(&spec))
	resp.Body.Close()
	assert.Equal("tmux", spec["kind"])
	assert.Equal(tmuxSession, spec["tmux_session"])

	resp = httpDo(t, ts, http.MethodDelete,
		"/api/v1/runtime/sessions/surface:host:console:console:root", nil,
	)
	require.Equal(http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()

	resp = httpDo(t, ts, http.MethodGet, "/api/v1/runtime/sessions", nil)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NoError(json.NewDecoder(resp.Body).Decode(&listBody))
	resp.Body.Close()
	assert.Empty(listBody.Sessions)
}

func TestHostRuntimeCommandSessionValidation(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	srv, _, _, _ := setupProjectWorktreeCommandSessionTestWithRecord(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	cases := []struct {
		payload    map[string]any
		wantStatus int
	}{
		// command is required (huma schema validation).
		{map[string]any{"cwd": "/tmp"}, http.StatusUnprocessableEntity},
		// cwd is required (huma schema validation).
		{
			map[string]any{"command": []string{"/bin/sh"}},
			http.StatusUnprocessableEntity,
		},
		// env keys must be shell identifiers (handler validation).
		{
			map[string]any{
				"command": []string{"/bin/sh"},
				"cwd":     "/tmp",
				"env":     map[string]string{"BAD-KEY": "v"},
			},
			http.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		resp := httpDo(t, ts, http.MethodPost,
			"/api/v1/runtime/sessions", mustMarshal(t, tc.payload),
		)
		body, err := io.ReadAll(resp.Body)
		require.NoError(err)
		resp.Body.Close()
		assert.Equal(
			tc.wantStatus, resp.StatusCode,
			"payload %v: %s", tc.payload, string(body),
		)
		assert.Contains(
			string(body), "validationError",
			"payload %v", tc.payload,
		)
	}
}

func TestHostRuntimeStoredSessionSurvivesRestart(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	srv, _, _, recordPath := setupProjectWorktreeCommandSessionTestWithRecord(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// A stored row without a live runtime session models a host session
	// surviving from before a middleman restart.
	require.NoError(srv.db.UpsertHostRuntimeTmuxSession(
		context.Background(), &db.HostRuntimeTmuxSession{
			SessionKey:  "surface:host:console:console:root",
			SessionName: "middleman-stored-console",
			Label:       "Stored Console",
			CWD:         "/tmp",
		},
	))

	resp := httpDo(t, ts, http.MethodGet, "/api/v1/runtime/sessions", nil)
	require.Equal(http.StatusOK, resp.StatusCode)
	var listBody struct {
		Sessions []map[string]any `json:"sessions"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&listBody))
	resp.Body.Close()
	require.Len(listBody.Sessions, 1)
	assert.Equal("Stored Console", listBody.Sessions[0]["label"])
	assert.Equal(
		"middleman-stored-console", listBody.Sessions[0]["tmux_session"],
	)

	resp = httpDo(t, ts, http.MethodDelete,
		"/api/v1/runtime/sessions/surface:host:console:console:root", nil,
	)
	require.Equal(http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()

	assertFakeTmuxKilledSession(t, recordPath, "middleman-stored-console")
	rows, err := srv.db.ListHostRuntimeTmuxSessions(context.Background())
	require.NoError(err)
	assert.Empty(rows)
}

// TestHostRuntimeCommandSessionExpandsHomeCWD proves a home-relative cwd
// ("~" or "~/...") is resolved against the daemon's own home before the
// session launches. Fleet clients send home-relative cwds because only the
// executing host knows its home directory.
func TestHostRuntimeCommandSessionExpandsHomeCWD(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	home, err := os.UserHomeDir()
	require.NoError(err)

	srv, _, _, recordPath := setupProjectWorktreeCommandSessionTestWithRecord(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	body := mustMarshal(t, map[string]any{
		"session_key": "surface:host:console:home",
		"command":     []string{"/bin/sh", "-lc", "exec sleep 60"},
		"cwd":         "~",
	})
	resp := httpDo(t, ts, http.MethodPost, "/api/v1/runtime/sessions", body)
	require.Equal(http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	record, err := os.ReadFile(recordPath)
	require.NoError(err)
	args := strings.Split(string(record), "\x00")
	cwdArg := ""
	for i, arg := range args {
		if arg == "-c" && i+1 < len(args) {
			cwdArg = args[i+1]
		}
	}
	assert.Equal(home, cwdArg,
		"tmux new-session must receive the expanded home, not a literal ~")
}

// TestFleetHostRuntimeSessionRoutesE2E exercises the fleet host-level
// runtime session routes end-to-end over the real server mux: launch
// through POST /api/v1/fleet/hosts/self/runtime/sessions (resolved via
// the self alias to the local handler), observe the session in the
// fleet listing, and stop it through the fleet DELETE route. This pins
// the /api/v1 mounting and proxy wiring, not just the handler.
func TestFleetHostRuntimeSessionRoutesE2E(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	srv, _, _, recordPath := setupProjectWorktreeCommandSessionTestWithRecord(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	body := mustMarshal(t, map[string]any{
		"session_key": "surface:host:console:fleet-e2e",
		"command":     []string{"/bin/sh", "-lc", "exec sleep 60"},
		"label":       "Fleet Console",
		"cwd":         t.TempDir(),
	})
	resp := httpDo(t, ts, http.MethodPost,
		"/api/v1/fleet/hosts/self/runtime/sessions", body)
	require.Equal(http.StatusOK, resp.StatusCode,
		"fleet self-alias launch must reach the local handler")
	var launched struct {
		Key         string `json:"key"`
		TmuxSession string `json:"tmux_session"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&launched))
	resp.Body.Close()
	require.Equal("surface:host:console:fleet-e2e", launched.Key)
	require.NotEmpty(launched.TmuxSession)

	// The fleet surface carries launch/stop/attach-spec; listings come
	// from the snapshot. The local listing confirms the launch landed.
	resp = httpDo(t, ts, http.MethodGet, "/api/v1/runtime/sessions", nil)
	require.Equal(http.StatusOK, resp.StatusCode)
	var listBody struct {
		Sessions []map[string]any `json:"sessions"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&listBody))
	resp.Body.Close()
	require.Len(listBody.Sessions, 1)
	assert.Equal("Fleet Console", listBody.Sessions[0]["label"])

	resp = httpDo(t, ts, http.MethodDelete,
		"/api/v1/fleet/hosts/self/runtime/sessions/surface:host:console:fleet-e2e",
		nil)
	require.Equal(http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()

	assertFakeTmuxKilledSession(t, recordPath, launched.TmuxSession)
}
