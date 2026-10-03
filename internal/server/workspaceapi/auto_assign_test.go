package workspaceapi

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/platform"
)

type spokePreparationRejectingAdmitter struct{}

func (spokePreparationRejectingAdmitter) Admit(context.Context) (func(), error) {
	return nil, providerplane.ErrSpokePreparationInProgress
}

type autoAssignProvider struct {
	pull           platform.MergeRequest
	issue          platform.Issue
	pullAssigned   []string
	issueAssigned  []string
	assignedRepo   platform.RepoRef
	listPullCalls  int
	getPullCalls   int
	listIssueCalls int
	getIssueCalls  int
}

func (p *autoAssignProvider) Platform() platform.Kind { return platform.KindGitLab }
func (p *autoAssignProvider) Host() string            { return "git.example.test" }
func (p *autoAssignProvider) Capabilities() platform.Capabilities {
	return platform.Capabilities{
		ReadMergeRequests:     true,
		ReadIssues:            true,
		ReadAuthenticatedUser: true,
		AssigneeMutation:      true,
	}
}

func (p *autoAssignProvider) AuthenticatedUser(context.Context, platform.RepoRef) (string, error) {
	return "maintainer", nil
}

func (p *autoAssignProvider) ListOpenMergeRequests(context.Context, platform.RepoRef) ([]platform.MergeRequest, error) {
	p.listPullCalls++
	return nil, nil
}

func (p *autoAssignProvider) GetMergeRequest(context.Context, platform.RepoRef, int) (platform.MergeRequest, error) {
	p.getPullCalls++
	return p.pull, nil
}

func (p *autoAssignProvider) ListMergeRequestEvents(context.Context, platform.RepoRef, int) ([]platform.MergeRequestEvent, error) {
	return nil, nil
}

func (p *autoAssignProvider) ListOpenIssues(context.Context, platform.RepoRef) ([]platform.Issue, error) {
	p.listIssueCalls++
	return nil, nil
}

func (p *autoAssignProvider) GetIssue(context.Context, platform.RepoRef, int) (platform.Issue, error) {
	p.getIssueCalls++
	return p.issue, nil
}

func (p *autoAssignProvider) ListIssueEvents(context.Context, platform.RepoRef, int) ([]platform.IssueEvent, error) {
	return nil, nil
}

func (p *autoAssignProvider) SetMergeRequestAssignees(
	_ context.Context, ref platform.RepoRef, _ int, usernames []string,
) ([]string, error) {
	p.assignedRepo = ref
	p.pullAssigned = slices.Clone(usernames)
	return slices.Clone(usernames), nil
}

func (p *autoAssignProvider) SetIssueAssignees(
	_ context.Context, ref platform.RepoRef, _ int, usernames []string,
) ([]string, error) {
	p.assignedRepo = ref
	p.issueAssigned = slices.Clone(usernames)
	return slices.Clone(usernames), nil
}

func TestAutoAssignWorkspaceItemPreservesExistingAssignees(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)

	database := dbtest.Open(t)
	repoIdentity := db.RepoIdentity{
		Platform:     string(platform.KindGitLab),
		PlatformHost: "git.example.test",
		Owner:        "acme",
		Name:         "widget",
	}
	repoID, err := reposeed.Seed(t.Context(), database, repoIdentity)
	require.NoError(err)
	now := time.Now().UTC().Truncate(time.Second)
	pullID, err := database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: repoID, PlatformID: 101, Number: 7, URL: "https://git.example.test/acme/widget/merge_requests/7",
		Title: "Improve widget", Author: "author", State: db.MergeRequestStateOpen,
		HeadBranch: "feature", BaseBranch: "main", CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
		Assignees: []string{"reviewer"},
	})
	require.NoError(err)
	issueID, err := database.UpsertIssue(t.Context(), &db.Issue{
		RepoID: repoID, PlatformID: 102, Number: 8, URL: "https://git.example.test/acme/widget/issues/8",
		Title: "Fix widget", Author: "author", State: "open", CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
		Assignees: []string{"reviewer"},
	})
	require.NoError(err)

	provider := &autoAssignProvider{
		pull:  platform.MergeRequest{Assignees: []string{"reviewer"}},
		issue: platform.Issue{Assignees: []string{"reviewer"}},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	syncer := ghclient.NewSyncerWithRegistry(registry, database, nil, nil, time.Hour, nil, nil)
	t.Cleanup(syncer.Stop)
	handler := New(Deps{
		DB:       database,
		Resolver: httpapi.NewRepositoryResolver(httpapi.RepositoryResolverDeps{DB: database}),
		Syncer:   syncer,
		Config:   ConfigSnapshot{AutoAssignOnCreate: true},
	})
	repo, err := database.GetRepoByIdentity(t.Context(), repoIdentity)
	require.NoError(err)
	require.NotNil(repo)

	tests := []struct {
		name     string
		number   int
		issue    bool
		assigned func() []string
		stored   func() []string
	}{
		{
			name: "pull request", number: 7,
			assigned: func() []string { return provider.pullAssigned },
			stored: func() []string {
				item, getErr := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 7)
				require.NoError(getErr)
				require.NotNil(item)
				assert.Equal(pullID, item.ID)
				return item.Assignees
			},
		},
		{
			name: "issue", number: 8, issue: true,
			assigned: func() []string { return provider.issueAssigned },
			stored: func() []string {
				item, getErr := database.GetIssueByRepoIDAndNumber(t.Context(), repoID, 8)
				require.NoError(getErr)
				require.NotNil(item)
				assert.Equal(issueID, item.ID)
				return item.Assignees
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(handler.autoAssignWorkspaceItem(t.Context(), repo.Repo, tt.number, tt.issue, false))
			assert.Equal([]string{"reviewer", "maintainer"}, tt.assigned())
			assert.Equal([]string{"reviewer", "maintainer"}, tt.stored())

			provider.pullAssigned = nil
			provider.issueAssigned = nil
			require.NoError(handler.autoAssignWorkspaceItem(t.Context(), repo.Repo, tt.number, tt.issue, true))
			assert.Nil(tt.assigned())
		})
	}

	// The spoke owns the auto-assignment preference and calls this service only
	// after applying it. The hub must execute that request even when
	// its own local workspace preference differs.
	handler.config.AutoAssignOnCreate = false
	provider.pullAssigned = nil
	require.NoError(handler.AutoAssignProviderWorkspaceItem(
		t.Context(), ProviderWorkspaceItemRequest{
			Repository: providerplane.RepositoryRoute{
				Provider: string(platform.KindGitLab), PlatformHost: "git.example.test",
				Owner: "acme", Name: "widget",
			},
			RepoKey:  repo.Key,
			ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 7,
		},
	))
	assert.Equal([]string{"reviewer", "maintainer"}, provider.pullAssigned)
	handler.config.AutoAssignOnCreate = true

	_, err = database.WriteDB().ExecContext(t.Context(), `
		INSERT INTO forge_archive_items (
			repo_id, item_type, item_number, provider_item_id,
			provider_created_at, provider_updated_at, lifecycle_state
		) VALUES
			(?, 'merge_request', 7, 'pull-7', ?, ?, 'removed_upstream'),
			(?, 'issue', 8, 'issue-8', ?, ?, 'removed_upstream')`,
		repoID, now, now, repoID, now, now,
	)
	require.NoError(err)
	provider.pullAssigned = nil
	provider.issueAssigned = nil

	err = handler.autoAssignWorkspaceItem(t.Context(), repo.Repo, 7, false, false)
	require.ErrorContains(err, "not visible")
	err = handler.autoAssignWorkspaceItem(t.Context(), repo.Repo, 8, true, false)
	require.ErrorContains(err, "not visible")
	assert.Empty(provider.pullAssigned)
	assert.Empty(provider.issueAssigned)
}

func TestAutoAssignmentPreservesRepositoryIdentityAfterRouteReuse(t *testing.T) {
	t.Parallel()
	for _, itemType := range []string{db.WorkspaceItemTypePullRequest, db.WorkspaceItemTypeIssue} {
		for _, renamed := range []bool{false, true} {
			name := itemType + "/inactive"
			if renamed {
				name = itemType + "/renamed"
			}
			t.Run(name, func(t *testing.T) {
				assert := assert.New(t)
				require := require.New(t)
				database := dbtest.Open(t)
				now := time.Now().UTC()
				original := db.RepoIdentity{
					Platform: "gitlab", PlatformHost: "git.example.test", Key: platform.RepositoryIDKey(101),
					Owner: "acme", Name: "widget",
				}
				entry, err := database.ObserveRepository(t.Context(), original)
				require.NoError(err)
				require.NotNil(entry)
				originalID := entry.Repository.ID
				request := ProviderWorkspaceItemRequest{
					Repository: providerplane.RepositoryRoute{Provider: "gitlab", PlatformHost: "git.example.test", Owner: "acme", Name: "widget"},
					RepoKey:    platform.RepositoryIDKey(101), ItemType: itemType, ItemNumber: 7,
				}
				if renamed {
					original.Name = "renamed"
					_, err = database.ObserveRepository(t.Context(), original)
					require.NoError(err)
				}
				replacement, err := database.ObserveRepository(t.Context(), db.RepoIdentity{
					Platform: "gitlab", PlatformHost: "git.example.test", Key: platform.RepositoryIDKey(202), Owner: "acme", Name: "widget",
				})
				require.NoError(err)
				require.NotNil(replacement)
				for _, repoID := range []int64{originalID, replacement.Repository.ID} {
					if itemType == db.WorkspaceItemTypeIssue {
						itemID, insertErr := database.UpsertIssue(t.Context(), &db.Issue{
							RepoID: repoID, PlatformID: 7, Number: 7, Title: "Fix widget", State: "open", Author: "author",
							CreatedAt: now, UpdatedAt: now, LastActivityAt: now, Assignees: []string{"reviewer"},
						})
						require.NoError(insertErr)
						err = database.UpdateIssueAssignees(t.Context(), repoID, itemID, []string{"reviewer"})
					} else {
						itemID, insertErr := database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
							RepoID: repoID, PlatformID: 7, Number: 7, Title: "Improve widget", State: "open", Author: "author",
							HeadBranch: "feature", BaseBranch: "main", CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
							Assignees: []string{"reviewer"},
						})
						require.NoError(insertErr)
						err = database.UpdateMergeRequestAssignees(t.Context(), repoID, itemID, []string{"reviewer"})
					}
					require.NoError(err)
				}
				provider := &autoAssignProvider{
					pull: platform.MergeRequest{Assignees: []string{"reviewer"}}, issue: platform.Issue{Assignees: []string{"reviewer"}},
				}
				registry, err := platform.NewRegistry(provider)
				require.NoError(err)
				syncer := ghclient.NewSyncerWithRegistry(registry, database, nil, nil, time.Hour, nil, nil)
				t.Cleanup(syncer.Stop)
				handler := New(Deps{
					DB: database, Resolver: httpapi.NewRepositoryResolver(httpapi.RepositoryResolverDeps{DB: database}), Syncer: syncer,
				})

				err = handler.AutoAssignProviderWorkspaceItem(t.Context(), request)

				if renamed {
					require.NoError(err)
					assert.Equal(platform.RepositoryIDKey(101), provider.assignedRepo.Key)
					assert.Equal("acme/renamed", provider.assignedRepo.RepoPath)
				} else {
					require.Error(err)
					assert.Empty(provider.pullAssigned)
					assert.Empty(provider.issueAssigned)
				}
				for _, repoID := range []int64{originalID, replacement.Repository.ID} {
					expected := []string{"reviewer"}
					if renamed && repoID == originalID {
						expected = []string{"reviewer", "maintainer"}
					}
					if itemType == db.WorkspaceItemTypeIssue {
						item, err := database.GetIssueByRepoIDAndNumber(t.Context(), repoID, 7)
						require.NoError(err)
						require.NotNil(item)
						assert.Equal(expected, item.Assignees)
					} else {
						item, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 7)
						require.NoError(err)
						require.NotNil(item)
						assert.Equal(expected, item.Assignees)
					}
				}
			})
		}
	}
}

func TestSpokePreparationBlocksWorkspaceAutoAssignBeforeProviderAccess(t *testing.T) {
	t.Parallel()
	handler := &Handler{
		syncer:            &ghclient.Syncer{},
		config:            ConfigSnapshot{AutoAssignOnCreate: true},
		providerWriteGate: spokePreparationRejectingAdmitter{},
	}
	err := handler.autoAssignWorkspaceItem(
		t.Context(), db.Repo{}, 7, false, false,
	)
	require.ErrorIs(t, err, providerplane.ErrSpokePreparationInProgress)
}
