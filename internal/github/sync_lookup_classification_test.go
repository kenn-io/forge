package github

import (
	"context"
	"net/http"
	"testing"
	"time"

	gh "github.com/google/go-github/v88/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/platform"
)

// ghLookupStatusError builds the go-github error shape for a single-item
// fetch that failed with the given HTTP status.
func ghLookupStatusError(status int) error {
	return &gh.ErrorResponse{
		Response: &http.Response{StatusCode: status, Header: http.Header{}},
	}
}

// TestSyncDetailFetchClassifiesLookupOutcomes drives the optimized GitHub
// detail-sync paths through the real sync + SQLite with a fake client and
// asserts that removed, inaccessible, and moved items surface the typed
// lookup-outcome errors instead of generic upstream failures: removed is
// not_found, inaccessible is permission_denied, and a repository transfer
// is not_found carrying the destination repository.
func TestSyncDetailFetchClassifiesLookupOutcomes(t *testing.T) {
	const movedRepoAPIURL = "https://api.github.com/repos/newowner/newname"

	outcomes := []struct {
		name            string
		fetchErr        error
		wantCode        platform.PlatformErrorCode
		wantDestination bool
	}{
		{
			name:     "removed",
			fetchErr: ghLookupStatusError(http.StatusNotFound),
			wantCode: platform.ErrCodeNotFound,
		},
		{
			name:     "inaccessible",
			fetchErr: ghLookupStatusError(http.StatusForbidden),
			wantCode: platform.ErrCodePermissionDenied,
		},
		{
			name:            "moved",
			wantCode:        platform.ErrCodeNotFound,
			wantDestination: true,
		},
	}

	movedIssue := func() *gh.Issue {
		return &gh.Issue{
			Number:        new(5),
			RepositoryURL: gh.Ptr(movedRepoAPIURL),
		}
	}
	movedPR := func() *gh.PullRequest {
		return &gh.PullRequest{
			Number: new(5),
			Base: &gh.PullRequestBranch{
				Repo: &gh.Repository{URL: gh.Ptr(movedRepoAPIURL)},
			},
		}
	}

	paths := []struct {
		name    string
		arrange func(mc *mockClient, fetchErr error)
		run     func(ctx context.Context, syncer *Syncer, repo RepoRef, repoID int64) error
	}{
		{
			// The issue detail-queue path (fetchIssueDetail).
			name: "issue detail",
			arrange: func(mc *mockClient, fetchErr error) {
				mc.getIssueFn = func(context.Context, string, string, int) (*gh.Issue, error) {
					if fetchErr != nil {
						return nil, fetchErr
					}
					return movedIssue(), nil
				}
			},
			run: func(ctx context.Context, syncer *Syncer, repo RepoRef, repoID int64) error {
				_, err := syncer.fetchIssueDetail(ctx, repo, repoID, 5)
				return err
			},
		},
		{
			// The MR detail-queue path (fetchMRDetail).
			name: "merge request detail",
			arrange: func(mc *mockClient, fetchErr error) {
				mc.getPullRequestFn = func(context.Context, string, string, int) (*gh.PullRequest, error) {
					if fetchErr != nil {
						return nil, fetchErr
					}
					return movedPR(), nil
				}
			},
			run: func(ctx context.Context, syncer *Syncer, repo RepoRef, repoID int64) error {
				_, err := syncer.fetchMRDetail(ctx, repo, repoID, 5, false)
				return err
			},
		},
		{
			// The single-MR sync path that fetches through the raw
			// GetGitHubPullRequest reader.
			name: "merge request raw sync",
			arrange: func(mc *mockClient, fetchErr error) {
				mc.getPullRequestFn = func(context.Context, string, string, int) (*gh.PullRequest, error) {
					if fetchErr != nil {
						return nil, fetchErr
					}
					return movedPR(), nil
				}
			},
			run: func(ctx context.Context, syncer *Syncer, repo RepoRef, _ int64) error {
				return syncer.SyncMROnProvider(
					ctx, platform.KindGitHub, "github.com", repo.Owner, repo.Name, 5,
				)
			},
		},
	}

	for _, path := range paths {
		for _, outcome := range outcomes {
			t.Run(path.name+" "+outcome.name, func(t *testing.T) {
				assert := assert.New(t)
				require := require.New(t)
				ctx := t.Context()
				d := openTestDB(t)

				repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}
				repoID, err := d.UpsertRepo(
					ctx, db.GitHubRepoIdentity("github.com", repo.Owner, repo.Name),
				)
				require.NoError(err)

				mc := &mockClient{}
				path.arrange(mc, outcome.fetchErr)
				syncer := NewSyncer(
					map[string]Client{"github.com": mc}, d, nil,
					[]RepoRef{repo}, time.Minute, nil, testBudget(1000),
				)

				err = path.run(ctx, syncer, repo, repoID)
				require.Error(err)
				var platformErr *platform.Error
				require.ErrorAs(err, &platformErr,
					"detail fetch must surface a typed platform error, got %v", err)
				assert.Equal(outcome.wantCode, platformErr.Code)
				if !outcome.wantDestination {
					assert.Nil(platformErr.Destination)
					return
				}
				require.NotNil(platformErr.Destination)
				assert.Equal(platform.KindGitHub, platformErr.Destination.Platform)
				assert.Equal("github.com", platformErr.Destination.Host)
				assert.Equal("newowner", platformErr.Destination.Owner)
				assert.Equal("newname", platformErr.Destination.Name)
			})
		}
	}
}
