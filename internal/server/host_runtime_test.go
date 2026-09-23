package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/db"
)

func TestHostRuntimeStoredSessionSurvivesRestart(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	srv, _, _, recordPath := setupProjectWorktreeCommandSessionTestWithRecord(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// A stored row without a live runtime session models a host session
	// surviving from before a kenn-forge restart.
	require.NoError(srv.db.UpsertHostRuntimeTmuxSession(
		t.Context(), &db.HostRuntimeTmuxSession{
			SessionKey:  "surface:host:console:console:root",
			SessionName: "kenn-forge-stored-console",
			Label:       "Stored Console",
			CWD:         "/tmp",
		},
	))

	resp := httpDo(t, ts, http.MethodGet, "/api/v1/runtime/sessions", nil)
	t.Cleanup(func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	})
	require.Equal(http.StatusOK, resp.StatusCode)
	var listBody struct {
		Sessions []map[string]any `json:"sessions"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&listBody))
	resp.Body.Close()
	require.Len(listBody.Sessions, 1)
	assert.Equal("Stored Console", listBody.Sessions[0]["label"])
	assert.Equal(
		"kenn-forge-stored-console", listBody.Sessions[0]["tmux_session"],
	)

	resp = httpDo(t, ts, http.MethodDelete,
		"/api/v1/runtime/sessions/surface:host:console:console:root", nil,
	)
	require.Equal(http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()

	assertFakeTmuxKilledSession(t, recordPath, "kenn-forge-stored-console")
	rows, err := srv.db.ListHostRuntimeTmuxSessions(t.Context())
	require.NoError(err)
	assert.Empty(rows)
}
