package mcpserver

import (
	"context"
	"encoding/json/v2"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListPullContextsReadsPagesWithoutPerPullCalls(t *testing.T) {
	for _, provider := range []string{"github", "gitlab", "forgejo", "gitea"} {
		t.Run(provider, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			repo := testRepository()
			repo.Provider = provider
			repo.PlatformHost = "git.example.test"
			var listCalls, detailCalls, workflowCalls int
			backend := &fakeBackend{
				listPullsFn: func(_ context.Context, query ItemListQuery) ([]Pull, error) {
					listCalls++
					assert.Equal(repo, query.Repository)
					assert.Equal("open", query.State)
					assert.Equal("bug", query.Label)
					assert.Equal(3, query.Limit)
					rows := []Pull{
						{
							Number: 1, State: "open", Repository: repo, MergeableState: "clean", WorkflowStatus: "awaiting_merge",
							ReviewDecision: "APPROVED", CIStatus: "success", HeadSHA: "head-one",
							Labels: []string{"bug", "priority: high"}, Checks: []Check{{Name: "unit", Conclusion: "success"}}, Body: "large description",
							DetailLoaded: true, DetailFetchedAt: "2026-09-12T12:00:00Z",
						},
						{Number: 2, State: "open", Repository: repo, MergeableState: "dirty", Stack: &Stack{Position: 2, Size: 3}},
						{Number: 3, State: "open", Repository: repo},
					}
					return rows[query.Offset:], nil
				},
				getPullFn: func(context.Context, ItemIdentity) (PullDetail, error) {
					detailCalls++
					return PullDetail{}, nil
				},
				listWorkflowStatesFn: func(context.Context, WorkflowQuery) (WorkflowPage, error) {
					workflowCalls++
					return WorkflowPage{}, nil
				},
			}
			client := connectMCPTestSession(t, newMCPTestServer(t, backend))
			input := listPullContextsInput{Repo: repoFilterInput{
				Provider: provider, PlatformHost: repo.PlatformHost, PlatformRepoID: repo.PlatformRepoID,
				Owner: repo.Owner, Name: repo.Name,
			}, Limit: 2, Label: "bug"}
			result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "kenn_forge_list_pull_contexts", Arguments: input})
			require.NoError(err)
			require.False(result.IsError)
			var page listPullContextsOutput
			encoded, err := json.Marshal(result.StructuredContent)
			require.NoError(err)
			require.NoError(json.Unmarshal(encoded, &page))
			require.Len(page.Items, 2)
			assert.Equal([]string{"bug", "priority: high"}, page.Items[0].Labels)
			assert.Equal("clean", page.Items[0].PullStatus.MergeableState)
			assert.Equal("APPROVED", page.Items[0].PullStatus.ReviewDecision)
			assert.Equal("head-one", page.Items[0].PullStatus.HeadSHA)
			assert.Equal("awaiting_merge", page.Items[0].Workflow.Status)
			assert.Equal("success", page.Items[0].Checks[0].Conclusion)
			assert.Equal("2026-09-12T12:00:00Z", page.Items[0].Cache.DetailFetchedAt)
			assert.Empty(page.Items[0].Body)
			assert.Equal("dirty", page.Items[1].PullStatus.MergeableState)
			assert.Equal(2, page.Items[1].Stack.Position)
			require.NotNil(page.NextOffset)
			input.Offset = *page.NextOffset
			last, err := newMCPTestServer(t, backend).listPullContexts(t.Context(), input)
			require.NoError(err)
			require.Len(last.Items, 1)
			assert.Equal(3, last.Items[0].Item.Number)
			assert.Equal("unknown", last.Items[0].PullStatus.MergeableState)
			assert.Nil(last.NextOffset)
			assert.Equal(2, listCalls)
			assert.Zero(workflowCalls)
			assert.Zero(detailCalls)
		})
	}
}

func TestListPullContextsIncludesRequestedReviewEvidence(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	pull := Pull{Number: 42, Repository: testRepository(), Body: "description"}
	backend := &fakeBackend{
		listPullsFn: func(context.Context, ItemListQuery) ([]Pull, error) { return []Pull{pull}, nil },
		getPullFn: func(_ context.Context, item ItemIdentity) (PullDetail, error) {
			assert.Equal(42, item.Number)
			return PullDetail{Pull: &pull, Stack: &Stack{Health: "blocked"}, Events: []DetailEvent{
				{EventType: "review", Body: "changes requested", CreatedAt: time.Unix(2, 0)},
				{EventType: "comment", Body: "older comment", CreatedAt: time.Unix(1, 0)},
			}}, nil
		},
	}
	out, err := newMCPTestServer(t, backend).listPullContexts(t.Context(), listPullContextsInput{
		Repo:          repoFilterInput{Provider: "github", PlatformRepoID: 1001, Owner: "acme", Name: "widget"},
		IncludeEvents: true, EventLimit: 1, IncludeBody: true,
	})
	require.NoError(err)
	require.Len(out.Items, 1)
	assert.Equal("description", out.Items[0].Body)
	require.Len(out.Items[0].Events, 1)
	assert.Equal("changes requested", out.Items[0].Events[0].BodyPreview)
	assert.Equal("blocked", out.Items[0].Stack.Health)
}

func TestListPullContextsRequiresRepositoryAndBoundsPages(t *testing.T) {
	for _, tc := range []struct {
		name      string
		input     listPullContextsInput
		wantErr   string
		wantLimit int
	}{
		{name: "missing repo", wantErr: "repo is required"},
		{name: "missing stable ID", input: listPullContextsInput{Repo: repoFilterInput{Provider: "github", Owner: "acme", Name: "widget"}}, wantErr: "platform_repo_id"},
		{name: "negative offset", input: listPullContextsInput{Repo: repoFilterInput{Provider: "github", PlatformRepoID: 1001, Owner: "acme", Name: "widget"}, Offset: -1}, wantErr: "offset"},
		{name: "default page", input: listPullContextsInput{Repo: repoFilterInput{Provider: "github", PlatformRepoID: 1001, Owner: "acme", Name: "widget"}}, wantLimit: 26},
		{name: "capped page", input: listPullContextsInput{Repo: repoFilterInput{Provider: "github", PlatformRepoID: 1001, Owner: "acme", Name: "widget"}, Limit: 1000}, wantLimit: 101},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotLimit int
			backend := &fakeBackend{listPullsFn: func(_ context.Context, query ItemListQuery) ([]Pull, error) {
				gotLimit = query.Limit
				return nil, nil
			}}
			_, err := newMCPTestServer(t, backend).listPullContexts(t.Context(), tc.input)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.wantLimit, gotLimit)
		})
	}
}

func TestListPullContextsReportsMoreEvents(t *testing.T) {
	for _, tc := range []struct {
		name      string
		include   bool
		count     int
		limit     int
		wantCount int
		wantMore  *bool
	}{
		{name: "default truncates", include: true, count: 6, wantCount: 5, wantMore: new(true)},
		{name: "exact limit", include: true, count: 5, wantCount: 5, wantMore: new(false)},
		{name: "empty", include: true, wantMore: new(false)},
		{name: "larger excerpt", include: true, count: 6, limit: 6, wantCount: 6, wantMore: new(false)},
		{name: "capped excerpt", include: true, count: 101, limit: 200, wantCount: 100, wantMore: new(true)},
		{name: "not requested", count: 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			pull := Pull{Number: 42, Repository: testRepository()}
			backend := &fakeBackend{
				listPullsFn: func(context.Context, ItemListQuery) ([]Pull, error) { return []Pull{pull}, nil },
				getPullFn: func(context.Context, ItemIdentity) (PullDetail, error) {
					return PullDetail{Pull: &pull, Events: make([]DetailEvent, tc.count)}, nil
				},
			}
			client := connectMCPTestSession(t, newMCPTestServer(t, backend))
			result, err := client.CallTool(t.Context(), &mcp.CallToolParams{
				Name: "kenn_forge_list_pull_contexts",
				Arguments: listPullContextsInput{
					Repo:          repoFilterInput{Provider: "github", PlatformRepoID: 1001, Owner: "acme", Name: "widget"},
					IncludeEvents: tc.include, EventLimit: tc.limit,
				},
			})
			require.NoError(err)
			require.False(result.IsError)
			encoded, err := json.Marshal(result.StructuredContent)
			require.NoError(err)
			var page listPullContextsOutput
			require.NoError(json.Unmarshal(encoded, &page))
			require.Len(page.Items, 1)
			assert.Len(page.Items[0].Events, tc.wantCount)
			assert.Equal(tc.wantMore, page.Items[0].EventsHasMore)
		})
	}
}
