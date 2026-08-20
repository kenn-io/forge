package mcpserver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepoFilterInputRepositoryIdentity(t *testing.T) {
	tests := []struct {
		name    string
		filter  repoFilterInput
		want    RepositoryIdentity
		wantErr string
	}{
		{
			name: "github default host",
			filter: repoFilterInput{
				Provider: "GH", PlatformRepoID: "repo-acme-widget",
				Owner: "acme", Name: "widget",
			},
			want: testRepository(),
		},
		{name: "empty filter"},
		{
			name: "nested path",
			filter: repoFilterInput{
				Provider: "gitlab", PlatformHost: "git.example.test",
				PlatformRepoID: "gid://gitlab/Project/42",
				RepoPath:       "group/subgroup/project",
			},
			want: RepositoryIdentity{
				Provider: "gitlab", PlatformHost: "git.example.test",
				PlatformRepoID: "gid://gitlab/Project/42",
				RepoPath:       "group/subgroup/project", Owner: "group/subgroup", Name: "project",
			},
		},
		{name: "missing provider", filter: repoFilterInput{PlatformRepoID: "repo-1", Owner: "acme", Name: "widget"}, wantErr: "provider"},
		{name: "missing stable id", filter: repoFilterInput{Provider: "github", Owner: "acme", Name: "widget"}, wantErr: "platform_repo_id"},
		{name: "missing name", filter: repoFilterInput{Provider: "github", PlatformRepoID: "repo-1", Owner: "acme"}, wantErr: "name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.filter.repositoryIdentity()
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestListReposUsesBackendSummaries(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	backend := &fakeBackend{listRepositoriesFn: func(context.Context) ([]RepositorySummary, error) {
		return []RepositorySummary{
			{Repository: testRepository(), OpenPRCount: 3, OpenIssueCount: 2, LastSyncCompletedAt: "2026-07-01T10:00:00Z"},
			{Repository: RepositoryIdentity{Provider: "gitlab", PlatformRepoID: "gitlab-project", PlatformHost: "git.example.test", RepoPath: "group/project", Owner: "group", Name: "project"}},
		}, nil
	}}
	s := newMCPTestServer(t, backend)

	out, err := s.listRepos(t.Context(), listReposInput{Limit: 1})

	require.NoError(err)
	require.Len(out.Repos, 1)
	assert.Equal("repo-acme-widget", out.Repos[0].PlatformRepoID)
	assert.Equal("acme/widget", out.Repos[0].RepoPath)
	assert.Equal(3, out.Repos[0].OpenPRCount)
	assert.Equal(2, out.Repos[0].OpenIssueCount)
}

func TestSearchItemsForwardsTypedQueriesAndOrdersResults(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var pullQuery, issueQuery ItemListQuery
	backend := &fakeBackend{
		listPullsFn: func(_ context.Context, query ItemListQuery) ([]Pull, error) {
			pullQuery = query
			return []Pull{{
				Number: 42, Title: "Retry budget", State: "open", Author: "alice",
				URL: "https://example.test/pulls/42", WorkflowStatus: "reviewing",
				LastActivityAt: time.Date(2026, 7, 1, 14, 0, 0, 0, time.UTC),
				Repository:     testRepository(), Body: "must not be returned",
			}}, nil
		},
		listIssuesFn: func(_ context.Context, query ItemListQuery) ([]Issue, error) {
			issueQuery = query
			return []Issue{{
				Number: 7, Title: "Retry docs", State: "open", Author: "bob",
				URL:            "https://example.test/issues/7",
				LastActivityAt: time.Date(2026, 7, 1, 15, 0, 0, 0, time.UTC),
				Repository:     testRepository(), Body: "must not be returned",
			}}, nil
		},
	}
	s := newMCPTestServer(t, backend)

	out, err := s.searchItems(t.Context(), searchItemsInput{
		Query: "retry", Repo: repoFilterInput{Provider: "github", PlatformRepoID: "repo-acme-widget", Owner: "acme", Name: "widget"},
	})

	require.NoError(err)
	assert.Equal(ItemListQuery{
		Repository: testRepository(), State: "open", Text: "retry", Limit: 26,
	}, pullQuery)
	assert.Equal(pullQuery, issueQuery)
	require.Len(out.Results, 2)
	assert.Equal("issue", out.Results[0].Item.Type)
	assert.Equal("new", out.Results[0].WorkflowStatus)
	assert.Equal("reviewing", out.Results[1].WorkflowStatus)
	raw, err := json.Marshal(out)
	require.NoError(err)
	assert.NotContains(string(raw), "must not be returned")
}

func TestSearchItemsPagesMergedPullsAndReportsCaps(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var offsets []int
	backend := &fakeBackend{listPullsFn: func(_ context.Context, query ItemListQuery) ([]Pull, error) {
		offsets = append(offsets, query.Offset)
		assert.Equal("all", query.State)
		assert.Equal(2, query.Limit)
		if query.Offset == 0 {
			return []Pull{
				{Number: 1, State: "open", Repository: testRepository()},
				{Number: 2, State: "closed", Repository: testRepository()},
			}, nil
		}
		return []Pull{
			{Number: 3, State: "merged", Repository: testRepository()},
			{Number: 4, State: "merged", Repository: testRepository()},
		}, nil
	}}
	s := newMCPTestServer(t, backend)

	out, err := s.searchItems(t.Context(), searchItemsInput{
		Query: "change", State: "merged", ItemTypes: []string{"pr"}, Limit: 1,
	})

	require.NoError(err)
	assert.Equal([]int{0, 2}, offsets)
	require.Len(out.Results, 1)
	assert.Equal(3, out.Results[0].Item.Number)
	assert.True(out.Capped)
}

func TestListActivityForwardsTypedFiltersAndAppliesOutputLimit(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var got ActivityQuery
	backend := &fakeBackend{listActivityFn: func(_ context.Context, query ActivityQuery) (ActivityPage, error) {
		got = query
		return ActivityPage{Items: []ActivityItem{
			{
				ID: "a1", Cursor: "c1", ActivityType: "comment", Repository: testRepository(),
				ItemType: "pr", ItemNumber: 42, ItemTitle: "Retry budget",
				ItemURL: "https://example.test/pulls/42", ItemState: "open",
				Author: "bob", ItemAuthor: "alice", CreatedAt: "2026-07-01T15:00:00Z",
				BodyPreview: "please retry",
			},
			{ID: "a2", ActivityType: "commit", Repository: testRepository()},
		}}, nil
	}}
	s := newMCPTestServer(t, backend)

	out, err := s.listActivity(t.Context(), listActivityInput{
		Since: "2026-07-01T00:00:00Z",
		Repo:  repoFilterInput{Provider: "github", PlatformRepoID: "repo-acme-widget", Owner: "acme", Name: "widget"},
		Types: []string{"comment", "commit"}, Search: "retry", Limit: 1, After: "cursor-1",
	})

	require.NoError(err)
	assert.Equal(ActivityQuery{
		Since: "2026-07-01T00:00:00Z", Repository: testRepository(),
		ActivityTypes: []string{"comment", "commit"}, Search: "retry", After: "cursor-1",
	}, got)
	require.Len(out.Items, 1)
	assert.True(out.Capped)
	assert.Equal("please retry", out.Items[0].BodyPreview)
}
