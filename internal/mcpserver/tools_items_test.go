package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/platform"
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
						Provider: "gitlab", Key: platform.RepositoryIDKey(2001), PlatformHost: "git.example.test",
						RepoPath: "group/sub/project", Owner: "group/sub", Name: "project",
					},
					LastActivityAt: time.Date(2026, 7, 1, 16, 0, 0, 0, time.UTC),
					MergeableState: "dirty", ReviewDecision: "CHANGES_REQUESTED", CIStatus: "success",
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
					Type: "pr", Provider: "gitlab", RepoKey: platform.RepositoryIDKey(2001), PlatformHost: "git.example.test",
					Owner: "group/sub", Name: "project", Number: 42,
				},
				Repository: RepositoryIdentity{
					Provider: "gitlab", Key: platform.RepositoryIDKey(2001), PlatformHost: "git.example.test",
					RepoPath: "group/sub/project", Owner: "group/sub", Name: "project",
				},
				Workflow: WorkflowState{Status: "reviewing", UpdatedSource: "mcp"},
			}}}, nil
		},
	}
	s := newMCPTestServer(t, backend)
	inputItem := itemRefInput{
		Type: "pr", Provider: "gitlab", PlatformRepoID: 2001, PlatformHost: "git.example.test",
		Owner: "group/sub", Name: "project", Number: 42,
	}

	out, err := s.getItemContext(t.Context(), getItemContextInput{Item: inputItem, EventLimit: 2})

	require.NoError(err)
	wantItem, err := inputItem.itemIdentity()
	require.NoError(err)
	assert.Equal(wantItem, got)
	assert.Equal("full body", out.Body)
	require.NotNil(out.PullStatus)
	assert.Equal("dirty", out.PullStatus.MergeableState)
	assert.Equal("CHANGES_REQUESTED", out.PullStatus.ReviewDecision)
	assert.Equal("mcp", out.Workflow.UpdatedSource)
	require.Len(out.Events, 2)
	assert.Equal(new(true), out.EventsHasMore)
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
		Item:          itemRefInput{Type: "issue", Provider: "github", PlatformRepoID: 1001, Owner: "acme", Name: "widget", Number: 7},
		IncludeEvents: &includeEvents,
	})

	require.NoError(err)
	assert.Equal("full issue body", out.Body)
	assert.Equal("waiting", out.Workflow.Status)
	assert.Empty(out.Events)
	assert.Nil(out.EventsHasMore)
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
					Type: "pr", Provider: "gitlab", RepoKey: platform.RepositoryIDKey(2001), PlatformHost: "git.example.test",
					Owner: "group/sub", Name: "project", Number: 42,
				},
				Repository: RepositoryIdentity{
					Provider: "gitlab", Key: platform.RepositoryIDKey(2001), PlatformHost: "git.example.test",
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
			Provider: "gitlab", PlatformRepoID: 2001, PlatformHost: "git.example.test", RepoPath: "group/sub/project",
		},
		IncludeClosed: true, Limit: 10, Cursor: "cursor",
	})

	require.NoError(err)
	assert.Equal(WorkflowQuery{
		Repository: RepositoryIdentity{
			Provider: "gitlab", Key: platform.RepositoryIDKey(2001), PlatformHost: "git.example.test",
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

func TestGetItemContextPassesBitbucketCloudUUIDToBackendAsKey(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	repositoryUUID := uuid.MustParse("5f0c6a1e-2b7d-4c3a-9e8f-0a1b2c3d4e5f")
	var got ItemIdentity
	backend := &fakeBackend{getPullFn: func(_ context.Context, item ItemIdentity) (PullDetail, error) {
		got = item
		return PullDetail{Pull: &Pull{
			Number: 42, State: "open",
			Repository: RepositoryIdentity{
				Provider: "bitbucket", PlatformHost: "bitbucket.org", Key: item.RepoKey,
				RepoPath: "acme/widget", Owner: "acme", Name: "widget",
			},
		}}, nil
	}}
	client := connectMCPTestSession(t, newMCPTestServer(t, backend))

	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "kenn_forge_get_item_context",
		Arguments: map[string]any{"item": map[string]any{
			"type": "pr", "provider": "bitbucket", "platform_host": "bitbucket.org",
			"bitbucket_repository_uuid": "{5F0C6A1E-2B7D-4C3A-9E8F-0A1B2C3D4E5F}",
			"owner":                     "acme", "name": "widget", "number": 42,
		}},
	})

	require.NoError(err)
	require.False(result.IsError, "%v", result.Content)
	assert.Equal(platform.RepositoryUUIDKey(repositoryUUID), got.RepoKey)
	encoded, err := json.Marshal(result.StructuredContent)
	require.NoError(err)
	var out struct {
		Item map[string]any `json:"item"`
	}
	require.NoError(json.Unmarshal(encoded, &out))
	assert.Equal("5f0c6a1e-2b7d-4c3a-9e8f-0a1b2c3d4e5f", out.Item["bitbucket_repository_uuid"])
	assert.InDelta(0, out.Item["platform_repo_id"], 0)
}
