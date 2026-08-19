package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSpawnWorkspaceWithAgentCallsDirectServicesAndUsesAuthoritativeEvidence(t *testing.T) {
	backend := successfulSpawnBackend("ws-new", "runtime-new", "coding-new")
	workspaceReads := 0
	backend.createPullWorkspaceFn = func(_ context.Context, item ItemIdentity, suppress bool) (Workspace, error) {
		assert.Equal(t, testItemIdentity("pr", 42), item)
		assert.True(t, suppress)
		return Workspace{ID: "ws-new", Status: "creating", Created: true}, nil
	}
	backend.getWorkspaceFn = func(context.Context, string) (Workspace, error) {
		workspaceReads++
		status := "creating"
		if workspaceReads > 1 {
			status = "ready"
		}
		return Workspace{ID: "ws-new", Status: status}, nil
	}
	backend.submitInitialMessageFn = func(_ context.Context, req InitialMessageRequest) (InitialMessageStatus, error) {
		assert.Equal(t, InitialMessageRequest{
			WorkspaceID: "ws-new", RuntimeSessionKey: "runtime-new",
			Agent: "codex", SessionID: "coding-new",
			Message: "review this\nthen implement",
		}, req)
		deliveredAt := time.Date(2026, 8, 7, 15, 0, 2, 0, time.UTC)
		return InitialMessageStatus{State: "delivered", MessageBytes: 26, DeliveredAt: &deliveredAt}, nil
	}
	s := newMCPTestServer(t, backend)

	out, err := s.spawnWorkspaceWithAgent(t.Context(), spawnWorkspaceWithAgentInput{
		Source: workspaceSourceInput{Type: "item", Item: &itemRefInput{
			Type: "pr", Provider: "github", PlatformHost: "github.com",
			PlatformRepoID: "repo-acme-widget",
			Owner:          "acme", Name: "widget", Number: 42,
		}},
		AgentTarget: "codex", InitialMessage: "review this\r\nthen implement", Timeout: "2s",
	})

	require.NoError(t, err)
	assert.Equal(t, "message_delivered", out.Stage)
	assert.Equal(t, "delivered", out.InitialMessage.State)
	assert.Equal(t, "ws-new", out.Workspace.ID)
	assert.False(t, out.Workspace.Reused)
	assert.Equal(t, "runtime-new", out.Runtime.SessionKey)
	assert.Equal(t, "coding-new", out.CodingSession.SessionID)
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), `"message_delivered":`)
}

func TestSpawnWorkspaceWithAgentReusesWorkspaceAndLaunchesFreshRuntime(t *testing.T) {
	backend := successfulSpawnBackend("ws-existing", "runtime-fresh", "coding-fresh")
	backend.getPullFn = func(context.Context, ItemIdentity) (PullDetail, error) {
		return PullDetail{
			Pull:      &Pull{Number: 42, Repository: testRepository()},
			Workspace: &WorkspaceRef{ID: "ws-existing", Status: "ready"},
		}, nil
	}
	backend.createPullWorkspaceFn = func(context.Context, ItemIdentity, bool) (Workspace, error) {
		return Workspace{}, errors.New("workspace create must not run")
	}
	s := newMCPTestServer(t, backend)

	out, err := s.spawnWorkspaceWithAgent(t.Context(), prSpawnInput("start"))

	require.NoError(t, err)
	assert.True(t, out.Workspace.Reused)
	assert.Equal(t, "runtime-fresh", out.Runtime.SessionKey)
}

func TestSpawnWorkspaceWithAgentReusesPRWorkspaceCreatedConcurrently(t *testing.T) {
	backend := successfulSpawnBackend("ws-raced", "runtime-raced", "coding-raced")
	pullReads := 0
	backend.getPullFn = func(context.Context, ItemIdentity) (PullDetail, error) {
		pullReads++
		detail := PullDetail{Pull: &Pull{Number: 42, Repository: testRepository()}}
		if pullReads > 1 {
			detail.Workspace = &WorkspaceRef{ID: "ws-raced", Status: "ready"}
		}
		return detail, nil
	}
	backend.createPullWorkspaceFn = func(context.Context, ItemIdentity, bool) (Workspace, error) {
		return Workspace{}, &Error{
			Kind: "conflict", Code: ErrorCodeWorkspaceAlreadyExists,
			Message: "workspace already exists for this pull request",
		}
	}
	s := newMCPTestServer(t, backend)

	out, err := s.spawnWorkspaceWithAgent(t.Context(), prSpawnInput("start"))

	require.NoError(t, err)
	assert.True(t, out.Workspace.Reused)
	assert.Equal(t, "ws-raced", out.Workspace.ID)
	assert.Equal(t, 2, pullReads)
}

func TestSpawnWorkspaceWithAgentTimeoutReturnsStageWithoutDeliveryClaim(t *testing.T) {
	backend := successfulSpawnBackend("ws-timeout", "runtime", "coding")
	backend.createPullWorkspaceFn = func(context.Context, ItemIdentity, bool) (Workspace, error) {
		return Workspace{ID: "ws-timeout", Status: "creating", Created: true}, nil
	}
	backend.getWorkspaceFn = func(ctx context.Context, _ string) (Workspace, error) {
		<-ctx.Done()
		return Workspace{ID: "ws-timeout", Status: "creating"}, context.Cause(ctx)
	}
	s := newMCPTestServer(t, backend)
	input := prSpawnInput("start")
	input.Timeout = "20ms"

	_, err := s.spawnWorkspaceWithAgent(t.Context(), input)

	var backendErr *Error
	require.ErrorAs(t, err, &backendErr)
	assert.Equal(t, "agent_handoff_timeout", backendErr.Kind)
	assert.Equal(t, "workspace_created", backendErr.Details["last_completed_stage"])
	assert.Equal(t, "workspace_ready", backendErr.Details["failed_stage"])
	assert.Equal(t, "ws-timeout", backendErr.Details["workspace_id"])
	assert.NotContains(t, backendErr.Details, "message_delivered")
}

func TestSpawnWorkspaceWithAgentReportsWorkspaceError(t *testing.T) {
	message := "clone failed"
	backend := successfulSpawnBackend("ws-error", "runtime", "coding")
	backend.createPullWorkspaceFn = func(context.Context, ItemIdentity, bool) (Workspace, error) {
		return Workspace{ID: "ws-error", Status: "creating", Created: true}, nil
	}
	backend.getWorkspaceFn = func(context.Context, string) (Workspace, error) {
		return Workspace{ID: "ws-error", Status: "error", ErrorMessage: &message}, nil
	}
	s := newMCPTestServer(t, backend)

	_, err := s.spawnWorkspaceWithAgent(t.Context(), prSpawnInput("start"))

	var backendErr *Error
	require.ErrorAs(t, err, &backendErr)
	assert.Equal(t, "error", backendErr.Details["workspace_status"])
	assert.Contains(t, backendErr.Message, "clone failed")
}

func TestSpawnWorkspaceWithAgentCreatesIssueAndAdHocWorkspaces(t *testing.T) {
	t.Run("issue", func(t *testing.T) {
		backend := successfulSpawnBackend("ws-issue", "runtime-issue", "coding-issue")
		backend.getIssueFn = func(context.Context, ItemIdentity) (IssueDetail, error) {
			return IssueDetail{Issue: &Issue{Number: 7, Repository: testRepository()}}, nil
		}
		backend.createIssueWorkspaceFn = func(_ context.Context, item ItemIdentity, suppress bool) (Workspace, error) {
			assert.Equal(t, testItemIdentity("issue", 7), item)
			assert.True(t, suppress)
			return Workspace{ID: "ws-issue", Status: "ready", Created: true}, nil
		}
		s := newMCPTestServer(t, backend)

		out, err := s.spawnWorkspaceWithAgent(t.Context(), spawnWorkspaceWithAgentInput{
			Source: workspaceSourceInput{Type: "item", Item: &itemRefInput{
				Type: "issue", Provider: "github", PlatformRepoID: "repo-acme-widget",
				Owner: "acme", Name: "widget", Number: 7,
			}},
			AgentTarget: "codex", InitialMessage: "fix the issue", Timeout: "2s",
		})
		require.NoError(t, err)
		assert.Equal(t, "ws-issue", out.Workspace.ID)
	})

	t.Run("ad hoc", func(t *testing.T) {
		backend := successfulSpawnBackend("ws-adhoc", "runtime-adhoc", "coding-adhoc")
		backend.createAdHocWorkspaceFn = func(_ context.Context, repo RepositoryIdentity, branch string) (Workspace, error) {
			assert.Equal(t, testRepository(), repo)
			assert.Empty(t, branch)
			return Workspace{
				ID: "ws-adhoc", Status: "ready", Created: true,
				GitHeadRef: "kenn-forge/work-abc123",
			}, nil
		}
		s := newMCPTestServer(t, backend)

		out, err := s.spawnWorkspaceWithAgent(t.Context(), spawnWorkspaceWithAgentInput{
			Source: workspaceSourceInput{Type: "adhoc", AdHoc: &adHocWorkspaceSource{
				Repo: repoFilterInput{
					Provider: "github", PlatformRepoID: "repo-acme-widget",
					Owner: "acme", Name: "widget",
				},
			}},
			AgentTarget: "codex", InitialMessage: "start work", Timeout: "2s",
		})
		require.NoError(t, err)
		assert.Equal(t, "kenn-forge/work-abc123", out.Source.AdHoc.Branch)
	})
}

func TestSpawnWorkspaceWithAgentRetriesMultilineMessageUntilInputModeIsReady(t *testing.T) {
	backend := successfulSpawnBackend("ws-paste", "runtime-paste", "coding-paste")
	messagePosts := 0
	backend.submitInitialMessageFn = func(context.Context, InitialMessageRequest) (InitialMessageStatus, error) {
		messagePosts++
		if messagePosts < 3 {
			return InitialMessageStatus{}, &Error{
				Kind: "unavailable", Code: ErrorCodeInitialMessageInputModeNotReady,
				Message: "agent terminal input mode is not ready", Retryable: true,
			}
		}
		return InitialMessageStatus{State: "delivered", MessageBytes: 12}, nil
	}
	s := newMCPTestServer(t, backend)

	out, err := s.spawnWorkspaceWithAgent(t.Context(), prSpawnInput("first\nsecond"))

	require.NoError(t, err)
	assert.Equal(t, "message_delivered", out.Stage)
	assert.Equal(t, "delivered", out.InitialMessage.State)
	assert.Equal(t, 3, messagePosts)
}

func TestSpawnWorkspaceWithAgentRecoversAmbiguousMessageFromSameBackend(t *testing.T) {
	backend := successfulSpawnBackend("ws-recovery", "runtime-recovery", "coding-recovery")
	messagePosts := 0
	statusReads := 0
	backend.submitInitialMessageFn = func(context.Context, InitialMessageRequest) (InitialMessageStatus, error) {
		messagePosts++
		return InitialMessageStatus{}, &Error{
			Kind: "internal_error", Message: "result unknown", Ambiguous: true,
		}
	}
	backend.getInitialMessageFn = func(context.Context, string, string) (InitialMessageStatus, error) {
		statusReads++
		deliveredAt := time.Date(2026, 8, 7, 15, 0, 2, 0, time.UTC)
		return InitialMessageStatus{State: "delivered", MessageBytes: 5, DeliveredAt: &deliveredAt}, nil
	}
	s := newMCPTestServer(t, backend)

	out, err := s.spawnWorkspaceWithAgent(t.Context(), prSpawnInput("start"))

	require.NoError(t, err)
	assert.Equal(t, "message_delivered", out.Stage)
	assert.Equal(t, "delivered", out.InitialMessage.State)
	assert.Equal(t, 1, messagePosts)
	assert.Equal(t, 1, statusReads)
}

func TestSpawnWorkspaceWithAgentTreatsPendingMessageStateAsAmbiguous(t *testing.T) {
	backend := successfulSpawnBackend("ws-pending", "runtime-pending", "coding-pending")
	backend.submitInitialMessageFn = func(context.Context, InitialMessageRequest) (InitialMessageStatus, error) {
		return InitialMessageStatus{State: "pending", MessageBytes: 5}, nil
	}
	s := newMCPTestServer(t, backend)

	_, err := s.spawnWorkspaceWithAgent(t.Context(), prSpawnInput("start"))

	var backendErr *Error
	require.ErrorAs(t, err, &backendErr)
	assert.True(t, backendErr.Ambiguous)
	assert.Equal(t, "pending", backendErr.Details["initial_message_state"])
	assert.NotContains(t, backendErr.Details, "message_delivered")
}

func TestSpawnWorkspaceWithAgentKeepsUncertainRecoveryAmbiguous(t *testing.T) {
	backend := successfulSpawnBackend("ws-recovery", "runtime-recovery", "coding-recovery")
	backend.submitInitialMessageFn = func(context.Context, InitialMessageRequest) (InitialMessageStatus, error) {
		return InitialMessageStatus{}, &Error{Kind: "internal_error", Message: "result unknown", Ambiguous: true}
	}
	backend.getInitialMessageFn = func(context.Context, string, string) (InitialMessageStatus, error) {
		return InitialMessageStatus{State: "uncertain", MessageBytes: 5}, nil
	}
	s := newMCPTestServer(t, backend)

	_, err := s.spawnWorkspaceWithAgent(t.Context(), prSpawnInput("start"))

	var backendErr *Error
	require.ErrorAs(t, err, &backendErr)
	assert.True(t, backendErr.Ambiguous)
	assert.Equal(t, "uncertain", backendErr.Details["initial_message_state"])
	assert.Equal(t, "message_delivered", backendErr.Details["failed_stage"])
	assert.NotContains(t, backendErr.Details, "message_delivered")
}

func TestSpawnWorkspaceWithAgentReportsRuntimeExitBeforeHookSession(t *testing.T) {
	backend := successfulSpawnBackend("ws-exit", "runtime-exit", "coding")
	backend.listWorkspaceAgentSessionsFn = func(context.Context, string) ([]WorkspaceAgentSession, error) {
		return nil, nil
	}
	backend.getWorkspaceRuntimeFn = func(context.Context, string) (WorkspaceRuntime, error) {
		return WorkspaceRuntime{Sessions: []RuntimeSession{{Key: "runtime-exit", Status: "error"}}}, nil
	}
	messagePosts := 0
	backend.submitInitialMessageFn = func(context.Context, InitialMessageRequest) (InitialMessageStatus, error) {
		messagePosts++
		return InitialMessageStatus{}, nil
	}
	s := newMCPTestServer(t, backend)

	_, err := s.spawnWorkspaceWithAgent(t.Context(), prSpawnInput("start"))

	var backendErr *Error
	require.ErrorAs(t, err, &backendErr)
	assert.Equal(t, "coding_session_observed", backendErr.Details["failed_stage"])
	assert.Contains(t, backendErr.Message, "runtime exited")
	assert.Equal(t, 0, messagePosts)
}

func TestSpawnWorkspaceWithAgentRejectsInvalidInputBeforeBackendCalls(t *testing.T) {
	calls := 0
	backend := &fakeBackend{listLaunchTargetsFn: func(context.Context) ([]LaunchTarget, error) {
		calls++
		return nil, nil
	}}
	s := newMCPTestServer(t, backend)
	inputs := []spawnWorkspaceWithAgentInput{
		{AgentTarget: "codex", InitialMessage: "start", Timeout: "16m"},
		{
			Source: workspaceSourceInput{Type: "item", Item: &itemRefInput{
				Type: "pr", Provider: "github", PlatformRepoID: "repo-acme-widget",
				Owner: "acme", Name: "widget", Number: 42,
			}},
			AgentTarget: "codex", InitialMessage: " \n\t",
		},
	}
	for _, input := range inputs {
		_, err := s.spawnWorkspaceWithAgent(t.Context(), input)
		require.Error(t, err)
	}
	assert.Equal(t, 0, calls)
}

func successfulSpawnBackend(workspaceID, runtimeKey, codingSessionID string) *fakeBackend {
	backend := &fakeBackend{}
	backend.listLaunchTargetsFn = func(context.Context) ([]LaunchTarget, error) {
		return []LaunchTarget{{
			Key: "codex", Label: "Codex", Kind: "agent", Source: "config", Available: true,
		}}, nil
	}
	backend.getPullFn = func(context.Context, ItemIdentity) (PullDetail, error) {
		return PullDetail{Pull: &Pull{Number: 42, Repository: testRepository()}}, nil
	}
	backend.createPullWorkspaceFn = func(context.Context, ItemIdentity, bool) (Workspace, error) {
		return Workspace{ID: workspaceID, Status: "ready", Created: true}, nil
	}
	backend.getWorkspaceFn = func(context.Context, string) (Workspace, error) {
		return Workspace{ID: workspaceID, Status: "ready"}, nil
	}
	backend.launchWorkspaceRuntimeFn = func(_ context.Context, gotWorkspace, target string) (RuntimeSession, error) {
		if gotWorkspace != workspaceID || target != "codex" {
			return RuntimeSession{}, errors.New("unexpected runtime launch")
		}
		return RuntimeSession{
			Key: runtimeKey, TargetKey: "codex", Status: "running",
			CreatedAt: time.Date(2026, 8, 7, 15, 0, 0, 0, time.UTC),
		}, nil
	}
	backend.listWorkspaceAgentSessionsFn = func(context.Context, string) ([]WorkspaceAgentSession, error) {
		return []WorkspaceAgentSession{{
			Agent: "codex", SessionID: codingSessionID, RuntimeSessionKey: runtimeKey,
			TargetKey: "codex", State: "working",
			UpdatedAt: time.Date(2026, 8, 7, 15, 0, 1, 0, time.UTC),
		}}, nil
	}
	backend.getWorkspaceRuntimeFn = func(context.Context, string) (WorkspaceRuntime, error) {
		return WorkspaceRuntime{Sessions: []RuntimeSession{{Key: runtimeKey, Status: "running"}}}, nil
	}
	backend.submitInitialMessageFn = func(_ context.Context, req InitialMessageRequest) (InitialMessageStatus, error) {
		return InitialMessageStatus{State: "delivered", MessageBytes: len(req.Message)}, nil
	}
	return backend
}

func prSpawnInput(message string) spawnWorkspaceWithAgentInput {
	return spawnWorkspaceWithAgentInput{
		Source: workspaceSourceInput{Type: "item", Item: &itemRefInput{
			Type: "pr", Provider: "github", PlatformRepoID: "repo-acme-widget",
			Owner: "acme", Name: "widget", Number: 42,
		}},
		AgentTarget: "codex", InitialMessage: message, Timeout: "2s",
	}
}

func TestNormalizeSpawnInitialMessage(t *testing.T) {
	message, err := normalizeSpawnInitialMessage("first\r\nsecond")
	require.NoError(t, err)
	assert.Equal(t, "first\nsecond", message)
	_, err = normalizeSpawnInitialMessage(strings.Repeat("a", (64<<10)+1))
	require.ErrorContains(t, err, "64 KiB")
}
