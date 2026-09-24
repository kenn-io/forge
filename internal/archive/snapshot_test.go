package archive

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/archive/snapshot"
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
	err = database.UpsertMREvents(t.Context(), []db.MREvent{{MergeRequestID: mrID, EventType: "review", Author: "reviewer", AuthorAssociation: new("CONTRIBUTOR"), Body: strings.Repeat("界", 3000), Summary: "CHANGES_REQUESTED", DedupeKey: "review-1", CreatedAt: now}})
	require.NoError(err)
	for i, created := range []time.Time{now.Add(-7 * 24 * time.Hour), now.Add(-time.Hour), now, now.Add(-8 * 24 * time.Hour)} {
		_, err = database.UpsertIssue(t.Context(), &db.Issue{RepoID: repoID, PlatformID: int64(i + 1), Number: i + 1, Title: "Issue", AuthorAssociation: new("FIRST_TIMER"), State: "open", CreatedAt: created, UpdatedAt: now})
		require.NoError(err)
	}
	before := len(provider.calls)
	result, err := service.Snapshot(t.Context(), SnapshotOptions{Start: now.Add(-7 * 24 * time.Hour), End: now})
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
	assert.Equal(strings.Repeat("界", 682), pr.Reviews[0].Body)
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
			issueID, err := database.UpsertIssue(t.Context(), &db.Issue{RepoID: repoID, PlatformID: 3, Number: 3, Title: "Older linked issue", State: "open", CreatedAt: now.Add(-30 * 24 * time.Hour), UpdatedAt: now})
			require.NoError(err)
			require.NoError(database.UpsertIssueEvents(t.Context(), []db.IssueEvent{{IssueID: issueID, EventType: "cross_referenced", DedupeKey: "reference-1", CreatedAt: now, MetadataJSON: `{"source_type":"PullRequest","source_owner":"owner","source_repo":"before","source_number":1,"source_url":"https://provider.test/owner/before/pull/1"}`}}))
			first, err := service.Snapshot(t.Context(), SnapshotOptions{Start: now.Add(-time.Hour), End: now})
			require.NoError(err)
			require.Len(first.PullRequests, 1)
			require.Len(first.Relations, 1)
			renamed := ref
			renamed.Name = "after"
			renamed.RepoPath = "owner/after"
			entry, accepted, err := database.ReconcileRepositoryObservation(t.Context(), platformdb.DBRepoIdentity(renamed), time.Now().Add(time.Hour))
			require.NoError(err)
			require.True(accepted)
			assert.Equal(repoID, entry.Repository.ID)
			second, err := service.snapshot(t.Context(), SnapshotOptions{Start: now.Add(-time.Hour), End: now}, func() error {
				_, writeErr := database.UpsertIssue(t.Context(), &db.Issue{RepoID: repoID, Number: 2, Title: "Concurrent", State: "open", CreatedAt: now.Add(-time.Minute), UpdatedAt: now})
				return writeErr
			})
			require.NoError(err)
			require.Len(second.PullRequests, 1)
			assert.Equal(first.PullRequests[0].ID, second.PullRequests[0].ID)
			assert.Equal("owner/after", second.Repositories[0].Path)
			require.Len(second.Issues, 1, "retain the old linked issue, excluding the concurrent write")
			assert.Equal(3, second.Issues[0].Number)
			assert.Equal(first.Relations, second.Relations, "renames must preserve references")
			third, err := service.Snapshot(t.Context(), SnapshotOptions{Start: now.Add(-time.Hour), End: now})
			require.NoError(err)
			assert.Len(third.Issues, 2)

			replacement := ref
			replacement.PlatformExternalID = "replacement-id"
			entry, accepted, err = database.ReconcileRepositoryObservation(t.Context(), platformdb.DBRepoIdentity(replacement), time.Now().Add(2*time.Hour))
			require.NoError(err)
			require.True(accepted)
			_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: entry.Repository.ID, Number: 1, Title: "Different repository", State: db.MergeRequestStateOpen, CreatedAt: now, UpdatedAt: now})
			require.NoError(err)
			fourth, err := service.Snapshot(t.Context(), SnapshotOptions{Start: now.Add(-time.Hour), End: now})
			require.NoError(err)
			assert.Empty(fourth.Relations, "a reused route cannot establish reference identity")
			require.Len(fourth.PullRequests, 1)
			assert.Contains(fourth.PullRequests[0].Gaps, "unresolved_issue_reference")
			require.NoError(database.UpsertIssueEvents(t.Context(), []db.IssueEvent{{IssueID: issueID, EventType: "cross_referenced", DedupeKey: "reference-1", CreatedAt: now, MetadataJSON: `{"source_type":"PullRequest","source_owner":"owner","source_repo":"after","source_number":1,"source_url":"https://provider.test/owner/after/pull/1"}`}}))
			refreshed, err := service.Snapshot(t.Context(), SnapshotOptions{Start: now.Add(-time.Hour), End: now})
			require.NoError(err)
			require.Len(refreshed.Relations, 1, "one event must remain one link after a renamed reference is refreshed")
			assert.Equal("https://provider.test/owner/after/pull/1", refreshed.Relations[0].URL)
			assert.NotContains(refreshed.PullRequests[0].Gaps, "unresolved_issue_reference")
		})
	}
}

func TestSnapshotPreflightsReviewVolume(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := archiveTestTime()
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "large")
	repoID := archiveServiceSeedRepo(t, database, ref)
	small := archiveServiceRef(platform.KindGitHub, "github.test", "small")
	smallID := archiveServiceSeedRepo(t, database, small)
	registry, err := platform.NewRegistry(newArchiveServiceProvider(ref.Platform, ref.Host))
	require.NoError(err)
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref, small}, nil, now)
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: smallID, Number: 2, Title: "Small repository", State: db.MergeRequestStateOpen, CreatedAt: now, UpdatedAt: now})
	require.NoError(err)
	mrID, err := database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: repoID, Number: 1, Title: "Long review history", State: db.MergeRequestStateOpen, CreatedAt: now, UpdatedAt: now})
	require.NoError(err)
	_, err = database.WriteDB().ExecContext(t.Context(), `
		WITH RECURSIVE sequence(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM sequence WHERE n<10001)
		INSERT INTO forge_mr_events (merge_request_id,event_type,dedupe_key,created_at)
		SELECT ?, 'review', 'review-' || n, ? FROM sequence`, mrID, now)
	require.NoError(err)
	_, err = service.Snapshot(t.Context(), SnapshotOptions{Start: now.Add(-time.Hour), End: now})
	require.ErrorIs(err, snapshot.ErrTooLarge)
	scoped, err := service.Snapshot(t.Context(), SnapshotOptions{Start: now.Add(-time.Hour), End: now, Repositories: []platform.RepoRef{small}})
	require.NoError(err)
	require.Len(scoped.Repositories, 1)
	require.Len(scoped.PullRequests, 1)
	assert.Equal("Small repository", scoped.PullRequests[0].Title)

	// Fewer records can still exceed the text budget through untruncated metadata.
	_, err = database.WriteDB().ExecContext(t.Context(), `DELETE FROM forge_mr_events WHERE merge_request_id=? AND id % 2=0`, mrID)
	require.NoError(err)
	_, err = database.WriteDB().ExecContext(t.Context(), `UPDATE forge_mr_events SET direct_url=? WHERE merge_request_id=?`, strings.Repeat("u", 8192), mrID)
	require.NoError(err)
	_, err = service.Snapshot(t.Context(), SnapshotOptions{Start: now.Add(-time.Hour), End: now})
	require.ErrorIs(err, snapshot.ErrTooLarge)
}
