package github

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/platformdb"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/platform"
)

func TestDetailSyncPreservesThreadResolutionWrittenDuringFetch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		resolved bool
	}{
		{name: "resolve", resolved: true},
		{name: "unresolve", resolved: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require := require.New(t)
			assert := assert.New(t)
			ctx := t.Context()
			d := openTestDB(t)
			repo := RepoRef{
				Platform: platform.KindGitLab, PlatformHost: "gitlab.test",
				Owner: "group", Name: "project", RepoPath: "group/project",
				Key: platform.RepositoryIDKey(1001),
			}
			repoID, err := reposeed.Seed(ctx, d, verifiedDBRepoIdentity(platformRepoRef(repo)))
			require.NoError(err)
			now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
			mr := platform.MergeRequest{
				Repo: platformRepoRef(repo), PlatformID: 1, Number: 1,
				URL:   "https://gitlab.test/group/project/-/merge_requests/1",
				Title: "Example change", Author: "user-a", State: "open",
				CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
			}
			parent := platformdb.DBMergeRequest(repoID, mr)
			parent.DetailFetchedAt = &now
			mrID, err := d.UpsertMergeRequest(ctx, parent)
			require.NoError(err)
			var initialResolvedAt, resolvedAt *time.Time
			if tc.resolved {
				resolvedAt = &now
			} else {
				initialResolvedAt = &now
			}
			require.NoError(d.UpsertMRReviewThreads(ctx, mrID, []db.MRReviewThread{{
				ProviderThreadID: "discussion-1", Body: "inline note",
				Resolved: !tc.resolved, ResolvedAt: initialResolvedAt, CreatedAt: now, UpdatedAt: now,
			}}))
			threads, err := d.ListMRReviewThreads(ctx, mrID)
			require.NoError(err)
			require.Len(threads, 1)
			threadID := threads[0].ID
			provider := &syncTestReadProvider{
				kind: platform.KindGitLab, host: "gitlab.test", readReviewThreads: true,
				mergeRequests: []platform.MergeRequest{mr},
				listReviewThreadsFn: func(ctx context.Context, _ platform.RepoRef, _ int) ([]platform.MergeRequestReviewThread, error) {
					// The provider read predates the local resolution change.
					err := d.SetMRReviewThreadResolved(ctx, mrID, threadID, tc.resolved, resolvedAt)
					return []platform.MergeRequestReviewThread{{
						ProviderThreadID: "discussion-1", Body: "inline note",
						Resolved: !tc.resolved, ResolvedAt: initialResolvedAt, CreatedAt: now, UpdatedAt: now,
					}}, err
				},
			}
			registry, err := platform.NewRegistry(provider)
			require.NoError(err)
			syncer := NewSyncerWithRegistry(registry, d, nil, []RepoRef{repo}, time.Minute, nil, nil)
			_, err = syncer.fetchProviderMRDetail(ctx, provider, repo, repoID, 1)
			require.NoError(err)
			assert.Equal(int32(1), provider.listReviewThreads.Load())

			threads, err = d.ListMRReviewThreads(ctx, mrID)
			require.NoError(err)
			require.Len(threads, 1)
			assert.Equal(tc.resolved, threads[0].Resolved)
			assert.Equal(resolvedAt, threads[0].ResolvedAt)
		})
	}
}
