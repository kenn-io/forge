package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetItemContextPullLimitsEventsAndMapsBackendDetail(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var got ItemIdentity
	backend := &fakeBackend{
		getPullFn: func(_ context.Context, item ItemIdentity) (PullDetail, error) {
			got = item
			return PullDetail{
				Pull: &Pull{
					Number: 42, Title: "Retry budget", State: "open", Author: "alice",
					URL: "https://git.example.test/group/project/pulls/42", Body: "full body",
					WorkflowStatus: "reviewing", Repository: RepositoryIdentity{
						Provider: "gitlab", PlatformRepoID: "gitlab-project", PlatformHost: "git.example.test",
						RepoPath: "group/sub/project", Owner: "group/sub", Name: "project",
					},
					LastActivityAt: time.Date(2026, 7, 1, 16, 0, 0, 0, time.UTC),
				},
				Events: []DetailEvent{
					{EventType: "comment", Author: "old", CreatedAt: time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)},
					{EventType: "commit", Author: "newest", Body: strings.Repeat("x", 620), CreatedAt: time.Date(2026, 7, 1, 16, 0, 0, 0, time.UTC)},
					{EventType: "review", Author: "middle", CreatedAt: time.Date(2026, 7, 1, 15, 0, 0, 0, time.UTC)},
				},
				DetailLoaded: true, DetailFetchedAt: "2026-07-01T16:05:00Z",
				Workspace: &WorkspaceRef{ID: "ws-pr", Status: "ready"},
				Stack:     &Stack{Position: 2, Size: 3, Health: "blocked"},
				Checks:    []Check{{Name: "unit", Status: "completed", Conclusion: "success"}},
			}, nil
		},
		listWorkflowStatesFn: func(context.Context, WorkflowQuery) (WorkflowPage, error) {
			return WorkflowPage{Items: []WorkflowItem{{
				Identity: ItemIdentity{
					Type: "pr", Provider: "gitlab", PlatformRepoID: "gitlab-project", PlatformHost: "git.example.test",
					Owner: "group/sub", Name: "project", Number: 42,
				},
				Repository: RepositoryIdentity{
					Provider: "gitlab", PlatformRepoID: "gitlab-project", PlatformHost: "git.example.test",
					RepoPath: "group/sub/project", Owner: "group/sub", Name: "project",
				},
				Workflow: WorkflowState{Status: "reviewing", UpdatedSource: "mcp"},
			}}}, nil
		},
	}
	s := newMCPTestServer(t, backend)
	inputItem := itemRefInput{
		Type: "pr", Provider: "gitlab", PlatformRepoID: "gitlab-project", PlatformHost: "git.example.test",
		Owner: "group/sub", Name: "project", Number: 42,
	}

	out, err := s.getItemContext(t.Context(), getItemContextInput{Item: inputItem, EventLimit: 2})

	require.NoError(err)
	assert.Equal(itemIdentity(inputItem), got)
	assert.Equal("full body", out.Body)
	assert.Equal("mcp", out.Workflow.UpdatedSource)
	require.Len(out.Events, 2)
	assert.Equal("newest", out.Events[0].Author)
	assert.Len(out.Events[0].BodyPreview, 500)
	require.NotNil(out.Workspace)
	assert.True(out.Stack.Present)
	require.Len(out.Checks, 1)
	assert.True(out.Cache.DetailLoaded)
}

func TestGetItemContextIssueCanOmitEvents(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	backend := &fakeBackend{getIssueFn: func(context.Context, ItemIdentity) (IssueDetail, error) {
		return IssueDetail{
			Issue: &Issue{
				Number: 7, Title: "Retry docs", State: "open", Author: "bob",
				Body: "full issue body", WorkflowStatus: "waiting", Repository: testRepository(),
			},
			Events: []DetailEvent{{EventType: "comment", Author: "carol"}},
			Workflow: &WorkflowState{
				Status: "waiting", UpdatedAt: "2026-07-01T15:02:00Z",
				UpdatedSource: "mcp", UpdatedActor: "agent", UpdatedReason: "checking docs",
			},
		}, nil
	}}
	s := newMCPTestServer(t, backend)
	includeEvents := false

	out, err := s.getItemContext(t.Context(), getItemContextInput{
		Item:          itemRefInput{Type: "issue", Provider: "github", PlatformRepoID: "repo-acme-widget", Owner: "acme", Name: "widget", Number: 7},
		IncludeEvents: &includeEvents,
	})

	require.NoError(err)
	assert.Equal("full issue body", out.Body)
	assert.Equal("waiting", out.Workflow.Status)
	assert.Empty(out.Events)
	raw, err := json.Marshal(out)
	require.NoError(err)
	assert.NotContains(string(raw), `"events"`)
}

func TestListItemsByWorkflowStateForwardsTypedQuery(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var got WorkflowQuery
	backend := &fakeBackend{listWorkflowStatesFn: func(_ context.Context, query WorkflowQuery) (WorkflowPage, error) {
		got = query
		return WorkflowPage{
			Items: []WorkflowItem{{
				Identity: ItemIdentity{
					Type: "pr", Provider: "gitlab", PlatformRepoID: "gitlab-project", PlatformHost: "git.example.test",
					Owner: "group/sub", Name: "project", Number: 42,
				},
				Repository: RepositoryIdentity{
					Provider: "gitlab", PlatformRepoID: "gitlab-project", PlatformHost: "git.example.test",
					RepoPath: "group/sub/project", Owner: "group/sub", Name: "project",
				},
				Title: "Retry budget", State: "open", LastActivityAt: "2026-07-01T16:00:00Z",
				Workflow: WorkflowState{Status: "reviewing", UpdatedSource: "mcp"},
			}},
			NextCursor: "next",
		}, nil
	}}
	s := newMCPTestServer(t, backend)

	out, err := s.listItemsByWorkflowState(t.Context(), listByWorkflowInput{
		States: []string{"reviewing", "waiting"}, ItemTypes: []string{"pr", "issue"},
		Repo: repoFilterInput{
			Provider: "gitlab", PlatformRepoID: "gitlab-project", PlatformHost: "git.example.test", RepoPath: "group/sub/project",
		},
		IncludeClosed: true, Limit: 10, Cursor: "cursor",
	})

	require.NoError(err)
	assert.Equal(WorkflowQuery{
		Repository: RepositoryIdentity{
			Provider: "gitlab", PlatformRepoID: "gitlab-project", PlatformHost: "git.example.test",
			RepoPath: "group/sub/project", Owner: "group/sub", Name: "project",
		},
		ItemTypes: []string{"pr", "issue"}, States: []string{"reviewing", "waiting"},
		IncludeClosed: true, Limit: 10, Cursor: "cursor",
	}, got)
	require.Len(out.Items, 1)
	assert.Equal("next", out.NextCursor)
	assert.Equal("group/sub/project", out.Items[0].Item.RepoPath)
}

func TestListItemsByWorkflowStatePropagatesBackendError(t *testing.T) {
	backend := &fakeBackend{listWorkflowStatesFn: func(context.Context, WorkflowQuery) (WorkflowPage, error) {
		return WorkflowPage{}, &Error{Kind: "unavailable", Message: "workflow state unavailable"}
	}}
	s := newMCPTestServer(t, backend)

	_, err := s.listItemsByWorkflowState(t.Context(), listByWorkflowInput{})

	var backendErr *Error
	require.ErrorAs(t, err, &backendErr)
	assert.Equal(t, "unavailable", backendErr.Kind)
}

func TestTruncateBytesPreservesUTF8(t *testing.T) {
	got := truncateBytes(strings.Repeat("a", 499)+"é", 500)
	assert.True(t, utf8.ValidString(got))
	assert.LessOrEqual(t, len(got), 500)
	assert.Equal(t, strings.Repeat("a", 499), got)
}
