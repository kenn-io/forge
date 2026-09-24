package archive

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/platformdb"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/platform"
)

func TestSnapshotReadsConfiguredCachedWork(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "project")
	repoID := archiveServiceSeedRepo(t, database, ref)
	other := archiveServiceRef(platform.KindGitHub, "github.test", "not-configured")
	otherID := archiveServiceSeedRepo(t, database, other)
	provider := newArchiveServiceProvider(ref.Platform, ref.Host)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, now)
	requireEnsureConfigured(t, service, []platform.RepoRef{ref})
	mrID, err := database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: repoID, Number: 7, Title: "Release repair", Author: "author", AuthorAssociation: new("MEMBER"), State: db.MergeRequestStateOpen, IsDraft: true, Body: strings.Repeat("x", 9000), CreatedAt: now.Add(-90 * 24 * time.Hour), UpdatedAt: now, Additions: 42, AdditionsKnown: true})
	require.NoError(err)
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: otherID, Number: 8, Title: "Outside scope", State: db.MergeRequestStateOpen, CreatedAt: now, UpdatedAt: now})
	require.NoError(err)
	err = database.UpsertMREvents(t.Context(), []db.MREvent{{MergeRequestID: mrID, EventType: "review", Author: "reviewer", AuthorAssociation: new("CONTRIBUTOR"), Body: strings.Repeat("y", 3000), Summary: "CHANGES_REQUESTED", DedupeKey: "review-1", CreatedAt: now}})
	require.NoError(err)
	for i, created := range []time.Time{now.Add(-7 * 24 * time.Hour), now.Add(-time.Hour), now, now.Add(-8 * 24 * time.Hour)} {
		_, err = database.UpsertIssue(t.Context(), &db.Issue{RepoID: repoID, PlatformID: int64(i + 1), Number: i + 1, Title: "Issue", AuthorAssociation: new("FIRST_TIMER"), State: "open", CreatedAt: created, UpdatedAt: now})
		require.NoError(err)
	}
	before := len(provider.calls)
	result, err := service.Snapshot(t.Context(), now.Add(-7*24*time.Hour), now)
	require.NoError(err)
	assert.Len(provider.calls, before)
	require.Len(result.Repositories, 1)
	assert.Equal(ref.PlatformExternalID, result.Repositories[0].ProviderID)
	require.Len(result.PullRequests, 1)
	pr := result.PullRequests[0]
	assert.True(pr.Draft)
	assert.Equal(new("MEMBER"), pr.AuthorAssociation)
	assert.Len(pr.Body, 8192)
	assert.True(pr.BodyTruncated)
	require.NotNil(pr.Additions)
	assert.Equal(42, *pr.Additions)
	assert.Nil(pr.Deletions, "zero values lack persisted availability metadata")
	require.Len(pr.Reviews, 1)
	assert.Equal("reviewer", pr.Reviews[0].Author)
	assert.Equal(new("CONTRIBUTOR"), pr.Reviews[0].AuthorAssociation)
	assert.Len(pr.Reviews[0].Body, 2048)
	assert.True(pr.Reviews[0].BodyTruncated)
	require.Len(result.Issues, 2)
	assert.Equal(new("FIRST_TIMER"), result.Issues[0].AuthorAssociation)
	assert.Equal(now, result.ObservedAt)
}

func TestSnapshotRetainsStableIdentityAndOneReadView(t *testing.T) {
	for _, kind := range []platform.Kind{platform.KindGitHub, platform.KindGitLab, platform.KindForgejo, platform.KindGitea} {
		t.Run(string(kind), func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			database := dbtest.Open(t)
			now := archiveTestTime()
			ref := archiveServiceRef(kind, "provider.test", "before")
			repoID := archiveServiceSeedRepo(t, database, ref)
			registry, err := platform.NewRegistry(newArchiveServiceProvider(kind, ref.Host))
			require.NoError(err)
			service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, now)
			requireEnsureConfigured(t, service, []platform.RepoRef{ref})
			_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: repoID, Number: 1, Title: "Cached", State: db.MergeRequestStateOpen, CreatedAt: now, UpdatedAt: now})
			require.NoError(err)
			first, err := service.Snapshot(t.Context(), now.Add(-time.Hour), now)
			require.NoError(err)
			require.Len(first.PullRequests, 1)
			renamed := ref
			renamed.Name = "after"
			renamed.RepoPath = "owner/after"
			entry, accepted, err := database.ReconcileRepositoryObservation(t.Context(), platformdb.DBRepoIdentity(renamed), time.Now().Add(time.Hour))
			require.NoError(err)
			require.True(accepted)
			assert.Equal(repoID, entry.Repository.ID)
			second, err := service.snapshot(t.Context(), now.Add(-time.Hour), now, func() error {
				_, writeErr := database.UpsertIssue(t.Context(), &db.Issue{RepoID: repoID, Number: 2, Title: "Concurrent", State: "open", CreatedAt: now.Add(-time.Minute), UpdatedAt: now})
				return writeErr
			})
			require.NoError(err)
			require.Len(second.PullRequests, 1)
			assert.Equal(first.PullRequests[0].ID, second.PullRequests[0].ID)
			assert.Equal("owner/after", second.Repositories[0].Path)
			assert.Empty(second.Issues, "the concurrent write must not enter an established snapshot")
			third, err := service.Snapshot(t.Context(), now.Add(-time.Hour), now)
			require.NoError(err)
			assert.Len(third.Issues, 1)
		})
	}
}
