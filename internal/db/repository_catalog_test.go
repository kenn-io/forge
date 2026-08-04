package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedRepositoryCatalogCollision(t *testing.T, d *DB) (int64, int64) {
	t.Helper()
	require := require.New(t)
	result, err := d.WriteDB().Exec(`
		INSERT INTO forge_repos (
			platform, platform_host, platform_repo_id,
			owner, name, repo_path, owner_key, name_key, repo_path_key,
			lifecycle_state
		) VALUES
			('github', 'github.com', 'provider-old',
			 'org-a', 'project-a', 'org-a/project-a',
			 'org-a', 'project-a', 'org-a/project-a', 'inactive'),
			('github', 'github.com', 'provider-new',
			 'org-a', 'project-a', 'org-a/project-a',
			 'org-a', 'project-a', 'org-a/project-a', 'active')`)
	require.NoError(err)
	lastID, err := result.LastInsertId()
	require.NoError(err)
	oldID, newID := lastID-1, lastID
	_, err = d.WriteDB().Exec(`
		INSERT INTO forge_repo_routes (
			repo_id, platform, platform_host,
			owner, name, repo_path, owner_key, name_key, repo_path_key,
			is_current, first_seen_at, last_seen_at
		) VALUES
			(?, 'github', 'github.com',
			 'org-a', 'project-a', 'org-a/project-a',
			 'org-a', 'project-a', 'org-a/project-a',
			 0, '2026-01-01T00:00:00Z', '2026-02-01T00:00:00Z'),
			(?, 'github', 'github.com',
			 'org-a', 'project-a', 'org-a/project-a',
			 'org-a', 'project-a', 'org-a/project-a',
			 1, '2026-02-02T00:00:00Z', '2026-02-02T00:00:00Z')`,
		oldID,
		newID,
	)
	require.NoError(err)
	return oldID, newID
}

func TestResolveActiveRepositoryRouteReturnsOnlyCurrentOccupant(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	_, newID := seedRepositoryCatalogCollision(t, d)
	entry, err := d.ResolveActiveRepositoryRoute(t.Context(), RepoIdentity{
		Platform:     "github",
		PlatformHost: "github.com",
		Owner:        "org-a",
		Name:         "project-a",
	})
	require.NoError(err)
	require.NotNil(entry)
	assert.Equal(newID, entry.Repository.ID)
	assert.Equal(RepositoryLifecycleActive, entry.Lifecycle)
}

func TestGetRepositoryByProviderIDReturnsInactiveRepository(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	oldID, _ := seedRepositoryCatalogCollision(t, d)
	entry, err := d.GetRepositoryByProviderID(
		t.Context(), "github", "github.com", "provider-old",
	)
	require.NoError(err)
	require.NotNil(entry)
	assert.Equal(oldID, entry.Repository.ID)
	assert.Equal(RepositoryLifecycleInactive, entry.Lifecycle)
	require.Len(entry.Routes, 1)
	assert.False(entry.Routes[0].Current)
}

func TestListRepositoryCatalogFindsHistoricalNameCollisions(t *testing.T) {
	d := openTestDB(t)
	oldID, newID := seedRepositoryCatalogCollision(t, d)
	entries, err := d.ListRepositoryCatalog(t.Context(), RepositoryCatalogFilter{
		Platform:     "github",
		PlatformHost: "github.com",
		RepoPath:     "org-a/project-a",
	})
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.ElementsMatch(t, []int64{oldID, newID}, []int64{
		entries[0].Repository.ID,
		entries[1].Repository.ID,
	})
}

func TestOperationalRepositoryReadsUseCurrentIncarnation(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	oldID, newID := seedRepositoryCatalogCollision(t, d)
	now := baseTime()
	for _, item := range []struct {
		repoID int64
		title  string
	}{
		{repoID: oldID, title: "historical item"},
		{repoID: newID, title: "current item"},
	} {
		_, err := d.UpsertMergeRequest(t.Context(), &MergeRequest{
			RepoID: item.repoID, PlatformID: item.repoID, Number: 7,
			Title: item.title, State: MergeRequestStateOpen,
			CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
		})
		require.NoError(err)
		_, err = d.UpsertIssue(t.Context(), &Issue{
			RepoID: item.repoID, PlatformID: item.repoID, Number: 8,
			Title: item.title, State: "open",
			CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
		})
		require.NoError(err)
	}

	repos, err := d.ListRepos(t.Context())
	require.NoError(err)
	require.Len(repos, 1)
	assert.Equal(newID, repos[0].ID)

	mrs, err := d.ListMergeRequests(t.Context(), ListMergeRequestsOpts{
		State: "all",
	})
	require.NoError(err)
	require.Len(mrs, 1)
	assert.Equal(newID, mrs[0].RepoID)
	assert.Equal("current item", mrs[0].Title)
	mr, err := d.GetMergeRequest(
		t.Context(), "github", "github.com", "org-a", "project-a", 7,
	)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal(newID, mr.RepoID)

	issues, err := d.ListIssues(t.Context(), ListIssuesOpts{State: "all"})
	require.NoError(err)
	require.Len(issues, 1)
	assert.Equal(newID, issues[0].RepoID)
	assert.Equal("current item", issues[0].Title)
	issue, err := d.GetIssue(
		t.Context(), "github", "github.com", "org-a", "project-a", 8,
	)
	require.NoError(err)
	require.NotNil(issue)
	assert.Equal(newID, issue.RepoID)
}

func TestOperationalAssociationsDoNotCrossRepositoryIncarnations(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	oldID, newID := seedRepositoryCatalogCollision(t, d)
	now := baseTime()

	oldNotification := notificationFixture("historical", "mention", now)
	oldNotification.RepoID = &oldID
	currentNotification := notificationFixture("current", "mention", now)
	for _, notification := range []*Notification{&oldNotification, &currentNotification} {
		notification.RepoOwner = "org-a"
		notification.RepoName = "project-a"
	}
	require.NoError(d.UpsertNotifications(
		t.Context(), []Notification{oldNotification, currentNotification},
	))
	var currentNotificationRepoID int64
	require.NoError(d.ReadDB().QueryRow(`
		SELECT repo_id FROM forge_notification_items
		WHERE platform_notification_id = 'current'`,
	).Scan(&currentNotificationRepoID))
	assert.Equal(newID, currentNotificationRepoID)
	notifications, err := d.ListNotifications(
		t.Context(), ListNotificationsOpts{State: "all"},
	)
	require.NoError(err)
	require.Len(notifications, 1)
	assert.Equal("current", notifications[0].PlatformNotificationID)

	_, err = d.UpsertMergeRequest(t.Context(), &MergeRequest{
		RepoID: newID, PlatformID: 7, Number: 7,
		Title: "replacement item", State: MergeRequestStateOpen,
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	_, err = d.WriteDB().Exec(`
		INSERT INTO forge_workspaces (
			id, platform, platform_host,
			repo_owner, repo_name,
			repo_owner_key, repo_name_key, repo_path_key,
			item_type, item_number, item_key,
			git_head_ref, workspace_branch,
			worktree_path, tmux_session, terminal_backend, created_at
		) VALUES (
			'historical-workspace', 'github', 'github.com',
			'org-a', 'project-a',
			'org-a', 'project-a', 'org-a/project-a',
			'pull_request', 7, '7',
			'', '__middleman_unknown__',
			'/tmp/historical-workspace', 'historical-workspace', 'tmux',
			'2026-02-01T00:00:00Z'
		)`)
	require.NoError(err)
	summary, err := d.GetWorkspaceSummary(t.Context(), "historical-workspace")
	require.NoError(err)
	require.NotNil(summary)
	assert.Nil(summary.SourceTitle)
}

func TestWorkspaceRouteLookupAndCreationFailClosedAfterReplacement(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	firstObservedAt := baseTime()
	reconcileCatalogRepository(
		t, d, "provider-old", "org-a", "project-a", firstObservedAt,
	)
	require.NoError(d.InsertWorkspace(t.Context(), &Workspace{
		ID: "historical-workspace", Platform: "github",
		PlatformHost: "github.com", RepoOwner: "org-a", RepoName: "project-a",
		ItemType: WorkspaceItemTypePullRequest, ItemNumber: 7,
		WorktreePath: "/tmp/historical-workspace", TmuxSession: "historical-workspace",
	}))
	reconcileCatalogRepository(
		t, d, "provider-new", "org-a", "project-a",
		firstObservedAt.Add(time.Hour),
	)

	byRoute, err := d.GetWorkspaceByMRForProvider(
		t.Context(), "github", "github.com", "org-a", "project-a", 7,
	)
	require.NoError(err)
	assert.Nil(byRoute)
	linkedByRoute, err := d.GetWorkspaceLinkedToMRForProvider(
		t.Context(), "github", "github.com", "org-a", "project-a", 7,
	)
	require.NoError(err)
	assert.Nil(linkedByRoute)

	err = d.InsertWorkspace(t.Context(), &Workspace{
		ID: "replacement-workspace", Platform: "github",
		PlatformHost: "github.com", RepoOwner: "org-a", RepoName: "project-a",
		ItemType: WorkspaceItemTypePullRequest, ItemNumber: 8,
		WorktreePath: "/tmp/replacement-workspace", TmuxSession: "replacement-workspace",
	})
	require.ErrorContains(err, "repository route has historical occupants")
}

func TestRepositoryCatalogLookupNeverFallsBackFromProviderID(t *testing.T) {
	d := openTestDB(t)
	seedRepositoryCatalogCollision(t, d)
	entry, err := d.GetRepositoryByProviderID(
		t.Context(), "github", "github.com", "missing-id",
	)
	require.NoError(t, err)
	assert.Nil(t, entry)
}

func TestListRepositoryCatalogRejectsUnqualifiedProviderID(t *testing.T) {
	d := openTestDB(t)
	_, err := d.ListRepositoryCatalog(t.Context(), RepositoryCatalogFilter{
		PlatformRepoID: "provider-old",
	})
	require.ErrorContains(t, err, "platform and host")
}

func TestListRepositoryCatalogFiltersLifecycle(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	_, newID := seedRepositoryCatalogCollision(t, d)
	entries, err := d.ListRepositoryCatalog(t.Context(), RepositoryCatalogFilter{
		Lifecycle: RepositoryLifecycleActive,
	})
	require.NoError(err)
	require.Len(entries, 1)
	assert.Equal(t, newID, entries[0].Repository.ID)

	_, err = d.ListRepositoryCatalog(t.Context(), RepositoryCatalogFilter{
		Lifecycle: "retired",
	})
	require.ErrorContains(err, "unsupported repository lifecycle")
}

func TestGetRepositoryByProviderIDOrdersRoutesByFirstSeen(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	oldID, _ := seedRepositoryCatalogCollision(t, d)
	_, err := d.WriteDB().Exec(`
		INSERT INTO forge_repo_routes (
			repo_id, platform, platform_host,
			owner, name, repo_path, owner_key, name_key, repo_path_key,
			is_current, first_seen_at, last_seen_at
		) VALUES (
			?, 'github', 'github.com',
			'org-z', 'earlier-project', 'org-z/earlier-project',
			'org-z', 'earlier-project', 'org-z/earlier-project',
			0, '2025-12-01T00:00:00Z', '2025-12-02T00:00:00Z'
		)`, oldID)
	require.NoError(err)

	entry, err := d.GetRepositoryByProviderID(
		t.Context(), "github", "github.com", "provider-old",
	)
	require.NoError(err)
	require.NotNil(entry)
	require.Len(entry.Routes, 2)
	assert.Equal("org-z/earlier-project", entry.Routes[0].RepoPath)
	assert.Equal("org-a/project-a", entry.Routes[1].RepoPath)
}

func reconcileCatalogRepository(
	t *testing.T,
	d *DB,
	providerID string,
	owner string,
	name string,
	observedAt time.Time,
) *RepositoryCatalogEntry {
	t.Helper()
	entry, accepted, err := d.ReconcileRepositoryObservation(t.Context(), RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: providerID,
		Owner:          owner,
		Name:           name,
	}, observedAt)
	require.NoError(t, err)
	require.True(t, accepted)
	require.NotNil(t, entry)
	assertDatabaseIntegrityForTest(t, d.ReadDB())
	return entry
}

func TestReconcileRepositoryObservationIsIdempotent(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	firstObservedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	first := reconcileCatalogRepository(
		t, d, "provider-1", "org-a", "project-a", firstObservedAt,
	)
	require.Len(first.Routes, 1)
	firstRouteID := first.Routes[0].ID
	firstSeenAt := first.Routes[0].FirstSeenAt

	secondObservedAt := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	second := reconcileCatalogRepository(
		t, d, "provider-1", "org-a", "project-a", secondObservedAt,
	)
	require.Len(second.Routes, 1)
	assert.Equal(first.Repository.ID, second.Repository.ID)
	assert.Equal(firstRouteID, second.Routes[0].ID)
	assert.Equal(firstSeenAt, second.Routes[0].FirstSeenAt)
	assert.Equal(secondObservedAt, second.Routes[0].LastSeenAt)
	assert.Equal(RepositoryLifecycleActive, second.Lifecycle)
}

func TestReconcileRepositoryObservationIgnoresStaleRename(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	firstObservedAt := baseTime()
	reconcileCatalogRepository(
		t, d, "provider-1", "org-a", "project-a", firstObservedAt,
	)
	newestObservedAt := firstObservedAt.Add(2 * time.Hour)
	newest := reconcileCatalogRepository(
		t, d, "provider-1", "org-b", "project-b", newestObservedAt,
	)

	stale, accepted, err := d.ReconcileRepositoryObservation(t.Context(), RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "provider-1",
		Owner:          "org-a",
		Name:           "project-a",
	}, firstObservedAt.Add(time.Hour))
	require.NoError(err)
	assert.False(accepted, "stale observation must report rejection")
	require.NotNil(stale)
	assert.Equal(newest.Repository.ID, stale.Repository.ID)
	assert.Equal("org-b/project-b", stale.Repository.RepoPath)
	require.Len(stale.Routes, 2)
	assert.False(stale.Routes[0].Current)
	assert.True(stale.Routes[1].Current)
	assert.Equal(newestObservedAt, stale.Routes[1].LastSeenAt)
}

// TestReconcileRepositoryObservationRejectsStaleReactivation covers a delayed
// observation whose route is currently free: without a repository-level
// watermark it would reactivate the inactive repository with stale data.
func TestReconcileRepositoryObservationRejectsStaleReactivation(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	old := reconcileCatalogRepository(
		t, d, "provider-old", "org-a", "project-a", baseTime(),
	)
	reconcileCatalogRepository(
		t, d, "provider-new", "org-a", "project-a", baseTime().Add(2*time.Hour),
	)

	stale, accepted, err := d.ReconcileRepositoryObservation(t.Context(), RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "provider-old",
		Owner:          "org-c",
		Name:           "project-c",
	}, baseTime().Add(time.Hour))
	require.NoError(err)
	assert.False(accepted,
		"observation older than the repository watermark must be rejected")
	require.NotNil(stale)
	assert.Equal(old.Repository.ID, stale.Repository.ID)
	assert.Equal(RepositoryLifecycleInactive, stale.Lifecycle)

	active, err := d.ResolveActiveRepositoryRoute(t.Context(), RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		Owner: "org-c", Name: "project-c",
	})
	require.NoError(err)
	assert.Nil(active)
}

func TestDeactivateRepositoryObservationIgnoresStaleAbsence(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	observedAt := baseTime().Add(2 * time.Hour)
	reconcileCatalogRepository(
		t, d, "provider-1", "org-a", "project-a", observedAt,
	)

	entry, err := d.DeactivateRepositoryObservation(
		t.Context(), "github", "github.com", "provider-1",
		baseTime().Add(time.Hour),
	)
	require.NoError(err)
	require.NotNil(entry)
	assert.Equal(RepositoryLifecycleActive, entry.Lifecycle)
	require.Len(entry.Routes, 1)
	assert.True(entry.Routes[0].Current)
	assert.Equal(observedAt, entry.Routes[0].LastSeenAt)
}

// TestInsertWorkspaceWaitsForReconciliation verifies workspace creation holds
// the reconciliation read lock, so a replacement that makes the route
// historically ambiguous cannot interleave with the collision check.
func TestInsertWorkspaceWaitsForReconciliation(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	reconcileCatalogRepository(
		t, d, "provider-1", "acme", "widget", baseTime(),
	)

	writerEntered := make(chan struct{})
	releaseWriter := make(chan struct{})
	defer func() {
		select {
		case <-releaseWriter:
		default:
			close(releaseWriter)
		}
	}()
	restoreHook := d.SetBeforeRepositoryReconciliationWriteLockForTest(func() {
		close(writerEntered)
		<-releaseWriter
	})
	t.Cleanup(restoreHook)

	writerDone := make(chan error, 1)
	go func() {
		_, _, reconcileErr := d.ReconcileRepositoryObservation(
			context.Background(),
			RepoIdentity{
				Platform:       "github",
				PlatformHost:   "github.com",
				PlatformRepoID: "provider-2",
				Owner:          "acme",
				Name:           "widget",
			},
			baseTime().Add(time.Hour),
		)
		writerDone <- reconcileErr
	}()
	<-writerEntered

	insertDone := make(chan error, 1)
	go func() {
		insertDone <- d.InsertWorkspace(context.Background(), &Workspace{
			ID: "ws-reconcile-race", Platform: "github",
			PlatformHost: "github.com", RepoOwner: "acme", RepoName: "widget",
			ItemType: WorkspaceItemTypePullRequest, ItemNumber: 7,
			WorktreePath: "/tmp/ws-reconcile-race",
			TmuxSession:  "ws-reconcile-race",
		})
	}()
	select {
	case <-insertDone:
		require.Fail("workspace insert crossed a pending reconciliation")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseWriter)
	require.NoError(<-writerDone)
	require.ErrorContains(<-insertDone, "historical occupants")
}

func TestReconcileRepositoryObservationRejectsStaleReplacement(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	observedAt := baseTime().Add(2 * time.Hour)
	current := reconcileCatalogRepository(
		t, d, "provider-current", "org-a", "project-a", observedAt,
	)

	entry, _, err := d.ReconcileRepositoryObservation(t.Context(), RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "provider-stale",
		Owner:          "org-a",
		Name:           "project-a",
	}, observedAt.Add(-time.Hour))
	require.ErrorContains(err, "predates current route observation")
	assert.Nil(entry)

	stillCurrent, err := d.ResolveActiveRepositoryRoute(t.Context(), RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		Owner: "org-a", Name: "project-a",
	})
	require.NoError(err)
	require.NotNil(stillCurrent)
	assert.Equal(current.Repository.ID, stillCurrent.Repository.ID)
	stale, err := d.GetRepositoryByProviderID(
		t.Context(), "github", "github.com", "provider-stale",
	)
	require.NoError(err)
	assert.Nil(stale)
}

func TestReconcileRepositoryObservationRejectsStaleKnownRepositoryAfterReplacement(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	firstObservedAt := baseTime()
	old := reconcileCatalogRepository(
		t, d, "provider-old", "org-a", "project-a", firstObservedAt,
	)
	replacementObservedAt := firstObservedAt.Add(2 * time.Hour)
	replacement := reconcileCatalogRepository(
		t, d, "provider-new", "org-a", "project-a", replacementObservedAt,
	)

	stale, accepted, err := d.ReconcileRepositoryObservation(t.Context(), RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "provider-old",
		Owner:          "org-a",
		Name:           "project-a",
	}, firstObservedAt.Add(time.Hour))
	require.NoError(err)
	assert.False(accepted,
		"observation older than the repository watermark must be rejected")
	require.NotNil(stale)
	assert.Equal(old.Repository.ID, stale.Repository.ID)

	active, err := d.ResolveActiveRepositoryRoute(t.Context(), RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		Owner: "org-a", Name: "project-a",
	})
	require.NoError(err)
	require.NotNil(active)
	assert.Equal(replacement.Repository.ID, active.Repository.ID)

	displaced, err := d.GetRepositoryByProviderID(
		t.Context(), "github", "github.com", "provider-old",
	)
	require.NoError(err)
	require.NotNil(displaced)
	assert.Equal(old.Repository.ID, displaced.Repository.ID)
	assert.Equal(RepositoryLifecycleInactive, displaced.Lifecycle)
}

func TestReconcileRepositoryObservationRenamesSameProviderID(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	original := reconcileCatalogRepository(
		t, d, "provider-1", "org-a", "project-a", baseTime(),
	)
	insertTestIssueWithOptions(t, d, testIssue(original.Repository.ID, 1))
	insertTestMRWithOptions(t, d, testMR(original.Repository.ID, 2))
	_, err := d.WriteDB().Exec(`
		INSERT INTO forge_archive_repos (
			repo_id, collection_mode, operator_state, created_at, updated_at
		) VALUES (?, 'full', 'active', datetime('now'), datetime('now'))`,
		original.Repository.ID,
	)
	require.NoError(err)

	renamed := reconcileCatalogRepository(
		t,
		d,
		"provider-1",
		"org-b",
		"project-b",
		baseTime().Add(time.Hour),
	)
	assert.Equal(original.Repository.ID, renamed.Repository.ID)
	require.Len(renamed.Routes, 2)
	assert.False(renamed.Routes[0].Current)
	assert.True(renamed.Routes[1].Current)
	assert.Equal("org-b/project-b", renamed.Repository.RepoPath)

	var issueRepoID, mergeRequestRepoID, archiveRepoID int64
	require.NoError(d.ReadDB().QueryRow(
		`SELECT repo_id FROM forge_issues WHERE number = 1`,
	).Scan(&issueRepoID))
	require.NoError(d.ReadDB().QueryRow(
		`SELECT repo_id FROM forge_merge_requests WHERE number = 2`,
	).Scan(&mergeRequestRepoID))
	require.NoError(d.ReadDB().QueryRow(
		`SELECT repo_id FROM forge_archive_repos WHERE repo_id = ?`,
		original.Repository.ID,
	).Scan(&archiveRepoID))
	assert.Equal(original.Repository.ID, issueRepoID)
	assert.Equal(original.Repository.ID, mergeRequestRepoID)
	assert.Equal(original.Repository.ID, archiveRepoID)
}

func TestReconcileRepositoryObservationReplacesAndReactivates(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	oldRepo := reconcileCatalogRepository(
		t, d, "provider-old", "org-a", "project-a", baseTime(),
	)
	insertTestIssueWithOptions(t, d, testIssue(oldRepo.Repository.ID, 1))
	newRepo := reconcileCatalogRepository(
		t,
		d,
		"provider-new",
		"org-a",
		"project-a",
		baseTime().Add(time.Hour),
	)
	assert.NotEqual(oldRepo.Repository.ID, newRepo.Repository.ID)
	oldByID, err := d.GetRepositoryByProviderID(
		t.Context(), "github", "github.com", "provider-old",
	)
	require.NoError(err)
	require.NotNil(oldByID)
	assert.Equal(RepositoryLifecycleInactive, oldByID.Lifecycle)

	reactivated := reconcileCatalogRepository(
		t,
		d,
		"provider-old",
		"org-b",
		"project-b",
		baseTime().Add(2*time.Hour),
	)
	assert.Equal(oldRepo.Repository.ID, reactivated.Repository.ID)
	assert.Equal(RepositoryLifecycleActive, reactivated.Lifecycle)
	activeOldRoute, err := d.ResolveActiveRepositoryRoute(t.Context(), RepoIdentity{
		Platform:     "github",
		PlatformHost: "github.com",
		Owner:        "org-a",
		Name:         "project-a",
	})
	require.NoError(err)
	require.NotNil(activeOldRoute)
	assert.Equal(newRepo.Repository.ID, activeOldRoute.Repository.ID)

	var issueRepoID int64
	require.NoError(d.ReadDB().QueryRow(
		`SELECT repo_id FROM forge_issues WHERE number = 1`,
	).Scan(&issueRepoID))
	assert.Equal(oldRepo.Repository.ID, issueRepoID)
}

func TestReconcileRepositoryObservationAdoptionKeepsLegacyContent(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	result, err := d.WriteDB().Exec(`
		INSERT INTO forge_repos (
			platform, platform_host, platform_repo_id,
			owner, name, repo_path, owner_key, name_key, repo_path_key,
			lifecycle_state
		) VALUES (
			'github', 'github.com', '',
			'org-a', 'project-a', 'org-a/project-a',
			'org-a', 'project-a', 'org-a/project-a', 'inactive'
		)`)
	require.NoError(err)
	legacyID, err := result.LastInsertId()
	require.NoError(err)
	_, err = d.WriteDB().Exec(`
		INSERT INTO forge_repo_routes (
			repo_id, platform, platform_host,
			owner, name, repo_path, owner_key, name_key, repo_path_key,
			is_current, first_seen_at, last_seen_at
		) VALUES (
			?, 'github', 'github.com',
			'org-a', 'project-a', 'org-a/project-a',
			'org-a', 'project-a', 'org-a/project-a',
			0, datetime('now'), datetime('now')
		)`, legacyID)
	require.NoError(err)
	insertTestIssueWithOptions(t, d, testIssue(legacyID, 1))

	canonical := reconcileCatalogRepository(
		t, d, "provider-new", "org-a", "project-a", baseTime(),
	)
	assert.Equal(legacyID, canonical.Repository.ID,
		"first verification adopts the legacy row instead of stranding it")
	var issueRepoID int64
	require.NoError(d.ReadDB().QueryRow(
		`SELECT repo_id FROM forge_issues WHERE number = 1`,
	).Scan(&issueRepoID))
	assert.Equal(canonical.Repository.ID, issueRepoID,
		"content linked to the legacy row stays bound to the adopted identity")
}

func TestReconcileRepositoryObservationRollsBackOnRouteWriteFailure(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	original := reconcileCatalogRepository(
		t, d, "provider-old", "org-a", "project-a", baseTime(),
	)
	_, err := d.WriteDB().Exec(`
		INSERT INTO forge_projects (
			id, display_name, local_path, repo_id
		) VALUES ('project-local', 'Project Local', '/tmp/project-local', ?)`,
		original.Repository.ID,
	)
	require.NoError(err)
	_, err = d.WriteDB().Exec(`
		CREATE TRIGGER reject_catalog_route_insert
		BEFORE INSERT ON forge_repo_routes
		BEGIN
			SELECT RAISE(ABORT, 'injected route failure');
		END`)
	require.NoError(err)

	_, _, err = d.ReconcileRepositoryObservation(t.Context(), RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "provider-new",
		Owner:          "org-a",
		Name:           "project-a",
	}, baseTime().Add(time.Hour))
	require.ErrorContains(err, "injected route failure")

	active, err := d.ResolveActiveRepositoryRoute(t.Context(), RepoIdentity{
		Platform:     "github",
		PlatformHost: "github.com",
		Owner:        "org-a",
		Name:         "project-a",
	})
	require.NoError(err)
	require.NotNil(active)
	assert.Equal(original.Repository.ID, active.Repository.ID)
	assert.Equal(RepositoryLifecycleActive, active.Lifecycle)
	var projectRepoID int64
	require.NoError(d.ReadDB().QueryRow(
		`SELECT repo_id FROM forge_projects WHERE id = 'project-local'`,
	).Scan(&projectRepoID))
	assert.Equal(original.Repository.ID, projectRepoID)
	assertDatabaseIntegrityForTest(t, d.ReadDB())
}

func TestReconcileRepositoryObservationMovesIntoOccupiedRoute(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	moving := reconcileCatalogRepository(
		t, d, "provider-moving", "org-a", "project-a", baseTime(),
	)
	displaced := reconcileCatalogRepository(
		t,
		d,
		"provider-displaced",
		"org-b",
		"project-b",
		baseTime().Add(time.Hour),
	)
	moved := reconcileCatalogRepository(
		t,
		d,
		"provider-moving",
		"org-b",
		"project-b",
		baseTime().Add(2*time.Hour),
	)
	assert.Equal(moving.Repository.ID, moved.Repository.ID)
	displacedByID, err := d.GetRepositoryByProviderID(
		t.Context(), "github", "github.com", "provider-displaced",
	)
	require.NoError(err)
	require.NotNil(displacedByID)
	assert.Equal(displaced.Repository.ID, displacedByID.Repository.ID)
	assert.Equal(RepositoryLifecycleInactive, displacedByID.Lifecycle)
}

func TestReconcileRepositoryObservationRejectsIncompleteIdentity(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	for _, identity := range []RepoIdentity{
		{
			Platform: "github", PlatformHost: "github.com",
			Owner: "org-a", Name: "project-a",
		},
		{
			Platform: "gitlab", PlatformRepoID: "provider-1",
			Owner: "org-a", Name: "project-a",
		},
	} {
		_, _, err := d.ReconcileRepositoryObservation(
			t.Context(), identity, baseTime(),
		)
		require.Error(err)
	}
	var repositoryCount int
	require.NoError(d.ReadDB().QueryRow(
		`SELECT COUNT(*) FROM forge_repos`,
	).Scan(&repositoryCount))
	require.Zero(repositoryCount)
}

func TestReconcileRepositoryObservationRefreshesCaseOnlyDisplayMetadata(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	firstObservedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	first, accepted, err := d.ReconcileRepositoryObservation(t.Context(), RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.example.com",
		PlatformRepoID: "provider-1",
		Owner:          "Group-A",
		Name:           "Project-A",
	}, firstObservedAt)
	require.NoError(err)
	require.True(accepted)
	require.NotNil(first)
	require.Len(first.Routes, 1)

	secondObservedAt := firstObservedAt.Add(time.Hour)
	second, accepted, err := d.ReconcileRepositoryObservation(t.Context(), RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.example.com",
		PlatformRepoID: "provider-1",
		Owner:          "group-a",
		Name:           "PROJECT-A",
	}, secondObservedAt)
	require.NoError(err)
	require.True(accepted, "case-only rename must be applied, not rejected")
	require.NotNil(second)
	require.Len(second.Routes, 1)
	assert.Equal(first.Repository.ID, second.Repository.ID)
	assert.Equal(first.Routes[0].ID, second.Routes[0].ID)
	assert.Equal(first.Routes[0].FirstSeenAt, second.Routes[0].FirstSeenAt)
	assert.Equal("group-a", second.Repository.Owner)
	assert.Equal("PROJECT-A", second.Repository.Name)
	assert.Equal("group-a/PROJECT-A", second.Repository.RepoPath)
	assert.Equal("group-a", second.Routes[0].Owner)
	assert.Equal("PROJECT-A", second.Routes[0].Name)
	assert.Equal("group-a/PROJECT-A", second.Routes[0].RepoPath)
	assert.Equal(secondObservedAt, second.Routes[0].LastSeenAt)
}

func TestRepositoryCatalogReadWaitsForReconciliation(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	reconcileCatalogRepository(
		t, d, "provider-old", "org-a", "project-a", baseTime(),
	)

	releaseRead, err := d.LockRepositoryReconciliationRead(t.Context())
	require.NoError(err)
	writerWaiting := make(chan struct{})
	restoreHook := d.SetBeforeRepositoryReconciliationWriteLockForTest(func() {
		close(writerWaiting)
	})
	t.Cleanup(restoreHook)

	writerDone := make(chan error, 1)
	go func() {
		_, _, reconcileErr := d.ReconcileRepositoryObservation(
			context.Background(),
			RepoIdentity{
				Platform:       "github",
				PlatformHost:   "github.com",
				PlatformRepoID: "provider-new",
				Owner:          "org-a",
				Name:           "project-a",
			},
			baseTime().Add(time.Hour),
		)
		writerDone <- reconcileErr
	}()
	<-writerWaiting

	type readResult struct {
		entry *RepositoryCatalogEntry
		err   error
	}
	readDone := make(chan readResult, 1)
	go func() {
		entry, readErr := d.GetRepositoryByProviderID(
			context.Background(), "github", "github.com", "provider-old",
		)
		readDone <- readResult{entry: entry, err: readErr}
	}()
	select {
	case <-readDone:
		require.Fail("catalog read crossed a pending reconciliation")
	case <-time.After(50 * time.Millisecond):
	}

	releaseRead()
	require.NoError(<-writerDone)
	result := <-readDone
	require.NoError(result.err)
	require.NotNil(result.entry)
	assert.Equal(RepositoryLifecycleInactive, result.entry.Lifecycle)
	require.Len(result.entry.Routes, 1)
	assert.False(result.entry.Routes[0].Current)
}

func TestDeactivateRepositoryObservationPreservesHistory(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	repository := reconcileCatalogRepository(
		t, d, "provider-1", "org-a", "project-a", baseTime(),
	)
	insertTestIssueWithOptions(t, d, testIssue(repository.Repository.ID, 1))
	_, err := d.WriteDB().Exec(`
		INSERT INTO forge_archive_repos (
			repo_id, collection_mode, operator_state, created_at, updated_at
		) VALUES (?, 'full', 'active', datetime('now'), datetime('now'))`,
		repository.Repository.ID,
	)
	require.NoError(err)

	deactivated, err := d.DeactivateRepositoryObservation(
		t.Context(), "github", "github.com", "provider-1",
		baseTime().Add(time.Hour),
	)
	require.NoError(err)
	require.NotNil(deactivated)
	assert.Equal(repository.Repository.ID, deactivated.Repository.ID)
	assert.Equal(RepositoryLifecycleInactive, deactivated.Lifecycle)
	require.Len(deactivated.Routes, 1)
	assert.False(deactivated.Routes[0].Current)

	active, err := d.ResolveActiveRepositoryRoute(t.Context(), RepoIdentity{
		Platform:     "github",
		PlatformHost: "github.com",
		Owner:        "org-a",
		Name:         "project-a",
	})
	require.NoError(err)
	assert.Nil(active)
	var issueRepoID, archiveRepoID int64
	require.NoError(d.ReadDB().QueryRow(
		`SELECT repo_id FROM forge_issues WHERE number = 1`,
	).Scan(&issueRepoID))
	require.NoError(d.ReadDB().QueryRow(
		`SELECT repo_id FROM forge_archive_repos WHERE repo_id = ?`,
		repository.Repository.ID,
	).Scan(&archiveRepoID))
	assert.Equal(repository.Repository.ID, issueRepoID)
	assert.Equal(repository.Repository.ID, archiveRepoID)

	again, err := d.DeactivateRepositoryObservation(
		t.Context(), "github", "github.com", "provider-1",
		baseTime().Add(2*time.Hour),
	)
	require.NoError(err)
	require.NotNil(again)
	require.Len(again.Routes, 1)
	assert.Equal(deactivated.Routes[0].ID, again.Routes[0].ID)
}

func TestLegacyRepositoryWritersMaintainCatalogState(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	legacyID, err := d.UpsertRepo(t.Context(), RepoIdentity{
		Platform:     "github",
		PlatformHost: "github.com",
		Owner:        "org-a",
		Name:         "project-a",
	})
	require.NoError(err)

	legacyEntries, err := d.ListRepositoryCatalog(
		t.Context(), RepositoryCatalogFilter{RepoPath: "org-a/project-a"},
	)
	require.NoError(err)
	require.Len(legacyEntries, 1)
	assert.Equal(legacyID, legacyEntries[0].Repository.ID)
	assert.Equal(RepositoryLifecycleInactive, legacyEntries[0].Lifecycle)
	require.Len(legacyEntries[0].Routes, 1)
	assert.False(legacyEntries[0].Routes[0].Current)

	require.NoError(d.UpdateRepoProviderMetadata(
		t.Context(), legacyID, RepoProviderMetadata{
			PlatformRepoID: "provider-1",
			WebURL:         "https://example.com/org-a/project-a",
			CloneURL:       "https://example.com/org-a/project-a.git",
			DefaultBranch:  "main",
		},
	))
	canonical, err := d.GetRepositoryByProviderID(
		t.Context(), "github", "github.com", "provider-1",
	)
	require.NoError(err)
	require.NotNil(canonical)
	assert.Equal(legacyID, canonical.Repository.ID)
	assert.Equal(RepositoryLifecycleActive, canonical.Lifecycle)
	require.Len(canonical.Routes, 1)
	assert.True(canonical.Routes[0].Current)
}

func TestUpsertRepoByProviderIDDoesNotMergeRouteReplacement(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	oldID, err := d.UpsertRepoByProviderID(t.Context(), RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "provider-old",
		Owner:          "org-a",
		Name:           "project-a",
	})
	require.NoError(err)
	insertTestIssueWithOptions(t, d, testIssue(oldID, 1))

	newID, err := d.UpsertRepoByProviderID(t.Context(), RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "provider-new",
		Owner:          "org-a",
		Name:           "project-a",
	})
	require.NoError(err)
	assert.NotEqual(oldID, newID)
	oldEntry, err := d.GetRepositoryByProviderID(
		t.Context(), "github", "github.com", "provider-old",
	)
	require.NoError(err)
	require.NotNil(oldEntry)
	assert.Equal(RepositoryLifecycleInactive, oldEntry.Lifecycle)
	var issueRepoID int64
	require.NoError(d.ReadDB().QueryRow(
		`SELECT repo_id FROM forge_issues WHERE number = 1`,
	).Scan(&issueRepoID))
	assert.Equal(oldID, issueRepoID)
}

func TestUpdateRepoProviderMetadataRejectsStableIDChange(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	repository := reconcileCatalogRepository(
		t, d, "provider-1", "org-a", "project-a", baseTime(),
	)
	err := d.UpdateRepoProviderMetadata(
		t.Context(), repository.Repository.ID, RepoProviderMetadata{
			PlatformRepoID: "provider-2",
		},
	)
	require.ErrorContains(err, "stable provider id")
	entry, err := d.GetRepositoryByProviderID(
		t.Context(), "github", "github.com", "provider-1",
	)
	require.NoError(err)
	require.NotNil(entry)
}

// TestUpsertRepoCachedIdentityDoesNotReclaimReusedRoute covers a delayed
// cached write: it carries no provider verification, so it must resolve by
// stable ID without stealing back a route another repository now holds.
func TestUpsertRepoCachedIdentityDoesNotReclaimReusedRoute(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	old := reconcileCatalogRepository(
		t, d, "provider-old", "acme", "widget", baseTime(),
	)
	reconcileCatalogRepository(
		t, d, "provider-old", "acme", "gadget", baseTime().Add(time.Hour),
	)
	current := reconcileCatalogRepository(
		t, d, "provider-new", "acme", "widget", baseTime().Add(2*time.Hour),
	)

	cached := RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: "provider-old",
		Owner:          "acme", Name: "widget",
	}
	upserted, err := d.UpsertRepo(t.Context(), cached)
	require.NoError(err)
	assert.Equal(old.Repository.ID, upserted)
	byProvider, err := d.UpsertRepoByProviderID(t.Context(), cached)
	require.NoError(err)
	assert.Equal(old.Repository.ID, byProvider)

	active, err := d.ResolveActiveRepositoryRoute(t.Context(), RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		Owner: "acme", Name: "widget",
	})
	require.NoError(err)
	require.NotNil(active)
	assert.Equal(current.Repository.ID, active.Repository.ID,
		"cached write must not reclaim the reused route")
}

func TestDeactivateRepositoryObservationAdvancesWatermarkWhenAlreadyInactive(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	reconcileCatalogRepository(
		t, d, "provider-1", "org-a", "project-a", baseTime(),
	)
	first, err := d.DeactivateRepositoryObservation(
		t.Context(), "github", "github.com", "provider-1",
		baseTime().Add(2*time.Hour),
	)
	require.NoError(err)
	require.NotNil(first)
	assert.Equal(RepositoryLifecycleInactive, first.Lifecycle)

	second, err := d.DeactivateRepositoryObservation(
		t.Context(), "github", "github.com", "provider-1",
		baseTime().Add(4*time.Hour),
	)
	require.NoError(err)
	require.NotNil(second)

	stale, accepted, err := d.ReconcileRepositoryObservation(t.Context(), RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: "provider-1",
		Owner:          "org-a", Name: "project-a",
	}, baseTime().Add(3*time.Hour))
	require.NoError(err)
	assert.False(accepted,
		"positive observation older than the second absence must be rejected")
	require.NotNil(stale)
	assert.Equal(RepositoryLifecycleInactive, stale.Lifecycle)
}

func TestWorkspaceRouteWithOnlyHistoricalOccupantsIsAmbiguous(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	reconcileCatalogRepository(
		t, d, "provider-1", "org-a", "project-a", baseTime(),
	)
	reconcileCatalogRepository(
		t, d, "provider-1", "org-a", "project-b", baseTime().Add(time.Hour),
	)

	vacated, err := d.WorkspaceRepoRouteHasHistoricalOccupants(
		t.Context(), "github", "github.com", "org-a", "project-a",
	)
	require.NoError(err)
	assert.True(vacated,
		"a vacated route must stay ambiguous until its next occupant is cataloged")

	current, err := d.WorkspaceRepoRouteHasHistoricalOccupants(
		t.Context(), "github", "github.com", "org-a", "project-b",
	)
	require.NoError(err)
	assert.False(current,
		"a route wholly owned by its current occupant is unambiguous")

	_, err = d.UpsertRepo(t.Context(), RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		Owner: "org-a", Name: "legacy",
	})
	require.NoError(err)
	legacy, err := d.WorkspaceRepoRouteHasHistoricalOccupants(
		t.Context(), "github", "github.com", "org-a", "legacy",
	)
	require.NoError(err)
	assert.False(legacy,
		"a legacy route-only repository is uncataloged, not vacated")
}

func TestReconcileRepositoryObservationAdoptsLegacyRouteOnlyRepository(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	legacyID, err := d.UpsertRepo(t.Context(), RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		Owner: "org-a", Name: "project-a",
	})
	require.NoError(err)

	entry := reconcileCatalogRepository(
		t, d, "provider-1", "org-a", "project-a", baseTime(),
	)

	assert.Equal(legacyID, entry.Repository.ID,
		"first verification must adopt the legacy route-only row, not strand it")
	assert.Equal("provider-1", entry.Repository.PlatformRepoID)
	assert.Equal(RepositoryLifecycleActive, entry.Lifecycle)
	collision, err := d.WorkspaceRepoRouteHasHistoricalOccupants(
		t.Context(), "github", "github.com", "org-a", "project-a",
	)
	require.NoError(err)
	assert.False(collision,
		"an adopted route has a single owner and must stay unambiguous")
}
