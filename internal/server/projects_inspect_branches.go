package server

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/workspace/localruntime"
)

// Branch listing and worktree inspection back delete-confirmation and
// new-worktree UX: which branches exist, whether a worktree holds
// uncommitted work or live sessions, and whether its branch can be
// deleted safely. Fleet proxies expose both for remote hosts.

type listProjectBranchesInput struct {
	ProjectID string `path:"project_id"`
}

type listProjectBranchesOutput struct {
	Body struct {
		Branches []string `json:"branches"`
	}
}

type inspectProjectWorktreeInput struct {
	ProjectID  string `path:"project_id"`
	WorktreeID string `path:"worktree_id"`
}

type inspectProjectWorktreeOutput struct {
	Body struct {
		IsDirty           bool `json:"is_dirty"`
		DirtyFileCount    int  `json:"dirty_file_count"`
		AliveSessionCount int  `json:"alive_session_count"`
		CanDeleteBranch   bool `json:"can_delete_branch"`
		// BranchDeleteBlockedReason explains a false CanDeleteBranch:
		// primary checkout, detached HEAD, default branch, or the
		// branch being checked out in sibling worktrees.
		BranchDeleteBlockedReason string   `json:"branch_delete_blocked_reason,omitempty"`
		SiblingWorktreeIDs        []string `json:"sibling_worktree_ids,omitempty"`
	}
}

func (s *Server) listProjectBranches(
	ctx context.Context, input *listProjectBranchesInput,
) (*listProjectBranchesOutput, error) {
	project, err := s.db.GetProjectByID(ctx, input.ProjectID)
	if err != nil {
		if errors.Is(err, db.ErrProjectNotFound) {
			return nil, problemNotFound(CodeProjectNotFound, "project not found", nil)
		}
		return nil, problemInternal("get project: " + err.Error())
	}

	output, err := gitDiscoveryOutput(
		ctx, project.LocalPath,
		"for-each-ref", "--format=%(refname:short)", "refs/heads",
	)
	if err != nil {
		return nil, problemInternal("list branches: " + err.Error())
	}

	out := &listProjectBranchesOutput{}
	out.Body.Branches = []string{}
	for line := range strings.SplitSeq(output, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			out.Body.Branches = append(out.Body.Branches, trimmed)
		}
	}
	sort.Strings(out.Body.Branches)
	return out, nil
}

func (s *Server) inspectProjectWorktree(
	ctx context.Context, input *inspectProjectWorktreeInput,
) (*inspectProjectWorktreeOutput, error) {
	worktree, err := s.db.GetProjectWorktreeByID(ctx, input.WorktreeID)
	if err != nil {
		if errors.Is(err, db.ErrProjectNotFound) {
			return nil, problemNotFound(CodeNotFound, "worktree not found", nil)
		}
		return nil, problemInternal("get worktree: " + err.Error())
	}
	if worktree.ProjectID != input.ProjectID {
		return nil, problemNotFound(CodeNotFound, "worktree not found", nil)
	}
	project, err := s.db.GetProjectByID(ctx, input.ProjectID)
	if err != nil {
		if errors.Is(err, db.ErrProjectNotFound) {
			return nil, problemNotFound(CodeNotFound, "worktree not found", nil)
		}
		return nil, problemInternal("get project: " + err.Error())
	}

	out := &inspectProjectWorktreeOutput{}
	out.Body.IsDirty, out.Body.DirtyFileCount = worktreeDirtyState(ctx, worktree.Path)
	aliveCount, err := s.countAliveWorktreeSessions(ctx, input.ProjectID, worktree.ID)
	if err != nil {
		return nil, problemInternal("list worktree sessions: " + err.Error())
	}
	out.Body.AliveSessionCount = aliveCount

	canDelete, reason, siblings := s.branchDeleteEligibility(ctx, project, worktree)
	out.Body.CanDeleteBranch = canDelete
	out.Body.BranchDeleteBlockedReason = reason
	out.Body.SiblingWorktreeIDs = siblings
	return out, nil
}

// worktreeDirtyState counts changed and untracked files. A missing
// path reads as clean (deletion of a vanished checkout is safe), but
// any other stat or git failure reads as dirty with an unknown count:
// the delete-confirmation UI must warn rather than promise a clean
// checkout it could not actually inspect.
func worktreeDirtyState(ctx context.Context, path string) (bool, int) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, 0
		}
		return true, 0
	}
	output, err := gitDiscoveryOutput(
		ctx, path, "status", "--porcelain", "--untracked-files=all",
	)
	if err != nil {
		return true, 0
	}
	count := 0
	for line := range strings.SplitSeq(output, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count > 0, count
}

// countAliveWorktreeSessions counts through the same merged source the
// runtime listing serves: in-memory runtime sessions plus stored tmux
// rows, so durable sessions that survived a daemon restart still count.
func (s *Server) countAliveWorktreeSessions(
	ctx context.Context, projectID, worktreeID string,
) (int, error) {
	if s.runtime == nil {
		// Without a runtime manager the stored tmux rows are still
		// durable live sessions.
		stored, err := s.db.ListProjectWorktreeTmuxSessions(ctx, worktreeID)
		if err != nil {
			return 0, err
		}
		return len(stored), nil
	}
	sessions, err := s.projectWorktreeRuntimeSessions(
		ctx, projectID, worktreeID,
		projectWorktreeRuntimeScope(worktreeID),
	)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, session := range sessions {
		if session.Status == localruntime.SessionStatusRunning ||
			session.Status == localruntime.SessionStatusStarting {
			count++
		}
	}
	return count, nil
}

// isDetachedWorktreeBranch reports whether a stored branch value
// denotes a detached checkout: discovery stores detached worktrees
// under the synthetic "detached" / "detached/<sha>" labels rather
// than an empty branch.
func isDetachedWorktreeBranch(branch string) bool {
	branch = strings.TrimSpace(branch)
	return branch == "" || branch == "detached" ||
		strings.HasPrefix(branch, "detached/")
}

// branchDeleteEligibility mirrors the delete-from-disk protections:
// the primary checkout and the default branch are never deletable, a
// detached worktree has no branch to delete, and a branch checked out
// in sibling worktrees must not be deleted from under them.
func (s *Server) branchDeleteEligibility(
	ctx context.Context, project *db.Project, worktree *db.ProjectWorktree,
) (bool, string, []string) {
	if normPath(worktree.Path) == normPath(project.LocalPath) {
		return false, "The primary checkout's branch cannot be deleted", nil
	}
	if isDetachedWorktreeBranch(worktree.Branch) {
		return false, "Worktree is in detached HEAD state", nil
	}
	if project.DefaultBranch != "" && worktree.Branch == project.DefaultBranch {
		return false, "The default branch cannot be deleted", nil
	}

	rows, err := s.db.ListProjectWorktrees(ctx, project.ID)
	if err != nil {
		return false, "worktree listing unavailable: " + err.Error(), nil
	}
	var siblings []string
	for _, row := range rows {
		if row.ID == worktree.ID || row.IsStale {
			continue
		}
		if row.Branch == worktree.Branch {
			siblings = append(siblings, row.ID)
		}
	}
	if len(siblings) > 0 {
		return false, "Branch is checked out in another worktree", siblings
	}
	return true, "", nil
}
