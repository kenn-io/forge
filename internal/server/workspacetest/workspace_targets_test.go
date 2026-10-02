package workspacetest

import (
	"encoding/json"
	"fmt"
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

func TestWorkspaceTargetHTTPResolvesStableRepositoryIdentity(t *testing.T) {
	srv, database := servertest.SetupTestServer(t)
	ctx := t.Context()
	repoID, err := reposeed.Seed(ctx, database, db.GitHubRepoIdentity("github.com", "acme", "target"))
	require.NoError(t, err)
	repo, err := database.GetRepoByID(ctx, repoID)
	require.NoError(t, err)
	key, ok := repo.Key.ID()
	require.True(t, ok)
	require.NoError(t, database.InsertWorkspace(ctx, &db.Workspace{ID: "ws-target", RepoID: repoID, Platform: "github", PlatformHost: "github.com", RepoOwner: "acme", RepoName: "target", ItemType: db.WorkspaceItemTypeAdHoc, ItemKey: "target", Status: "ready", WorktreePath: t.TempDir()}))
	now := time.Now().UTC()
	_, err = database.UpsertIssue(ctx, &db.Issue{RepoID: repoID, PlatformID: 42, Number: 42, URL: "https://github.com/acme/target/issues/42", Title: "Follow up", State: "open", Author: "alice", CreatedAt: now, UpdatedAt: now})
	require.NoError(t, err)
	result := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/workspaces/ws-target/targets", map[string]any{
		"type": "issue", "number": 42, "url": "https://github.com/acme/target/issues/42",
		"repository": map[string]any{"provider": "github", "platform_host": "github.com", "platform_repo_id": key},
	})
	require.Equal(t, http.StatusOK, result.Code, result.Body.String())
	var target workspaceapi.WorkspaceTarget
	require.NoError(t, json.Unmarshal(result.Body.Bytes(), &target))
	assert.Equal(t, repo.Key, target.Repo.Key)
	assert.Positive(t, target.ID)
	listed := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/workspaces/ws-target/targets", nil)
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	var targets workspaceapi.WorkspaceTargetsResponse
	require.NoError(t, json.Unmarshal(listed.Body.Bytes(), &targets))
	require.Len(t, targets.Targets, 1)
	assert.Equal(t, target.ID, targets.Targets[0].ID)
	deleted := testutil.DoJSON(t, srv, http.MethodDelete, fmt.Sprintf("/api/v1/workspaces/ws-target/targets/%d?type=issue", target.ID), nil)
	require.Equal(t, http.StatusNoContent, deleted.Code, deleted.Body.String())
}
