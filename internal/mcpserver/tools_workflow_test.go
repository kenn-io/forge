package mcpserver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetItemWorkflowStateForwardsTypedMutation(t *testing.T) {
	assert := assert.New(t)
	var gotItem ItemIdentity
	var gotUpdate WorkflowUpdate
	backend := &fakeBackend{setWorkflowStateFn: func(
		_ context.Context, item ItemIdentity, update WorkflowUpdate,
	) (WorkflowMutation, error) {
		gotItem = item
		gotUpdate = update
		return WorkflowMutation{
			PreviousStatus: "new",
			State: WorkflowState{
				Status: "reviewing", UpdatedAt: "2026-07-01T16:01:00Z",
				UpdatedSource: "mcp", UpdatedActor: "agent", UpdatedReason: "checking docs",
			},
		}, nil
	}}
	s := newMCPTestServer(t, backend)

	out, err := s.setItemWorkflowState(t.Context(), setWorkflowInput{
		Item: itemRefInput{
			Type: "pr", Provider: "github", PlatformRepoID: "repo-acme-widget", PlatformHost: "github.com",
			Owner: "acme", Name: "widget", Number: 42,
		},
		Status: "reviewing", ExpectedStatus: "new",
		Reason: "checking docs", Actor: "agent",
	})

	require.NoError(t, err)
	assert.Equal(testItemIdentity("pr", 42), gotItem)
	assert.Equal(WorkflowUpdate{
		Status: "reviewing", ExpectedStatus: "new", Source: "mcp",
		Actor: "agent", Reason: "checking docs",
	}, gotUpdate)
	assert.Equal("new", out.PreviousStatus)
	assert.Equal("reviewing", out.Status)
	assert.Equal("agent", out.UpdatedActor)
}

func TestSetItemWorkflowStateForwardsForce(t *testing.T) {
	assert := assert.New(t)
	var got WorkflowUpdate
	backend := &fakeBackend{setWorkflowStateFn: func(
		_ context.Context, _ ItemIdentity, update WorkflowUpdate,
	) (WorkflowMutation, error) {
		got = update
		return WorkflowMutation{PreviousStatus: "reviewing", State: WorkflowState{Status: "waiting"}}, nil
	}}
	s := newMCPTestServer(t, backend)

	out, err := s.setItemWorkflowState(t.Context(), setWorkflowInput{
		Item:   itemRefInput{Type: "issue", Provider: "gitlab", PlatformRepoID: "gitlab-project", PlatformHost: "git.example.test", Owner: "group/sub", Name: "project", Number: 7},
		Status: "waiting", Force: true,
	})

	require.NoError(t, err)
	assert.True(got.Force)
	assert.Empty(got.ExpectedStatus)
	assert.Equal("waiting", out.Status)
}

func TestSetItemWorkflowStateRequiresOneMutationGuard(t *testing.T) {
	s := newMCPTestServer(t, &fakeBackend{})
	tests := []setWorkflowInput{
		{
			Item:   itemRefInput{Type: "pr", Provider: "github", PlatformRepoID: "repo-acme-widget", Owner: "acme", Name: "widget", Number: 42},
			Status: "reviewing",
		},
		{
			Item:   itemRefInput{Type: "pr", Provider: "github", PlatformRepoID: "repo-acme-widget", Owner: "acme", Name: "widget", Number: 42},
			Status: "reviewing", ExpectedStatus: "new", Force: true,
		},
	}
	for _, input := range tests {
		_, err := s.setItemWorkflowState(t.Context(), input)
		assertBackendErrorKind(t, err, "invalid_request")
	}
}

func TestSetItemWorkflowStatePreservesConflictDetails(t *testing.T) {
	backend := &fakeBackend{setWorkflowStateFn: func(
		context.Context, ItemIdentity, WorkflowUpdate,
	) (WorkflowMutation, error) {
		return WorkflowMutation{}, &Error{
			Kind: "conflict", Code: "conflict", Message: "workflow state changed",
			Details: map[string]any{"current_status": "reviewing", "expected_status": "new"},
		}
	}}
	s := newMCPTestServer(t, backend)

	_, err := s.setItemWorkflowState(t.Context(), setWorkflowInput{
		Item:   itemRefInput{Type: "pr", Provider: "github", PlatformRepoID: "repo-acme-widget", Owner: "acme", Name: "widget", Number: 42},
		Status: "waiting", ExpectedStatus: "new",
	})

	var backendErr *Error
	require.ErrorAs(t, err, &backendErr)
	assert.Equal(t, "reviewing", backendErr.Details["current_status"])
	assert.Equal(t, "new", backendErr.Details["expected_status"])
}
