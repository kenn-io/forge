package workspaceapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/workspace"
)

func TestDeleteWorkspaceRejectsConcurrentSetup(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := dbtest.Open(t)
	base := t.TempDir()
	insertDeletionTestWorkspace(
		t, database, "ws-creating", filepath.Join(base, "missing"), "creating",
	)
	handler := New(Deps{
		DB:         database,
		Workspaces: workspace.NewManager(database, base),
	})

	_, err := handler.DeleteWorkspace(t.Context(), &DeleteWorkspaceInput{
		ID: "ws-creating",
	})
	problem, ok := err.(*httpapi.ProblemError)
	require.True(ok, "want *ProblemError, got %T", err)
	assert.Equal(http.StatusConflict, problem.Status)
	assert.Equal(httpapi.CodeWorkspaceSetupInProgress, problem.Code)
	assert.Contains(problem.Detail, "setup is still in progress")

	got, err := database.GetWorkspace(t.Context(), "ws-creating")
	require.NoError(err)
	require.NotNil(got)
	assert.Equal("creating", got.Status)
}

func TestDeleteWorkspaceReportsDeletionAlreadyInProgress(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := dbtest.Open(t)
	base := t.TempDir()
	insertDeletionTestWorkspace(
		t, database, "ws-deleting", filepath.Join(base, "missing"), "deleting",
	)
	handler := New(Deps{
		DB:         database,
		Workspaces: workspace.NewManager(database, base),
	})

	_, err := handler.DeleteWorkspace(t.Context(), &DeleteWorkspaceInput{
		ID: "ws-deleting",
	})
	problem, ok := err.(*httpapi.ProblemError)
	require.True(ok, "want *ProblemError, got %T", err)
	assert.Equal(http.StatusConflict, problem.Status)
	assert.Equal(httpapi.CodeWorkspaceDeletionInProgress, problem.Code)
	assert.Contains(problem.Detail, "deletion already in progress")
}

func insertDeletionTestWorkspace(
	t *testing.T, database *db.DB, id, path, status string,
) {
	t.Helper()
	require.NoError(t, database.InsertWorkspace(t.Context(), &db.Workspace{
		ID:              id,
		Platform:        "github",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      42,
		GitHeadRef:      "feature/delete",
		WorkspaceBranch: "kenn-forge/pr-42",
		WorktreePath:    path,
		TmuxSession:     "kenn-forge-" + id,
		Status:          status,
	}))
}

func TestQueueWorkspaceDeletionPersistsFailure(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	base := t.TempDir()
	worktreePath := filepath.Join(base, "ws-dirty")
	require.NoError(os.MkdirAll(worktreePath, 0o755))
	runGit(t, worktreePath, "init")
	require.NoError(os.WriteFile(
		filepath.Join(worktreePath, "notes.txt"), []byte("keep me\n"), 0o644,
	))
	insertDeletionTestWorkspace(t, database, "ws-dirty", worktreePath, "ready")

	var mu sync.Mutex
	var events []Event
	handler := New(Deps{
		DB:         database,
		Workspaces: workspace.NewManager(database, base),
		Broadcast: func(event Event) uint64 {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
			return 1
		},
	})
	handler.Start(t.Context(), true)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(handler.Shutdown(ctx))
	})

	require.NoError(handler.QueueWorkspaceDeletion("ws-dirty"))
	mu.Lock()
	require.NotEmpty(events)
	assert.Equal("workspace_status", events[0].Type)
	mu.Unlock()

	require.Eventually(func() bool {
		got, err := database.GetWorkspace(t.Context(), "ws-dirty")
		return err == nil && got != nil && got.Status == "deletion_failed"
	}, 3*time.Second, 10*time.Millisecond)
	got, err := database.GetWorkspace(t.Context(), "ws-dirty")
	require.NoError(err)
	require.NotNil(got)
	require.NotNil(got.ErrorMessage)
	assert.Contains(*got.ErrorMessage, "notes.txt")
}

func TestQueueWorkspaceDeletionIsIdempotentAfterRemoval(t *testing.T) {
	database := dbtest.Open(t)
	handler := New(Deps{
		DB:         database,
		Workspaces: workspace.NewManager(database, t.TempDir()),
	})

	require.NoError(t, handler.QueueWorkspaceDeletion("already-removed"))
}

func TestDeleteWorkspaceDirtyPreservesReadyStatus(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	base := t.TempDir()
	worktreePath := filepath.Join(base, "ws-dirty")
	require.NoError(os.MkdirAll(worktreePath, 0o755))
	runGit(t, worktreePath, "init")
	require.NoError(os.WriteFile(
		filepath.Join(worktreePath, "notes.txt"), []byte("keep me\n"), 0o644,
	))
	insertDeletionTestWorkspace(t, database, "ws-dirty", worktreePath, "ready")

	handler := New(Deps{
		DB:         database,
		Workspaces: workspace.NewManager(database, base),
	})
	_, err := handler.DeleteWorkspace(t.Context(), &DeleteWorkspaceInput{ID: "ws-dirty"})
	problem, ok := err.(*httpapi.ProblemError)
	require.True(ok, "want *ProblemError, got %T", err)
	assert.Equal(http.StatusConflict, problem.Status)
	assert.Equal(httpapi.CodeWorktreeDirty, problem.Code)

	got, err := database.GetWorkspace(t.Context(), "ws-dirty")
	require.NoError(err)
	require.NotNil(got)
	assert.Equal("ready", got.Status)
	assert.Nil(got.ErrorMessage)
}

func TestDeleteWorkspacePersistsFailureAfterAdmission(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	base := t.TempDir()
	script := filepath.Join(base, "failing-tmux")
	require.NoError(os.WriteFile(
		script,
		[]byte("#!/bin/sh\necho 'permission denied' >&2\nexit 1\n"),
		0o755,
	))
	insertDeletionTestWorkspace(
		t, database, "ws-failure", filepath.Join(base, "missing"), "ready",
	)
	manager := workspace.NewManager(database, base)
	manager.SetTmuxCommand([]string{script})
	var events []Event
	handler := New(Deps{
		DB:         database,
		Workspaces: manager,
		Broadcast: func(event Event) uint64 {
			events = append(events, event)
			return 1
		},
	})

	_, err := handler.DeleteWorkspace(t.Context(), &DeleteWorkspaceInput{
		ID: "ws-failure", Force: true,
	})
	require.Error(err)
	assert.Contains(err.Error(), "permission denied")

	got, err := database.GetWorkspace(t.Context(), "ws-failure")
	require.NoError(err)
	require.NotNil(got)
	assert.Equal("deletion_failed", got.Status)
	require.NotNil(got.ErrorMessage)
	assert.Contains(*got.ErrorMessage, "permission denied")
	require.Len(events, 2)
	assert.Equal("workspace_status", events[0].Type)
	assert.Equal("workspace_status", events[1].Type)
}

func TestDeleteWorkspacePublishesConfirmedIdentity(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	base := t.TempDir()
	insertDeletionTestWorkspace(
		t, database, "ws-delete", filepath.Join(base, "missing"), "ready",
	)
	var events []Event
	handler := New(Deps{
		DB:         database,
		Workspaces: workspace.NewManager(database, base),
		Broadcast: func(event Event) uint64 {
			events = append(events, event)
			return 1
		},
	})

	_, err := handler.DeleteWorkspace(t.Context(), &DeleteWorkspaceInput{
		ID: "ws-delete", Force: true,
	})
	require.NoError(err)
	got, err := database.GetWorkspace(t.Context(), "ws-delete")
	require.NoError(err)
	assert.Nil(got)

	require.Len(events, 3)
	assert.Equal("workspace_status", events[0].Type)
	assert.Equal("workspace_deleted", events[1].Type)
	deleted, ok := events[1].Data.(workspaceDeletedEventData)
	require.True(ok)
	assert.Equal(workspaceDeletedEventData{
		WorkspaceID:  "ws-delete",
		Provider:     "github",
		PlatformHost: "github.com",
		Owner:        "acme",
		Name:         "widget",
		RepoPath:     "acme/widget",
		Number:       42,
		ItemType:     db.WorkspaceItemTypePullRequest,
	}, deleted)
	assert.Equal("workspace_status", events[2].Type)

	_, err = handler.DeleteWorkspace(t.Context(), &DeleteWorkspaceInput{
		ID: "ws-delete", Force: true,
	})
	require.NoError(err)
	assert.Len(events, 3, "an idempotent retry must not publish a second deletion")
}

func TestStartMarksInterruptedWorkspaceDeletionFailed(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	insertDeletionTestWorkspace(t, database, "ws-interrupted", t.TempDir(), "deleting")
	handler := New(Deps{DB: database})
	handler.Start(t.Context(), true)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(handler.Shutdown(ctx))
	})

	got, err := database.GetWorkspace(t.Context(), "ws-interrupted")
	require.NoError(err)
	require.NotNil(got)
	assert.Equal("deletion_failed", got.Status)
	require.NotNil(got.ErrorMessage)
	assert.Equal(interruptedWorkspaceDeletionMessage, *got.ErrorMessage)
}

func TestStartMarksInterruptedWorkspaceSetupFailed(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	insertDeletionTestWorkspace(t, database, "ws-interrupted", t.TempDir(), "creating")
	handler := New(Deps{DB: database})
	handler.Start(t.Context(), true)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(handler.Shutdown(ctx))
	})

	got, err := database.GetWorkspace(t.Context(), "ws-interrupted")
	require.NoError(err)
	require.NotNil(got)
	assert.Equal("error", got.Status)
	require.NotNil(got.ErrorMessage)
	assert.Equal(interruptedWorkspaceSetupMessage, *got.ErrorMessage)
}
