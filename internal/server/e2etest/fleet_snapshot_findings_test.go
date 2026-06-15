package e2etest

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbpkg "go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/fleet"
)

func putJSON(
	t *testing.T,
	client *http.Client,
	url string,
	body any,
) (int, string) {
	t.Helper()
	require := require.New(t)

	var payload io.Reader = http.NoBody
	if body != nil {
		buf, err := json.Marshal(body)
		require.NoError(err)
		payload = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(http.MethodPut, url, payload)
	require.NoError(err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	require.NoError(err)
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	require.NoError(err)
	return resp.StatusCode, string(respBody)
}

// seedLinkedProject creates a synced repo row plus a project linked to it,
// mirroring a registered project whose checkout tracks a platform repository.
func seedLinkedProject(
	t *testing.T,
	database *dbpkg.DB,
	identity dbpkg.RepoIdentity,
) (*dbpkg.Project, int64) {
	t.Helper()
	ctx := context.Background()
	repoID, err := database.UpsertRepo(ctx, identity)
	require.NoError(t, err)
	project, err := database.CreateProject(ctx, dbpkg.CreateProjectInput{
		DisplayName:   identity.Name,
		LocalPath:     filepath.Join(t.TempDir(), identity.Name),
		RepoID:        sql.NullInt64{Int64: repoID, Valid: true},
		DefaultBranch: "main",
	})
	require.NoError(t, err)
	return project, repoID
}

// registerWorktree registers a registry-only worktree through the HTTP API and
// returns its id.
func registerWorktree(
	t *testing.T,
	ts *httptest.Server,
	projectID, branch, path string,
) string {
	t.Helper()
	status, body := postJSON(
		t, ts.Client(), ts.URL+"/api/v1/projects/"+projectID+"/worktrees",
		map[string]any{"branch": branch, "path": path},
	)
	require.Equal(t, http.StatusCreated, status, body)
	var wt struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &wt))
	require.NotEmpty(t, wt.ID)
	return wt.ID
}

func findWorktreeByPath(
	worktrees []fleet.WorktreeSummary, path string,
) *fleet.WorktreeSummary {
	for i := range worktrees {
		if worktrees[i].Path == path {
			return &worktrees[i]
		}
	}
	return nil
}

func findRawWorktreeByPath(
	worktrees []fleet.RawWorktree, path string,
) *fleet.RawWorktree {
	for i := range worktrees {
		if worktrees[i].Path == path {
			return &worktrees[i]
		}
	}
	return nil
}

// TestFleetSnapshotBranchMatchLinkE2E drives the branch-match worktree-link
// flow over the wire: registering a worktree whose branch matches an open
// merge request recomputes the durable links, and both raw and enriched
// snapshots overlay the linked PR onto the registered worktree.
func TestFleetSnapshotBranchMatchLinkE2E(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ts, database := bootFleetServer(t, nil)
	ctx := context.Background()

	project, repoID := seedLinkedProject(
		t, database, dbpkg.GitHubRepoIdentity("github.com", "acme", "widget"),
	)
	now := time.Now().UTC().Truncate(time.Second)
	_, err := database.UpsertMergeRequest(ctx, &dbpkg.MergeRequest{
		RepoID: repoID, PlatformID: 7700, Number: 77,
		URL: "https://github.com/acme/widget/pull/77", Title: "Add feature",
		Author: "dev", State: "open", IsDraft: true,
		HeadBranch: "feature", BaseBranch: "main",
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)

	wtPath := filepath.Join(t.TempDir(), "wt-feature")
	registerWorktree(t, ts, project.ID, "feature", wtPath)

	var raw fleet.RawSnapshot
	getJSON(t, ts, "/api/v1/snapshot/raw", &raw)
	rawWt := findRawWorktreeByPath(raw.Worktrees, wtPath)
	require.NotNil(rawWt, "registered worktree must appear in the raw snapshot")
	require.NotNil(rawWt.LinkedPRNumber, "branch-matched PR must overlay the raw worktree")
	assert.Equal(77, *rawWt.LinkedPRNumber)
	require.NotNil(rawWt.PRState)
	assert.Equal("draft", *rawWt.PRState, "an open draft folds to the draft display state")
	require.NotNil(rawWt.PRTitle)
	assert.Equal("Add feature", *rawWt.PRTitle)

	var snap fleet.Snapshot
	getJSON(t, ts, "/api/v1/snapshot", &snap)
	wt := findWorktreeByPath(snap.Worktrees, wtPath)
	require.NotNil(wt, "registered worktree must appear in the enriched snapshot")
	require.NotNil(wt.LinkedPRNumber)
	assert.Equal(77, *wt.LinkedPRNumber)

	// Deleting the worktree recomputes the links away again.
	status, body := deleteJSON(
		t, ts.Client(),
		ts.URL+"/api/v1/projects/"+project.ID+"/worktrees/"+rawWt.RegistryID,
	)
	require.Equal(http.StatusNoContent, status, body)
	links, err := database.GetAllWorktreeLinks(ctx)
	require.NoError(err)
	assert.Empty(links, "deleting the worktree must drop its branch-match link")
}

// TestProjectWorktreeHiddenToggleE2E toggles a worktree's hidden flag through
// the PUT route and verifies the flag lands on the wire in both the route
// response and the raw/enriched snapshots.
func TestProjectWorktreeHiddenToggleE2E(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ts, database := bootFleetServer(t, nil)

	project, _ := seedLinkedProject(
		t, database, dbpkg.GitHubRepoIdentity("github.com", "acme", "hideme"),
	)
	wtPath := filepath.Join(t.TempDir(), "wt-hidden")
	wtID := registerWorktree(t, ts, project.ID, "feature-hidden", wtPath)

	status, body := putJSON(
		t, ts.Client(),
		ts.URL+"/api/v1/projects/"+project.ID+"/worktrees/"+wtID+"/hidden",
		map[string]any{"hidden": true},
	)
	require.Equal(http.StatusOK, status, body)
	var updated struct {
		IsHidden bool `json:"is_hidden"`
	}
	require.NoError(json.Unmarshal([]byte(body), &updated))
	assert.True(updated.IsHidden, "PUT response must reflect the new hidden state")

	var raw fleet.RawSnapshot
	getJSON(t, ts, "/api/v1/snapshot/raw", &raw)
	rawWt := findRawWorktreeByPath(raw.Worktrees, wtPath)
	require.NotNil(rawWt)
	assert.True(rawWt.IsHidden, "raw snapshot must carry isHidden")

	var snap fleet.Snapshot
	getJSON(t, ts, "/api/v1/snapshot", &snap)
	wt := findWorktreeByPath(snap.Worktrees, wtPath)
	require.NotNil(wt)
	assert.True(wt.IsHidden, "enriched snapshot must carry isHidden")

	// Toggle back off over the wire.
	status, body = putJSON(
		t, ts.Client(),
		ts.URL+"/api/v1/projects/"+project.ID+"/worktrees/"+wtID+"/hidden",
		map[string]any{"hidden": false},
	)
	require.Equal(http.StatusOK, status, body)
	// Decode into a fresh value: isHidden is omitempty, so an absent field
	// would leave the previous true in a reused struct.
	var snapAfter fleet.Snapshot
	getJSON(t, ts, "/api/v1/snapshot", &snapAfter)
	wt = findWorktreeByPath(snapAfter.Worktrees, wtPath)
	require.NotNil(wt)
	assert.False(wt.IsHidden)
}

// TestFleetSnapshotWorktreeStatsE2E seeds sampled git stats and verifies the
// snapshot endpoints overlay them by path: a sampled worktree reports all four
// counts (even zero) while an unsampled one omits them.
func TestFleetSnapshotWorktreeStatsE2E(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ts, database := bootFleetServer(t, nil)
	ctx := context.Background()

	project, _ := seedLinkedProject(
		t, database, dbpkg.GitHubRepoIdentity("github.com", "acme", "stats"),
	)
	sampledPath := filepath.Join(t.TempDir(), "wt-sampled")
	unsampledPath := filepath.Join(t.TempDir(), "wt-unsampled")
	registerWorktree(t, ts, project.ID, "feature-sampled", sampledPath)
	registerWorktree(t, ts, project.ID, "feature-unsampled", unsampledPath)

	_, err := database.UpsertWorktreeStats(ctx, sampledPath, dbpkg.WorktreeGitStats{
		DiffAdded: 12, DiffRemoved: 3, SyncAhead: 2, SyncBehind: 0,
	}, time.Now().UTC())
	require.NoError(err)

	var raw fleet.RawSnapshot
	getJSON(t, ts, "/api/v1/snapshot/raw", &raw)
	sampled := findRawWorktreeByPath(raw.Worktrees, sampledPath)
	require.NotNil(sampled)
	require.NotNil(sampled.DiffAdded)
	assert.Equal(12, *sampled.DiffAdded)
	require.NotNil(sampled.DiffRemoved)
	assert.Equal(3, *sampled.DiffRemoved)
	require.NotNil(sampled.SyncAhead)
	assert.Equal(2, *sampled.SyncAhead)
	require.NotNil(sampled.SyncBehind)
	assert.Equal(0, *sampled.SyncBehind, "a sampled zero must be reported, not omitted")

	unsampled := findRawWorktreeByPath(raw.Worktrees, unsampledPath)
	require.NotNil(unsampled)
	assert.Nil(unsampled.DiffAdded, "an unsampled worktree omits diff stats")
	assert.Nil(unsampled.SyncBehind)

	var snap fleet.Snapshot
	getJSON(t, ts, "/api/v1/snapshot", &snap)
	wt := findWorktreeByPath(snap.Worktrees, sampledPath)
	require.NotNil(wt)
	require.NotNil(wt.DiffAdded)
	assert.Equal(12, *wt.DiffAdded)
	require.NotNil(wt.SyncAhead)
	assert.Equal(2, *wt.SyncAhead)
}

// TestFleetSnapshotSessionBackendE2E verifies the enriched snapshot's session
// backend wire vocabulary: an empty stored backend defaults to localPTY and a
// stored localTmux override surfaces with its canonical casing.
func TestFleetSnapshotSessionBackendE2E(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ts, database := bootFleetServer(t, nil)

	project, _ := seedLinkedProject(
		t, database, dbpkg.GitHubRepoIdentity("github.com", "acme", "backend"),
	)
	defaultPath := filepath.Join(t.TempDir(), "wt-default")
	tmuxPath := filepath.Join(t.TempDir(), "wt-tmux")
	registerWorktree(t, ts, project.ID, "feature-default", defaultPath)
	tmuxID := registerWorktree(t, ts, project.ID, "feature-tmux", tmuxPath)

	status, body := putJSON(
		t, ts.Client(),
		ts.URL+"/api/v1/projects/"+project.ID+"/worktrees/"+tmuxID+"/session-backend",
		map[string]any{"session_backend": "localTmux"},
	)
	require.Equal(http.StatusOK, status, body)

	var snap fleet.Snapshot
	getJSON(t, ts, "/api/v1/snapshot", &snap)
	defWt := findWorktreeByPath(snap.Worktrees, defaultPath)
	require.NotNil(defWt)
	assert.Equal(fleet.SessionBackendLocalPTY, defWt.SessionBackend,
		"a registered worktree with no backend defaults to localPTY on the wire")
	tmuxWt := findWorktreeByPath(snap.Worktrees, tmuxPath)
	require.NotNil(tmuxWt)
	assert.Equal(fleet.SessionBackendLocalTmux, tmuxWt.SessionBackend,
		"a stored localTmux backend keeps its canonical casing on the wire")
}

// TestFleetSnapshotProjectPlatformE2E verifies raw and enriched snapshots
// carry the project's provider kind so clients can build provider-aware
// routes instead of assuming GitHub.
func TestFleetSnapshotProjectPlatformE2E(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ts, database := bootFleetServer(t, nil)

	project, _ := seedLinkedProject(t, database, dbpkg.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		Owner:        "grp",
		Name:         "proj",
	})

	var raw fleet.RawSnapshot
	getJSON(t, ts, "/api/v1/snapshot/raw", &raw)
	var rawProj *fleet.RawProject
	for i := range raw.Projects {
		if raw.Projects[i].RegistryID == project.ID {
			rawProj = &raw.Projects[i]
		}
	}
	require.NotNil(rawProj)
	assert.Equal("gitlab", rawProj.Platform)
	assert.Equal("gitlab.example.com", rawProj.PlatformHost)
	assert.Equal("grp/proj", rawProj.PlatformRepo)

	var snap fleet.Snapshot
	getJSON(t, ts, "/api/v1/snapshot", &snap)
	var enriched *fleet.ProjectSummary
	for i := range snap.Projects {
		if snap.Projects[i].RegistryID == project.ID {
			enriched = &snap.Projects[i]
		}
	}
	require.NotNil(enriched)
	assert.Equal("gitlab", enriched.Platform,
		"enriched project must carry the provider kind")
}
