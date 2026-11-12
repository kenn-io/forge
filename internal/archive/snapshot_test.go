package archive

import (
	"database/sql"
	"encoding/json/v2"
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
	t.Parallel()
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
	wantProviderID, ok := ref.Key.ID()
	require.True(ok)
	assert.Equal(wantProviderID, result.Repositories[0].ProviderID)
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

func TestSnapshotMergeStatusObservedAtIsTheOldestObservedTime(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := archiveTestTime()
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "project")
	repoID := archiveServiceSeedRepo(t, database, ref)
	registry, err := platform.NewRegistry(newArchiveServiceProvider(ref.Platform, ref.Host))
	require.NoError(err)
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, now)

	ci := now.Add(-3 * time.Hour)
	review := now.Add(-2 * time.Hour)
	mergeable := now.Add(-1 * time.Hour)
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: repoID, PlatformID: 1, Number: 1, Title: "All observed", State: db.MergeRequestStateOpen,
		CreatedAt: now, UpdatedAt: now,
		CIObservedAt: &ci, ReviewDecisionObservedAt: &review, MergeableStateObservedAt: &mergeable,
	})
	require.NoError(err)
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: repoID, PlatformID: 2, Number: 2, Title: "Mergeable time unknown", State: db.MergeRequestStateOpen,
		CreatedAt: now, UpdatedAt: now,
		CIObservedAt: &ci, ReviewDecisionObservedAt: &review,
	})
	require.NoError(err)

	result, err := service.Snapshot(t.Context(), SnapshotOptions{Start: now.Add(-time.Hour), End: now})
	require.NoError(err)
	require.Len(result.PullRequests, 2)

	allObserved := result.PullRequests[0]
	require.NotNil(allObserved.MergeStatusObservedAt)
	assert.True(ci.Equal(*allObserved.MergeStatusObservedAt), "the oldest of the three GitHub times")
	assert.NotContains(allObserved.Gaps, "merge_status_observation_time_unknown")

	oneUnknown := result.PullRequests[1]
	assert.Nil(oneUnknown.MergeStatusObservedAt)
	assert.Contains(oneUnknown.Gaps, "merge_status_observation_time_unknown")
}

func TestSnapshotMergeStatusObservedAtIgnoresReviewDecisionOffGitHub(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := archiveTestTime()
	ref := archiveServiceRef(platform.KindGitLab, "gitlab.test", "project")
	repoID := archiveServiceSeedRepo(t, database, ref)
	registry, err := platform.NewRegistry(newArchiveServiceProvider(ref.Platform, ref.Host))
	require.NoError(err)
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, now)

	ci := now.Add(-2 * time.Hour)
	mergeable := now.Add(-1 * time.Hour)
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: repoID, Number: 1, Title: "GitLab PR", State: db.MergeRequestStateOpen,
		CreatedAt: now, UpdatedAt: now,
		CIObservedAt: &ci, MergeableStateObservedAt: &mergeable,
	})
	require.NoError(err)

	result, err := service.Snapshot(t.Context(), SnapshotOptions{Start: now.Add(-time.Hour), End: now})
	require.NoError(err)
	require.Len(result.PullRequests, 1)
	pull := result.PullRequests[0]
	require.NotNil(pull.MergeStatusObservedAt, "GitLab never reports a review decision, so it must not count")
	assert.True(ci.Equal(*pull.MergeStatusObservedAt), "the older of CI and mergeable times")
	assert.NotContains(pull.Gaps, "merge_status_observation_time_unknown")
}

func TestMinObservedAtOnEmptyInputIsNilNotZeroTime(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	assert.Nil(minObservedAt(nil))
	assert.Nil(minObservedAt([]*time.Time{}))
}

func TestSnapshotExportsOwnershipAndActivity(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := archiveTestTime()
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "project")
	repoID := archiveServiceSeedRepo(t, database, ref)
	registry, err := platform.NewRegistry(newArchiveServiceProvider(ref.Platform, ref.Host))
	require.NoError(err)
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, now)

	lastActivity := now.Add(-time.Minute)
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: repoID, PlatformID: 1, Number: 1, Title: "Never reported", State: db.MergeRequestStateOpen,
		CreatedAt: now, UpdatedAt: now, LastActivityAt: lastActivity,
	})
	require.NoError(err)
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: repoID, PlatformID: 2, Number: 2, Title: "Confirmed empty", State: db.MergeRequestStateOpen,
		CreatedAt: now, UpdatedAt: now, LastActivityAt: lastActivity,
		AssigneesJSON: "[]", ReviewersJSON: "[]",
	})
	require.NoError(err)
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: repoID, PlatformID: 3, Number: 3, Title: "Has owners", State: db.MergeRequestStateOpen,
		CreatedAt: now, UpdatedAt: now, LastActivityAt: lastActivity,
		AssigneesJSON: `["alice"]`, ReviewersJSON: `["bob"]`,
	})
	require.NoError(err)
	closedAt := now.Add(-24 * time.Hour)
	_, err = database.UpsertIssue(t.Context(), &db.Issue{
		RepoID: repoID, PlatformID: 4, Number: 4, Title: "Closed issue", State: "closed",
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now, LastActivityAt: lastActivity, ClosedAt: &closedAt,
		AssigneesJSON: `["carol"]`,
	})
	require.NoError(err)

	result, err := service.Snapshot(t.Context(), SnapshotOptions{Start: now.Add(-time.Hour), End: now})
	require.NoError(err)
	require.Len(result.PullRequests, 3)
	require.Len(result.Issues, 1)

	neverReported := result.PullRequests[0]
	assert.Nil(neverReported.Assignees)
	assert.Nil(neverReported.RequestedReviewers)
	require.NotNil(neverReported.LastActivityAt)
	assert.True(lastActivity.Equal(*neverReported.LastActivityAt))
	assert.Nil(neverReported.ClosedAt, "pull requests in scope are always open")

	confirmedEmpty := result.PullRequests[1]
	assert.Equal([]string{}, confirmedEmpty.Assignees)
	assert.Equal([]string{}, confirmedEmpty.RequestedReviewers)

	hasOwners := result.PullRequests[2]
	assert.Equal([]string{"alice"}, hasOwners.Assignees)
	assert.Equal([]string{"bob"}, hasOwners.RequestedReviewers)

	issue := result.Issues[0]
	assert.Equal([]string{"carol"}, issue.Assignees)
	require.NotNil(issue.LastActivityAt)
	assert.True(lastActivity.Equal(*issue.LastActivityAt))
	require.NotNil(issue.ClosedAt)
	assert.True(closedAt.Equal(*issue.ClosedAt))
}

func TestSnapshotOmitsClosedAtForOpenPullRequests(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	database := dbtest.Open(t)
	now := archiveTestTime()
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "project")
	repoID := archiveServiceSeedRepo(t, database, ref)
	registry, err := platform.NewRegistry(newArchiveServiceProvider(ref.Platform, ref.Host))
	require.NoError(err)
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, now)
	// A reopened pull request can still carry its earlier close time.
	earlierClose := now.Add(-24 * time.Hour)
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: repoID, PlatformID: 1, Number: 1, Title: "Reopened", State: db.MergeRequestStateOpen,
		CreatedAt: now.Add(-48 * time.Hour), UpdatedAt: now, LastActivityAt: now, ClosedAt: &earlierClose,
	})
	require.NoError(err)

	result, err := service.Snapshot(t.Context(), SnapshotOptions{Start: now.Add(-time.Hour), End: now})
	require.NoError(err)
	require.Len(result.PullRequests, 1)
	assert.Nil(t, result.PullRequests[0].ClosedAt)
}

func TestSnapshotOpenIssueScope(t *testing.T) {
	t.Parallel()
	for _, kind := range []platform.Kind{platform.KindGitHub, platform.KindGitLab, platform.KindForgejo, platform.KindGitea} {
		t.Run(string(kind), func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			database := dbtest.Open(t)
			now := archiveTestTime()
			ref := archiveServiceRef(kind, "provider.test", "project")
			repoID := archiveServiceSeedRepo(t, database, ref)
			provider := newArchiveServiceProvider(kind, ref.Host)
			registry, err := platform.NewRegistry(provider)
			require.NoError(err)
			service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, now)
			_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: repoID, Number: 1, Title: "Open fix", State: db.MergeRequestStateOpen, CreatedAt: now.Add(-90 * 24 * time.Hour), UpdatedAt: now})
			require.NoError(err)
			for _, item := range []struct {
				number  int
				state   string
				created time.Time
			}{{2, "open", now.Add(-90 * 24 * time.Hour)}, {3, "closed", now.Add(-time.Minute)}, {4, "closed", now.Add(-90 * 24 * time.Hour)}} {
				id, err := database.UpsertIssue(t.Context(), &db.Issue{RepoID: repoID, PlatformID: int64(item.number), Number: item.number, Title: "Cached issue", State: item.state, CreatedAt: item.created, UpdatedAt: now})
				require.NoError(err)
				if item.number == 4 {
					require.NoError(database.UpsertIssueEvents(t.Context(), []db.IssueEvent{{IssueID: id, EventType: "cross_referenced", DedupeKey: "reference-1", CreatedAt: now, MetadataJSON: `{"source_type":"PullRequest","source_owner":"owner","source_repo":"project","source_number":1,"source_url":"https://provider.test/owner/project/pull/1"}`}}))
				}
			}
			options := SnapshotOptions{Start: now.Add(-time.Hour), End: now, Repositories: []platform.RepoRef{ref}}
			window, err := service.Snapshot(t.Context(), options)
			require.NoError(err)
			require.Len(window.Issues, 2)
			assert.Equal([]int{3, 4}, []int{window.Issues[0].Number, window.Issues[1].Number})
			options.IssueScope = "open"
			open, err := service.Snapshot(t.Context(), options)
			require.NoError(err)
			assert.Equal("open", open.IssueScope)
			require.Len(open.Issues, 2)
			assert.Equal([]int{2, 4}, []int{open.Issues[0].Number, open.Issues[1].Number})
			require.Len(open.Relations, 1)
			assert.Equal(open.Issues[1].ID, open.Relations[0].TargetID)
			assert.Empty(provider.calls)
			options.IssueScope = "unsupported"
			_, err = service.Snapshot(t.Context(), options)
			require.Error(err)
		})
	}
}

func TestSnapshotOpenIssueScopeKeepsExportLimit(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := archiveTestTime()
	ref := archiveServiceRef(platform.KindGitHub, "provider.test", "project")
	repoID := archiveServiceSeedRepo(t, database, ref)
	registry, err := platform.NewRegistry(newArchiveServiceProvider(ref.Platform, ref.Host))
	require.NoError(err)
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, now)
	_, err = database.WriteDB().ExecContext(t.Context(), `
 WITH RECURSIVE sequence(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM sequence WHERE n<10001)
 INSERT INTO forge_issues(repo_id,platform_id,number,title,state,created_at,updated_at,last_activity_at)
 SELECT ?, n, n, 'Old open issue', 'open', ?, ?, ? FROM sequence`, repoID, now.Add(-30*24*time.Hour), now, now)
	require.NoError(err)
	options := SnapshotOptions{Start: now.Add(-time.Hour), End: now}
	window, err := service.Snapshot(t.Context(), options)
	require.NoError(err)
	assert.Empty(window.Issues)
	options.IssueScope = "open"
	result, err := service.Snapshot(t.Context(), options)
	require.ErrorIs(err, snapshot.ErrTooLarge)
	assert.Empty(result.Issues, "oversized exports fail instead of silently dropping issues")
}

func TestSnapshotDistinguishesUnknownHeadRepository(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, head string
		stale      bool
		want       *bool
	}{
		{name: "same", head: "https://github.test/owner/project.git", want: new(true)},
		{name: "fork", head: "https://github.test/contributor/project.git", want: new(false)},
		{name: "missing"},
		{name: "stale", head: "https://github.test/owner/project.git", stale: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			require := require.New(t)
			database := dbtest.Open(t)
			now := archiveTestTime()
			ref := archiveServiceRef(platform.KindGitHub, "github.test", "project")
			ref.CloneURL = "https://github.test/owner/project.git"
			repoID := archiveServiceSeedRepo(t, database, ref)
			_, err := database.WriteDB().ExecContext(t.Context(), `UPDATE forge_repos SET clone_url=? WHERE id=?`, ref.CloneURL, repoID)
			require.NoError(err)
			registry, err := platform.NewRegistry(newArchiveServiceProvider(ref.Platform, ref.Host))
			require.NoError(err)
			service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, now)
			mrID, err := database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: repoID, Number: 1, Title: "Cached work", State: db.MergeRequestStateOpen, HeadRepoCloneURL: test.head, CreatedAt: now, UpdatedAt: now})
			require.NoError(err)
			if test.stale {
				_, err = database.WriteDB().ExecContext(t.Context(), `UPDATE forge_merge_requests SET head_repo_identity_stale=1 WHERE id=?`, mrID)
				require.NoError(err)
			}
			result, err := service.Snapshot(t.Context(), SnapshotOptions{Start: now.Add(-time.Hour), End: now})
			require.NoError(err)
			require.Len(result.PullRequests, 1)
			assert.Equal(t, test.want, result.PullRequests[0].HeadInSameRepository)
		})
	}
}

func TestSnapshotRetainsStableIdentityAndOneReadView(t *testing.T) {
	t.Parallel()
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
			entry, err := database.ObserveRepository(t.Context(), platformdb.DBRepoIdentity(renamed))
			require.NoError(err)
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
			refID, ok := ref.Key.ID()
			require.True(ok)
			replacement.Key = platform.RepositoryIDKey(refID + 1)
			entry, err = database.ObserveRepository(t.Context(), platformdb.DBRepoIdentity(replacement))
			require.NoError(err)
			_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: entry.Repository.ID, Number: 1, Title: "Different repository", State: db.MergeRequestStateOpen, CreatedAt: now, UpdatedAt: now})
			require.NoError(err)
			fourth, err := service.Snapshot(t.Context(), SnapshotOptions{Start: now.Add(-time.Hour), End: now})
			require.NoError(err)
			assert.Equal(first.Relations, fourth.Relations,
				"a reference keeps the repository that held its route when observed, even after the route is reused")
			require.Len(fourth.PullRequests, 1)
			assert.NotContains(fourth.PullRequests[0].Gaps, "unresolved_issue_reference")
			bothService := newArchiveTestService(t, database, registry, []platform.RepoRef{ref, replacement}, nil, now)
			both, err := bothService.Snapshot(t.Context(), SnapshotOptions{Start: now.Add(-time.Hour), End: now})
			require.NoError(err)
			assert.Equal(first.Relations, both.Relations,
				"the route's new repository never gains the old repository's references")
			require.Len(both.PullRequests, 2)
			for _, pull := range both.PullRequests {
				assert.NotContains(pull.Gaps, "unresolved_issue_reference")
			}
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
	t.Parallel()
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
		WITH RECURSIVE sequence(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM sequence WHERE n<9998)
		INSERT INTO forge_mr_events (merge_request_id,event_type,dedupe_key,created_at)
		SELECT ?, 'review', 'review-' || n, ? FROM sequence`, mrID, now)
	require.NoError(err)
	boundary, err := service.Snapshot(t.Context(), SnapshotOptions{Start: now.Add(-time.Hour), End: now})
	require.NoError(err, "two PRs plus 9,998 reviews fit the 10,000-record limit")
	require.Len(boundary.PullRequests, 2)
	assert.Equal(9998, len(boundary.PullRequests[0].Reviews)+len(boundary.PullRequests[1].Reviews))
	require.NoError(database.UpsertMREvents(t.Context(), []db.MREvent{{MergeRequestID: mrID, EventType: "review", DedupeKey: "one-over", CreatedAt: now}}))
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

func TestSnapshotResponseByteBoundary(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := archiveTestTime()
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "project")
	repoID := archiveServiceSeedRepo(t, database, ref)
	registry, err := platform.NewRegistry(newArchiveServiceProvider(ref.Platform, ref.Host))
	require.NoError(err)
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, now)
	mrID, err := database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: repoID, Number: 1, Title: "x", State: db.MergeRequestStateOpen, CreatedAt: now, UpdatedAt: now})
	require.NoError(err)
	opts := SnapshotOptions{Start: now.Add(-time.Hour), End: now}
	baseline, err := service.Snapshot(t.Context(), opts)
	require.NoError(err)
	encoded, err := json.Marshal(baseline)
	require.NoError(err)
	// Pad an untruncated ASCII field to the response budget, including JSON overhead.
	title := strings.Repeat("x", (32<<20)-len(encoded)+1)
	_, err = database.WriteDB().ExecContext(t.Context(), `UPDATE forge_merge_requests SET title=? WHERE id=?`, title, mrID)
	require.NoError(err)
	boundary, err := service.Snapshot(t.Context(), opts)
	require.NoError(err)
	encoded, err = json.Marshal(boundary)
	require.NoError(err)
	assert.Len(encoded, 32<<20)
	require.Len(boundary.PullRequests, 1)
	assert.Equal(title, boundary.PullRequests[0].Title)

	_, err = database.WriteDB().ExecContext(t.Context(), `UPDATE forge_merge_requests SET title=title || 'x' WHERE id=?`, mrID)
	require.NoError(err)
	_, err = service.Snapshot(t.Context(), opts)
	require.ErrorIs(err, snapshot.ErrTooLarge, "one byte above the response budget must fail")
}

func TestSnapshotRecentlyClosedPullRequests(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	database, service, repoID, now := newPullScopeSnapshotFixture(t)
	closed := now.Add(-30 * 24 * time.Hour)
	old := closed.Add(-time.Second)
	original, err := database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: repoID, PlatformID: 1, Number: 1, Title: "Original", Author: "contributor", AuthorAccountType: new("user"), State: db.MergeRequestStateClosed, CreatedAt: old, UpdatedAt: now, ClosedAt: &closed})
	require.NoError(err)
	rebuild, err := database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: repoID, PlatformID: 2, Number: 2, Title: "Rebuild", Author: "bot", AuthorAccountType: new("bot"), State: db.MergeRequestStateMerged, CreatedAt: old.Add(-20 * 24 * time.Hour), UpdatedAt: now, MergedAt: new(now.Add(-45 * 24 * time.Hour))})
	require.NoError(err)
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: repoID, PlatformID: 3, Number: 3, Title: "Old", State: db.MergeRequestStateClosed, CreatedAt: old, UpdatedAt: now, ClosedAt: &old})
	require.NoError(err)
	mergedAt := now.Add(-time.Hour)
	merged, err := database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: repoID, PlatformID: 4, Number: 4, Title: "Merged", State: db.MergeRequestStateMerged, CreatedAt: old, UpdatedAt: now, MergedAt: &mergedAt})
	require.NoError(err)
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: repoID, PlatformID: 5, Number: 5, Title: "No close time", State: db.MergeRequestStateClosed, CreatedAt: old, UpdatedAt: now})
	require.NoError(err)
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: repoID, PlatformID: 6, Number: 6, Title: "Second hop", State: db.MergeRequestStateClosed, CreatedAt: old, UpdatedAt: now, ClosedAt: &old})
	require.NoError(err)
	require.NoError(database.UpsertMREvents(t.Context(), []db.MREvent{
		{MergeRequestID: original, EventType: "issue_comment", Author: "contributor", Body: strings.Repeat("界", 1000), DedupeKey: "author", CreatedAt: now},
		{MergeRequestID: original, EventType: "issue_comment", Author: "maintainer", AuthorAssociation: new("MEMBER"), Body: "Thanks", DedupeKey: "member", CreatedAt: now},
		{MergeRequestID: original, EventType: "issue_comment", Author: "owner", AuthorAssociation: new("OWNER"), Body: "Approved", DedupeKey: "owner", CreatedAt: now},
		{MergeRequestID: original, EventType: "issue_comment", Author: "unknown", Body: "Reason", DedupeKey: "unknown", CreatedAt: now},
		{MergeRequestID: original, EventType: "issue_comment", Author: "collaborator", AuthorAssociation: new("COLLABORATOR"), Body: "Closing in favor of the replacement", DedupeKey: "collaborator", CreatedAt: now},
		{MergeRequestID: original, EventType: "issue_comment", Author: "other", AuthorAssociation: new("CONTRIBUTOR"), Body: "Other", DedupeKey: "other", CreatedAt: now},
		{MergeRequestID: original, EventType: "cross_referenced", MetadataJSON: `{"source_type":"PullRequest","source_owner":"owner","source_repo":"project","source_number":2,"source_url":"https://github.test/pull/2","is_cross_repository":false}`, DedupeKey: "reference", CreatedAt: now},
		{MergeRequestID: original, EventType: "cross_referenced", MetadataJSON: `{"source_type":"PullRequest","source_owner":"owner","source_repo":"project","source_number":2,"is_cross_repository":true}`, DedupeKey: "cross", CreatedAt: now},
		{MergeRequestID: merged, EventType: "issue_comment", Body: "Unknown author", DedupeKey: "empty-author", CreatedAt: now},
		{MergeRequestID: merged, EventType: "issue_comment", AuthorAssociation: new("MEMBER"), Body: "Member", DedupeKey: "empty-member", CreatedAt: now},
		{MergeRequestID: rebuild, EventType: "cross_referenced", MetadataJSON: `{"source_type":"PullRequest","source_number":6,"is_cross_repository":false}`, DedupeKey: "second-hop", CreatedAt: now},
	}))
	tx, err := database.ReadDB().BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	require.NoError(err)
	defer func() { _ = tx.Rollback() }()
	normalSize, err := db.MeasureArchiveSnapshot(t.Context(), tx, []int64{repoID}, now.Add(-time.Hour), now, false, false, now)
	require.NoError(err)
	expandedSize, err := db.MeasureArchiveSnapshot(t.Context(), tx, []int64{repoID}, now.Add(-time.Hour), now, false, true, now)
	require.NoError(err)
	require.Equal(normalSize.Records+9, expandedSize.Records)
	require.Greater(expandedSize.TextBytes, normalSize.TextBytes)
	options := SnapshotOptions{Start: now.Add(-time.Hour), End: now}
	normal, err := service.Snapshot(t.Context(), options)
	require.NoError(err)
	require.Empty(normal.PullRequests)
	require.Empty(normal.Relations)
	options.PullRequestScope = "recently_closed"
	result, err := service.Snapshot(t.Context(), options)
	require.NoError(err)
	require.Equal("recently_closed", result.PullRequestScope)
	require.Len(result.PullRequests, 3)
	require.Equal(&mergedAt, result.PullRequests[2].ClosedAt)
	require.Nil(result.PullRequests[2].AuthorAccountType)
	require.Len(result.PullRequests[2].Comments, 1)
	require.Equal("Member", result.PullRequests[2].Comments[0].Body)
	require.Contains(result.PullRequests[2].Gaps, "comment_author_association_unknown")
	require.Equal(db.MergeRequestStateClosed, db.MergeRequestState(result.PullRequests[0].State))
	require.Equal(&closed, result.PullRequests[0].ClosedAt)
	require.Len(result.PullRequests[0].Comments, 4)
	require.Equal("Closing in favor of the replacement", result.PullRequests[0].Comments[3].Body)
	require.Equal(new("COLLABORATOR"), result.PullRequests[0].Comments[3].AuthorAssociation)
	require.Equal(strings.Repeat("界", 682), result.PullRequests[0].Comments[0].Body)
	require.True(result.PullRequests[0].Comments[0].BodyTruncated)
	require.Contains(result.PullRequests[0].Gaps, "comment_author_association_unknown")
	require.Equal(new("bot"), result.PullRequests[1].AuthorAccountType)
	require.Len(result.Relations, 1)
}

func TestSnapshotRecentlyClosedScopeSizeLimit(t *testing.T) {
	database, service, repoID, now := newPullScopeSnapshotFixture(t)
	old := now.Add(-45 * 24 * time.Hour)
	_, err := database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: repoID, PlatformID: 1, Number: 1, State: db.MergeRequestStateOpen, CreatedAt: now, UpdatedAt: now})
	require.NoError(t, err)
	_, err = database.WriteDB().ExecContext(t.Context(), `WITH RECURSIVE n(value) AS (VALUES(2) UNION ALL SELECT value+1 FROM n WHERE value<4098)
INSERT INTO forge_merge_requests (repo_id,platform_id,number,state,body,created_at,updated_at,last_activity_at,closed_at)
SELECT ?,value,value,'closed',?,?,?,?,? FROM n`, repoID, strings.Repeat("x", 8192), old, now, now, old)
	require.NoError(t, err)
	_, err = database.WriteDB().ExecContext(t.Context(), `INSERT INTO forge_mr_events (merge_request_id,repo_id,event_type,metadata_json,created_at,dedupe_key)
SELECT id,repo_id,'cross_referenced','{"source_type":"PullRequest","source_number":1,"is_cross_repository":false}',?,'reference' FROM forge_merge_requests WHERE repo_id=? AND number>1`, now, repoID)
	require.NoError(t, err)
	tx, err := database.ReadDB().BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	require.NoError(t, err)
	measured, err := db.MeasureArchiveSnapshot(t.Context(), tx, []int64{repoID}, now.Add(-time.Hour), now, false, true, now)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	require.Less(t, measured.Records, snapshot.MaxRecords)
	require.Greater(t, measured.TextBytes, int64(snapshot.MaxBytes))
	_, err = service.Snapshot(t.Context(), SnapshotOptions{Start: now.Add(-time.Hour), End: now})
	require.NoError(t, err)
	_, err = service.Snapshot(t.Context(), SnapshotOptions{Start: now.Add(-time.Hour), End: now, PullRequestScope: "recently_closed"})
	require.ErrorIs(t, err, snapshot.ErrTooLarge)
}

func TestSnapshotReferenceFromRecentSourceIncludesOlderTarget(t *testing.T) {
	for _, state := range []db.MergeRequestState{db.MergeRequestStateOpen, db.MergeRequestStateMerged} {
		t.Run(string(state), func(t *testing.T) {
			database, service, repoID, now := newPullScopeSnapshotFixture(t)
			old := now.Add(-45 * 24 * time.Hour)
			recent := now.Add(-time.Hour)
			original, err := database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: repoID, PlatformID: 1, Number: 1, Title: "Original", Author: "contributor", State: db.MergeRequestStateClosed, CreatedAt: old, UpdatedAt: now, ClosedAt: &old})
			require.NoError(t, err)
			rebuild := &db.MergeRequest{RepoID: repoID, PlatformID: 2, Number: 2, Title: "Rebuild", State: state, CreatedAt: old, UpdatedAt: now}
			if state == db.MergeRequestStateMerged {
				rebuild.MergedAt = &recent
			}
			_, err = database.UpsertMergeRequest(t.Context(), rebuild)
			require.NoError(t, err)
			other, err := database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: repoID, PlatformID: 3, Number: 3, Title: "Other context", State: db.MergeRequestStateClosed, CreatedAt: old, UpdatedAt: now, ClosedAt: &old})
			require.NoError(t, err)
			require.NoError(t, database.UpsertMREvents(t.Context(), []db.MREvent{
				{MergeRequestID: original, EventType: "issue_comment", Author: "contributor", Body: "Closing", DedupeKey: "closing", CreatedAt: now},
				{MergeRequestID: original, EventType: "cross_referenced", MetadataJSON: `{"source_type":"PullRequest","source_number":2,"is_cross_repository":false}`, DedupeKey: "reference", CreatedAt: now},
				{MergeRequestID: other, EventType: "cross_referenced", MetadataJSON: `{"source_type":"PullRequest","source_number":2,"is_cross_repository":false}`, DedupeKey: "other", CreatedAt: now},
				{MergeRequestID: other, EventType: "cross_referenced", MetadataJSON: `{"source_type":"PullRequest","source_number":1,"is_cross_repository":false}`, DedupeKey: "context-only", CreatedAt: now},
			}))
			opts := SnapshotOptions{Start: now.Add(-time.Hour), End: now}
			normal, err := service.Snapshot(t.Context(), opts)
			require.NoError(t, err)
			for _, pull := range normal.PullRequests {
				require.NotEqual(t, 1, pull.Number)
			}
			opts.PullRequestScope = "recently_closed"
			result, err := service.Snapshot(t.Context(), opts)
			require.NoError(t, err)
			require.Len(t, result.PullRequests, 3)
			require.Equal(t, "Closing", result.PullRequests[0].Comments[0].Body)
			require.Len(t, result.Relations, 2)
			for _, relation := range result.Relations {
				require.Equal(t, result.PullRequests[1].ID, relation.SourceID)
			}
			require.Equal(t, result.PullRequests[0].ID, result.Relations[0].TargetID)
			_, err = database.WriteDB().ExecContext(t.Context(), `INSERT INTO forge_archive_items (repo_id,item_type,item_number,provider_item_id,provider_created_at,provider_updated_at,lifecycle_state) VALUES (?,'merge_request',1,'1',?,?,'removed_upstream')`, repoID, old, now)
			require.NoError(t, err)
			result, err = service.Snapshot(t.Context(), opts)
			require.NoError(t, err)
			require.Len(t, result.PullRequests, 2)
			require.Len(t, result.Relations, 1)
			require.Equal(t, 3, result.PullRequests[1].Number)
			_, err = database.WriteDB().ExecContext(t.Context(), `INSERT INTO forge_archive_items (repo_id,item_type,item_number,provider_item_id,provider_created_at,provider_updated_at,lifecycle_state) VALUES (?,'merge_request',2,'2',?,?,'removed_upstream')`, repoID, old, now)
			require.NoError(t, err)
			result, err = service.Snapshot(t.Context(), opts)
			require.NoError(t, err)
			require.Empty(t, result.PullRequests)
			require.Empty(t, result.Relations)
		})
	}
}

func TestSnapshotUnresolvedPullReference(t *testing.T) {
	database, service, repoID, now := newPullScopeSnapshotFixture(t)
	old, recent := now.Add(-45*24*time.Hour), now.Add(-time.Hour)
	original, err := database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: repoID, PlatformID: 1, Number: 1, State: db.MergeRequestStateClosed, CreatedAt: old, UpdatedAt: now, ClosedAt: &recent})
	require.NoError(t, err)
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: repoID, PlatformID: 2, Number: 2, State: db.MergeRequestStateOpen, CreatedAt: old, UpdatedAt: now})
	require.NoError(t, err)
	contextID, err := database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: repoID, PlatformID: 3, Number: 3, State: db.MergeRequestStateClosed, CreatedAt: old, UpdatedAt: now, ClosedAt: &old})
	require.NoError(t, err)
	exclusions, err := database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: repoID, PlatformID: 4, Number: 4, State: db.MergeRequestStateClosed, CreatedAt: old, UpdatedAt: now, ClosedAt: &recent})
	require.NoError(t, err)
	unselected, err := database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: repoID, PlatformID: 5, Number: 5, State: db.MergeRequestStateClosed, CreatedAt: old, UpdatedAt: now, ClosedAt: &old})
	require.NoError(t, err)
	_, err = database.WriteDB().ExecContext(t.Context(), `INSERT INTO forge_archive_items (repo_id,item_type,item_number,provider_item_id,provider_created_at,provider_updated_at,lifecycle_state) VALUES (?,'merge_request',8,'8',?,?,'removed_upstream')`, repoID, old, now)
	require.NoError(t, err)
	require.NoError(t, database.UpsertMREvents(t.Context(), []db.MREvent{
		{MergeRequestID: original, EventType: "cross_referenced", DedupeKey: "missing-a", CreatedAt: now, MetadataJSON: `{"source_type":"PullRequest","source_number":7,"is_cross_repository":false}`},
		{MergeRequestID: original, EventType: "cross_referenced", DedupeKey: "missing-b", CreatedAt: now, MetadataJSON: `{"source_type":"PullRequest","source_number":7,"is_cross_repository":false}`},
		{MergeRequestID: contextID, EventType: "cross_referenced", DedupeKey: "root", CreatedAt: now, MetadataJSON: `{"source_type":"PullRequest","source_number":2,"is_cross_repository":false}`},
		{MergeRequestID: contextID, EventType: "cross_referenced", DedupeKey: "second-hop", CreatedAt: now, MetadataJSON: `{"source_type":"PullRequest","source_number":9,"is_cross_repository":false}`},
		{MergeRequestID: unselected, EventType: "cross_referenced", DedupeKey: "unselected", CreatedAt: now, MetadataJSON: `{"source_type":"PullRequest","source_number":10,"is_cross_repository":false}`},
		{MergeRequestID: exclusions, EventType: "cross_referenced", DedupeKey: "removed", CreatedAt: now, MetadataJSON: `{"source_type":"PullRequest","source_number":8,"is_cross_repository":false}`},
		{MergeRequestID: exclusions, EventType: "cross_referenced", DedupeKey: "cross", CreatedAt: now, MetadataJSON: `{"source_type":"PullRequest","source_number":11,"is_cross_repository":true}`},
		{MergeRequestID: exclusions, EventType: "cross_referenced", DedupeKey: "issue", CreatedAt: now, MetadataJSON: `{"source_type":"Issue","source_number":11,"is_cross_repository":false}`},
		{MergeRequestID: exclusions, EventType: "cross_referenced", DedupeKey: "invalid", CreatedAt: now, MetadataJSON: `{`},
		{MergeRequestID: exclusions, EventType: "cross_referenced", DedupeKey: "absent", CreatedAt: now, MetadataJSON: `{"source_type":"PullRequest","is_cross_repository":false}`},
		{MergeRequestID: exclusions, EventType: "cross_referenced", DedupeKey: "zero", CreatedAt: now, MetadataJSON: `{"source_type":"PullRequest","source_number":0,"is_cross_repository":false}`},
		{MergeRequestID: exclusions, EventType: "cross_referenced", DedupeKey: "string", CreatedAt: now, MetadataJSON: `{"source_type":"PullRequest","source_number":"12","is_cross_repository":false}`},
	}))
	opts := SnapshotOptions{Start: recent, End: now}
	normal, err := service.Snapshot(t.Context(), opts)
	require.NoError(t, err)
	require.Len(t, normal.PullRequests, 1)
	require.NotContains(t, normal.PullRequests[0].Gaps, "unresolved_pull_request_reference")
	require.Empty(t, normal.Relations)
	opts.PullRequestScope = "recently_closed"
	result, err := service.Snapshot(t.Context(), opts)
	require.NoError(t, err)
	require.Len(t, result.PullRequests, 4)
	require.Equal(t, 1, strings.Count(strings.Join(result.PullRequests[0].Gaps, ","), "unresolved_pull_request_reference"))
	require.NotContains(t, result.PullRequests[2].Gaps, "unresolved_pull_request_reference")
	require.NotContains(t, result.PullRequests[3].Gaps, "unresolved_pull_request_reference")
	require.Len(t, result.Relations, 1)
	for _, number := range []int{7, 9} {
		_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: repoID, PlatformID: int64(number), Number: number, State: db.MergeRequestStateClosed, CreatedAt: old, UpdatedAt: now, ClosedAt: &old})
		require.NoError(t, err)
	}
	result, err = service.Snapshot(t.Context(), opts)
	require.NoError(t, err)
	require.Len(t, result.PullRequests, 5)
	require.NotContains(t, result.PullRequests[0].Gaps, "unresolved_pull_request_reference")
	require.NotContains(t, result.PullRequests[2].Gaps, "unresolved_pull_request_reference")
	require.Equal(t, 7, result.PullRequests[4].Number)
	require.Len(t, result.Relations, 3)
	require.Equal(t, result.PullRequests[4].ID, result.Relations[0].SourceID)
	require.Equal(t, result.PullRequests[0].ID, result.Relations[0].TargetID)
}

func newPullScopeSnapshotFixture(t *testing.T) (*db.DB, *Service, int64, time.Time) {
	t.Helper()
	now := archiveTestTime()
	database := dbtest.Open(t)
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "project")
	repoID := archiveServiceSeedRepo(t, database, ref)
	registry, err := platform.NewRegistry(newArchiveServiceProvider(ref.Platform, ref.Host))
	require.NoError(t, err)
	return database, newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, now), repoID, now
}
