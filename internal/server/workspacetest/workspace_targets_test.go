package workspacetest

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/internal/testutil/servertest"
	"go.kenn.io/forge/platform"
)

func TestWorkspaceTargetHTTPListsStableRepositoryIdentity(t *testing.T) {
	runParallelWorkspaceGitTest(t)
	srv, database := servertest.SetupTestServer(t)
	ctx := t.Context()
	repoID, err := reposeed.Seed(ctx, database, db.GitHubRepoIdentity("github.com", "acme", "target"))
	require.NoError(t, err)
	repo, err := database.GetRepoByID(ctx, repoID)
	require.NoError(t, err)
	require.NoError(t, database.InsertWorkspace(ctx, &db.Workspace{ID: "ws-target", RepoID: repoID, Platform: "github", PlatformHost: "github.com", RepoOwner: "acme", RepoName: "target", ItemType: db.WorkspaceItemTypeAdHoc, ItemKey: "target", Status: "ready", WorktreePath: t.TempDir()}))
	now := time.Now().UTC()
	_, err = database.UpsertIssue(ctx, &db.Issue{RepoID: repoID, PlatformID: 42, Number: 42, URL: "https://github.com/acme/target/issues/42", Title: "Follow up", State: "open", Author: "alice", CreatedAt: now, UpdatedAt: now})
	require.NoError(t, err)
	targetID, err := database.AddWorkspaceTarget(ctx, db.WorkspaceTarget{WorkspaceID: "ws-target", RepoID: repoID, ItemType: db.WorkspaceItemTypeIssue, ItemNumber: 42, URL: "https://github.com/acme/target/issues/42"})
	require.NoError(t, err)
	listed := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/workspaces/ws-target/targets", nil)
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	var targets workspaceapi.WorkspaceTargetsResponse
	require.NoError(t, json.Unmarshal(listed.Body.Bytes(), &targets))
	require.Len(t, targets.Targets, 1)
	assert.Equal(t, targetID, targets.Targets[0].ID)
	assert.Equal(t, repo.Key, targets.Targets[0].Repo.Key)
}

func TestWorkspaceTargetVisitsPersistRemovalAndRevisit(t *testing.T) {
	for _, kind := range []string{"pr", "issue"} {
		t.Run(kind, func(t *testing.T) {
			itemType := db.WorkspaceItemTypeIssue
			if kind == "pr" {
				itemType = db.WorkspaceItemTypePullRequest
			}
			runParallelWorkspaceGitTest(t)
			srv, database := servertest.SetupTestServer(t)
			ctx := t.Context()
			repoID, err := reposeed.Seed(ctx, database, db.GitHubRepoIdentity("github.com", "acme", "visits"))
			require.NoError(t, err)
			repo, err := database.GetRepoByID(ctx, repoID)
			require.NoError(t, err)
			require.NoError(t, database.InsertWorkspace(ctx, &db.Workspace{ID: "ws-visits", RepoID: repoID, Platform: "github", PlatformHost: "github.com", RepoOwner: "acme", RepoName: "visits", ItemType: itemType, ItemKey: "42", ItemNumber: 42, Status: "ready", WorktreePath: t.TempDir()}))
			now := time.Now().UTC()
			for _, number := range []int{42, 43} {
				if kind == "issue" {
					_, err = database.UpsertIssue(ctx, &db.Issue{RepoID: repoID, PlatformID: int64(number), Number: number, URL: "https://github.com/acme/visits/issues/" + strconv.Itoa(number), Title: "Visited issue", State: "open", Author: "alice", CreatedAt: now, UpdatedAt: now})
				} else {
					_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{RepoID: repoID, PlatformID: int64(number), Number: number, URL: "https://github.com/acme/visits/pull/" + strconv.Itoa(number), Title: "Visited PR", State: "open", Author: "alice", CreatedAt: now, UpdatedAt: now})
				}
				require.NoError(t, err)
			}
			for _, step := range []struct {
				number int
				hidden bool
				want   []int
			}{
				{43, false, []int{43, 42}},
				{43, false, []int{43, 42}},
				{42, true, []int{43}},
				{43, true, []int{}},
				{42, false, []int{42}},
				{43, false, []int{43, 42}},
			} {
				response := testutil.DoJSON(t, srv, http.MethodPut, "/api/v1/workspaces/ws-visits/targets", map[string]any{
					"repository": platform.RepositoryIdentity{Provider: "github", PlatformHost: "github.com", Key: repo.Key},
					"type":       kind, "number": step.number, "hidden": step.hidden,
				})
				require.Equal(t, http.StatusNoContent, response.Code, response.Body.String())
				listed := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/workspaces/ws-visits/targets", nil)
				require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
				var targets workspaceapi.WorkspaceTargetsResponse
				require.NoError(t, json.Unmarshal(listed.Body.Bytes(), &targets))
				numbers := make([]int, 0, len(targets.Targets))
				for _, target := range targets.Targets {
					numbers = append(numbers, target.Number)
				}
				assert.Equal(t, step.want, numbers)
			}
			ws, err := database.GetWorkspace(ctx, "ws-visits")
			require.NoError(t, err)
			assert.Equal(t, 42, ws.ItemNumber)
			assert.Equal(t, itemType, ws.ItemType)
		})
	}
}
