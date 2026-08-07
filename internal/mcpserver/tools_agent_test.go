package mcpserver

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListAgentTargetsFiltersToSupportedHookAgentsWithoutArgv(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/settings", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"launch_targets":[
			{"key":"gemini","label":"Gemini","kind":"agent","source":"builtin","command":["gemini","--secret"],"available":true},
			{"key":"opencode","label":"OpenCode","kind":"agent","source":"builtin","command":["opencode"],"available":true},
			{"key":"claude","label":"Claude","kind":"shell","source":"config","command":["claude"],"available":true},
			{"key":"codex","label":"Codex","kind":"agent","source":"config","command":["codex","--profile","private"],"available":false,"disabled_reason":"disabled by config"},
			{"key":"custom","label":"Custom","kind":"agent","source":"config","command":["custom"],"available":true}
		]}`))
	})
	s := newMCPTestServer(t, mux)

	out, err := s.listAgentTargets(t.Context(), listAgentTargetsInput{})
	require.NoError(err)
	require.Len(out.Targets, 2)
	assert.Equal("codex", out.Targets[0].Key)
	assert.Equal("disabled by config", out.Targets[0].DisabledReason)
	assert.False(out.Targets[0].Available)
	assert.Equal("gemini", out.Targets[1].Key)
	assert.True(out.Targets[1].Available)

	raw, err := json.Marshal(out)
	require.NoError(err)
	assert.NotContains(string(raw), "command")
	assert.NotContains(string(raw), "--secret")
	assert.NotContains(string(raw), "private")
}

func TestListWorkspaceAgentSessionsMapsLiveProjectionDeterministically(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/workspaces/ws-1/agent-sessions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sessions":[
			{"agent":"codex","session_id":"session-b","runtime_session_key":"runtime-b","target_key":"codex","state":"done","updated_at":"2026-08-07T14:00:00Z"},
			{"agent":"claude","session_id":"session-a","runtime_session_key":"runtime-a","target_key":"claude","state":"working","updated_at":"2026-08-07T15:00:00Z","initial_message":{"agent":"claude","session_id":"session-a","state":"delivered","message_bytes":12,"reserved_at":"2026-08-07T14:59:00Z","delivered_at":"2026-08-07T14:59:01Z"}}
		]}`))
	})
	s := newMCPTestServer(t, mux)

	out, err := s.listWorkspaceAgentSessions(
		t.Context(), listWorkspaceAgentSessionsInput{WorkspaceID: "ws-1"},
	)
	require.NoError(err)
	require.Len(out.Sessions, 2)
	assert.Equal("claude", out.Sessions[0].Agent)
	assert.Equal("session-a", out.Sessions[0].SessionID)
	assert.Equal("runtime-a", out.Sessions[0].RuntimeSessionKey)
	assert.Equal("working", out.Sessions[0].State)
	require.NotNil(out.Sessions[0].InitialMessage)
	assert.Equal("delivered", out.Sessions[0].InitialMessage.State)
	assert.Equal(12, out.Sessions[0].InitialMessage.MessageBytes)
	assert.Equal("codex", out.Sessions[1].Agent)
	assert.Nil(out.Sessions[1].InitialMessage)
}

func TestListWorkspaceAgentSessionsRequiresWorkspaceID(t *testing.T) {
	s := newMCPTestServer(t, http.NewServeMux())
	_, err := s.listWorkspaceAgentSessions(t.Context(), listWorkspaceAgentSessionsInput{})
	require.ErrorContains(t, err, "workspace_id is required")
}
