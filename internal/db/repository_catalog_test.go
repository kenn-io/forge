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

func TestUpdateRepoProviderObservationRejectsOlderSameRouteSettings(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	identity := RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: "repo-a", Owner: "acme", Name: "widget",
	}
	older := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	newer := older.Add(time.Second)
	entry, accepted, err := d.ReconcileRepositoryObservation(ctx, identity, older)
	require.NoError(err)
	require.True(accepted)
	require.NotNil(entry)
	_, accepted, err = d.ReconcileRepositoryObservation(ctx, identity, newer)
	require.NoError(err)
	require.True(accepted)

	applied, err := d.UpdateRepoProviderObservation(
		ctx,
		entry.Repository.ID,
		newer,
		RepoProviderMetadata{
			PlatformRepoID: "repo-a",
			WebURL:         "https://github.com/acme/widget",
			CloneURL:       "https://github.com/acme/widget.git",
			DefaultBranch:  "main",
		},
		&RepoMergeSettings{AllowSquashMerge: false, AllowMergeCommit: true},
		new(true),
	)
	require.NoError(err)
	require.True(applied)

	applied, err = d.UpdateRepoProviderObservation(
		ctx,
		entry.Repository.ID,
		older,
		RepoProviderMetadata{
			PlatformRepoID: "repo-a",
			WebURL:         "https://stale.example/acme/widget",
			CloneURL:       "https://stale.example/acme/widget.git",
			DefaultBranch:  "stale",
		},
		&RepoMergeSettings{AllowSquashMerge: true},
		new(false),
	)
	require.NoError(err)
	require.False(applied)

	stored, err := d.GetRepoByID(ctx, entry.Repository.ID)
	require.NoError(err)
	require.NotNil(stored)
	require.Equal("https://github.com/acme/widget", stored.WebURL)
	require.Equal("main", stored.DefaultBranch)
	require.False(stored.AllowSquashMerge)
	require.True(stored.AllowMergeCommit)
	require.True(stored.ViewerCanMerge)
}

// TestUpdateRepoProviderObservationPersistsViewerOnlySnapshot covers
// providers that report the viewer's merge permission without repository
// merge-method settings (for example a GitLab snapshot). The permission must
// persist on its own; the merge methods keep their stored values.
func TestUpdateRepoProviderObservationPersistsViewerOnlySnapshot(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	identity := RepoIdentity{
		Platform: "gitlab", PlatformHost: "gitlab.example.com",
		PlatformRepoID: "gid://gitlab/Project/42",
		Owner:          "group", Name: "project", RepoPath: "group/project",
	}
	when := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	entry, accepted, err := d.ReconcileRepositoryObservation(ctx, identity, when)
	require.NoError(err)
	require.True(accepted)
	require.NoError(d.UpdateRepoMergeSettings(
		ctx, entry.Repository.ID, true, false, true,
	))

	applied, err := d.UpdateRepoProviderObservation(
		ctx,
		entry.Repository.ID,
		when,
		RepoProviderMetadata{
			PlatformRepoID: "gid://gitlab/Project/42",
			DefaultBranch:  "main",
		},
		nil,
		new(false),
	)
	require.NoError(err)
	require.True(applied)

	stored, err := d.GetRepoByID(ctx, entry.Repository.ID)
	require.NoError(err)
	require.NotNil(stored)
	assert.False(stored.ViewerCanMerge)
	assert.True(stored.AllowSquashMerge)
	assert.False(stored.AllowMergeCommit)
	assert.True(stored.AllowRebaseMerge)
}

// TestUpdateRepoProviderObservationPreservesMetadataOnEmptyFields covers
// provider snapshots that omit URLs or the default branch (minimal REST
// payloads and test fixtures): known stored metadata must survive, while the
// snapshot's merge settings still commit.
func TestUpdateRepoProviderObservationPreservesMetadataOnEmptyFields(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	identity := RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: "repo-a", Owner: "acme", Name: "widget",
		RepoPath: "acme/widget",
	}
	when := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	entry, accepted, err := d.ReconcileRepositoryObservation(ctx, identity, when)
	require.NoError(err)
	require.True(accepted)
	require.NoError(d.UpdateRepoProviderMetadata(
		ctx, entry.Repository.ID, RepoProviderMetadata{
			PlatformRepoID: "repo-a",
			WebURL:         "https://github.com/acme/widget",
			CloneURL:       "/fixtures/acme/widget.git",
			DefaultBranch:  "main",
		},
	))

	applied, err := d.UpdateRepoProviderObservation(
		ctx,
		entry.Repository.ID,
		when,
		RepoProviderMetadata{PlatformRepoID: "repo-a"},
		&RepoMergeSettings{AllowSquashMerge: true},
		new(false),
	)
	require.NoError(err)
	require.True(applied)

	stored, err := d.GetRepoByID(ctx, entry.Repository.ID)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("https://github.com/acme/widget", stored.WebURL)
	assert.Equal("/fixtures/acme/widget.git", stored.CloneURL)
	assert.Equal("main", stored.DefaultBranch)
	assert.True(stored.AllowSquashMerge)
	assert.False(stored.AllowMergeCommit)
	assert.False(stored.ViewerCanMerge)
}

func TestUpdateRepoProviderObservationFailsClosedForReplacementOnReusedRoute(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	observedAt := baseTime()

	reconcileCatalogRepository(t, d, "provider-original", "acme", "widget", observedAt)
	reconcileCatalogRepository(
		t, d, "provider-original", "acme", "renamed", observedAt.Add(time.Minute),
	)
	replacementObservedAt := observedAt.Add(2 * time.Minute)
	replacement := reconcileCatalogRepository(
		t, d, "provider-replacement", "acme", "widget", replacementObservedAt,
	)

	applied, err := d.UpdateRepoProviderObservation(
		ctx,
		replacement.Repository.ID,
		replacementObservedAt,
		RepoProviderMetadata{PlatformRepoID: "provider-replacement"},
		nil,
		nil,
	)
	require.NoError(err)
	require.True(applied)

	stored, err := d.GetRepoByID(ctx, replacement.Repository.ID)
	require.NoError(err)
	require.NotNil(stored)
	require.False(stored.ViewerCanMerge,
		"a replacement must not inherit the permissive schema default")
}

func TestUpdateRepoProviderObservationPreservesKnownViewerPermissionWhenOmitted(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	observedAt := baseTime()
	entry := reconcileCatalogRepository(t, d, "provider-1", "acme", "widget", observedAt)
	require.NoError(d.UpdateRepoViewerCanMerge(ctx, entry.Repository.ID, true))

	applied, err := d.UpdateRepoProviderObservation(
		ctx,
		entry.Repository.ID,
		observedAt,
		RepoProviderMetadata{PlatformRepoID: "provider-1"},
		nil,
		nil,
	)
	require.NoError(err)
	require.True(applied)

	stored, err := d.GetRepoByID(ctx, entry.Repository.ID)
	require.NoError(err)
	require.NotNil(stored)
	require.True(stored.ViewerCanMerge)
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

func TestReconcileRepositoryObservationFailsClosedWhenAdoptingLegacyRepository(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	legacyID, err := d.UpsertRepo(t.Context(), RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		Owner: "org-a", Name: "project-a",
	})
	require.NoError(err)
	require.NoError(d.UpdateRepoViewerCanMerge(t.Context(), legacyID, true))

	entry := reconcileCatalogRepository(
		t, d, "provider-1", "org-a", "project-a", baseTime(),
	)
	require.Equal(legacyID, entry.Repository.ID)

	stored, err := d.GetRepoByID(t.Context(), legacyID)
	require.NoError(err)
	require.NotNil(stored)
	require.False(stored.ViewerCanMerge,
		"first provider verification must reset a legacy path-only permission")
}

func TestAdoptLegacyClonesIfSafeRequiresUnreusedVerifiedRoute(t *testing.T) {
	t.Run("verified route with one owner", func(t *testing.T) {
		d := openTestDB(t)
		entry := reconcileCatalogRepository(
			t, d, "provider-1", "acme", "widget", baseTime(),
		)
		called := false

		adopted, err := d.AdoptLegacyClonesIfSafe(
			t.Context(), RepoIdentity{
				Platform: "github", PlatformHost: "github.com",
				PlatformRepoID: "provider-1",
				Owner:          "acme", Name: "widget", RepoPath: "acme/widget",
			}, entry.Repository.ID,
			func() error { called = true; return nil },
		)
		require.NoError(t, err)
		assert.True(t, adopted)
		assert.True(t, called)
	})

	t.Run("route with a different legacy owner", func(t *testing.T) {
		d := openTestDB(t)
		_, err := d.UpsertRepo(t.Context(), RepoIdentity{
			Platform: "github", PlatformHost: "github.com",
			Owner: "acme", Name: "widget", RepoPath: "acme/widget",
		})
		require.NoError(t, err)
		entry := reconcileCatalogRepository(
			t, d, "provider-1", "acme", "renamed", baseTime(),
		)
		moved, _, err := d.ReconcileRepositoryObservation(t.Context(), RepoIdentity{
			Platform: "github", PlatformHost: "github.com",
			PlatformRepoID: "provider-1",
			Owner:          "acme", Name: "widget", RepoPath: "acme/widget",
		}, baseTime().Add(time.Minute))
		require.NoError(t, err)
		require.Equal(t, entry.Repository.ID, moved.Repository.ID)

		called := false
		adopted, err := d.AdoptLegacyClonesIfSafe(
			t.Context(), RepoIdentity{
				Platform: "github", PlatformHost: "github.com",
				PlatformRepoID: "provider-1",
				Owner:          "acme", Name: "widget", RepoPath: "acme/widget",
			}, moved.Repository.ID,
			func() error { called = true; return nil },
		)
		require.NoError(t, err)
		assert.False(t, adopted)
		assert.False(t, called)
	})

	t.Run("stale route snapshot after reuse", func(t *testing.T) {
		d := openTestDB(t)
		original := reconcileCatalogRepository(
			t, d, "provider-original", "acme", "widget", baseTime(),
		)
		_, _, err := d.ReconcileRepositoryObservation(t.Context(), RepoIdentity{
			Platform: "github", PlatformHost: "github.com",
			PlatformRepoID: "provider-original",
			Owner:          "acme", Name: "renamed", RepoPath: "acme/renamed",
		}, baseTime().Add(time.Minute))
		require.NoError(t, err)
		reconcileCatalogRepository(
			t, d, "provider-replacement", "acme", "widget",
			baseTime().Add(2*time.Minute),
		)
		called := false

		adopted, err := d.AdoptLegacyClonesIfSafe(
			t.Context(), RepoIdentity{
				Platform: "github", PlatformHost: "github.com",
				PlatformRepoID: "provider-original",
				Owner:          "acme", Name: "widget", RepoPath: "acme/widget",
			}, original.Repository.ID,
			func() error { called = true; return nil },
		)
		require.NoError(t, err)
		assert.False(t, adopted)
		assert.False(t, called)
	})
}

func TestAdoptLegacyClonesIfSafeHoldsRouteGuardThroughAdoption(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	entry := reconcileCatalogRepository(
		t, d, "provider-1", "acme", "widget", baseTime(),
	)
	identity := RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: "provider-1",
		Owner:          "acme", Name: "widget", RepoPath: "acme/widget",
	}
	adoptionStarted := make(chan struct{})
	releaseAdoption := make(chan struct{})
	adoptionDone := make(chan error, 1)
	go func() {
		_, err := d.AdoptLegacyClonesIfSafe(
			t.Context(), identity, entry.Repository.ID,
			func() error {
				close(adoptionStarted)
				<-releaseAdoption
				return nil
			},
		)
		adoptionDone <- err
	}()
	<-adoptionStarted

	writerWaiting := make(chan struct{})
	restoreHook := d.SetBeforeRepositoryReconciliationWriteLockForTest(func() {
		close(writerWaiting)
	})
	t.Cleanup(restoreHook)
	renameDone := make(chan error, 1)
	go func() {
		_, _, err := d.ReconcileRepositoryObservation(t.Context(), RepoIdentity{
			Platform: "github", PlatformHost: "github.com",
			PlatformRepoID: "provider-1",
			Owner:          "acme", Name: "renamed", RepoPath: "acme/renamed",
		}, baseTime().Add(time.Minute))
		renameDone <- err
	}()
	<-writerWaiting
	select {
	case err := <-renameDone:
		require.Fail("route changed during clone adoption", "unexpected error: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseAdoption)
	require.NoError(<-adoptionDone)
	require.NoError(<-renameDone)
}

func TestReconcileRepositoryObservationClearsVacatedRouteSyncState(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	observedAt := baseTime()
	number := 7

	original := reconcileCatalogRepository(
		t, d, "provider-original", "acme", "alpha", observedAt,
	)
	require.NoError(d.UpdateNotificationSyncWatermark(
		ctx, "github", "github.com", "acme", "alpha", observedAt, &observedAt,
	))
	require.NoError(d.UpsertHTTPEtag(
		ctx, "github", "github.com", "acme", "alpha", "pull_request", number, `"old"`,
	))
	require.NoError(d.UpsertNotifications(ctx, []Notification{
		{
			Platform: "github", PlatformHost: "github.com",
			PlatformNotificationID: "linked", RepoOwner: "acme", RepoName: "alpha",
			SubjectType: "PullRequest", SubjectTitle: "linked", ItemNumber: &number,
			ItemType: ItemTypePR, Reason: "mention", Unread: true,
			SourceUpdatedAt: observedAt, SyncedAt: observedAt,
		},
		{
			Platform: "github", PlatformHost: "github.com",
			PlatformNotificationID: "path-only", RepoOwner: "acme", RepoName: "alpha",
			SubjectType: "PullRequest", SubjectTitle: "path only", ItemNumber: &number,
			ItemType: ItemTypePR, Reason: "mention", Unread: true,
			SourceUpdatedAt: observedAt, SyncedAt: observedAt,
		},
	}))
	_, err := d.WriteDB().ExecContext(ctx,
		`UPDATE forge_notification_items SET repo_id = NULL
		 WHERE platform_notification_id = 'path-only'`,
	)
	require.NoError(err)

	renamed, _, err := d.ReconcileRepositoryObservation(ctx, RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: "provider-original",
		Owner:          "acme", Name: "beta", RepoPath: "acme/beta",
	}, observedAt.Add(time.Minute))
	require.NoError(err)
	assert.Equal(original.Repository.ID, renamed.Repository.ID)

	watermark, err := d.GetNotificationSyncWatermark(
		ctx, "github", "github.com", "acme", "alpha",
	)
	require.NoError(err)
	assert.Nil(watermark)
	etag, err := d.GetHTTPEtag(
		ctx, "github", "github.com", "acme", "alpha", "pull_request", number,
	)
	require.NoError(err)
	assert.Empty(etag)
	notifications, err := d.ListNotifications(ctx, ListNotificationsOpts{State: "all"})
	require.NoError(err)
	require.Len(notifications, 1)
	assert.Equal("linked", notifications[0].PlatformNotificationID)
	require.NotNil(notifications[0].RepoID)
	assert.Equal(original.Repository.ID, *notifications[0].RepoID)
}

func TestReconcileRepositoryObservationClearsHistoricalRouteStateBeforeReuse(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	observedAt := baseTime()
	number := 7

	reconcileCatalogRepository(t, d, "provider-original", "acme", "alpha", observedAt)
	_, _, err := d.ReconcileRepositoryObservation(ctx, RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: "provider-original",
		Owner:          "acme", Name: "beta", RepoPath: "acme/beta",
	}, observedAt.Add(time.Minute))
	require.NoError(err)

	require.NoError(d.UpdateNotificationSyncWatermark(
		ctx, "github", "github.com", "acme", "alpha", observedAt, &observedAt,
	))
	require.NoError(d.UpsertHTTPEtag(
		ctx, "github", "github.com", "acme", "alpha", "pull_request", number, `"stale"`,
	))
	require.NoError(d.UpsertNotifications(ctx, []Notification{{
		Platform: "github", PlatformHost: "github.com",
		PlatformNotificationID: "path-only", RepoOwner: "acme", RepoName: "alpha",
		SubjectType: "PullRequest", SubjectTitle: "stale", ItemNumber: &number,
		ItemType: ItemTypePR, Reason: "mention", Unread: true,
		SourceUpdatedAt: observedAt, SyncedAt: observedAt,
	}}))

	replacement, _, err := d.ReconcileRepositoryObservation(ctx, RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: "provider-replacement",
		Owner:          "acme", Name: "alpha", RepoPath: "acme/alpha",
	}, observedAt.Add(2*time.Minute))
	require.NoError(err)
	assert.Equal("provider-replacement", replacement.Repository.PlatformRepoID)

	watermark, err := d.GetNotificationSyncWatermark(
		ctx, "github", "github.com", "acme", "alpha",
	)
	require.NoError(err)
	assert.Nil(watermark)
	etag, err := d.GetHTTPEtag(
		ctx, "github", "github.com", "acme", "alpha", "pull_request", number,
	)
	require.NoError(err)
	assert.Empty(etag)
	notifications, err := d.ListNotifications(ctx, ListNotificationsOpts{State: "all"})
	require.NoError(err)
	assert.Empty(notifications)
}

func TestReconcileRepositoryObservationClearsLegacyHistoricalRouteStateBeforeReuse(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	observedAt := baseTime()
	number := 7

	legacyID, err := d.UpsertRepo(ctx, RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		Owner: "acme", Name: "alpha", RepoPath: "acme/alpha",
	})
	require.NoError(err)
	replacement := reconcileCatalogRepository(
		t, d, "provider-replacement", "acme", "beta", observedAt,
	)
	require.NotEqual(legacyID, replacement.Repository.ID)

	require.NoError(d.UpdateNotificationSyncWatermark(
		ctx, "github", "github.com", "acme", "alpha", observedAt, &observedAt,
	))
	require.NoError(d.UpsertHTTPEtag(
		ctx, "github", "github.com", "acme", "alpha", "pull_request", number, `"stale"`,
	))
	require.NoError(d.UpsertNotifications(ctx, []Notification{{
		Platform: "github", PlatformHost: "github.com",
		PlatformNotificationID: "path-only", RepoOwner: "acme", RepoName: "alpha",
		SubjectType: "PullRequest", SubjectTitle: "stale", ItemNumber: &number,
		ItemType: ItemTypePR, Reason: "mention", Unread: true,
		SourceUpdatedAt: observedAt, SyncedAt: observedAt,
	}}))

	moved, _, err := d.ReconcileRepositoryObservation(ctx, RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: "provider-replacement",
		Owner:          "acme", Name: "alpha", RepoPath: "acme/alpha",
	}, observedAt.Add(time.Minute))
	require.NoError(err)
	assert.Equal(replacement.Repository.ID, moved.Repository.ID)

	watermark, err := d.GetNotificationSyncWatermark(
		ctx, "github", "github.com", "acme", "alpha",
	)
	require.NoError(err)
	assert.Nil(watermark)
	etag, err := d.GetHTTPEtag(
		ctx, "github", "github.com", "acme", "alpha", "pull_request", number,
	)
	require.NoError(err)
	assert.Empty(etag)
	notifications, err := d.ListNotifications(ctx, ListNotificationsOpts{State: "all"})
	require.NoError(err)
	assert.Empty(notifications)
}

func TestRepositoryRouteGuardRejectsWriteAfterABAReuse(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	observedAt := baseTime()
	identity := RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: "provider-original",
		Owner:          "acme", Name: "widget", RepoPath: "acme/widget",
	}
	original, _, err := d.ReconcileRepositoryObservation(ctx, identity, observedAt)
	require.NoError(err)
	require.NoError(d.UpdateRepoMergeSettings(
		ctx, original.Repository.ID, false, false, false,
	))
	fence, found, err := d.CurrentRepositoryRouteFence(
		ctx, identity, original.Repository.ID,
	)
	require.NoError(err)
	require.True(found)
	guarded := d.WithRepositoryRouteFence(ctx, identity, fence)

	_, _, err = d.ReconcileRepositoryObservation(ctx, RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: "provider-original",
		Owner:          "acme", Name: "renamed", RepoPath: "acme/renamed",
	}, observedAt.Add(time.Minute))
	require.NoError(err)
	_, _, err = d.ReconcileRepositoryObservation(ctx, RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: "provider-replacement",
		Owner:          "acme", Name: "widget", RepoPath: "acme/widget",
	}, observedAt.Add(2*time.Minute))
	require.NoError(err)
	_, _, err = d.ReconcileRepositoryObservation(ctx, RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: "provider-replacement",
		Owner:          "acme", Name: "elsewhere", RepoPath: "acme/elsewhere",
	}, observedAt.Add(3*time.Minute))
	require.NoError(err)
	_, _, err = d.ReconcileRepositoryObservation(ctx, identity, observedAt.Add(4*time.Minute))
	require.NoError(err)

	err = d.UpdateRepoMergeSettings(guarded, original.Repository.ID, true, false, false)
	require.ErrorIs(err, ErrRepositoryRouteFenceChanged)
	repo, err := d.GetRepoByID(ctx, original.Repository.ID)
	require.NoError(err)
	require.NotNil(repo)
	require.False(repo.AllowSquashMerge)
}
