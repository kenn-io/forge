package pullapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/platform"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/workspace"
)

func TestExecuteWorkspaceCleanupReportsOutcomeWithoutChangingMergeSuccess(t *testing.T) {
	tests := []struct {
		name      string
		plan      workspaceCleanupPlan
		dirty     []string
		deleteErr error
		want      *WorkspaceCleanupResult
		wantCalls int
	}{
		{
			name: "not requested",
		},
		{
			name: "requested without a linked workspace",
			plan: workspaceCleanupPlan{Requested: true},
			want: &WorkspaceCleanupResult{Status: workspaceCleanupNotFoundAtSubmission},
		},
		{
			name:      "deleted",
			plan:      workspaceCleanupPlan{Requested: true, WorkspaceID: "ws-old"},
			want:      &WorkspaceCleanupResult{WorkspaceID: "ws-old", Status: workspaceCleanupDeleted},
			wantCalls: 1,
		},
		{
			name:      "already absent",
			plan:      workspaceCleanupPlan{Requested: true, WorkspaceID: "ws-old"},
			deleteErr: workspace.ErrWorkspaceNotFound,
			want:      &WorkspaceCleanupResult{WorkspaceID: "ws-old", Status: workspaceCleanupAlreadyAbsent},
			wantCalls: 1,
		},
		{
			name:  "dirty workspace remains",
			plan:  workspaceCleanupPlan{Requested: true, WorkspaceID: "ws-old"},
			dirty: []string{"notes.txt", "draft.patch"},
			want: &WorkspaceCleanupResult{
				WorkspaceID: "ws-old",
				Status:      workspaceCleanupFailed,
				Warning:     "workspace has uncommitted changes: notes.txt, draft.patch",
			},
			wantCalls: 1,
		},
		{
			name:      "delete error remains a cleanup warning",
			plan:      workspaceCleanupPlan{Requested: true, WorkspaceID: "ws-old"},
			deleteErr: errors.New("tmux cleanup failed"),
			want: &WorkspaceCleanupResult{
				WorkspaceID: "ws-old",
				Status:      workspaceCleanupFailed,
				Warning:     "tmux cleanup failed",
			},
			wantCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			handler := New(Deps{
				DeleteWorkspace: func(_ context.Context, id string, force bool) ([]string, error) {
					calls++
					assert.Equal(t, "ws-old", id)
					assert.False(t, force)
					return tt.dirty, tt.deleteErr
				},
			})

			got := handler.executeWorkspaceCleanup(t.Context(), tt.plan)

			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantCalls, calls)
		})
	}
}

func TestCaptureWorkspaceCleanupPlanPinsCurrentWorkspaceID(t *testing.T) {
	database := dbtest.Open(t)
	manager := workspace.NewManager(database, t.TempDir())
	repo := db.Repo{
		Platform:     string(platform.KindGitHub),
		PlatformHost: "github.com",
		Owner:        "acme",
		Name:         "widget",
		RepoPath:     "acme/widget",
	}
	insertWorkspace := func(id string, createdAt time.Time) {
		require.NoError(t, database.InsertWorkspace(t.Context(), &db.Workspace{
			ID:           id,
			Platform:     repo.Platform,
			PlatformHost: repo.PlatformHost,
			RepoOwner:    repo.Owner,
			RepoName:     repo.Name,
			ItemType:     db.WorkspaceItemTypePullRequest,
			ItemNumber:   7,
			ItemKey:      "7",
			WorktreePath: t.TempDir(),
			Status:       "ready",
			CreatedAt:    createdAt,
		}))
	}
	insertWorkspace("ws-old", time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC))
	handler := New(Deps{Workspaces: manager})

	plan, err := handler.captureWorkspaceCleanupPlan(t.Context(), repo, 7, true)
	require.NoError(t, err)
	require.NoError(t, database.DeleteWorkspace(t.Context(), "ws-old"))
	insertWorkspace("ws-new", time.Date(2026, 8, 5, 13, 0, 0, 0, time.UTC))

	assert.Equal(t, workspaceCleanupPlan{Requested: true, WorkspaceID: "ws-old"}, plan)
}
