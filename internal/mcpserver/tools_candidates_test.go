package mcpserver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindReviewCandidatesGroupsActivityAndEnrichesItems(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var activityQuery ActivityQuery
	backend := &fakeBackend{
		listActivityFn: func(_ context.Context, query ActivityQuery) (ActivityPage, error) {
			activityQuery = query
			return ActivityPage{Items: []ActivityItem{
				{
					ID: "pr-comment", ActivityType: "comment", Repository: testRepository(),
					ItemType: "pr", ItemNumber: 42, ItemTitle: "Retry budget",
					ItemURL: "https://example.test/pulls/42", ItemState: "open",
					Author: "bob", ItemAuthor: "alice", CreatedAt: "2026-07-01T14:00:00Z",
				},
				{
					ID: "pr-commit", ActivityType: "commit", Repository: testRepository(),
					ItemType: "pr", ItemNumber: 42, Author: "alice", ItemAuthor: "alice",
					CreatedAt: "2026-07-01T13:00:00Z",
				},
				{
					ID: "issue-comment", ActivityType: "comment", Repository: testRepository(),
					ItemType: "issue", ItemNumber: 7, ItemTitle: "Retry docs",
					ItemURL: "https://example.test/issues/7", ItemState: "open",
					Author: "carol", ItemAuthor: "dave", CreatedAt: "2026-07-01T15:00:00Z",
				},
				{ID: "repo", ActivityType: "default_branch_commit", Repository: testRepository()},
			}}, nil
		},
		listPullsFn: func(_ context.Context, query ItemListQuery) ([]Pull, error) {
			assert.Equal(ItemListQuery{Repository: testRepository(), State: "all", Limit: 200}, query)
			return []Pull{{
				Number: 42, Title: "Retry budget", State: "open", Author: "alice",
				Repository: testRepository(), Workspace: &WorkspaceRef{ID: "ws-pr", Status: "ready"},
				DetailLoaded: true, DetailFetchedAt: "2026-07-01T14:05:00Z",
			}}, nil
		},
		listIssuesFn: func(_ context.Context, query ItemListQuery) ([]Issue, error) {
			assert.Equal(ItemListQuery{Repository: testRepository(), State: "all", Limit: 200}, query)
			return []Issue{{
				Number: 7, Title: "Retry docs", State: "open", Author: "dave",
				WorkflowStatus: "waiting", Repository: testRepository(),
			}}, nil
		},
		listWorkflowStatesFn: func(context.Context, WorkflowQuery) (WorkflowPage, error) {
			return WorkflowPage{}, nil
		},
		getPullStackFn: func(_ context.Context, item ItemIdentity) (Stack, error) {
			assert.Equal(testItemIdentity("pr", 42), item)
			return Stack{Position: 2, Size: 4, Health: "blocked"}, nil
		},
	}
	s := newMCPTestServer(t, backend)

	out, err := s.findReviewCandidates(t.Context(), findCandidatesInput{
		Since: "2026-07-01T00:00:00Z", ActivityTypes: []string{"comment"},
	})

	require.NoError(err)
	assert.Equal([]string{"comment"}, activityQuery.ActivityTypes)
	require.Len(out.Candidates, 2)
	issue := out.Candidates[0]
	assert.Equal(7, issue.Item.Number)
	assert.Equal("waiting", issue.Workflow.Status)
	assert.Equal([]string{"carol commented"}, issue.Activity.Reasons)
	pr := out.Candidates[1]
	assert.Equal(2, pr.Activity.EventCount)
	assert.Equal([]string{"bob", "alice"}, pr.Activity.Actors)
	assert.True(pr.Workspace.Exists)
	assert.True(pr.Stack.Present)
	assert.True(pr.Cache.DetailLoaded)
	raw, err := json.Marshal(out)
	require.NoError(err)
	assert.NotContains(string(raw), "EventCount")
}

func TestFindReviewCandidatesUsesWorkflowMetadataAndFiltersBeforeStackLookup(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	stackCalls := 0
	backend := candidateBackendForPull(42)
	backend.listWorkflowStatesFn = func(_ context.Context, query WorkflowQuery) (WorkflowPage, error) {
		return WorkflowPage{Items: []WorkflowItem{{
			Identity: testItemIdentity("pr", 42), Repository: testRepository(),
			Workflow: WorkflowState{
				Status: "reviewing", UpdatedAt: "2026-07-01T14:10:00Z",
				UpdatedSource: "mcp", UpdatedActor: "agent", UpdatedReason: "claim",
			},
		}}}, nil
	}
	backend.getPullStackFn = func(context.Context, ItemIdentity) (Stack, error) {
		stackCalls++
		return Stack{Position: 1, Size: 1, Health: "ok"}, nil
	}
	s := newMCPTestServer(t, backend)

	excluded, err := s.findReviewCandidates(t.Context(), findCandidatesInput{
		ExcludeWorkflowStates: []string{"reviewing"},
	})
	require.NoError(err)
	assert.Empty(excluded.Candidates)
	assert.Equal(0, stackCalls)

	included, err := s.findReviewCandidates(t.Context(), findCandidatesInput{
		WorkflowStates: []string{"reviewing"},
	})
	require.NoError(err)
	require.Len(included.Candidates, 1)
	assert.Equal("2026-07-01T14:10:00Z", included.Candidates[0].Workflow.UpdatedAt)
	assert.Equal(1, stackCalls)
}

func TestFindReviewCandidatesDefaultsTo25AndCapsAt100(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	backend := &fakeBackend{
		listActivityFn: func(context.Context, ActivityQuery) (ActivityPage, error) {
			items := make([]ActivityItem, 102)
			for i := range items {
				number := i + 1
				items[i] = ActivityItem{
					ID: string(rune(number)), ActivityType: "comment", Repository: testRepository(),
					ItemType: "issue", ItemNumber: number,
					CreatedAt: time.Date(2026, 7, 1, 0, 0, number, 0, time.UTC).Format(time.RFC3339),
				}
			}
			return ActivityPage{Items: items}, nil
		},
		listIssuesFn: func(context.Context, ItemListQuery) ([]Issue, error) {
			items := make([]Issue, 102)
			for i := range items {
				items[i] = Issue{Number: i + 1, State: "open", Repository: testRepository()}
			}
			return items, nil
		},
	}
	s := newMCPTestServer(t, backend)

	defaultOut, err := s.findReviewCandidates(t.Context(), findCandidatesInput{ItemTypes: []string{"issue"}})
	require.NoError(err)
	assert.Len(defaultOut.Candidates, 25)
	assert.True(defaultOut.Capped)

	maxOut, err := s.findReviewCandidates(t.Context(), findCandidatesInput{
		ItemTypes: []string{"issue"}, Limit: 500,
	})
	require.NoError(err)
	assert.Len(maxOut.Candidates, 100)
	assert.True(maxOut.Capped)
}

func TestFindReviewCandidatesPropagatesStackFailure(t *testing.T) {
	backend := candidateBackendForPull(42)
	backend.getPullStackFn = func(context.Context, ItemIdentity) (Stack, error) {
		return Stack{}, &Error{Kind: "internal_error", Message: "stack cache unavailable"}
	}
	s := newMCPTestServer(t, backend)

	_, err := s.findReviewCandidates(t.Context(), findCandidatesInput{})

	var backendErr *Error
	require.ErrorAs(t, err, &backendErr)
	assert.Equal(t, "internal_error", backendErr.Kind)
}

func candidateBackendForPull(number int) *fakeBackend {
	return &fakeBackend{
		listActivityFn: func(context.Context, ActivityQuery) (ActivityPage, error) {
			return ActivityPage{Items: []ActivityItem{{
				ID: "candidate", ActivityType: "comment", Repository: testRepository(),
				ItemType: "pr", ItemNumber: number, CreatedAt: "2026-07-01T14:00:00Z",
			}}}, nil
		},
		listPullsFn: func(context.Context, ItemListQuery) ([]Pull, error) {
			return []Pull{{Number: number, State: "open", Repository: testRepository()}}, nil
		},
	}
}
