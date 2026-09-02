package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/providerplane"
)

type unavailableLaunchSpecResolver struct{}

type staticLaunchSpecResolver struct {
	spec         db.WorkspaceLaunchSpec
	refreshCalls int
}

func (r *staticLaunchSpecResolver) ResolveWorkspaceLaunchSpec(
	context.Context, providerplane.WorkspaceLaunchRequest,
) (db.WorkspaceLaunchSpec, error) {
	return r.spec, nil
}

func (r *staticLaunchSpecResolver) RefreshWorkspaceLaunchSpec(
	context.Context, db.WorkspaceLaunchSpec,
) (db.WorkspaceLaunchSpec, error) {
	r.refreshCalls++
	return r.spec, nil
}

func (unavailableLaunchSpecResolver) ResolveWorkspaceLaunchSpec(
	context.Context, providerplane.WorkspaceLaunchRequest,
) (db.WorkspaceLaunchSpec, error) {
	return db.WorkspaceLaunchSpec{}, providerplane.ErrHubUnavailable
}

func (unavailableLaunchSpecResolver) RefreshWorkspaceLaunchSpec(
	context.Context, db.WorkspaceLaunchSpec,
) (db.WorkspaceLaunchSpec, error) {
	return db.WorkspaceLaunchSpec{}, providerplane.ErrHubUnavailable
}

func launchSpecForTest() WorkspaceLaunchSpec {
	issuedAt := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	return WorkspaceLaunchSpec{
		Version: WorkspaceLaunchSpecVersion,
		Repository: WorkspaceLaunchRepository{
			Provider: "github", PlatformHost: "github.com",
			PlatformRepoID: "repo-1", Owner: "acme", Name: "widget",
			CloneURL: "https://github.com/acme/widget.git", DefaultBranch: "main",
		},
		ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 7,
		ItemKey: "7", GitHeadRef: "feature/seven",
		Pull: &WorkspaceLaunchPull{
			HeadBranch: "feature/seven", HeadRepoKind: "same_repo",
			SnapshotRevision: 3,
		},
		SourceVisible: true, IssuedAt: issuedAt,
		SourceVisibleUntil: issuedAt.Add(WorkspaceLaunchSpecVisibilityLease),
	}
}

func seedLaunchSpecRepository(
	t *testing.T, database *db.DB, spec WorkspaceLaunchSpec,
) int64 {
	t.Helper()
	repoID, err := database.UpsertRepo(t.Context(), db.RepoIdentity{
		Platform: spec.Repository.Provider, PlatformHost: spec.Repository.PlatformHost,
		PlatformRepoID: spec.Repository.PlatformRepoID,
		Owner:          spec.Repository.Owner, Name: spec.Repository.Name,
	})
	require.NoError(t, err)
	return repoID
}

func TestWorkspaceLaunchSpecVisibilityLeaseBoundary(t *testing.T) {
	spec := launchSpecForTest()
	require.NoError(t, spec.Validate())
	assert.NoError(t, spec.RequireVisible(spec.SourceVisibleUntil.Add(-time.Nanosecond)))
	assert.ErrorIs(t, spec.RequireVisible(spec.SourceVisibleUntil), ErrLaunchSpecRefreshRequired)
}

func TestWorkspaceLaunchSpecValidatesHeadRepositorySemantics(t *testing.T) {
	tests := []struct {
		name string
		edit func(*WorkspaceLaunchSpec)
	}{
		{name: "wrong version", edit: func(spec *WorkspaceLaunchSpec) { spec.Version++ }},
		{name: "missing stable repository id", edit: func(spec *WorkspaceLaunchSpec) { spec.Repository.PlatformRepoID = "" }},
		{name: "fork without clone url", edit: func(spec *WorkspaceLaunchSpec) { spec.Pull.HeadRepoKind = "fork" }},
		{name: "same repository with clone url", edit: func(spec *WorkspaceLaunchSpec) { spec.Pull.HeadRepoCloneURL = "https://example.test/fork.git" }},
		{name: "wrong lease", edit: func(spec *WorkspaceLaunchSpec) { spec.SourceVisibleUntil = spec.SourceVisibleUntil.Add(time.Second) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := launchSpecForTest()
			test.edit(&spec)
			assert.Error(t, spec.Validate())
		})
	}

	fork := launchSpecForTest()
	fork.Pull.HeadRepoKind = "fork"
	fork.Pull.HeadRepoCloneURL = "https://github.com/contributor/widget.git"
	require.NoError(t, fork.Validate())

	unknown := launchSpecForTest()
	unknown.Pull.HeadRepoKind = "unknown"
	require.NoError(t, unknown.Validate())
}

func TestLaunchSpecLeaseExpiredHubUnavailableIsRetryable(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	spec := launchSpecForTest()
	seedLaunchSpecRepository(t, database, spec)
	workspace := &db.Workspace{
		ID: "ws-expired-launch", Platform: spec.Repository.Provider,
		PlatformHost: spec.Repository.PlatformHost,
		RepoOwner:    spec.Repository.Owner, RepoName: spec.Repository.Name,
		ItemType: spec.ItemType, ItemNumber: spec.ItemNumber,
		ItemKey: spec.ItemKey, GitHeadRef: spec.GitHeadRef,
		WorkspaceBranch: "kenn-forge/pr-7", WorktreePath: t.TempDir(),
		TmuxSession: "forge-ws-expired-launch", Status: "creating",
	}
	require.NoError(database.InsertWorkspace(t.Context(), workspace))
	require.NoError(database.PutWorkspaceLaunchSpec(t.Context(), workspace.ID, spec))

	manager := NewManager(database, t.TempDir())
	manager.SetNow(func() time.Time {
		return spec.SourceVisibleUntil.Add(-time.Nanosecond)
	})
	resolved, err := manager.RequireWorkspaceLaunchSpec(t.Context(), workspace)
	require.NoError(err)
	assert.Equal(spec, *resolved)

	manager.SetNow(func() time.Time { return spec.SourceVisibleUntil })
	manager.SetLaunchSpecResolver(unavailableLaunchSpecResolver{})
	_, err = manager.RequireWorkspaceLaunchSpec(t.Context(), workspace)
	require.Error(err)
	require.ErrorIs(err, ErrLaunchSpecRefreshRequired)
	require.ErrorIs(err, providerplane.ErrHubUnavailable)
	assert.True(LaunchSpecErrorRetryable(err))

	adHoc := &db.Workspace{ItemType: db.WorkspaceItemTypeAdHoc}
	resolved, err = manager.RequireWorkspaceLaunchSpec(t.Context(), adHoc)
	require.NoError(err)
	assert.Nil(resolved)
}

func TestMissingWorkspaceLaunchSpecIsResolvedAndPersisted(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	spec := launchSpecForTest()
	seedLaunchSpecRepository(t, database, spec)
	workspace := &db.Workspace{
		ID: "ws-missing-launch", Platform: spec.Repository.Provider,
		PlatformHost: spec.Repository.PlatformHost,
		RepoOwner:    spec.Repository.Owner, RepoName: spec.Repository.Name,
		ItemType: spec.ItemType, ItemNumber: spec.ItemNumber,
		ItemKey: spec.ItemKey, GitHeadRef: spec.GitHeadRef,
		WorkspaceBranch: "kenn-forge/pr-7", WorktreePath: t.TempDir(),
		TmuxSession: "forge-ws-missing-launch", Status: "creating",
	}
	require.NoError(database.InsertWorkspace(t.Context(), workspace))
	resolver := &staticLaunchSpecResolver{spec: spec}
	manager := NewManager(database, t.TempDir())
	manager.SetLaunchSpecResolver(resolver)
	manager.SetNow(func() time.Time { return spec.IssuedAt })

	resolved, err := manager.RequireWorkspaceLaunchSpec(t.Context(), workspace)
	require.NoError(err)
	assert.Equal(t, spec, *resolved)
	assert.Equal(t, 1, resolver.refreshCalls)
	persisted, err := database.GetWorkspaceLaunchSpec(t.Context(), workspace.ID)
	require.NoError(err)
	require.NotNil(persisted)
	assert.Equal(t, spec, *persisted)
}

func TestProviderBackedSetupDoesNotFallBackToAnonymousGit(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	spec := launchSpecForTest()
	seedLaunchSpecRepository(t, database, spec)
	workspace := &db.Workspace{
		ID: "ws-required-setup-credential", Platform: spec.Repository.Provider,
		PlatformHost: spec.Repository.PlatformHost,
		RepoOwner:    spec.Repository.Owner, RepoName: spec.Repository.Name,
		ItemType: spec.ItemType, ItemNumber: spec.ItemNumber,
		ItemKey: spec.ItemKey, GitHeadRef: spec.GitHeadRef,
		WorktreePath: filepath.Join(t.TempDir(), "workspace"),
		TmuxSession:  "forge-ws-required-setup-credential", Status: "creating",
	}
	require.NoError(database.InsertWorkspace(t.Context(), workspace))
	require.NoError(database.PutWorkspaceLaunchSpec(t.Context(), workspace.ID, spec))
	fakeGitDir := t.TempDir()
	require.NoError(os.WriteFile(
		filepath.Join(fakeGitDir, "git"), []byte("#!/bin/sh\nexit 1\n"), 0o755,
	))
	t.Setenv("PATH", fakeGitDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	manager := NewManager(database, t.TempDir())
	manager.SetNow(func() time.Time { return spec.IssuedAt })
	manager.SetClones(gitclone.New(t.TempDir(), nil))
	manager.SetRequireProviderCredential(true)

	err := manager.Setup(t.Context(), workspace)

	require.ErrorIs(err, gitclone.ErrCredentialUnavailable)
}

func TestRefreshWorkspaceLaunchSpecRenewsFreshProjection(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	current := launchSpecForTest()
	seedLaunchSpecRepository(t, database, current)
	workspace := &db.Workspace{
		ID: "ws-manual-refresh", Platform: current.Repository.Provider,
		PlatformHost: current.Repository.PlatformHost,
		RepoOwner:    current.Repository.Owner, RepoName: current.Repository.Name,
		ItemType: current.ItemType, ItemNumber: current.ItemNumber,
		ItemKey: current.ItemKey, GitHeadRef: current.GitHeadRef,
		WorkspaceBranch: "kenn-forge/pr-7", WorktreePath: t.TempDir(),
		TmuxSession: "forge-ws-manual-refresh", Status: "ready",
	}
	require.NoError(database.InsertWorkspace(t.Context(), workspace))
	require.NoError(database.PutWorkspaceLaunchSpec(t.Context(), workspace.ID, current))

	refreshed := current
	refreshed.SourceTitle = "Updated title"
	refreshed.IssuedAt = current.IssuedAt.Add(time.Minute)
	refreshed.SourceVisibleUntil = refreshed.IssuedAt.Add(WorkspaceLaunchSpecVisibilityLease)
	resolver := &staticLaunchSpecResolver{spec: refreshed}
	manager := NewManager(database, t.TempDir())
	manager.SetLaunchSpecResolver(resolver)
	manager.SetNow(func() time.Time { return refreshed.IssuedAt })

	got, err := manager.RefreshWorkspaceLaunchSpec(t.Context(), workspace)
	require.NoError(err)
	assert.Equal(t, refreshed, *got)
	assert.Equal(t, 1, resolver.refreshCalls)
	persisted, err := database.GetWorkspaceLaunchSpec(t.Context(), workspace.ID)
	require.NoError(err)
	require.NotNil(persisted)
	assert.Equal(t, refreshed, *persisted)
}

func TestRefreshWorkspaceLaunchSpecAdoptsVerifiedRepositoryRename(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := openTestDB(t)
	current := launchSpecForTest()
	observedAt := current.IssuedAt.Add(-time.Minute)
	_, accepted, err := database.ReconcileRepositoryObservation(
		t.Context(), db.RepoIdentity{
			Platform: current.Repository.Provider, PlatformHost: current.Repository.PlatformHost,
			PlatformRepoID: current.Repository.PlatformRepoID,
			Owner:          current.Repository.Owner, Name: current.Repository.Name,
		}, observedAt,
	)
	require.NoError(err)
	require.True(accepted)

	workspace := &db.Workspace{
		ID: "ws-renamed-launch", Platform: current.Repository.Provider,
		PlatformHost: current.Repository.PlatformHost,
		RepoOwner:    current.Repository.Owner, RepoName: current.Repository.Name,
		ItemType: current.ItemType, ItemNumber: current.ItemNumber,
		ItemKey: current.ItemKey, GitHeadRef: current.GitHeadRef,
		WorkspaceBranch: "kenn-forge/pr-7", WorktreePath: t.TempDir(),
		TmuxSession: "forge-ws-renamed-launch", Status: "ready",
	}
	require.NoError(database.InsertWorkspace(t.Context(), workspace))
	require.NoError(database.PutWorkspaceLaunchSpec(t.Context(), workspace.ID, current))

	refreshed := current
	refreshed.Repository.Owner = "acme-renamed"
	refreshed.Repository.Name = "widget-renamed"
	refreshed.Repository.CloneURL = "https://github.com/acme-renamed/widget-renamed.git"
	refreshed.IssuedAt = current.IssuedAt.Add(time.Minute)
	refreshed.SourceVisibleUntil = refreshed.IssuedAt.Add(WorkspaceLaunchSpecVisibilityLease)
	_, accepted, err = database.ReconcileRepositoryObservation(
		t.Context(), db.RepoIdentity{
			Platform: refreshed.Repository.Provider, PlatformHost: refreshed.Repository.PlatformHost,
			PlatformRepoID: refreshed.Repository.PlatformRepoID,
			Owner:          refreshed.Repository.Owner, Name: refreshed.Repository.Name,
		}, refreshed.IssuedAt,
	)
	require.NoError(err)
	require.True(accepted)

	manager := NewManager(database, t.TempDir())
	manager.SetLaunchSpecResolver(&staticLaunchSpecResolver{spec: refreshed})
	manager.SetNow(func() time.Time { return refreshed.IssuedAt })

	got, err := manager.RefreshWorkspaceLaunchSpec(t.Context(), workspace)
	require.NoError(err)
	assert.Equal(refreshed, *got)
	assert.Equal("acme-renamed", workspace.RepoOwner)
	assert.Equal("widget-renamed", workspace.RepoName)

	persistedWorkspace, err := database.GetWorkspace(t.Context(), workspace.ID)
	require.NoError(err)
	require.NotNil(persistedWorkspace)
	assert.Equal("acme-renamed", persistedWorkspace.RepoOwner)
	assert.Equal("widget-renamed", persistedWorkspace.RepoName)
	persistedSpec, err := database.GetWorkspaceLaunchSpec(t.Context(), workspace.ID)
	require.NoError(err)
	require.NotNil(persistedSpec)
	assert.Equal(refreshed, *persistedSpec)
}

func TestRequireWorkspaceLaunchSpecRefreshesVerifiedRepositoryRename(t *testing.T) {
	for _, expired := range []bool{false, true} {
		name := "valid lease"
		workspaceID := "ws-require-rename-valid"
		if expired {
			name = "expired lease"
			workspaceID = "ws-require-rename-expired"
		}
		t.Run(name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			database := openTestDB(t)
			current := launchSpecForTest()
			_, accepted, err := database.ReconcileRepositoryObservation(
				t.Context(), db.RepoIdentity{
					Platform: current.Repository.Provider, PlatformHost: current.Repository.PlatformHost,
					PlatformRepoID: current.Repository.PlatformRepoID,
					Owner:          current.Repository.Owner, Name: current.Repository.Name,
				}, current.IssuedAt,
			)
			require.NoError(err)
			require.True(accepted)

			workspace := &db.Workspace{
				ID: workspaceID, Platform: current.Repository.Provider,
				PlatformHost: current.Repository.PlatformHost,
				RepoOwner:    current.Repository.Owner, RepoName: current.Repository.Name,
				ItemType: current.ItemType, ItemNumber: current.ItemNumber,
				ItemKey: current.ItemKey, GitHeadRef: current.GitHeadRef,
				WorkspaceBranch: "kenn-forge/pr-7", WorktreePath: t.TempDir(),
				TmuxSession: "forge-ws-require-rename", Status: "ready",
			}
			require.NoError(database.InsertWorkspace(t.Context(), workspace))
			require.NoError(database.PutWorkspaceLaunchSpec(t.Context(), workspace.ID, current))

			now := current.IssuedAt.Add(time.Minute)
			if expired {
				now = current.SourceVisibleUntil
			}
			refreshed := current
			refreshed.Repository.Owner = "acme-renamed"
			refreshed.Repository.Name = "widget-renamed"
			refreshed.Repository.CloneURL = "https://github.com/acme-renamed/widget-renamed.git"
			refreshed.IssuedAt = now
			refreshed.SourceVisibleUntil = now.Add(WorkspaceLaunchSpecVisibilityLease)
			_, accepted, err = database.ReconcileRepositoryObservation(
				t.Context(), db.RepoIdentity{
					Platform: refreshed.Repository.Provider, PlatformHost: refreshed.Repository.PlatformHost,
					PlatformRepoID: refreshed.Repository.PlatformRepoID,
					Owner:          refreshed.Repository.Owner, Name: refreshed.Repository.Name,
				}, current.IssuedAt.Add(time.Minute),
			)
			require.NoError(err)
			require.True(accepted)

			resolver := &staticLaunchSpecResolver{spec: refreshed}
			manager := NewManager(database, t.TempDir())
			manager.SetLaunchSpecResolver(resolver)
			manager.SetNow(func() time.Time { return now })

			got, err := manager.RequireWorkspaceLaunchSpec(t.Context(), workspace)
			require.NoError(err)
			assert.Equal(refreshed, *got)
			assert.Equal(1, resolver.refreshCalls)
			assert.Equal(refreshed.Repository.Owner, workspace.RepoOwner)
			assert.Equal(refreshed.Repository.Name, workspace.RepoName)
			persisted, err := database.GetWorkspaceLaunchSpec(t.Context(), workspace.ID)
			require.NoError(err)
			require.NotNil(persisted)
			assert.Equal(refreshed, *persisted)
		})
	}
}

func TestCreateFromLaunchSpecDedupesRenamedRepositoryByStableIdentity(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := openTestDB(t)
	original := launchSpecForTest()
	_, accepted, err := database.ReconcileRepositoryObservation(
		t.Context(), db.RepoIdentity{
			Platform: original.Repository.Provider, PlatformHost: original.Repository.PlatformHost,
			PlatformRepoID: original.Repository.PlatformRepoID,
			Owner:          original.Repository.Owner, Name: original.Repository.Name,
		}, original.IssuedAt,
	)
	require.NoError(err)
	require.True(accepted)

	now := original.IssuedAt
	manager := NewManager(database, t.TempDir())
	manager.SetNow(func() time.Time { return now })
	created, err := manager.CreateFromLaunchSpec(t.Context(), original)
	require.NoError(err)

	renamed := original
	renamed.Repository.Owner = "acme-renamed"
	renamed.Repository.Name = "widget-renamed"
	renamed.Repository.CloneURL = "https://github.com/acme-renamed/widget-renamed.git"
	renamed.IssuedAt = original.IssuedAt.Add(time.Minute)
	renamed.SourceVisibleUntil = renamed.IssuedAt.Add(WorkspaceLaunchSpecVisibilityLease)
	now = renamed.IssuedAt
	_, accepted, err = database.ReconcileRepositoryObservation(
		t.Context(), db.RepoIdentity{
			Platform: renamed.Repository.Provider, PlatformHost: renamed.Repository.PlatformHost,
			PlatformRepoID: renamed.Repository.PlatformRepoID,
			Owner:          renamed.Repository.Owner, Name: renamed.Repository.Name,
		}, now,
	)
	require.NoError(err)
	require.True(accepted)

	_, err = manager.CreateFromLaunchSpec(t.Context(), renamed)
	require.ErrorIs(err, ErrWorkspaceDuplicate)
	persisted, err := database.GetWorkspace(t.Context(), created.ID)
	require.NoError(err)
	require.NotNil(persisted)
	assert.Equal(renamed.Repository.Owner, persisted.RepoOwner)
	assert.Equal(renamed.Repository.Name, persisted.RepoName)
	summaries, err := database.ListWorkspaceSummaries(t.Context())
	require.NoError(err)
	assert.Len(summaries, 1)
}

func TestProviderWorkspaceCreationKeepsDisplacedRouteOwner(t *testing.T) {
	for _, itemType := range []string{
		db.WorkspaceItemTypePullRequest,
		db.WorkspaceItemTypeIssue,
	} {
		t.Run(itemType, func(t *testing.T) {
			require := require.New(t)
			database := openTestDB(t)
			original := launchSpecForTest()
			original.ItemType = itemType
			if itemType == db.WorkspaceItemTypeIssue {
				original.Pull = nil
				original.GitHeadRef = "kenn-forge/issue-7"
			}
			observedAt := original.IssuedAt.Add(-time.Minute)
			originalEntry, _, err := database.ReconcileRepositoryObservation(
				t.Context(), db.RepoIdentity{
					Platform: original.Repository.Provider, PlatformHost: original.Repository.PlatformHost,
					PlatformRepoID: original.Repository.PlatformRepoID,
					Owner:          original.Repository.Owner, Name: original.Repository.Name,
				}, observedAt,
			)
			require.NoError(err)
			require.NotNil(originalEntry)

			manager := NewManager(database, t.TempDir())
			manager.SetNow(func() time.Time { return original.IssuedAt })
			var originalWorkspace *Workspace
			if itemType == db.WorkspaceItemTypePullRequest {
				originalWorkspace, err = manager.CreateFromLaunchSpec(t.Context(), original)
			} else {
				originalWorkspace, err = manager.CreateIssueFromLaunchSpec(
					t.Context(), original, CreateIssueOptions{},
				)
			}
			require.NoError(err)

			_, _, err = database.ReconcileRepositoryObservation(
				t.Context(), db.RepoIdentity{
					Platform: original.Repository.Provider, PlatformHost: original.Repository.PlatformHost,
					PlatformRepoID: original.Repository.PlatformRepoID,
					Owner:          original.Repository.Owner, Name: "moved-away",
				}, observedAt.Add(time.Minute),
			)
			require.NoError(err)
			replacement := original
			replacement.Repository.PlatformRepoID = "repo-replacement"
			replacement.IssuedAt = original.IssuedAt.Add(time.Minute)
			replacement.SourceVisibleUntil = replacement.IssuedAt.Add(WorkspaceLaunchSpecVisibilityLease)
			replacementEntry, _, err := database.ReconcileRepositoryObservation(
				t.Context(), db.RepoIdentity{
					Platform: replacement.Repository.Provider, PlatformHost: replacement.Repository.PlatformHost,
					PlatformRepoID: replacement.Repository.PlatformRepoID,
					Owner:          replacement.Repository.Owner, Name: replacement.Repository.Name,
				}, observedAt.Add(2*time.Minute),
			)
			require.NoError(err)
			require.NotNil(replacementEntry)
			manager.SetNow(func() time.Time { return replacement.IssuedAt })

			var replacementWorkspace *Workspace
			if itemType == db.WorkspaceItemTypePullRequest {
				replacementWorkspace, err = manager.CreateFromLaunchSpec(t.Context(), replacement)
			} else {
				replacementWorkspace, err = manager.CreateIssueFromLaunchSpec(
					t.Context(), replacement, CreateIssueOptions{},
				)
			}
			require.NoError(err)
			require.NotNil(replacementWorkspace)
			require.Contains(originalWorkspace.WorktreePath, fmt.Sprintf("repo-%d", originalEntry.Repository.ID))
			require.Contains(replacementWorkspace.WorktreePath, fmt.Sprintf("repo-%d", replacementEntry.Repository.ID))
			require.NotEqual(originalWorkspace.WorktreePath, replacementWorkspace.WorktreePath)

			persistedOriginal, err := database.GetWorkspace(t.Context(), originalWorkspace.ID)
			require.NoError(err)
			require.Equal(originalWorkspace.WorktreePath, persistedOriginal.WorktreePath)
		})
	}
}

func TestRequireWorkspaceLaunchSpecRefreshesCurrentRouteAfterRepositoryRename(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	original := launchSpecForTest()
	entry, _, err := database.ReconcileRepositoryObservation(
		t.Context(), db.RepoIdentity{
			Platform: original.Repository.Provider, PlatformHost: original.Repository.PlatformHost,
			PlatformRepoID: original.Repository.PlatformRepoID,
			Owner:          original.Repository.Owner, Name: original.Repository.Name,
		}, original.IssuedAt.Add(-time.Minute),
	)
	require.NoError(err)
	require.NotNil(entry)

	manager := NewManager(database, t.TempDir())
	manager.SetNow(func() time.Time { return original.IssuedAt })
	workspace, err := manager.CreateFromLaunchSpec(t.Context(), original)
	require.NoError(err)

	renamed := original
	renamed.Repository.Owner = "acme-renamed"
	renamed.Repository.Name = "widget-renamed"
	renamed.Repository.CloneURL = "https://github.com/acme-renamed/widget-renamed.git"
	renamed.IssuedAt = original.IssuedAt.Add(time.Minute)
	renamed.SourceVisibleUntil = renamed.IssuedAt.Add(WorkspaceLaunchSpecVisibilityLease)
	_, _, err = database.ReconcileRepositoryObservation(
		t.Context(), db.RepoIdentity{
			Platform: renamed.Repository.Provider, PlatformHost: renamed.Repository.PlatformHost,
			PlatformRepoID: renamed.Repository.PlatformRepoID,
			Owner:          renamed.Repository.Owner, Name: renamed.Repository.Name,
		}, renamed.IssuedAt,
	)
	require.NoError(err)

	workspace, err = database.GetWorkspace(t.Context(), workspace.ID)
	require.NoError(err)
	require.Equal(renamed.Repository.Owner, workspace.RepoOwner)
	require.Equal(renamed.Repository.Name, workspace.RepoName)
	manager.SetLaunchSpecResolver(&staticLaunchSpecResolver{spec: renamed})
	manager.SetNow(func() time.Time { return renamed.IssuedAt })

	resolved, err := manager.RequireWorkspaceLaunchSpec(t.Context(), workspace)
	require.NoError(err)
	require.Equal(renamed, *resolved)
	persisted, err := database.GetWorkspaceLaunchSpec(t.Context(), workspace.ID)
	require.NoError(err)
	require.Equal(renamed, *persisted)
}

func TestProviderWorkspaceCreationAllowsCurrentRepositoryOnReusedRoute(t *testing.T) {
	for _, itemType := range []string{
		db.WorkspaceItemTypePullRequest,
		db.WorkspaceItemTypeIssue,
	} {
		t.Run(itemType, func(t *testing.T) {
			require := require.New(t)
			database := openTestDB(t)
			spec := launchSpecForTest()
			spec.ItemType = itemType
			if itemType == db.WorkspaceItemTypeIssue {
				spec.Pull = nil
				spec.GitHeadRef = "kenn-forge/issue-7"
			}
			observedAt := spec.IssuedAt.Add(-2 * time.Minute)
			_, _, err := database.ReconcileRepositoryObservation(
				t.Context(), db.RepoIdentity{
					Platform: spec.Repository.Provider, PlatformHost: spec.Repository.PlatformHost,
					PlatformRepoID: "displaced-repository", Owner: spec.Repository.Owner,
					Name: spec.Repository.Name,
				}, observedAt,
			)
			require.NoError(err)
			_, _, err = database.ReconcileRepositoryObservation(
				t.Context(), db.RepoIdentity{
					Platform: spec.Repository.Provider, PlatformHost: spec.Repository.PlatformHost,
					PlatformRepoID: "displaced-repository", Owner: "acme",
					Name: "moved-away",
				}, observedAt.Add(time.Minute),
			)
			require.NoError(err)
			current, _, err := database.ReconcileRepositoryObservation(
				t.Context(), db.RepoIdentity{
					Platform: spec.Repository.Provider, PlatformHost: spec.Repository.PlatformHost,
					PlatformRepoID: spec.Repository.PlatformRepoID, Owner: spec.Repository.Owner,
					Name: spec.Repository.Name,
				}, observedAt.Add(2*time.Minute),
			)
			require.NoError(err)
			require.NotNil(current)

			manager := NewManager(database, t.TempDir())
			manager.SetNow(func() time.Time { return spec.IssuedAt })
			var workspace *Workspace
			if itemType == db.WorkspaceItemTypePullRequest {
				workspace, err = manager.CreateFromLaunchSpec(t.Context(), spec)
			} else {
				workspace, err = manager.CreateIssueFromLaunchSpec(
					t.Context(), spec, CreateIssueOptions{},
				)
			}

			require.NoError(err)
			require.NotNil(workspace)
			require.Equal(current.Repository.ID, workspace.RepoID)
		})
	}
}

func TestRefreshWorkspaceLaunchSpecAcceptsVerifiedIdentityAtReusedRoute(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := openTestDB(t)
	current := launchSpecForTest()
	observedAt := current.IssuedAt.Add(-2 * time.Minute)
	for _, identity := range []db.RepoIdentity{
		{
			Platform: current.Repository.Provider, PlatformHost: current.Repository.PlatformHost,
			PlatformRepoID: current.Repository.PlatformRepoID,
			Owner:          current.Repository.Owner, Name: current.Repository.Name,
		},
		{
			Platform: current.Repository.Provider, PlatformHost: current.Repository.PlatformHost,
			PlatformRepoID: "repo-previous-target",
			Owner:          "acme", Name: "renamed-target",
		},
	} {
		_, accepted, err := database.ReconcileRepositoryObservation(
			t.Context(), identity, observedAt,
		)
		require.NoError(err)
		require.True(accepted)
	}
	workspace := &db.Workspace{
		ID: "ws-reused-route", Platform: current.Repository.Provider,
		PlatformHost: current.Repository.PlatformHost,
		RepoOwner:    current.Repository.Owner, RepoName: current.Repository.Name,
		ItemType: current.ItemType, ItemNumber: current.ItemNumber,
		ItemKey: current.ItemKey, GitHeadRef: current.GitHeadRef,
		WorkspaceBranch: "kenn-forge/pr-7", WorktreePath: t.TempDir(),
		TmuxSession: "forge-ws-reused-route", Status: "ready",
	}
	require.NoError(database.InsertWorkspace(t.Context(), workspace))
	require.NoError(database.PutWorkspaceLaunchSpec(t.Context(), workspace.ID, current))

	_, accepted, err := database.ReconcileRepositoryObservation(
		t.Context(), db.RepoIdentity{
			Platform: current.Repository.Provider, PlatformHost: current.Repository.PlatformHost,
			PlatformRepoID: "repo-previous-target",
			Owner:          "acme", Name: "moved-away",
		}, observedAt.Add(time.Minute),
	)
	require.NoError(err)
	require.True(accepted)
	refreshed := current
	refreshed.Repository.Owner = "acme"
	refreshed.Repository.Name = "renamed-target"
	refreshed.Repository.CloneURL = "https://github.com/acme/renamed-target.git"
	refreshed.IssuedAt = current.IssuedAt.Add(time.Minute)
	refreshed.SourceVisibleUntil = refreshed.IssuedAt.Add(WorkspaceLaunchSpecVisibilityLease)
	_, accepted, err = database.ReconcileRepositoryObservation(
		t.Context(), db.RepoIdentity{
			Platform: refreshed.Repository.Provider, PlatformHost: refreshed.Repository.PlatformHost,
			PlatformRepoID: refreshed.Repository.PlatformRepoID,
			Owner:          refreshed.Repository.Owner, Name: refreshed.Repository.Name,
		}, observedAt.Add(2*time.Minute),
	)
	require.NoError(err)
	require.True(accepted)
	manager := NewManager(database, t.TempDir())
	manager.SetLaunchSpecResolver(&staticLaunchSpecResolver{spec: refreshed})
	manager.SetNow(func() time.Time { return refreshed.IssuedAt })

	got, err := manager.RefreshWorkspaceLaunchSpec(t.Context(), workspace)
	require.NoError(err)
	require.NotNil(got)
	assert.Equal(refreshed.Repository.Name, got.Repository.Name)
	persistedWorkspace, readErr := database.GetWorkspace(t.Context(), workspace.ID)
	require.NoError(readErr)
	require.NotNil(persistedWorkspace)
	assert.Equal(refreshed.Repository.Name, persistedWorkspace.RepoName)
	persistedSpec, readErr := database.GetWorkspaceLaunchSpec(t.Context(), workspace.ID)
	require.NoError(readErr)
	require.NotNil(persistedSpec)
	assert.Equal(refreshed, *persistedSpec)
}

func TestLaunchSpecSummaryPreservesSourceIdentity(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	title := "Fix widget refresh"
	url := "https://github.com/acme/widget/pull/7"
	spec := launchSpecForTest()
	spec.SourceTitle = title
	spec.SourceURL = url

	summary := launchSpecSummary(db.Workspace{
		ID: "ws-source", Platform: spec.Repository.Provider,
		PlatformHost: spec.Repository.PlatformHost,
		RepoOwner:    spec.Repository.Owner, RepoName: spec.Repository.Name,
		ItemType: spec.ItemType, ItemNumber: spec.ItemNumber,
		GitHeadRef: spec.GitHeadRef,
	}, spec)
	require.NotNil(summary.SourceTitle)
	require.NotNil(summary.SourceURL)
	require.NotNil(summary.MRTitle)
	assert.Equal(title, *summary.SourceTitle)
	assert.Equal(title, *summary.MRTitle)
	assert.Equal(url, *summary.SourceURL)
}
