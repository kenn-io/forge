package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/workspace/localruntime"
)

func TestWorkspaceEnrichmentInvalidationRejectsOlderRefresh(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	srv := &Server{
		now:                            func() time.Time { return now },
		workspaceEnrichmentCache:       make(map[string]workspaceEnrichmentCacheEntry),
		workspaceEnrichmentGenerations: make(map[string]uint64),
	}

	oldGeneration := srv.workspaceEnrichmentGeneration("ws-1")
	srv.invalidateWorkspaceEnrichment("ws-1")
	ahead := 1
	stored := srv.storeWorkspaceEnrichment(
		"ws-1",
		oldGeneration,
		workspaceResponse{CommitsAhead: &ahead},
	)

	assert.False(t, stored)
	assert.NotContains(t, srv.workspaceEnrichmentCache, "ws-1")
}

func TestCachedWorkspaceEnrichmentReportsStaleAndFailedState(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	_, _, _, _, srv := setupTestServerWithWorkspacesServer(t, nil)
	srv.workspaceEnrichmentDisabled = false
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	srv.now = func() time.Time { return now }
	for range cap(srv.workspaceEnrichmentSlots) {
		srv.workspaceEnrichmentSlots <- struct{}{}
	}
	t.Cleanup(func() {
		for range cap(srv.workspaceEnrichmentSlots) {
			<-srv.workspaceEnrichmentSlots
		}
	})
	summary := db.WorkspaceSummary{Workspace: db.Workspace{
		ID:     "ws-status",
		Status: "ready",
	}}
	ahead := 2
	srv.workspaceEnrichmentCache[summary.ID] = workspaceEnrichmentCacheEntry{
		response: workspaceResponse{
			CommitsAhead: &ahead,
		},
		hasDivergence:         true,
		divergenceRefreshedAt: now.Add(-workspaceEnrichmentTTL - time.Second),
	}

	stale := srv.toCachedWorkspaceResponse(&summary)
	require.NotNil(stale.CommitsAhead)
	assert.Equal(2, *stale.CommitsAhead)
	assert.Equal("stale", stale.EnrichmentStatus)
	require.NotNil(stale.EnrichmentRefreshedAt)
	assert.Nil(stale.EnrichmentError)

	srv.workspaceEnrichmentMu.Lock()
	entry := srv.workspaceEnrichmentCache[summary.ID]
	entry.lastAttemptAt = now
	entry.lastError = "tmux activity probe failed"
	srv.workspaceEnrichmentCache[summary.ID] = entry
	srv.workspaceEnrichmentMu.Unlock()

	failed := srv.toCachedWorkspaceResponse(&summary)
	assert.Equal("failed", failed.EnrichmentStatus)
	require.NotNil(failed.EnrichmentError)
	assert.Equal("tmux activity probe failed", *failed.EnrichmentError)
}

func TestWorkspaceEnrichmentRefreshFailurePreservesLastKnownGood(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-tmux")
	require.NoError(os.WriteFile(script, []byte("#!/bin/sh\nexit 1\n"), 0o755))
	_, _, _, srv := setupWrapperServerWithScriptAndDBAndServer(t, script)
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	srv.now = func() time.Time { return now }
	worktree := filepath.Join(dir, "worktree")
	remote := filepath.Join(dir, "remote.git")
	runGit(t, dir, "init", "--bare", "--initial-branch=main", remote)
	require.NoError(os.MkdirAll(worktree, 0o755))
	runGit(t, worktree, "init", "--initial-branch=main")
	runGit(t, worktree, "config", "user.email", "test@test.com")
	runGit(t, worktree, "config", "user.name", "Test")
	require.NoError(os.WriteFile(filepath.Join(worktree, "base.txt"), []byte("base\n"), 0o644))
	runGit(t, worktree, "add", ".")
	runGit(t, worktree, "commit", "-m", "base")
	runGit(t, worktree, "remote", "add", "origin", remote)
	runGit(t, worktree, "push", "-u", "origin", "main")
	title := "last known title"
	trackerNow := now.Add(-tmuxSampleMinInterval - time.Second)
	srv.tmuxActivity = newTmuxActivityTracker(func() time.Time { return trackerNow })
	srv.tmuxActivity.Update("missing-session", tmuxActivityObservation{
		PaneTitle: title,
		Output:    "last known output",
		HasOutput: true,
	})
	trackerNow = now
	lastGood := workspaceEnrichmentCacheEntry{
		response: workspaceResponse{
			TmuxPaneTitle:      &title,
			TmuxWorking:        true,
			TmuxActivitySource: tmuxActivitySourceTitle,
		},
		hasTmux:         true,
		tmuxRefreshedAt: now.Add(-workspaceEnrichmentTTL - time.Second),
	}
	srv.workspaceEnrichmentMu.Lock()
	srv.workspaceEnrichmentCache["ws-failed-refresh"] = lastGood
	srv.workspaceEnrichmentMu.Unlock()

	srv.scheduleWorkspaceEnrichment(db.WorkspaceSummary{Workspace: db.Workspace{
		ID:           "ws-failed-refresh",
		WorktreePath: worktree,
		TmuxSession:  "missing-session",
		Status:       "ready",
	}})

	require.Eventually(func() bool {
		srv.workspaceEnrichmentMu.Lock()
		defer srv.workspaceEnrichmentMu.Unlock()
		_, pending := srv.workspaceEnrichmentPending["ws-failed-refresh"]
		_, inFlight := srv.workspaceEnrichmentInFlight["ws-failed-refresh"]
		entry := srv.workspaceEnrichmentCache["ws-failed-refresh"]
		return !pending && !inFlight && !entry.lastAttemptAt.IsZero()
	}, 2*time.Second, 10*time.Millisecond)
	srv.workspaceEnrichmentMu.Lock()
	got := srv.workspaceEnrichmentCache["ws-failed-refresh"]
	srv.workspaceEnrichmentMu.Unlock()
	assert.Equal(lastGood.response.TmuxPaneTitle, got.response.TmuxPaneTitle)
	assert.Equal(lastGood.response.TmuxWorking, got.response.TmuxWorking)
	assert.Equal(lastGood.response.TmuxActivitySource, got.response.TmuxActivitySource)
	assert.Equal(now, got.divergenceRefreshedAt)
	assert.Equal(lastGood.tmuxRefreshedAt, got.tmuxRefreshedAt)
	assert.Equal(now, got.lastAttemptAt)
	assert.Contains(got.lastError, "exit status 1")

	missingSummary := db.WorkspaceSummary{Workspace: db.Workspace{
		ID:           "ws-partial-refresh",
		WorktreePath: worktree,
		TmuxSession:  "missing-session-2",
		Status:       "ready",
	}}
	srv.scheduleWorkspaceEnrichment(missingSummary)
	require.Eventually(func() bool {
		srv.workspaceEnrichmentMu.Lock()
		defer srv.workspaceEnrichmentMu.Unlock()
		_, pending := srv.workspaceEnrichmentPending[missingSummary.ID]
		_, inFlight := srv.workspaceEnrichmentInFlight[missingSummary.ID]
		entry := srv.workspaceEnrichmentCache[missingSummary.ID]
		return !pending && !inFlight && !entry.lastAttemptAt.IsZero()
	}, 2*time.Second, 10*time.Millisecond)
	partial := srv.toCachedWorkspaceResponse(&missingSummary)
	assert.Equal(tmuxActivitySourceUnknown, partial.TmuxActivitySource)
	assert.Equal(workspaceEnrichmentFailed, partial.EnrichmentStatus)
}

func TestWorkspaceEnrichmentUsesBoundedWorkersPastBackgroundCapacity(t *testing.T) {
	require := require.New(t)
	_, _, _, _, srv := setupTestServerWithWorkspacesServer(t, nil)
	srv.workspaceEnrichmentDisabled = false
	for range cap(srv.workspaceEnrichmentSlots) {
		srv.workspaceEnrichmentSlots <- struct{}{}
	}
	t.Cleanup(func() {
		for range cap(srv.workspaceEnrichmentSlots) {
			<-srv.workspaceEnrichmentSlots
		}
	})

	for i := range 12 {
		srv.scheduleWorkspaceEnrichment(db.WorkspaceSummary{Workspace: db.Workspace{
			ID:     "ws-" + string(rune('a'+i)),
			Status: "ready",
		}})
	}

	srv.workspaceEnrichmentMu.Lock()
	pending := len(srv.workspaceEnrichmentPending)
	workers := srv.workspaceEnrichmentWorkers
	inFlight := len(srv.workspaceEnrichmentInFlight)
	srv.workspaceEnrichmentMu.Unlock()
	require.Equal(12, pending)
	require.Equal(cap(srv.workspaceEnrichmentSlots), workers)
	require.Zero(inFlight)
}

func TestWorkspaceTmuxPruneUsesEnrichmentBackgroundCapacity(t *testing.T) {
	_, _, _, _, srv := setupTestServerWithWorkspacesServer(t, nil)
	srv.workspaceEnrichmentDisabled = false
	for range cap(srv.workspaceEnrichmentSlots) {
		srv.workspaceEnrichmentSlots <- struct{}{}
	}
	t.Cleanup(func() {
		for range cap(srv.workspaceEnrichmentSlots) {
			<-srv.workspaceEnrichmentSlots
		}
	})

	srv.scheduleWorkspaceTmuxPrune()

	srv.workspaceEnrichmentMu.Lock()
	pending := srv.workspaceTmuxPrunePending
	inFlight := srv.workspaceTmuxPruneInFlight
	srv.workspaceEnrichmentMu.Unlock()
	assert.True(t, pending)
	assert.False(t, inFlight)
}

func TestWorkspacePushInvalidatesCachedDivergence(t *testing.T) {
	require := require.New(t)
	client, _, _, _, srv := setupTestServerWithWorkspacesServer(t, nil)
	srv.workspaceEnrichmentDisabled = false
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)
	runGit(t, ws.WorktreePath, "config", "user.email", "test@test.com")
	runGit(t, ws.WorktreePath, "config", "user.name", "Test")
	require.NoError(os.WriteFile(
		filepath.Join(ws.WorktreePath, "ahead.txt"), []byte("ahead\n"), 0o644,
	))
	runGit(t, ws.WorktreePath, "add", ".")
	runGit(t, ws.WorktreePath, "commit", "-m", "ahead")
	ahead := 1
	behind := 0
	srv.workspaceEnrichmentMu.Lock()
	srv.workspaceEnrichmentCache[ws.Id] = workspaceEnrichmentCacheEntry{
		response: workspaceResponse{
			CommitsAhead:  &ahead,
			CommitsBehind: &behind,
		},
		hasDivergence:         true,
		divergenceRefreshedAt: srv.now(),
	}
	srv.workspaceEnrichmentMu.Unlock()

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/workspaces/"+ws.Id+"/push", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	getResp, err := client.HTTP.GetWorkspaceWithResponse(ctx, ws.Id)
	require.NoError(err)
	require.Equal(http.StatusOK, getResp.StatusCode())
	require.NotNil(getResp.JSON200)
	require.NotNil(getResp.JSON200.CommitsAhead)
	assert.Zero(t, *getResp.JSON200.CommitsAhead)
}

func TestWorkspaceRuntimeExitInvalidatesCachedTmuxEnrichment(t *testing.T) {
	_, _, _, _, srv := setupTestServerWithWorkspacesServer(t, nil)
	srv.workspaceEnrichmentCache["ws-runtime"] = workspaceEnrichmentCacheEntry{
		hasTmux:         true,
		tmuxRefreshedAt: srv.now(),
	}

	srv.handleRuntimeSessionExit(localruntime.SessionInfo{
		WorkspaceID: "ws-runtime",
		Key:         "agent",
		CreatedAt:   srv.now(),
	})

	assert.NotContains(t, srv.workspaceEnrichmentCache, "ws-runtime")
}
