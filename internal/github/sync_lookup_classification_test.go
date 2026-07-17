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

// TestSyncerDoesNotPersistTransferredIssueUnderSourceRepo proves through the
// real repository sync + SQLite that a transferred item is never rewritten
// under its source repository. It seeds an open issue in repo A via a full
// sync, then runs a second full sync where the issue is absent from A's open
// inventory and the closed-item detail fetch returns the item as it appears
// after the transfer to repo B (GitHub follows the 301 and serves the item
// with the destination repository URL). The source row must keep its repo-A
// data, nothing may be persisted under repo B, and the sync outcome must be
// observable as a failed repo cycle.
func TestSyncerDoesNotPersistTransferredIssueUnderSourceRepo(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	repos := []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}

	issueNumber := 7
	sourceTitle := "issue before transfer"
	sourceURL := "https://github.com/owner/repo/issues/7"
	openState := "open"
	body := ""
	issueID := int64(777)
	sourceIssue := &gh.Issue{
		ID:        &issueID,
		Number:    &issueNumber,
		Title:     &sourceTitle,
		State:     &openState,
		HTMLURL:   &sourceURL,
		Body:      &body,
		CreatedAt: makeTimestamp(now),
		UpdatedAt: makeTimestamp(now),
	}

	// Seed issue #7 as open under repo A via an initial full sync.
	seedMC := &mockClient{openIssues: []*gh.Issue{sourceIssue}}
	seedMC.getIssueFn = func(context.Context, string, string, int) (*gh.Issue, error) {
		return sourceIssue, nil
	}
	seedSyncer := NewSyncer(
		map[string]Client{"github.com": seedMC},
		d, nil, repos, time.Minute, nil, nil,
	)
	seedSyncer.RunOnce(ctx)

	seeded, err := d.GetIssue(ctx, "github", "github.com", "owner", "repo", issueNumber)
	require.NoError(err)
	require.NotNil(seeded, "seed cycle should persist issue #7 under repo A")
	require.Equal(sourceTitle, seeded.Title)

	// Second sync: the issue is gone from A's open inventory, and the
	// closed-item detail fetch returns it as it now exists in repo B.
	movedTitle := "issue after transfer"
	movedURL := "https://github.com/newowner/newname/issues/7"
	movedRepositoryURL := "https://api.github.com/repos/newowner/newname"
	movedIssue := &gh.Issue{
		ID:            &issueID,
		Number:        &issueNumber,
		Title:         &movedTitle,
		State:         &openState,
		HTMLURL:       &movedURL,
		RepositoryURL: &movedRepositoryURL,
		Body:          &body,
		CreatedAt:     makeTimestamp(now),
		UpdatedAt:     makeTimestamp(now.Add(time.Hour)),
	}
	mc := &mockClient{openIssues: []*gh.Issue{}}
	mc.getIssueFn = func(context.Context, string, string, int) (*gh.Issue, error) {
		return movedIssue, nil
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": mc},
		d, nil, repos, time.Minute, nil, nil,
	)
	syncer.RunOnce(ctx)

	// The transfer must be observable as a failed repo cycle rather than
	// silently swallowed or persisted.
	_, flagged := syncer.failedRepos.Load(repoFailKey(repos[0]))
	assert.True(flagged, "repo must be marked failed after a transferred closed item")

	// The source row keeps its repo-A identity and data.
	source, err := d.GetIssue(ctx, "github", "github.com", "owner", "repo", issueNumber)
	require.NoError(err)
	require.NotNil(source, "source row must survive the failed transfer refresh")
	assert.Equal(sourceTitle, source.Title,
		"source row must not be rewritten with destination data")
	assert.Equal(sourceURL, source.URL,
		"source row must keep the repo-A item URL")
	assert.Equal("open", source.State,
		"source row must not be flipped by the transferred item's state")

	// Nothing may appear under the destination repository.
	destination, err := d.GetIssue(
		ctx, "github", "github.com", "newowner", "newname", issueNumber,
	)
	require.NoError(err)
	assert.Nil(destination, "no row may be persisted under the destination repo")
}

// TestGitHubArchiveDestinationIgnoresRepoCasing pins that transfer detection
// treats GitHub owner/repo names as case-insensitive: a RepoRef that differs
// from the provider-returned repository URL only in casing identifies the
// same repository and must not be classified as moved, while a genuinely
// different repository still yields a lowercased canonical destination.
func TestGitHubArchiveDestinationIgnoresRepoCasing(t *testing.T) {
	ref := platform.RepoRef{
		Platform: platform.KindGitHub,
		Host:     "github.com",
		Owner:    "MixedOwner",
		Name:     "MixedName",
		RepoPath: "MixedOwner/MixedName",
	}

	cases := []struct {
		name      string
		url       string
		wantMoved bool
	}{
		{
			name: "same repo lowercased in URL",
			url:  "https://api.github.com/repos/mixedowner/mixedname",
		},
		{
			name: "same repo mixed case in URL",
			url:  "https://api.github.com/repos/MixedOwner/MixedName",
		},
		{
			name:      "different repo",
			url:       "https://api.github.com/repos/OtherOwner/OtherName",
			wantMoved: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			destination := githubArchiveDestination(ref, tc.url)
			if !tc.wantMoved {
				assert.Nil(destination)
				return
			}
			require.NotNil(t, destination)
			assert.Equal("otherowner", destination.Owner)
			assert.Equal("othername", destination.Name)
		})
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
		{
			// The closed-issue refresh path.
			name: "closed issue refresh",
			arrange: func(mc *mockClient, fetchErr error) {
				mc.getIssueFn = func(context.Context, string, string, int) (*gh.Issue, error) {
					if fetchErr != nil {
						return nil, fetchErr
					}
					return movedIssue(), nil
				}
			},
			run: func(ctx context.Context, syncer *Syncer, repo RepoRef, repoID int64) error {
				return syncer.fetchAndUpdateClosedIssue(ctx, repo, repoID, 5)
			},
		},
		{
			// The closed-MR refresh path.
			name: "closed merge request refresh",
			arrange: func(mc *mockClient, fetchErr error) {
				mc.getPullRequestFn = func(context.Context, string, string, int) (*gh.PullRequest, error) {
					if fetchErr != nil {
						return nil, fetchErr
					}
					return movedPR(), nil
				}
			},
			run: func(ctx context.Context, syncer *Syncer, repo RepoRef, repoID int64) error {
				return syncer.fetchAndUpdateClosed(ctx, repo, repoID, 5, false)
			},
		},
		{
			// The item-resolution path used by /resolve/{number}, which
			// probes the Issues API to determine the item type.
			name: "item resolution",
			arrange: func(mc *mockClient, fetchErr error) {
				mc.getIssueFn = func(context.Context, string, string, int) (*gh.Issue, error) {
					if fetchErr != nil {
						return nil, fetchErr
					}
					return movedIssue(), nil
				}
			},
			run: func(ctx context.Context, syncer *Syncer, repo RepoRef, _ int64) error {
				_, err := syncer.SyncItemByNumber(ctx, repo.Owner, repo.Name, 5)
				return err
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
