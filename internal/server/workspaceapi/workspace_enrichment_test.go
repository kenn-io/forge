package workspaceapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/agentactivity"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/workspace"
	"go.kenn.io/forge/internal/workspace/localruntime"
	gitcmd "go.kenn.io/kit/git/cmd"
)

func newEnrichmentTestHandler(t *testing.T, tmuxScript string) *Handler {
	t.Helper()
	database := dbtest.Open(t)
	manager := workspace.NewManager(database, t.TempDir())
	if tmuxScript != "" {
		manager.SetTmuxCommand([]string{tmuxScript})
	}
	ctx, cancel := context.WithCancel(context.Background())
	handler := New(Deps{
		DB:         database,
		Workspaces: manager,
	})
	handler.Start(ctx, true)
	t.Cleanup(func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
		defer shutdownCancel()
		require.NoError(t, handler.Shutdown(shutdownCtx))
	})
	return handler
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	_, stderr, err := gitcmd.New().WithConfig("init.defaultBranch", "main").Run(
		t.Context(), dir, nil, args...,
	)
	require.NoError(t, err, "git %v failed: %s", args, stderr)
}

func TestFormatAgentActivityUpdatedAtPreservesSubsecondPrecision(t *testing.T) {
	t.Parallel()
	updatedAt := time.Date(2026, 7, 28, 12, 0, 0, 123456789, time.UTC)

	assert.Equal(t, "2026-07-28T12:00:00.123456789Z", formatAgentActivityUpdatedAt(updatedAt))
}

func TestWorkspaceEnrichmentRestoresDivergenceAfterObserverHealsUpstream(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	seed := filepath.Join(dir, "seed")
	worktree := filepath.Join(dir, "worktree")

	runGit(t, dir, "init", "--bare", "--initial-branch=main", remote)
	runGit(t, dir, "clone", remote, seed)
	runGit(t, seed, "config", "user.email", "test@test.com")
	runGit(t, seed, "config", "user.name", "Test")
	require.NoError(os.WriteFile(filepath.Join(seed, "base.txt"), []byte("base\n"), 0o644))
	runGit(t, seed, "add", ".")
	runGit(t, seed, "commit", "-m", "base")
	runGit(t, seed, "push", "-u", "origin", "main")
	runGit(t, seed, "checkout", "-b", "feature")
	require.NoError(os.WriteFile(filepath.Join(seed, "feature.txt"), []byte("feature\n"), 0o644))
	runGit(t, seed, "add", ".")
	runGit(t, seed, "commit", "-m", "feature")
	runGit(t, seed, "push", "-u", "origin", "feature")
	runGit(t, dir, "clone", remote, worktree)
	runGit(t, worktree, "checkout", "feature")
	runGit(t, worktree, "config", "user.email", "test@test.com")
	runGit(t, worktree, "config", "user.name", "Test")
	require.NoError(os.WriteFile(filepath.Join(worktree, "ahead.txt"), []byte("ahead\n"), 0o644))
	runGit(t, worktree, "add", ".")
	runGit(t, worktree, "commit", "-m", "ahead")
	runGit(t, worktree, "config", "--unset", "branch.feature.remote")
	runGit(t, worktree, "config", "--unset", "branch.feature.merge")

	database := dbtest.Open(t)
	identity := db.GitHubRepoIdentity("github.com", "acme", "widget")
	identity.PlatformRepoID = "repo-acme-widget"
	repoID, err := database.UpsertRepo(
		t.Context(), identity,
	)
	require.NoError(err)
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	seedMR := func(headRepoCloneURL string) {
		_, err := database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
			RepoID:           repoID,
			PlatformID:       1000,
			Number:           1,
			URL:              "https://github.com/acme/widget/pull/1",
			Title:            "Test PR #1",
			Author:           "testuser",
			State:            db.MergeRequestStateOpen,
			HeadBranch:       "feature",
			HeadRepoCloneURL: headRepoCloneURL,
			BaseBranch:       "main",
			CreatedAt:        now,
			UpdatedAt:        now,
			LastActivityAt:   now,
		})
		require.NoError(err)
	}
	seedMR("https://github.com/contributor/widget.git")
	require.NoError(database.InsertWorkspace(t.Context(), &db.Workspace{
		ID:              "ws-upstream-heal",
		Platform:        "github",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      1,
		GitHeadRef:      "feature",
		WorkspaceBranch: "feature",
		WorktreePath:    worktree,
		Status:          "ready",
		CreatedAt:       now,
	}))
	summary, err := database.GetWorkspaceSummary(t.Context(), "ws-upstream-heal")
	require.NoError(err)
	require.NotNil(summary)

	clockNow := now
	manager := workspace.NewManager(database, filepath.Join(dir, "managed-worktrees"))
	handler := New(Deps{
		DB:         database,
		Workspaces: manager,
		Now:        func() time.Time { return clockNow },
	})
	ctx, cancel := context.WithCancel(context.Background())
	handler.Start(ctx, true)
	t.Cleanup(func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
		defer shutdownCancel()
		require.NoError(handler.Shutdown(shutdownCtx))
	})

	broken := handler.refreshWorkspaceResponse(t.Context(), summary)
	assert.Nil(broken.CommitsAhead)
	assert.Nil(broken.CommitsBehind)
	assert.Equal(workspaceEnrichmentFresh, broken.EnrichmentStatus)

	seedMR("https://github.com/acme/widget.git")
	handler.runWorkspacePushedHeadObserverPass(t.Context())
	clockNow = now.Add(workspaceEnrichmentTTL + time.Second)

	var healed workspaceResponse
	require.Eventually(func() bool {
		healed = handler.toCachedWorkspaceResponse(summary)
		return healed.CommitsAhead != nil && healed.CommitsBehind != nil &&
			*healed.CommitsAhead == 1 && *healed.CommitsBehind == 0 &&
			healed.EnrichmentStatus == workspaceEnrichmentFresh
	}, 2*time.Second, 10*time.Millisecond)
	require.NotNil(healed.CommitsAhead)
	require.NotNil(healed.CommitsBehind)
	assert.Equal(1, *healed.CommitsAhead)
	assert.Equal(0, *healed.CommitsBehind)
}

func TestWorkspaceEnrichmentSupersedeRejectsOlderRefreshAndPreservesCache(t *testing.T) {
	assert := assert.New(t)
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	srv := &Handler{
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
	entry, recorded, _ := srv.recordWorkspaceEnrichmentResult(
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
	srv := &Handler{
		now:                            time.Now,
		workspaceEnrichmentCache:       make(map[string]workspaceEnrichmentCacheEntry),
		workspaceEnrichmentGenerations: make(map[string]uint64),
	}
	ahead := 1

	_, recorded, _ := srv.recordWorkspaceEnrichmentResult(
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
	srv := &Handler{now: func() time.Time { return now }}
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
	srv := newEnrichmentTestHandler(t, "")
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
	srv := &Handler{
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
	srv := newEnrichmentTestHandler(t, "")
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
	srv := newEnrichmentTestHandler(t, script)
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

func TestWorkspaceEnrichmentBroadcastsOnlyDurableChanges(t *testing.T) {
	assert := assert.New(t)
	srv := &Handler{
		workspaceEnrichmentCache:       make(map[string]workspaceEnrichmentCacheEntry),
		workspaceEnrichmentGenerations: map[string]uint64{"ws-1": 1},
		now: func() time.Time {
			return time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
		},
	}
	ahead := 1
	behind := 0
	title := "pane"
	record := func(result workspaceEnrichmentProbeResult) bool {
		_, recorded, changed := srv.recordWorkspaceEnrichmentResult("ws-1", 1, result)
		assert.True(recorded)
		return changed
	}

	// First completion is the pending -> fresh transition clients wait on.
	assert.True(record(workspaceEnrichmentProbeResult{
		response:           workspaceResponse{CommitsAhead: &ahead, CommitsBehind: &behind, TmuxPaneTitle: &title},
		divergenceComplete: true,
		tmuxComplete:       true,
	}))

	// Tmux-activity-only movement (a busy agent changes it every probe)
	// must not notify: broadcasting it re-poked every open view forever.
	spinnerTitle := "pane *"
	assert.False(record(workspaceEnrichmentProbeResult{
		response: workspaceResponse{
			CommitsAhead:  &ahead,
			CommitsBehind: &behind,
			TmuxPaneTitle: &spinnerTitle,
			TmuxWorking:   true,
		},
		divergenceComplete: true,
		tmuxComplete:       true,
	}))

	// Divergence movement notifies.
	newBehind := 3
	assert.True(record(workspaceEnrichmentProbeResult{
		response:           workspaceResponse{CommitsAhead: &ahead, CommitsBehind: &newBehind, TmuxPaneTitle: &spinnerTitle},
		divergenceComplete: true,
		tmuxComplete:       true,
	}))

	// A new failure notifies once; the same repeated failure stays silent.
	assert.True(record(workspaceEnrichmentProbeResult{err: errors.New("boom")}))
	assert.False(record(workspaceEnrichmentProbeResult{err: errors.New("boom")}))

	// Recovery notifies.
	assert.True(record(workspaceEnrichmentProbeResult{
		response:           workspaceResponse{CommitsAhead: &ahead, CommitsBehind: &newBehind, TmuxPaneTitle: &spinnerTitle},
		divergenceComplete: true,
		tmuxComplete:       true,
	}))
}

func TestWorkspaceEnrichmentUsesBoundedWorkersPastBackgroundCapacity(t *testing.T) {
	require := require.New(t)
	srv := newEnrichmentTestHandler(t, "")
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
	srv := newEnrichmentTestHandler(t, "")
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

func TestWorkspaceRuntimeExitInvalidatesCachedTmuxEnrichment(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv := newEnrichmentTestHandler(t, "")
	activityRoot := t.TempDir()
	workspace := t.TempDir()
	srv.agentActivity = agentactivity.NewStore(activityRoot)
	payload, err := json.Marshal(map[string]string{
		"session_id":      "agent-session",
		"cwd":             workspace,
		"hook_event_name": "UserPromptSubmit",
	})
	require.NoError(err)
	require.NoError(srv.agentActivity.HandleHook(
		bytes.NewReader(payload), "agent-runtime",
	))
	srv.workspaceEnrichmentCache["ws-runtime"] = workspaceEnrichmentCacheEntry{
		hasTmux:         true,
		tmuxRefreshedAt: srv.now(),
	}

	srv.HandleRuntimeSessionExit(localruntime.SessionInfo{
		WorkspaceID: "ws-runtime",
		Key:         "agent-runtime",
		CreatedAt:   srv.now(),
	})

	assert.NotContains(srv.workspaceEnrichmentCache, "ws-runtime")
	_, ok := srv.agentActivity.SnapshotForWorkspace(
		workspace, []string{"agent-runtime"},
	)
	assert.False(ok)
}
