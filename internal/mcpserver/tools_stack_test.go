package mcpserver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetStackContextTreatsTypedNotStackedAsAbsent(t *testing.T) {
	backend := &fakeBackend{getPullStackFn: func(context.Context, ItemIdentity) (Stack, error) {
		return Stack{}, &Error{Kind: "not_found", Code: "notFound", Message: "PR is not part of a stack"}
	}}
	s := newMCPTestServer(t, backend)

	out, err := s.getStackContext(t.Context(), getStackContextInput{
		Item: itemRefInput{Type: "pr", Provider: "github", PlatformRepoID: "repo-acme-widget", Owner: "acme", Name: "widget", Number: 42},
	})

	require.NoError(t, err)
	assert.False(t, out.Present)

	_, err = s.getStackContext(t.Context(), getStackContextInput{
		Item: itemRefInput{Type: "issue", Provider: "github", PlatformRepoID: "repo-acme-widget", Owner: "acme", Name: "widget", Number: 7},
	})
	assertBackendErrorKind(t, err, "invalid_request")
}

func TestGetStackContextPropagatesMissingPull(t *testing.T) {
	backend := &fakeBackend{getPullStackFn: func(context.Context, ItemIdentity) (Stack, error) {
		return Stack{}, &Error{Kind: "not_found", Code: "pullNotFound", Message: "pull request not found"}
	}}
	s := newMCPTestServer(t, backend)

	_, err := s.getStackContext(t.Context(), getStackContextInput{
		Item: itemRefInput{Type: "pr", Provider: "github", PlatformRepoID: "repo-acme-widget", Owner: "acme", Name: "widget", Number: 99},
	})

	var backendErr *Error
	require.ErrorAs(t, err, &backendErr)
	assert.Equal(t, "pullNotFound", backendErr.Code)
}

func TestGetStackContextSortsMembersAndJoinsPagedWorkflowState(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var cursors []string
	backend := &fakeBackend{
		getPullStackFn: func(context.Context, ItemIdentity) (Stack, error) {
			return Stack{Health: "blocked", Members: []StackMember{
				{Number: 43, Title: "Tip", State: "open", Position: 3},
				{Number: 41, Title: "Base", State: "open", Position: 1},
				{Number: 42, Title: "Middle", State: "open", Position: 2, IsDraft: true},
			}}, nil
		},
		listWorkflowStatesFn: func(_ context.Context, query WorkflowQuery) (WorkflowPage, error) {
			cursors = append(cursors, query.Cursor)
			assert.Equal(testRepository(), query.Repository)
			assert.Equal([]string{"pr"}, query.ItemTypes)
			assert.True(query.IncludeClosed)
			assert.Equal(200, query.Limit)
			if query.Cursor == "" {
				return WorkflowPage{
					Items:      []WorkflowItem{{Identity: testItemIdentity("pr", 41), Workflow: WorkflowState{Status: "waiting"}}},
					NextCursor: "next",
				}, nil
			}
			return WorkflowPage{Items: []WorkflowItem{
				{Identity: testItemIdentity("pr", 42), Workflow: WorkflowState{Status: "reviewing"}},
				{Identity: testItemIdentity("pr", 43), Workflow: WorkflowState{Status: "new"}},
			}}, nil
		},
	}
	s := newMCPTestServer(t, backend)

	out, err := s.getStackContext(t.Context(), getStackContextInput{
		Item: itemRefInput{
			Type: "pr", Provider: "github", PlatformRepoID: "repo-acme-widget", PlatformHost: "github.com",
			Owner: "acme", Name: "widget", Number: 42,
		},
	})

	require.NoError(err)
	assert.Equal([]string{"", "next"}, cursors)
	require.Len(out.Members, 3)
	assert.Equal(41, out.Members[0].Number)
	assert.Equal("waiting", out.Members[0].WorkflowStatus)
	assert.True(out.Members[1].IsRequested)
	assert.True(out.Members[1].IsDraft)
	assert.Equal("reviewing", out.Members[1].WorkflowStatus)
}
