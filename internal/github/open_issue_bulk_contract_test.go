package github

import (
	"testing"
	"time"

	gh "github.com/google/go-github/v88/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/platform"
)

func bulkTestIssue(number int, repositoryURL string) *gh.Issue {
	title := "issue"
	state := "open"
	id := int64(1000 + number)
	issue := &gh.Issue{
		ID:        &id,
		Number:    &number,
		Title:     &title,
		State:     &state,
		CreatedAt: makeTimestamp(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)),
		UpdatedAt: makeTimestamp(time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)),
	}
	if repositoryURL != "" {
		issue.RepositoryURL = &repositoryURL
	}
	return issue
}

// TestGitHubListOpenIssuesRejectsMalformedBulkResults proves the raw
// open-issue bulk observation used by the optimized index sync applies the
// same contract checks the validating canonical reader wrapper would run on a
// normalized page: items must be non-nil, numbers positive and unique within
// one open list, and every item bound to the requested repository. A
// malformed bulk result is a typed provider contract error.
func TestGitHubListOpenIssuesRejectsMalformedBulkResults(t *testing.T) {
	ref := platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.com",
		Owner: "acme", Name: "widget", RepoPath: "acme/widget",
	}

	tests := []struct {
		name   string
		issues []*gh.Issue
	}{
		{
			name:   "nil item",
			issues: []*gh.Issue{bulkTestIssue(1, ""), nil},
		},
		{
			name:   "nonpositive number",
			issues: []*gh.Issue{bulkTestIssue(0, "")},
		},
		{
			name:   "duplicate number",
			issues: []*gh.Issue{bulkTestIssue(3, ""), bulkTestIssue(3, "")},
		},
		{
			name: "foreign repository binding",
			issues: []*gh.Issue{
				bulkTestIssue(4, "https://api.github.com/repos/acme/other"),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			provider := &gitHubClientProvider{
				host:   "github.com",
				client: &mockClient{openIssues: tt.issues},
			}
			_, err := provider.ListOpenGitHubIssues(t.Context(), ref)
			require.ErrorIs(err, platform.ErrProviderContract)
			var typed *platform.Error
			require.ErrorAs(err, &typed)
			assert.Equal(platform.KindGitHub, typed.Provider)
			assert.Equal("github.com", typed.PlatformHost)
		})
	}

	t.Run("well-formed list passes", func(t *testing.T) {
		provider := &gitHubClientProvider{
			host: "github.com",
			client: &mockClient{openIssues: []*gh.Issue{
				bulkTestIssue(1, "https://api.github.com/repos/acme/widget"),
				bulkTestIssue(2, ""),
			}},
		}
		issues, err := provider.ListOpenGitHubIssues(t.Context(), ref)
		require.NoError(t, err)
		assert.Len(t, issues, 2)
	})
}

// TestSyncerRejectsMalformedOpenIssueBulkListWithoutPersisting proves the
// index sync consumer of the raw bulk path treats a contract violation like
// any other issue-list failure: nothing from the malformed list is persisted
// (including the well-formed items it contains) and the repo is marked failed
// so the next cycle refetches unconditionally.
func TestSyncerRejectsMalformedOpenIssueBulkListWithoutPersisting(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repos := []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}
	mc := &mockClient{
		listOpenPRsErr: notModifiedErr(),
		openIssues: []*gh.Issue{
			bulkTestIssue(7, ""),
			bulkTestIssue(8, "https://api.github.com/repos/owner/other"),
		},
	}

	syncer := NewSyncer(
		map[string]Client{"github.com": mc},
		d, nil, repos, time.Minute, nil, nil,
	)
	syncer.RunOnce(ctx)

	for _, number := range []int{7, 8} {
		issue, err := d.GetIssue(ctx, "github", "github.com", "owner", "repo", number)
		require.NoError(err)
		assert.Nil(issue, "no issue from a malformed bulk list may be persisted")
	}
	_, flagged := syncer.failedRepos.Load(repoFailKey(repos[0]))
	assert.True(flagged, "repo must be marked failed after a bulk contract violation")
}
