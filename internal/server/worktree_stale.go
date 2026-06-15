package server

import (
	"context"
	"errors"
	"os"
	"strings"

	gitcmd "go.kenn.io/kit/git/cmd"
	"go.kenn.io/middleman/internal/db"
)

// worktreeScopedKeyPrefix is the fleet scoped-key prefix for worktrees. The
// remove-stale route accepts a scoped key and recovers the on-disk path from it,
// matching how the fleet snapshot builds the key from the stored path.
const worktreeScopedKeyPrefix = "worktree:"

type removeStaleWorktreeInput struct {
	Body struct {
		ScopedKey    string `json:"scopedKey"`
		RemoveBranch bool   `json:"removeBranch,omitempty"`
	}
}

type removeStaleWorktreeOutput struct {
	Body struct {
		Removed bool `json:"removed"`
	}
}

// removeStaleWorktree handles POST /api/v1/worktrees/remove-stale. It drops a
// worktree that discovery has flagged stale — its checkout vanished from
// `git worktree list` — addressed by fleet scoped key.
//
// The worktree must be stale and its path must still be absent: a reappeared
// checkout is refused so a live worktree is never removed by a racing caller.
// Removing the registry row cascades the worktree's stored runtime tmux
// sessions. When removeBranch is set, the worktree's branch is deleted from the
// owning checkout on a best-effort basis (a missing branch never blocks the
// removal), mirroring the host behavior this route replaces.
func (s *Server) removeStaleWorktree(
	ctx context.Context, input *removeStaleWorktreeInput,
) (*removeStaleWorktreeOutput, error) {
	path := strings.TrimSpace(
		strings.TrimPrefix(strings.TrimSpace(input.Body.ScopedKey), worktreeScopedKeyPrefix),
	)
	if path == "" {
		return nil, problemValidation("body.scopedKey", "scopedKey is required")
	}

	worktree, err := s.db.GetProjectWorktreeByPath(ctx, path)
	if err != nil {
		if errors.Is(err, db.ErrProjectNotFound) {
			return nil, problemNotFound(CodeNotFound, "worktree not found", nil)
		}
		return nil, problemInternal("get worktree: " + err.Error())
	}
	if !worktree.IsStale {
		return nil, problemConflict(CodeConflict, "worktree is not stale", nil)
	}
	if _, statErr := os.Stat(worktree.Path); statErr == nil {
		return nil, problemConflict(
			CodeConflict, "worktree path still exists on disk", nil,
		)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		// A permission or I/O error is not evidence the checkout is gone;
		// refusing keeps a possibly-live worktree (and its branch) intact.
		return nil, problemInternal(
			"stat worktree path: " + statErr.Error(),
		)
	}

	if input.Body.RemoveBranch && strings.TrimSpace(worktree.Branch) != "" {
		if project, perr := s.db.GetProjectByID(ctx, worktree.ProjectID); perr == nil {
			_, _ = gitcmd.New().Output(
				ctx, project.LocalPath, "branch", "-D", "--", worktree.Branch,
			)
		}
	}

	if err := s.db.DeleteProjectWorktree(ctx, worktree.ProjectID, worktree.ID); err != nil {
		if errors.Is(err, db.ErrProjectNotFound) {
			return nil, problemNotFound(CodeNotFound, "worktree not found", nil)
		}
		return nil, problemInternal("delete worktree: " + err.Error())
	}
	s.recomputeWorktreeLinksNow(ctx)

	out := &removeStaleWorktreeOutput{}
	out.Body.Removed = true
	return out, nil
}
