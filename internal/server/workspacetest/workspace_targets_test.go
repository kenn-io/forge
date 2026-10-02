package workspacetest

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/internal/testutil/servertest"
)

func TestWorkspaceTargetHTTPListsStableRepositoryIdentity(t *testing.T) {
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
