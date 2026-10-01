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

func TestLinkWorkspacePullRequestPersistsWithoutReplacing(t *testing.T) {
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
					_, err := h.LinkWorkspacePullRequestService(ctx, id, 42, "https://other.example.com/acme/widget/pull/42")
					require.Error(err)
					rejected, err := database.GetWorkspace(ctx, id)
					require.NoError(err)
					assert.Nil(rejected.AssociatedPRNumber)
					_, err = h.LinkWorkspacePullRequestService(ctx, id, 99, "https://"+host+"/acme/widget/pull/99")
					require.Error(err)
					already, err := h.LinkWorkspacePullRequestService(ctx, id, 42, urls[42])
					require.NoError(err)
					assert.Equal(itemType == db.WorkspaceItemTypePullRequest, already)
					again, err := h.LinkWorkspacePullRequestService(ctx, id, 42, urls[42])
					require.NoError(err)
					assert.True(again)
					_, err = h.LinkWorkspacePullRequestService(ctx, id, 43, urls[43])
					require.Error(err)
					stored, err := database.GetWorkspace(ctx, id)
					require.NoError(err)
					if itemType == db.WorkspaceItemTypePullRequest {
						assert.Nil(stored.AssociatedPRNumber)
					} else {
						require.NotNil(stored.AssociatedPRNumber)
						assert.Equal(42, *stored.AssociatedPRNumber)
						summary, err := h.GetWorkspaceService(ctx, id)
						require.NoError(err)
						require.NotNil(summary.Workspace.AssociatedPRNumber)
						assert.Equal(42, *summary.Workspace.AssociatedPRNumber)
					}
				})
			}
		})
	}
}
