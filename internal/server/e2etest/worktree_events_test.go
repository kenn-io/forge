package e2etest

import (
	"bufio"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/config"
	dbpkg "go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/server"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

// TestE2E_WorktreeLinkChangeReachesSSE drives the sync-completed path
// over the wire: an open merge request on branch "feature" matches a
// registered worktree, and the sync hook — composed exactly as
// cmd/middleman and the embedded Instance wire it, recompute feeding
// the server's notify fanout — must surface worktree_links_changed to
// an /api/v1/events subscriber. This is the refetch hint standalone
// clients rely on instead of embedder hooks; the mutation path is
// covered separately by the in-package fanout unit test.
func TestE2E_WorktreeLinkChangeReachesSSE(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()

	database := dbtest.Open(t)
	syncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	cfg := &config.Config{BasePath: "/"}
	cfg.Tmux.Command = []string{"middleman-no-such-tmux"}
	srv := server.New(database, syncer, nil, "/", cfg, server.ServerOptions{
		WorktreeDir:                        t.TempDir(),
		DisableWorkspaceBackgroundMonitors: true,
		HostCheck: server.HostCheckOptions{
			Bind:                 config.HostKey{Host: "127.0.0.1", Port: "8091"},
			AllowLoopbackAnyPort: true,
		},
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	repoID, err := database.UpsertRepo(
		ctx, dbpkg.GitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)
	project, err := database.CreateProject(ctx, dbpkg.CreateProjectInput{
		DisplayName: "widget",
		LocalPath:   filepath.Join(t.TempDir(), "widget"),
		RepoID:      sql.NullInt64{Int64: repoID, Valid: true},
	})
	require.NoError(err)
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	_, err = database.UpsertMergeRequest(ctx, &dbpkg.MergeRequest{
		RepoID: repoID, PlatformID: 42, Number: 42,
		Title: "PR feature", Author: "a",
		State:      dbpkg.MergeRequestStateOpen,
		HeadBranch: "feature", BaseBranch: "main",
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)

	// The request context doubles as the read deadline: scanner.Scan
	// blocks inside the response body, so only cancelling the request
	// can unblock a missing-event hang.
	sseCtx, cancelSSE := context.WithTimeout(ctx, 10*time.Second)
	defer cancelSSE()
	sseReq, err := http.NewRequestWithContext(
		sseCtx, http.MethodGet, ts.URL+"/api/v1/events", nil,
	)
	require.NoError(err)
	sseResp, err := ts.Client().Do(sseReq)
	require.NoError(err)
	defer sseResp.Body.Close()
	require.Equal(http.StatusOK, sseResp.StatusCode)
	waitForSubscribe(t, srv, 1)

	_, err = database.CreateProjectWorktree(ctx, dbpkg.CreateProjectWorktreeInput{
		ProjectID: project.ID, Branch: "feature",
		Path: filepath.Join(t.TempDir(), "wt-feature"),
	})
	require.NoError(err)

	// Compose the sync-completed hook exactly as cmd/middleman and the
	// embedded Instance do — recompute feeding the server fanout — and
	// fire it as the syncer would after a sync.
	hook := server.WorktreeLinksSyncHook(
		ctx, database, syncer, srv.NotifyWorktreeLinksChanged, nil,
	)
	hook(nil)

	scanner := bufio.NewScanner(sseResp.Body)
	for {
		frame := readSSEFrame(t, scanner)
		if frame.Event == "worktree_links_changed" {
			return
		}
	}
}
