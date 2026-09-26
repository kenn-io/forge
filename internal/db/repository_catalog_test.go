package db

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/platform"
)

func observeCatalogRepository(
	t *testing.T, d *DB, providerID int64, owner, name string,
) *RepositoryCatalogEntry {
	t.Helper()
	entry, err := d.ObserveRepository(t.Context(), RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: providerID,
		Owner:          owner,
		Name:           name,
	})
	require.NoError(t, err)
	require.NotNil(t, entry)
	assertDatabaseIntegrityForTest(t, d.ReadDB())
	return entry
}

func githubRepositoryIdentity(providerID int64) platform.RepositoryIdentity {
	return platform.RepositoryIdentity{
		Provider: "github", PlatformHost: "github.com", PlatformRepoID: providerID,
	}
}

func githubRoute(owner, name string) RepoIdentity {
	return RepoIdentity{
		Platform: "github", PlatformHost: "github.com", Owner: owner, Name: name,
	}
}

// seedRepositoryRouteReplacement leaves two rows at org-a/project-a: 1001 was
// displaced by 1002, which now holds the route.
func seedRepositoryRouteReplacement(t *testing.T, d *DB) (int64, int64) {
	t.Helper()
	original := observeCatalogRepository(t, d, 1001, "org-a", "project-a")
	replacement := observeCatalogRepository(t, d, 1002, "org-a", "project-a")
	return original.Repository.ID, replacement.Repository.ID
}

func TestObserveRepositoryCreatesActiveRepository(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)

	entry, err := d.ObserveRepository(t.Context(), RepoIdentity{
		Platform: "gitlab", PlatformHost: "GitLab.Example.com",
		PlatformRepoID: 250833, Owner: "Group-A", Name: "Project-A",
	})
	require.NoError(err)
	require.NotNil(entry)
	assert.Equal(RepositoryLifecycleActive, entry.Lifecycle)
	assert.Equal("gitlab.example.com", entry.Repository.PlatformHost)
	assert.EqualValues(250833, entry.Repository.PlatformRepoID)
	assert.Equal("Group-A/Project-A", entry.Repository.RepoPath)
	assert.False(entry.Repository.ViewerCanMerge,
		"a new repository must not inherit a permissive merge permission")

	again, err := d.ObserveRepository(t.Context(), RepoIdentity{
		Platform: "gitlab", PlatformHost: "gitlab.example.com",
		PlatformRepoID: 250833, Owner: "Group-A", Name: "Project-A",
	})
	require.NoError(err)
	assert.Equal(entry.Repository.ID, again.Repository.ID)
	entries, err := d.ListRepositoryCatalog(t.Context(), RepositoryCatalogFilter{})
	require.NoError(err)
	assert.Len(entries, 1)
}

func TestObserveRepositoryRejectsIncompleteIdentity(t *testing.T) {
	d := openTestDB(t)
	for name, identity := range map[string]RepoIdentity{
		"missing provider id": githubRoute("org-a", "project-a"),
		"missing route": {
			Platform: "github", PlatformHost: "github.com", PlatformRepoID: 1001,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := d.ObserveRepository(t.Context(), identity)
			require.Error(t, err)
		})
	}
	entries, err := d.ListRepositoryCatalog(t.Context(), RepositoryCatalogFilter{})
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestObserveRepositoryRenamesSameProviderIDInPlace(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	original := observeCatalogRepository(t, d, 1001, "org-a", "project-a")
	now := baseTime()
	_, err := d.UpsertMergeRequest(ctx, &MergeRequest{
		RepoID: original.Repository.ID, PlatformID: 9001, Number: 7,
		Title: "carried over", State: MergeRequestStateOpen,
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)

	renamed := observeCatalogRepository(t, d, 1001, "org-b", "project-b")
	assert.Equal(original.Repository.ID, renamed.Repository.ID)
	assert.Equal("org-b/project-b", renamed.Repository.RepoPath)
	assert.Equal(RepositoryLifecycleActive, renamed.Lifecycle)

	mr, err := d.GetMergeRequest(ctx, "github", "github.com", "org-b", "project-b", 7)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal("carried over", mr.Title)
	oldRoute, err := d.ResolveActiveRepositoryRoute(ctx, githubRoute("org-a", "project-a"))
	require.NoError(err)
	assert.Nil(oldRoute, "the vacated route resolves to nothing")
}

func TestObserveRepositoryRefreshesCaseOnlyDisplayRoute(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	identity := RepoIdentity{
		Platform: "gitlab", PlatformHost: "gitlab.example.com",
		PlatformRepoID: 42, Owner: "Group-A", Name: "Project-A",
	}
	first, err := d.ObserveRepository(t.Context(), identity)
	require.NoError(err)
	identity.Owner, identity.Name = "group-a", "PROJECT-A"
	second, err := d.ObserveRepository(t.Context(), identity)
	require.NoError(err)
	assert.Equal(first.Repository.ID, second.Repository.ID)
	assert.Equal("group-a", second.Repository.Owner)
	assert.Equal("PROJECT-A", second.Repository.Name)
	assert.Equal("group-a/PROJECT-A", second.Repository.RepoPath)
}

func TestObserveRepositoryRenameClearsVacatedRouteState(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	number := 7
	original := observeCatalogRepository(t, d, 1001, "acme", "alpha")
	seedRouteScopedState(t, d, "acme", "alpha", number)

	renamed := observeCatalogRepository(t, d, 1001, "acme", "beta")
	assert.Equal(original.Repository.ID, renamed.Repository.ID)
	assertRouteScopedStateCleared(t, d, "acme", "alpha", number)

	notifications, err := d.ListNotifications(ctx, ListNotificationsOpts{State: "all"})
	require.NoError(err)
	require.Len(notifications, 1, "notifications linked to the repository survive its rename")
	assert.Equal("linked", notifications[0].PlatformNotificationID)
}

func TestObserveRepositoryNewIDDisplacesRouteOccupant(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	number := 7
	original := observeCatalogRepository(t, d, 1001, "acme", "widget")
	now := baseTime()
	_, err := d.UpsertIssue(ctx, &Issue{
		RepoID: original.Repository.ID, PlatformID: 9001, Number: 3,
		Title: "original history", State: "open",
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	seedRouteScopedState(t, d, "acme", "widget", number)

	replacement := observeCatalogRepository(t, d, 1002, "acme", "widget")
	assert.NotEqual(original.Repository.ID, replacement.Repository.ID)
	assert.Equal(RepositoryLifecycleActive, replacement.Lifecycle)

	displaced, err := d.GetRepositoryByProviderID(ctx, githubRepositoryIdentity(1001))
	require.NoError(err)
	require.NotNil(displaced)
	assert.Equal(original.Repository.ID, displaced.Repository.ID)
	assert.Equal(RepositoryLifecycleInactive, displaced.Lifecycle)
	assert.Equal("acme/widget", displaced.Repository.RepoPath,
		"an inactive row keeps its last route for display")

	active, err := d.ResolveActiveRepositoryRoute(ctx, githubRoute("acme", "widget"))
	require.NoError(err)
	require.NotNil(active)
	assert.Equal(replacement.Repository.ID, active.Repository.ID)
	assertRouteScopedStateCleared(t, d, "acme", "widget", number)

	issue, err := d.GetIssue(ctx, "github", "github.com", "acme", "widget", 3)
	require.NoError(err)
	assert.Nil(issue, "the replacement does not inherit the displaced repository's items")
	var issueRepoID int64
	require.NoError(d.ReadDB().QueryRowContext(ctx,
		`SELECT repo_id FROM forge_issues WHERE number = 3`,
	).Scan(&issueRepoID))
	assert.Equal(original.Repository.ID, issueRepoID, "displaced history is kept")
}

func TestObserveRepositoryReactivatesDisplacedIDAtNewRoute(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	originalID, replacementID := seedRepositoryRouteReplacement(t, d)

	moved := observeCatalogRepository(t, d, 1001, "org-a", "project-moved")
	assert.Equal(originalID, moved.Repository.ID)
	assert.Equal(RepositoryLifecycleActive, moved.Lifecycle)
	assert.Equal("org-a/project-moved", moved.Repository.RepoPath)

	replacement, err := d.GetRepositoryByProviderID(t.Context(), githubRepositoryIdentity(1002))
	require.NoError(err)
	require.NotNil(replacement)
	assert.Equal(replacementID, replacement.Repository.ID)
	assert.Equal(RepositoryLifecycleActive, replacement.Lifecycle,
		"reactivating at another route leaves the current occupant alone")
}

func seedRouteScopedState(t *testing.T, d *DB, owner, name string, number int) {
	t.Helper()
	require := require.New(t)
	ctx := t.Context()
	observedAt := baseTime()
	require.NoError(d.UpdateNotificationSyncWatermark(
		ctx, "github", "github.com", owner, name, observedAt, &observedAt,
	))
	require.NoError(d.UpsertHTTPEtag(
		ctx, "github", "github.com", owner, name, "pull_request", number, `"old"`,
	))
	require.NoError(d.UpsertNotifications(ctx, []Notification{
		{
			Platform: "github", PlatformHost: "github.com",
			PlatformNotificationID: "linked", RepoOwner: owner, RepoName: name,
			SubjectType: "PullRequest", SubjectTitle: "linked", ItemNumber: &number,
			ItemType: ItemTypePR, Reason: "mention", Unread: true,
			SourceUpdatedAt: observedAt, SyncedAt: observedAt,
		},
		{
			Platform: "github", PlatformHost: "github.com",
			PlatformNotificationID: "route-only", RepoOwner: owner, RepoName: name,
			SubjectType: "PullRequest", SubjectTitle: "route only", ItemNumber: &number,
			ItemType: ItemTypePR, Reason: "mention", Unread: true,
			SourceUpdatedAt: observedAt, SyncedAt: observedAt,
		},
	}))
	_, err := d.WriteDB().ExecContext(ctx,
		`UPDATE forge_notification_items SET repo_id = NULL
		 WHERE platform_notification_id = 'route-only'`,
	)
	require.NoError(err)
}

func assertRouteScopedStateCleared(t *testing.T, d *DB, owner, name string, number int) {
	t.Helper()
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	watermark, err := d.GetNotificationSyncWatermark(ctx, "github", "github.com", owner, name)
	require.NoError(err)
	assert.Nil(watermark)
	etag, err := d.GetHTTPEtag(ctx, "github", "github.com", owner, name, "pull_request", number)
	require.NoError(err)
	assert.Empty(etag)
	var routeOnly int
	require.NoError(d.ReadDB().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM forge_notification_items
		WHERE platform_notification_id = 'route-only'`,
	).Scan(&routeOnly))
	assert.Zero(routeOnly)
}

func TestResolveActiveRepositoryRouteReturnsOnlyCurrentOccupant(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	_, newID := seedRepositoryRouteReplacement(t, d)
	entry, err := d.ResolveActiveRepositoryRoute(t.Context(), githubRoute("org-a", "project-a"))
	require.NoError(err)
	require.NotNil(entry)
	assert.Equal(newID, entry.Repository.ID)
	assert.Equal(RepositoryLifecycleActive, entry.Lifecycle)
}

func TestGetRepoByIdentityPrefersProviderID(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	renamed := observeCatalogRepository(t, d, 1001, "org-a", "project-renamed")
	other := observeCatalogRepository(t, d, 1002, "org-a", "project-a")

	// A stale route paired with the provider ID still finds the renamed
	// repository, not whichever repository now holds the route.
	byID, err := d.GetRepoByIdentity(t.Context(), RepoIdentity{
		Platform: "github", PlatformHost: "github.com", PlatformRepoID: 1001,
		Owner: "org-a", Name: "project-a",
	})
	require.NoError(err)
	require.NotNil(byID)
	assert.Equal(renamed.Repository.ID, byID.ID)

	byRoute, err := d.GetRepoByIdentity(t.Context(), githubRoute("org-a", "project-a"))
	require.NoError(err)
	require.NotNil(byRoute)
	assert.Equal(other.Repository.ID, byRoute.ID)

	missing, err := d.GetRepoByIdentity(t.Context(), RepoIdentity{
		Platform: "github", PlatformHost: "github.com", PlatformRepoID: 9999,
		Owner: "org-a", Name: "project-a",
	})
	require.NoError(err)
	assert.Nil(missing, "an unknown provider ID never falls back to the route")
}

func TestActiveRepoCarriesProviderIdentity(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	oldID, newID := seedRepositoryRouteReplacement(t, d)
	want := githubRepositoryIdentity(1002)

	active, err := d.GetRepoByIdentity(t.Context(), githubRoute("org-a", "project-a"))
	require.NoError(err)
	require.NotNil(active)
	assert.Equal(newID, active.ID)
	assert.Equal(want, active.Identity())
	// The identity is fixed when the row is read; editing the row copy
	// cannot redirect comparisons to another repository.
	active.PlatformRepoID = 1001
	assert.Equal(want, active.Identity())

	byID, err := d.GetActiveRepoByID(t.Context(), oldID)
	require.NoError(err)
	assert.Nil(byID)
	inactive, err := d.GetActiveRepoByProviderID(t.Context(), githubRepositoryIdentity(1001))
	require.NoError(err)
	assert.Nil(inactive)
}

func TestActiveRepoRejectsActiveRowWithoutProviderIdentity(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	// Normal writes cannot produce this row; a corrupted store must fail
	// loudly instead of yielding an empty identity that compares equal to
	// other unresolved references.
	result, err := d.WriteDB().ExecContext(t.Context(), `
		INSERT INTO forge_repos (
			platform, platform_host, platform_repo_id,
			owner, name, repo_path, owner_key, name_key, repo_path_key,
			lifecycle_state
		) VALUES ('github', 'github.com', 0, 'org-a', 'project-b', 'org-a/project-b',
			'org-a', 'project-b', 'org-a/project-b', 'active')`)
	require.NoError(err)
	id, err := result.LastInsertId()
	require.NoError(err)
	_, err = d.GetActiveRepoByID(t.Context(), id)
	require.ErrorContains(err, "no provider identity")
}

func TestListRepositoryCatalogFindsEveryRowAtRoute(t *testing.T) {
	d := openTestDB(t)
	oldID, newID := seedRepositoryRouteReplacement(t, d)
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

func TestListRepositoryCatalogRejectsUnqualifiedProviderID(t *testing.T) {
	d := openTestDB(t)
	_, err := d.ListRepositoryCatalog(t.Context(), RepositoryCatalogFilter{
		PlatformRepoID: 1001,
	})
	require.ErrorContains(t, err, "platform and host")
}

func TestListRepositoryCatalogFiltersLifecycle(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	_, newID := seedRepositoryRouteReplacement(t, d)
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

func TestOperationalRepositoryReadsUseActiveRepository(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	oldID, newID := seedRepositoryRouteReplacement(t, d)
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

	mrs, err := d.ListMergeRequests(t.Context(), ListMergeRequestsOpts{State: "all"})
	require.NoError(err)
	require.Len(mrs, 1)
	assert.Equal("current item", mrs[0].Title)
	mr, err := d.GetMergeRequest(t.Context(), "github", "github.com", "org-a", "project-a", 7)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal(newID, mr.RepoID)

	issues, err := d.ListIssues(t.Context(), ListIssuesOpts{State: "all"})
	require.NoError(err)
	require.Len(issues, 1)
	assert.Equal("current item", issues[0].Title)
	issue, err := d.GetIssue(t.Context(), "github", "github.com", "org-a", "project-a", 8)
	require.NoError(err)
	require.NotNil(issue)
	assert.Equal(newID, issue.RepoID)
}

func TestWorkspaceRouteLookupBindsCurrentRepositoryAfterReplacement(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	original := observeCatalogRepository(t, d, 1001, "org-a", "project-a")
	require.NoError(d.InsertWorkspace(t.Context(), &Workspace{
		ID: "historical-workspace", Platform: "github",
		PlatformHost: "github.com", RepoOwner: "org-a", RepoName: "project-a",
		ItemType: WorkspaceItemTypePullRequest, ItemNumber: 7,
		WorktreePath: "/tmp/historical-workspace", TmuxSession: "historical-workspace",
	}))
	current := observeCatalogRepository(t, d, 1002, "org-a", "project-a")
	originalWorkspace, err := d.GetWorkspace(t.Context(), "historical-workspace")
	require.NoError(err)
	require.NotNil(originalWorkspace)
	assert.Equal(original.Repository.ID, originalWorkspace.RepoID)

	byRoute, err := d.GetWorkspaceByMRForProvider(
		t.Context(), "github", "github.com", "org-a", "project-a", 7,
	)
	require.NoError(err)
	assert.Nil(byRoute, "a displaced repository's workspace does not answer for the route")

	require.NoError(d.InsertWorkspace(t.Context(), &Workspace{
		ID: "replacement-workspace", Platform: "github",
		PlatformHost: "github.com", RepoOwner: "org-a", RepoName: "project-a",
		ItemType: WorkspaceItemTypePullRequest, ItemNumber: 7,
		WorktreePath: "/tmp/replacement-workspace", TmuxSession: "replacement-workspace",
	}))
	replacementWorkspace, err := d.GetWorkspace(t.Context(), "replacement-workspace")
	require.NoError(err)
	require.NotNil(replacementWorkspace)
	assert.Equal(current.Repository.ID, replacementWorkspace.RepoID)
	byRoute, err = d.GetWorkspaceByMRForProvider(
		t.Context(), "github", "github.com", "org-a", "project-a", 7,
	)
	require.NoError(err)
	require.NotNil(byRoute)
	assert.Equal("replacement-workspace", byRoute.ID)
}

func TestInsertWorkspaceRejectsChangedExplicitRepositoryIdentity(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	originalID, _ := seedRepositoryRouteReplacement(t, d)

	err := d.InsertWorkspace(t.Context(), &Workspace{
		ID: "stale-repository-workspace", Platform: "github",
		PlatformHost: "github.com", RepoOwner: "org-a", RepoName: "project-a",
		RepoID:   originalID,
		ItemType: WorkspaceItemTypePullRequest, ItemNumber: 9,
		WorktreePath: "/tmp/stale-repository-workspace",
		TmuxSession:  "stale-repository-workspace",
	})
	require.ErrorIs(err, ErrRepositoryIdentityChanged)
}

func TestDeactivateRepositoryKeepsHistoryAndRoute(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repository := observeCatalogRepository(t, d, 1001, "org-a", "project-a")
	insertTestIssueWithOptions(t, d, testIssue(repository.Repository.ID, 1))
	require.NoError(d.InsertWorkspace(ctx, &Workspace{
		ID: "retired-workspace", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "org-a", RepoName: "project-a",
		ItemType: WorkspaceItemTypePullRequest, ItemNumber: 7,
		WorktreePath: "/tmp/retired-workspace", TmuxSession: "retired-workspace",
	}))

	deactivated, err := d.DeactivateRepository(ctx, githubRepositoryIdentity(1001))
	require.NoError(err)
	require.NotNil(deactivated)
	assert.Equal(repository.Repository.ID, deactivated.Repository.ID)
	assert.Equal(RepositoryLifecycleInactive, deactivated.Lifecycle)
	assert.Equal("org-a/project-a", deactivated.Repository.RepoPath)

	active, err := d.ResolveActiveRepositoryRoute(ctx, githubRoute("org-a", "project-a"))
	require.NoError(err)
	assert.Nil(active)
	var issueRepoID int64
	require.NoError(d.ReadDB().QueryRowContext(ctx,
		`SELECT repo_id FROM forge_issues WHERE number = 1`,
	).Scan(&issueRepoID))
	assert.Equal(repository.Repository.ID, issueRepoID)
	workspace, err := d.GetWorkspace(ctx, "retired-workspace")
	require.NoError(err)
	require.NotNil(workspace)
	assert.Equal(repository.Repository.ID, workspace.RepoID)
	assert.Equal("project-a", workspace.RepoName)

	unknown, err := d.DeactivateRepository(ctx, githubRepositoryIdentity(9999))
	require.NoError(err)
	assert.Nil(unknown)
	_, err = d.DeactivateRepository(ctx, platform.RepositoryIdentity{
		Provider: "github", PlatformHost: "github.com",
	})
	require.Error(err)
}

func TestRepositoryRouteHasOtherRepository(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	originalID, replacementID := seedRepositoryRouteReplacement(t, d)
	solo := observeCatalogRepository(t, d, 1003, "org-a", "project-solo")

	for name, tc := range map[string]struct {
		route  RepoIdentity
		repoID int64
		want   bool
	}{
		"active holder sees displaced row": {
			route: githubRoute("org-a", "project-a"), repoID: replacementID, want: true,
		},
		"displaced row sees active holder": {
			route: githubRoute("Org-A", "Project-A"), repoID: originalID, want: true,
		},
		"sole holder": {
			route: githubRoute("org-a", "project-solo"), repoID: solo.Repository.ID,
		},
		"unused route": {
			route: githubRoute("org-a", "project-unused"), repoID: solo.Repository.ID,
		},
	} {
		shared, err := d.RepositoryRouteHasOtherRepository(ctx, tc.route, tc.repoID)
		require.NoError(err, name)
		assert.Equal(tc.want, shared, name)
	}
	_, err := d.RepositoryRouteHasOtherRepository(ctx, githubRoute("org-a", "project-a"), 0)
	require.Error(err)
}

func TestUpdateRepoProviderObservationPersistsViewerOnlySnapshot(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	entry := observeCatalogRepository(t, d, 1001, "acme", "widget")
	require.NoError(d.UpdateRepoMergeSettings(ctx, entry.Repository.ID, true, false, true))

	require.NoError(d.UpdateRepoProviderObservation(
		ctx, entry.Repository.ID, RepoProviderMetadata{DefaultBranch: "main"}, nil, new(false),
	))

	stored, err := d.GetRepoByID(ctx, entry.Repository.ID)
	require.NoError(err)
	require.NotNil(stored)
	assert.False(stored.ViewerCanMerge)
	assert.True(stored.AllowSquashMerge)
	assert.False(stored.AllowMergeCommit)
	assert.True(stored.AllowRebaseMerge)
	assert.Equal("main", stored.DefaultBranch)
}

func TestUpdateRepoProviderObservationPreservesOmittedFields(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	entry := observeCatalogRepository(t, d, 1001, "acme", "widget")
	require.NoError(d.UpdateRepoProviderObservation(ctx, entry.Repository.ID, RepoProviderMetadata{
		WebURL:        "https://github.com/acme/widget",
		CloneURL:      "/fixtures/acme/widget.git",
		DefaultBranch: "main",
	}, nil, new(true)))

	require.NoError(d.UpdateRepoProviderObservation(
		ctx, entry.Repository.ID, RepoProviderMetadata{},
		&RepoMergeSettings{AllowSquashMerge: true}, nil,
	))

	stored, err := d.GetRepoByID(ctx, entry.Repository.ID)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("https://github.com/acme/widget", stored.WebURL)
	assert.Equal("/fixtures/acme/widget.git", stored.CloneURL)
	assert.Equal("main", stored.DefaultBranch)
	assert.True(stored.AllowSquashMerge)
	assert.False(stored.AllowMergeCommit)
	assert.True(stored.ViewerCanMerge, "an omitted viewer permission keeps the known value")
}

// insertPendingGitHubRepository writes a GitHub repository in the shape
// migration 60 leaves it: inactive, keyed only by its GraphQL node ID.
func insertPendingGitHubRepository(t *testing.T, d *DB, nodeID, owner, name string) int64 {
	t.Helper()
	result, err := d.WriteDB().ExecContext(t.Context(), `
		INSERT INTO forge_repos (
			platform, platform_host, platform_repo_id, github_node_id,
			owner, name, repo_path, owner_key, name_key, repo_path_key,
			lifecycle_state
		) VALUES ('github', 'github.com', 0, ?, ?, ?, ?, ?, ?, ?, 'inactive')`,
		nodeID, owner, name, owner+"/"+name, owner, name, owner+"/"+name,
	)
	require.NoError(t, err)
	id, err := result.LastInsertId()
	require.NoError(t, err)
	return id
}

func insertLaunchSpecForTest(t *testing.T, d *DB, workspaceID string, repoID int64, specJSON string) {
	t.Helper()
	_, err := d.WriteDB().ExecContext(t.Context(), `
		INSERT INTO forge_workspaces (
			id, platform, platform_host, repo_owner, repo_name,
			repo_owner_key, repo_name_key, repo_path_key, repo_id,
			item_type, item_number, item_key, git_head_ref,
			workspace_branch, worktree_path, tmux_session, status
		) VALUES (?, 'github', 'github.com', 'org-a', 'project-a',
			'org-a', 'project-a', 'org-a/project-a', ?,
			'pull_request', 5, '5', 'feature/five',
			'feature/five', '/tmp/' || ?, ?, 'ready')`,
		workspaceID, repoID, workspaceID, workspaceID,
	)
	require.NoError(t, err)
	_, err = d.WriteDB().ExecContext(t.Context(), `
		INSERT INTO forge_workspace_launch_specs (
			workspace_id, version, spec_json, source_visible_until, created_at
		) VALUES (?, 1, ?, datetime('now'), datetime('now'))`,
		workspaceID, specJSON,
	)
	require.NoError(t, err)
}

func TestObserveRepositoryWaitsForPendingGitHubConversion(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	pendingID := insertPendingGitHubRepository(t, d, "R_kgDOexample", "org-a", "project-a")

	_, err := d.ObserveRepository(t.Context(), RepoIdentity{
		Platform: "github", PlatformHost: "github.com", PlatformRepoID: 1001,
		Owner: "org-a", Name: "project-a",
	})
	require.ErrorIs(err, ErrGitHubRepositoryConversionPending)
	_, err = d.ObserveRepository(t.Context(), RepoIdentity{
		Platform: "github", PlatformHost: "ghe.example.com", PlatformRepoID: 1001,
		Owner: "org-a", Name: "project-a",
	})
	require.NoError(err, "other hosts are not blocked")
	_, err = d.ObserveRepository(t.Context(), RepoIdentity{
		Platform: "gitlab", PlatformHost: "github.com", PlatformRepoID: 1001,
		Owner: "org-a", Name: "project-a",
	})
	require.NoError(err, "other providers are not blocked")

	require.NoError(d.CompleteGitHubRepositoryConversion(t.Context(), pendingID, 1001))
	entry := observeCatalogRepository(t, d, 1001, "org-a", "project-a")
	require.Equal(pendingID, entry.Repository.ID, "the converted row is observed, not duplicated")
	require.Equal(RepositoryLifecycleActive, entry.Lifecycle)
}

func TestCompleteGitHubRepositoryConversionRecordsIntegerID(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	pendingID := insertPendingGitHubRepository(t, d, "R_kgDOexample", "org-a", "project-a")
	otherID := insertPendingGitHubRepository(t, d, "R_kgDOother", "org-a", "project-b")
	// Migration 60 zeroes the node IDs launch specifications embedded.
	insertLaunchSpecForTest(t, d, "ws-converted", pendingID, `{
		"repository":{"provider":"github","platform_host":"github.com","platform_repo_id":0},
		"pull":{"base_repo_id":0}}`)
	insertLaunchSpecForTest(t, d, "ws-other", otherID, `{
		"repository":{"provider":"github","platform_host":"github.com","platform_repo_id":0},
		"pull":{"base_repo_id":0}}`)

	pending, err := d.ListPendingGitHubRepositories(ctx)
	require.NoError(err)
	assert.Equal([]PendingGitHubRepository{
		{RepoID: pendingID, PlatformHost: "github.com", Owner: "org-a", Name: "project-a", NodeID: "R_kgDOexample"},
		{RepoID: otherID, PlatformHost: "github.com", Owner: "org-a", Name: "project-b", NodeID: "R_kgDOother"},
	}, pending)

	require.NoError(d.CompleteGitHubRepositoryConversion(ctx, pendingID, 1001))

	entry, err := d.GetRepositoryByProviderID(ctx, githubRepositoryIdentity(1001))
	require.NoError(err)
	require.NotNil(entry)
	assert.Equal(pendingID, entry.Repository.ID)
	assert.Equal(RepositoryLifecycleInactive, entry.Lifecycle,
		"conversion alone does not reactivate; the next observation does")

	readSpecIDs := func(workspaceID string) (any, any) {
		var repoID, baseRepoID any
		require.NoError(d.ReadDB().QueryRowContext(ctx, `
			SELECT json_extract(spec_json, '$.repository.platform_repo_id'),
			       json_extract(spec_json, '$.pull.base_repo_id')
			FROM forge_workspace_launch_specs WHERE workspace_id = ?`, workspaceID,
		).Scan(&repoID, &baseRepoID))
		return repoID, baseRepoID
	}
	repoID, baseRepoID := readSpecIDs("ws-converted")
	assert.EqualValues(1001, repoID)
	assert.EqualValues(1001, baseRepoID)
	repoID, baseRepoID = readSpecIDs("ws-other")
	assert.EqualValues(0, repoID, "other repositories' specifications wait for their own conversion")
	assert.EqualValues(0, baseRepoID)

	pending, err = d.ListPendingGitHubRepositories(ctx)
	require.NoError(err)
	require.Len(pending, 1)
	assert.Equal(otherID, pending[0].RepoID)
}

func TestCompleteGitHubRepositoryConversionUnresolvableNode(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	pendingID := insertPendingGitHubRepository(t, d, "R_kgDOgone", "org-a", "project-a")
	insertTestIssueWithOptions(t, d, testIssue(pendingID, 1))

	require.NoError(d.CompleteGitHubRepositoryConversion(ctx, pendingID, 0))

	pending, err := d.ListPendingGitHubRepositories(ctx)
	require.NoError(err)
	assert.Empty(pending)
	repo, err := d.GetRepoByID(ctx, pendingID)
	require.NoError(err)
	require.NotNil(repo, "an unresolvable repository keeps its row and history")
	assert.Zero(repo.PlatformRepoID)
	var lifecycle string
	require.NoError(d.ReadDB().QueryRowContext(ctx,
		`SELECT lifecycle_state FROM forge_repos WHERE id = ?`, pendingID,
	).Scan(&lifecycle))
	assert.Equal("inactive", lifecycle)

	// The host is no longer blocked, and a new repository at the old route
	// gets its own row.
	entry := observeCatalogRepository(t, d, 1001, "org-a", "project-a")
	assert.NotEqual(pendingID, entry.Repository.ID)
	var issueRepoID sql.NullInt64
	require.NoError(d.ReadDB().QueryRowContext(ctx,
		`SELECT repo_id FROM forge_issues WHERE number = 1`,
	).Scan(&issueRepoID))
	assert.Equal(pendingID, issueRepoID.Int64)

	require.ErrorContains(d.CompleteGitHubRepositoryConversion(ctx, pendingID, -1), "negative")
}
