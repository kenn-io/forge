package mcpserver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListAgentTargetsIncludesCustomAgentsWithoutCommands(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	backend := &fakeBackend{listLaunchTargetsFn: func(context.Context) ([]LaunchTarget, error) {
		return []LaunchTarget{
			{Key: "gemini", Label: "Gemini", Kind: "agent", Source: "builtin", Available: true},
			{Key: "opencode", Label: "OpenCode", Kind: "agent", Source: "builtin", Available: true},
			{Key: "claude", Label: "Claude", Kind: "shell", Source: "config", Available: true},
			{Key: "codex", Label: "Codex", Kind: "agent", Source: "config", DisabledReason: "disabled by config"},
			{Key: "custom", Label: "Custom", Kind: "agent", Source: "config", Available: true},
			{Key: "chat", Label: "Chat", Kind: "acp", Source: "config", Available: true},
		}, nil
	}}
	s := newMCPTestServer(t, backend)

	out, err := s.listAgentTargets(t.Context(), listAgentTargetsInput{})

	require.NoError(err)
	keys := make([]string, 0, len(out.Targets))
	protocols := make([]string, 0, len(out.Targets))
	for _, target := range out.Targets {
		keys = append(keys, target.Key)
		protocols = append(protocols, target.Protocol)
	}
	assert.Equal([]string{"chat", "codex", "custom", "gemini", "opencode"}, keys)
	assert.Equal([]string{"acp", "terminal", "terminal", "terminal", "terminal"}, protocols)
	assert.False(out.Targets[1].Available)
	raw, err := json.Marshal(out)
	require.NoError(err)
	assert.NotContains(string(raw), "command")
}

func TestSendAgentMessageSubmitsToACPRuntime(t *testing.T) {
	var got AgentMessageRequest
	backend := &fakeBackend{
		getWorkspaceRuntimeFn: func(context.Context, string) (WorkspaceRuntime, error) {
			return WorkspaceRuntime{Sessions: []RuntimeSession{{Key: "chat", TargetKey: "chat", Kind: "acp", Status: "running"}}}, nil
		},
		submitAgentMessageFn: func(_ context.Context, request AgentMessageRequest) (AgentMessageResult, error) {
			got = request
			return AgentMessageResult{TargetKey: "chat", MessageBytes: 5}, nil
		},
	}
	s := newMCPTestServer(t, backend)

	out, err := s.sendAgentMessage(t.Context(), sendAgentMessageInput{
		WorkspaceID: "workspace", RuntimeSessionKey: "chat", Message: "hello",
	})

	require.NoError(t, err)
	assert.Equal(t, AgentMessageRequest{WorkspaceID: "workspace", RuntimeSessionKey: "chat", Message: "hello"}, got)
	assert.Equal(t, "chat", out.TargetKey)
}

func TestSendAgentMessageRejectsNonAgentRuntime(t *testing.T) {
	submitted := false
	backend := &fakeBackend{
		getWorkspaceRuntimeFn: func(context.Context, string) (WorkspaceRuntime, error) {
			return WorkspaceRuntime{Sessions: []RuntimeSession{{Key: "shell", Kind: "plain_shell", Status: "running"}}}, nil
		},
		submitAgentMessageFn: func(context.Context, AgentMessageRequest) (AgentMessageResult, error) {
			submitted = true
			return AgentMessageResult{}, nil
		},
	}
	s := newMCPTestServer(t, backend)

	_, err := s.sendAgentMessage(t.Context(), sendAgentMessageInput{
		WorkspaceID: "workspace", RuntimeSessionKey: "shell", Message: "hello",
	})

	require.ErrorContains(t, err, "not a live coding agent")
	assert.False(t, submitted)
}

func TestListWorkspaceAgentSessionsMapsLiveProjectionDeterministically(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	deliveredAt := time.Date(2026, 8, 7, 14, 59, 1, 0, time.UTC)
	var workspaceID string
	backend := &fakeBackend{listWorkspaceAgentSessionsFn: func(
		_ context.Context, id string,
	) ([]WorkspaceAgentSession, error) {
		workspaceID = id
		return []WorkspaceAgentSession{
			{
				Agent: "codex", SessionID: "session-b", RuntimeSessionKey: "runtime-b",
				TargetKey: "codex", State: "done",
				UpdatedAt: time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC),
			},
			{
				Agent: "claude", SessionID: "session-a", RuntimeSessionKey: "runtime-a",
				TargetKey: "claude", State: "working",
				UpdatedAt: time.Date(2026, 8, 7, 15, 0, 0, 0, time.UTC),
				InitialMessage: &InitialMessageStatus{
					State: "delivered", MessageBytes: 12, DeliveredAt: &deliveredAt,
				},
			},
		}, nil
	}}
	backend.getWorkspaceRuntimeFn = func(context.Context, string) (WorkspaceRuntime, error) {
		return WorkspaceRuntime{Sessions: []RuntimeSession{
			{
				Key: "runtime-b", TargetKey: "codex", Kind: "agent", Status: "running",
				CreatedAt: time.Date(2026, 8, 7, 13, 0, 0, 0, time.UTC),
			},
			{
				Key: "runtime-a", TargetKey: "claude", Kind: "agent", Status: "running",
				CreatedAt: time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC),
			},
		}}, nil
	}
	s := newMCPTestServer(t, backend)

	out, err := s.listWorkspaceAgentSessions(
		t.Context(), listWorkspaceAgentSessionsInput{WorkspaceID: "ws-1"},
	)

	require.NoError(err)
	assert.Equal("ws-1", workspaceID)
	require.Len(out.Sessions, 2)
	assert.Equal("claude", out.Sessions[0].Agent)
	require.NotNil(out.Sessions[0].InitialMessage)
	assert.Equal("delivered", out.Sessions[0].InitialMessage.State)
	assert.Equal("2026-08-07T14:59:01Z", out.Sessions[0].InitialMessage.DeliveredAt)
	assert.Equal("codex", out.Sessions[1].Agent)
	require.Len(out.Runtimes, 2)
	assert.True(out.Runtimes[0].HookObserved)
	assert.True(out.Runtimes[1].HookObserved)
}

func TestListWorkspaceAgentSessionsShowsRuntimeBeforeFirstHook(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	backend := &fakeBackend{
		getWorkspaceRuntimeFn: func(context.Context, string) (WorkspaceRuntime, error) {
			return WorkspaceRuntime{Sessions: []RuntimeSession{
				{
					Key: "runtime-waiting", TargetKey: "codex", Kind: "agent", Status: "running",
					CreatedAt: time.Date(2026, 8, 7, 15, 0, 0, 0, time.UTC),
				},
				{Key: "shell", TargetKey: "shell", Kind: "shell", Status: "running"},
			}}, nil
		},
		listWorkspaceAgentSessionsFn: func(context.Context, string) ([]WorkspaceAgentSession, error) {
			return nil, nil
		},
	}
	s := newMCPTestServer(t, backend)

	out, err := s.listWorkspaceAgentSessions(
		t.Context(), listWorkspaceAgentSessionsInput{WorkspaceID: "ws-1"},
	)

	require.NoError(err)
	require.Len(out.Runtimes, 1)
	assert.Equal("runtime-waiting", out.Runtimes[0].RuntimeSessionKey)
	assert.False(out.Runtimes[0].HookObserved)
	assert.Empty(out.Sessions)
}
