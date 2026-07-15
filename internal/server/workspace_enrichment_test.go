package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/workspace/localruntime"
)

func TestWorkspaceEnrichmentSupersedeRejectsOlderRefreshAndPreservesCache(t *testing.T) {
	assert := assert.New(t)
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	srv := &Server{
		now:                            func() time.Time { return now },
		workspaceEnrichmentCache:       make(map[string]workspaceEnrichmentCacheEntry),
		workspaceEnrichmentGenerations: make(map[string]uint64),
	}

	oldGeneration := srv.workspaceEnrichmentGeneration("ws-1")
	ahead := 1
	srv.workspaceEnrichmentCache["ws-1"] = workspaceEnrichmentCacheEntry{
		response:              workspaceResponse{CommitsAhead: &ahead},
		hasDivergence:         true,
		divergenceRefreshedAt: now,
	}
	srv.supersedeWorkspaceEnrichment("ws-1")
	entry, recorded := srv.recordWorkspaceEnrichmentResult(
		"ws-1",
		oldGeneration,
		workspaceEnrichmentProbeResult{
			response:           workspaceResponse{CommitsAhead: &ahead},
			divergenceComplete: true,
		},
	)

	assert.False(recorded)
	assert.Equal(&ahead, entry.response.CommitsAhead)
	assert.Contains(srv.workspaceEnrichmentCache, "ws-1")
	assert.Equal(&ahead, srv.workspaceEnrichmentCache["ws-1"].response.CommitsAhead)
}

func TestWorkspaceEnrichmentRejectsResultAfterGenerationIsTrimmed(t *testing.T) {
	assert := assert.New(t)
	srv := &Server{
		now:                            time.Now,
		workspaceEnrichmentCache:       make(map[string]workspaceEnrichmentCacheEntry),
		workspaceEnrichmentGenerations: make(map[string]uint64),
	}
	ahead := 1

	_, recorded := srv.recordWorkspaceEnrichmentResult(
		"deleted-workspace",
		0,
		workspaceEnrichmentProbeResult{
			response:           workspaceResponse{CommitsAhead: &ahead},
			divergenceComplete: true,
		},
	)

	assert.False(recorded)
	assert.NotContains(srv.workspaceEnrichmentCache, "deleted-workspace")
}

func TestWorkspaceEnrichmentSupersededResponseUsesCurrentCacheState(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	srv := &Server{now: func() time.Time { return now }}
	summary := db.WorkspaceSummary{Workspace: db.Workspace{
		ID:     "ws-superseded",
		Status: "ready",
	}}
	currentAhead := 7
	entry := workspaceEnrichmentCacheEntry{
		response:              workspaceResponse{CommitsAhead: &currentAhead},
		hasDivergence:         true,
		divergenceRefreshedAt: now,
	}
	rejectedAhead := 1
	rejectedError := "rejected probe failed"
	result := workspaceEnrichmentProbeResult{response: workspaceResponse{
		CommitsAhead:     &rejectedAhead,
		TmuxWorking:      true,
		EnrichmentStatus: workspaceEnrichmentFailed,
		EnrichmentError:  &rejectedError,
	}}

	response := srv.workspaceResponseAfterEnrichmentAttempt(
		&summary, result, entry, false,
	)

	require.NotNil(response.CommitsAhead)
	assert.Equal(currentAhead, *response.CommitsAhead)
	assert.False(response.TmuxWorking)
	assert.Equal(workspaceEnrichmentFresh, response.EnrichmentStatus)
	assert.Nil(response.EnrichmentError)
}

func TestWorkspaceEnrichmentPendingJobUsesLatestSummary(t *testing.T) {
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

	srv.scheduleWorkspaceEnrichment(db.WorkspaceSummary{Workspace: db.Workspace{
		ID: "ws-latest", Status: "ready", WorktreePath: "/old",
	}})
	srv.scheduleWorkspaceEnrichment(db.WorkspaceSummary{Workspace: db.Workspace{
		ID: "ws-latest", Status: "ready", WorktreePath: "/new",
	}})

	srv.workspaceEnrichmentMu.Lock()
	pending := srv.workspaceEnrichmentPending["ws-latest"]
	srv.workspaceEnrichmentMu.Unlock()
	require.Equal("/new", pending.summary.WorktreePath)
}

func TestTrimWorkspaceEnrichmentCacheDropsDeletedPendingState(t *testing.T) {
	assert := assert.New(t)
	srv := &Server{
		workspaceEnrichmentCache: map[string]workspaceEnrichmentCacheEntry{
			"keep": {},
			"drop": {},
		},
		workspaceEnrichmentGenerations: map[string]uint64{
			"keep":            1,
			"drop":            2,
			"generation-only": 3,
		},
		workspaceEnrichmentPending: map[string]workspaceEnrichmentJob{
			"drop": {generation: 2},
		},
	}

	srv.trimWorkspaceEnrichmentCache([]db.WorkspaceSummary{{Workspace: db.Workspace{ID: "keep"}}})

	assert.Contains(srv.workspaceEnrichmentCache, "keep")
	assert.NotContains(srv.workspaceEnrichmentCache, "drop")
	assert.NotContains(srv.workspaceEnrichmentGenerations, "drop")
	assert.NotContains(srv.workspaceEnrichmentGenerations, "generation-only")
	assert.NotContains(srv.workspaceEnrichmentPending, "drop")
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

	synchronousSummary := missingSummary
	synchronousSummary.ID = "ws-synchronous-refresh"
	srv.workspaceEnrichmentMu.Lock()
	srv.workspaceEnrichmentCache[synchronousSummary.ID] = lastGood
	srv.workspaceEnrichmentMu.Unlock()
	synchronous := srv.refreshWorkspaceResponse(context.Background(), &synchronousSummary)
	assert.Equal(workspaceEnrichmentFailed, synchronous.EnrichmentStatus)
	require.NotNil(synchronous.EnrichmentError)
	assert.Contains(*synchronous.EnrichmentError, "exit status 1")
	require.NotNil(synchronous.CommitsAhead)
	require.NotNil(synchronous.CommitsBehind)
	require.NotNil(synchronous.TmuxPaneTitle)
	assert.Equal(title, *synchronous.TmuxPaneTitle)
	assert.True(synchronous.TmuxWorking)
	assert.Equal(tmuxActivitySourceTitle, synchronous.TmuxActivitySource)
	require.NotNil(synchronous.EnrichmentRefreshedAt)
	srv.workspaceEnrichmentMu.Lock()
	synchronousEntry := srv.workspaceEnrichmentCache[synchronousSummary.ID]
	srv.workspaceEnrichmentMu.Unlock()
	assert.True(synchronousEntry.hasTmux)
	assert.Equal(lastGood.response.TmuxPaneTitle, synchronousEntry.response.TmuxPaneTitle)
}

func TestWorkspaceEnrichmentCompletionBroadcastsWorkspaceStatusE2E(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	client, _, _, _, srv := setupTestServerWithWorkspacesServer(t, nil)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)
	srv.workspaceEnrichmentDisabled = false
	srv.workspaceEnrichmentMu.Lock()
	clear(srv.workspaceEnrichmentCache)
	srv.workspaceEnrichmentMu.Unlock()

	httpServer := httptest.NewServer(srv)
	t.Cleanup(httpServer.Close)
	req, err := http.NewRequestWithContext(
		ctx, http.MethodGet, httpServer.URL+"/api/v1/events", nil,
	)
	require.NoError(err)
	eventsResp, err := httpServer.Client().Do(req)
	require.NoError(err)
	t.Cleanup(func() { eventsResp.Body.Close() })
	require.Equal(http.StatusOK, eventsResp.StatusCode)

	initial, err := client.HTTP.GetWorkspaceWithResponse(ctx, ws.Id)
	require.NoError(err)
	require.Equal(http.StatusOK, initial.StatusCode())
	require.NotNil(initial.JSON200)
	assert.Equal("pending", string(initial.JSON200.EnrichmentStatus))

	scanner := bufio.NewScanner(eventsResp.Body)
	var frame sseFrame
	for {
		frame = readSSEFrameWithin(t, scanner, 5*time.Second, nil)
		if frame.Event != "workspace_status" {
			continue
		}
		var payload map[string]json.RawMessage
		require.NoError(json.Unmarshal([]byte(frame.Data), &payload))
		if len(payload) != 1 {
			continue
		}
		var id string
		require.NoError(json.Unmarshal(payload["id"], &id))
		if id == ws.Id {
			break
		}
	}
	assert.Equal("workspace_status", frame.Event)

	require.Eventually(func() bool {
		got, getErr := client.HTTP.GetWorkspaceWithResponse(ctx, ws.Id)
		return getErr == nil &&
			got.StatusCode() == http.StatusOK &&
			got.JSON200 != nil &&
			string(got.JSON200.EnrichmentStatus) == workspaceEnrichmentFresh
	}, 2*time.Second, 10*time.Millisecond)
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
