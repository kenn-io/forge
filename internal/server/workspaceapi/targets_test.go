package workspaceapi

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/internal/workspace"
	"go.kenn.io/forge/platform"
)

func TestWorkspaceTargetsPreserveOwnerAndBranch(t *testing.T) {
	for _, provider := range []string{"github", "gitlab", "forgejo", "gitea", "bitbucket", "bitbucket-cloud"} {
		t.Run(provider, func(t *testing.T) {
			database := dbtest.Open(t)
			ctx := t.Context()
			host := provider + ".example.com"
			identity := db.RepoIdentity{Platform: strings.TrimSuffix(provider, "-cloud"), PlatformHost: host, Owner: "acme", Name: "widget"}
			if provider == "bitbucket-cloud" {
				identity.PlatformHost = "bitbucket.org"
				identity.Key = platform.RepositoryUUIDKey(uuid.MustParse("11111111-1111-4111-8111-111111111111"))
			}
			host = identity.PlatformHost
			repoID, err := reposeed.Seed(ctx, database, identity)
			require.NoError(t, err)
			urls := map[int]string{}
			for _, number := range []int{42, 43} {
				urls[number] = fmt.Sprintf("https://%s/acme/widget/pull/%d", host, number)
				_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{RepoID: repoID, PlatformID: int64(number), Number: number, URL: urls[number], State: "open", Title: "Work", Author: "alice", CreatedAt: time.Now(), UpdatedAt: time.Now()})
				require.NoError(t, err)
			}
			for _, itemType := range []string{db.WorkspaceItemTypeIssue, db.WorkspaceItemTypeAdHoc, db.WorkspaceItemTypeKataTask, db.WorkspaceItemTypePullRequest} {
				t.Run(itemType, func(t *testing.T) {
					require := require.New(t)
					assert := assert.New(t)
					id := "ws-" + itemType
					ws := &db.Workspace{ID: id, RepoID: repoID, Platform: identity.Platform, PlatformHost: host, RepoOwner: "acme", RepoName: "widget", ItemType: itemType, ItemKey: id, ItemNumber: 42, WorktreePath: t.TempDir(), Status: "ready"}
					require.NoError(database.InsertWorkspace(ctx, ws))
					h := New(Deps{DB: database, Workspaces: workspace.NewManager(database, t.TempDir()), EnrichmentDisabled: true})
					_, err := h.AddWorkspaceTargetService(ctx, id, WorkspaceTargetInput{Type: "pr", Number: 42, URL: "https://other.example.com/acme/widget/pull/42"})
					require.Error(err)
					rejected, err := database.GetWorkspace(ctx, id)
					require.NoError(err)
					assert.Nil(rejected.AssociatedPRNumber)
					_, err = h.AddWorkspaceTargetService(ctx, id, WorkspaceTargetInput{Type: "pr", Number: 99, URL: "https://" + host + "/acme/widget/pull/99"})
					require.Error(err)
					already, err := h.AddWorkspaceTargetService(ctx, id, WorkspaceTargetInput{Type: "pr", Number: 42, URL: urls[42]})
					require.NoError(err)
					assert.Positive(already.ID)
					again, err := h.AddWorkspaceTargetService(ctx, id, WorkspaceTargetInput{Type: "pr", Number: 42, URL: urls[42]})
					require.NoError(err)
					assert.Equal(already.ID, again.ID)
					second, err := h.AddWorkspaceTargetService(ctx, id, WorkspaceTargetInput{Type: "pr", Number: 43, URL: urls[43]})
					require.NoError(err)
					assert.Positive(second.ID)
					listed, err := h.ListWorkspaceTargetsService(ctx, id)
					require.NoError(err)
					count := 2
					if itemType == db.WorkspaceItemTypeIssue {
						count++
					}
					assert.Len(listed.Targets, count)
					require.Error(h.RemoveWorkspaceTargetService(ctx, id, "issue", second.ID))
					require.NoError(h.RemoveWorkspaceTargetService(ctx, id, "pr", second.ID))
					stored, err := database.GetWorkspace(ctx, id)
					require.NoError(err)
					assert.Nil(stored.AssociatedPRNumber)
					assert.Equal(ws.ItemType, stored.ItemType)
					assert.Equal(ws.ItemNumber, stored.ItemNumber)
				})
			}
		})
	}
}

func TestWorkspaceTargetsKeepDistinctTypesAndRepositories(t *testing.T) {
	database := dbtest.Open(t)
	ctx := t.Context()
	h := New(Deps{DB: database, Workspaces: workspace.NewManager(database, t.TempDir()), EnrichmentDisabled: true})
	repoID, err := reposeed.Seed(ctx, database, db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(t, err)
	otherID, err := reposeed.Seed(ctx, database, db.GitHubRepoIdentity("github.com", "acme", "other"))
	require.NoError(t, err)
	other, err := database.GetRepoByID(ctx, otherID)
	require.NoError(t, err)
	require.NoError(t, database.InsertWorkspace(ctx, &db.Workspace{ID: "ws-targets", RepoID: repoID, Platform: "github", PlatformHost: "github.com", RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc, ItemKey: "targets", Status: "ready", WorktreePath: t.TempDir()}))
	now := time.Now().UTC()
	pull := &db.MergeRequest{RepoID: repoID, PlatformID: 42, Number: 42, URL: "https://github.com/acme/widget/pull/42", Title: "Stack base", State: "open", Author: "alice", CreatedAt: now, UpdatedAt: now}
	_, err = database.UpsertMergeRequest(ctx, pull)
	require.NoError(t, err)
	linked, err := h.AddWorkspaceTargetService(ctx, "ws-targets", WorkspaceTargetInput{Type: "pr", Number: 42, URL: pull.URL})
	require.NoError(t, err)
	issue := &db.Issue{RepoID: otherID, PlatformID: 42, Number: 42, URL: "https://github.com/acme/other/issues/42", Title: "Follow up", State: "open", Author: "alice", CreatedAt: now, UpdatedAt: now}
	_, err = database.UpsertIssue(ctx, issue)
	require.NoError(t, err)
	_, err = h.AddWorkspaceTargetService(ctx, "ws-targets", WorkspaceTargetInput{Repository: &WorkspaceTargetRepository{Provider: other.Platform, PlatformHost: other.PlatformHost, Key: other.Key}, Type: "issue", Number: 42, URL: issue.URL})
	require.NoError(t, err)
	_, err = database.SetWorkspaceAssociatedPRNumberIfNull(ctx, "ws-targets", 42)
	require.NoError(t, err)
	pull.State = "merged"
	_, err = database.UpsertMergeRequest(ctx, pull)
	require.NoError(t, err)
	listed, err := h.ListWorkspaceTargetsService(ctx, "ws-targets")
	require.NoError(t, err)
	require.Len(t, listed.Targets, 2)
	assert.Equal(t, linked.ID, listed.Targets[0].ID)
	assert.Equal(t, "branch", listed.Targets[0].Source)
	assert.Equal(t, "merged", listed.Targets[0].State)
	assert.Equal(t, other.Key, listed.Targets[1].Repo.Key)
	require.NoError(t, database.InsertWorkspace(ctx, &db.Workspace{ID: "ws-other", RepoID: repoID, Platform: "github", PlatformHost: "github.com", RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc, ItemKey: "other", Status: "ready", WorktreePath: t.TempDir()}))
	require.Error(t, h.RemoveWorkspaceTargetService(ctx, "ws-other", "pr", linked.ID))
	require.NoError(t, h.RemoveWorkspaceTargetService(ctx, "ws-targets", "pr", linked.ID))
	listed, err = h.ListWorkspaceTargetsService(ctx, "ws-targets")
	require.NoError(t, err)
	require.Len(t, listed.Targets, 2)
	for _, target := range listed.Targets {
		if target.Type == "pr" {
			assert.Zero(t, target.ID)
			assert.Equal(t, "branch", target.Source)
		}
	}
	// Cached metadata can vanish while an explicit tracking link remains.
	_, err = database.AddWorkspaceTarget(ctx, db.WorkspaceTarget{WorkspaceID: "ws-targets", RepoID: repoID, ItemType: db.WorkspaceItemTypeIssue, ItemNumber: 99, URL: "https://github.com/acme/widget/issues/99"})
	require.NoError(t, err)
	listed, err = h.ListWorkspaceTargetsService(ctx, "ws-targets")
	require.NoError(t, err)
	require.Len(t, listed.Targets, 3)
	for _, target := range listed.Targets {
		if target.Number == 99 {
			assert.True(t, target.Unavailable)
			assert.NotEmpty(t, target.URL)
		}
	}
	require.NoError(t, database.DeleteWorkspace(ctx, "ws-targets"))
	stored, err := database.ListWorkspaceTargets(ctx, "ws-targets")
	require.NoError(t, err)
	assert.Empty(t, stored)
}
