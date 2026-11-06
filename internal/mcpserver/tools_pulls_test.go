package mcpserver

import (
	"context"
	"encoding/json/v2"
	"fmt"
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
			var listCalls, detailCalls int
			backend := &fakeBackend{
				listPullsFn: func(_ context.Context, query ItemListQuery) ([]Pull, error) {
					listCalls++
					assert.Equal(repo, query.Repository)
					assert.Equal("open", query.State)
					assert.Equal("bug", query.Label)
					assert.Equal(3, query.Limit)
					rows := []Pull{
						{
							Number: 1, State: "open", Repository: repo, MergeableState: "clean",
							ReviewDecision: "APPROVED", CIStatus: "success", HeadSHA: "head-one",
							Labels: []string{"bug", "priority: high"}, Body: "large description",
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
			}
			client := connectMCPTestSession(t, newMCPTestServer(t, backend))
			input := listPullContextsInput{Repo: repoFilterInput{
				Provider: provider, PlatformHost: repo.PlatformHost, PlatformRepoID: 1001,
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
			assert.Zero(detailCalls)
			assert.Zero(backend.getPullsCalls)
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
	assert.Equal(1, backend.getPullsCalls)
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
		{name: "missing name", input: listPullContextsInput{Repo: repoFilterInput{Provider: "github", PlatformRepoID: 1001, Owner: "acme"}}, wantErr: "repo.name is required"},
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
		wantHints []resultHint
	}{
		{name: "default truncates", include: true, count: 6, wantCount: 5, wantMore: new(true)},
		{name: "exact limit", include: true, count: 5, wantCount: 5, wantMore: new(false)},
		{name: "empty", include: true, wantMore: new(false)},
		{name: "larger excerpt", include: true, count: 6, limit: 6, wantCount: 6, wantMore: new(false)},
		{name: "capped excerpt", include: true, count: 101, limit: 200, wantCount: 100, wantMore: new(true), wantHints: []resultHint{{Code: "limit_lowered", Message: "Requested event_limit 200 was lowered to 100"}}},
		{name: "not requested", count: 6, limit: 200},
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
			assert.Equal(tc.wantHints, page.Hints)
			assert.Empty(page.Items[0].Hints)
		})
	}
}

func TestListPullContextsCheckPageSize(t *testing.T) {
	pulls := make([]Pull, 25)
	fullChecks := make([][]contextCheck, 25)
	for i := range pulls {
		pulls[i] = Pull{Number: i + 1, Repository: testRepository(), State: "open", Title: "Update dependency and verify supported platforms", CIStatus: "failure"}
		for j := range 35 {
			status, conclusion := "completed", "success"
			switch j {
			case 1:
				conclusion = "failure"
			case 2:
				status, conclusion = "in_progress", ""
			}
			check := contextCheck{
				Name: fmt.Sprintf("Go integration tests / platform-%02d", j), Status: status, Conclusion: conclusion,
				URL: fmt.Sprintf("https://github.com/acme/widget/actions/runs/%d/job/%d", i+1, j+1),
				App: "GitHub Actions", DurationSeconds: new(int64(120)),
			}
			fullChecks[i] = append(fullChecks[i], check)
			pulls[i].Checks = append(pulls[i].Checks, Check(check))
		}
	}
	backend := &fakeBackend{
		listPullsFn: func(context.Context, ItemListQuery) ([]Pull, error) { return pulls, nil },
		getPullFn: func(_ context.Context, item ItemIdentity) (PullDetail, error) {
			pull := &pulls[item.Number-1]
			return PullDetail{Pull: pull, Checks: pull.Checks}, nil
		},
	}
	client := connectMCPTestSession(t, newMCPTestServer(t, backend))
	for _, tc := range []struct {
		name       string
		checks     *bool
		events     bool
		wantChecks int
	}{
		{name: "omitted", wantChecks: 2},
		{name: "all", checks: new(true), wantChecks: 35},
		{name: "events", events: true, wantChecks: 2},
		{name: "all after events", checks: new(true), events: true, wantChecks: 35},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			args := map[string]any{"repo": repoFilterInput{Provider: "github", PlatformRepoID: 1001, Owner: "acme", Name: "widget"}, "include_events": tc.events}
			if tc.checks != nil {
				args["include_checks"] = *tc.checks
			}
			result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "kenn_forge_list_pull_contexts", Arguments: args})
			require.NoError(err)
			require.False(result.IsError)
			var size int
			for _, content := range result.Content {
				if text, ok := content.(*mcp.TextContent); ok {
					size += len(text.Text)
				}
			}
			t.Logf("25 PRs, 35 source checks each: %d text bytes", size)
			encoded, err := json.Marshal(result.StructuredContent)
			require.NoError(err)
			var page listPullContextsOutput
			require.NoError(json.Unmarshal(encoded, &page))
			require.Len(page.Items, 25)
			for i, item := range page.Items {
				assert.Equal("failure", item.PullStatus.CIStatus)
				require.Len(item.Checks, tc.wantChecks)
				if tc.wantChecks == 35 {
					assert.Equal(fullChecks[i], item.Checks)
				} else {
					assert.Equal(contextCheck{
						Name: "Go integration tests / platform-01", Status: "completed", Conclusion: "failure",
						URL: fmt.Sprintf("https://github.com/acme/widget/actions/runs/%d/job/2", i+1),
						App: "GitHub Actions", DurationSeconds: new(int64(120)),
					}, item.Checks[0])
					assert.Equal("Go integration tests / platform-02", item.Checks[1].Name)
					assert.Equal("in_progress", item.Checks[1].Status)
				}
			}
			if tc.wantChecks == 2 {
				assert.Less(size, 32_000)
			}
		})
	}
}

func TestListPullContextsCheckSelection(t *testing.T) {
	for _, tc := range []struct {
		status     string
		conclusion string
		keep       bool
	}{
		{status: "completed", conclusion: "success"},
		{status: "completed", conclusion: "neutral"},
		{status: "completed", conclusion: "skipped"},
		{status: "completed", conclusion: "failure", keep: true},
		{status: "completed", conclusion: "cancelled", keep: true},
		{status: "completed", conclusion: "timed_out", keep: true},
		{status: "completed", conclusion: "action_required", keep: true},
		{status: "queued", keep: true},
		{status: "in_progress", keep: true},
	} {
		t.Run(tc.status+"/"+tc.conclusion, func(t *testing.T) {
			pull := Pull{Number: 1, Repository: testRepository(), Checks: []Check{{Name: "unit", Status: tc.status, Conclusion: tc.conclusion}}}
			backend := &fakeBackend{listPullsFn: func(context.Context, ItemListQuery) ([]Pull, error) { return []Pull{pull}, nil }}
			out, err := newMCPTestServer(t, backend).listPullContexts(t.Context(), listPullContextsInput{
				Repo: repoFilterInput{Provider: "github", PlatformRepoID: 1001, Owner: "acme", Name: "widget"},
			})
			require.NoError(t, err)
			require.Len(t, out.Items, 1)
			if tc.keep {
				assert.Equal(t, []contextCheck{{Name: "unit", Status: tc.status, Conclusion: tc.conclusion}}, out.Items[0].Checks)
			} else {
				assert.Empty(t, out.Items[0].Checks)
			}
		})
	}
}
