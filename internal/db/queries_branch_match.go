package db

import (
	"context"
	"fmt"
)

// WorktreeRepoRef is a registered worktree paired with its owning project's
// linked repository identity. It is the enumeration step of the
// worktree-to-merge-request branch-match recompute: only worktrees whose
// project links a middleman_repos row are returned, since a local-only project
// cannot match a platform merge request. Stale, empty-branch, and detached-HEAD
// worktrees are excluded because none of them can branch-match an open merge
// request; discovery stores detached checkouts with the synthetic labels
// "detached" or "detached/<short-sha>" rather than an empty branch. The platform identity rides along so the recompute can derive the
// watched-MR set without a second per-worktree repo fetch.
type WorktreeRepoRef struct {
	Path     string
	Branch   string
	RepoID   int64
	Platform string
	Host     string
	Owner    string
	Name     string
}

// ListWorktreesForBranchMatch returns every registered worktree that can
// branch-match an open merge request: its project links a repo, its branch is
// non-empty, and it is not stale. The inner join on middleman_repos excludes
// local-only projects, and each row carries the repo's id and platform identity
// so the recompute can both scope the merge-request lookup and build watched-MR
// entries from one read.
func (d *DB) ListWorktreesForBranchMatch(ctx context.Context) ([]WorktreeRepoRef, error) {
	rows, err := d.ro.QueryContext(ctx, `
		SELECT w.path, w.branch,
		       r.id, r.platform, r.platform_host, r.owner, r.name
		FROM middleman_project_worktrees w
		JOIN middleman_projects p ON p.id = w.project_id
		JOIN middleman_repos r ON r.id = p.repo_id
		WHERE w.branch != ''
		  AND w.branch != 'detached'
		  AND w.branch NOT LIKE 'detached/%'
		  AND w.is_stale = 0
		ORDER BY w.path`)
	if err != nil {
		return nil, fmt.Errorf("list worktrees for branch match: %w", err)
	}
	defer rows.Close()

	var refs []WorktreeRepoRef
	for rows.Next() {
		var ref WorktreeRepoRef
		if err := rows.Scan(
			&ref.Path, &ref.Branch,
			&ref.RepoID, &ref.Platform, &ref.Host, &ref.Owner, &ref.Name,
		); err != nil {
			return nil, fmt.Errorf("scan worktree repo ref: %w", err)
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate worktrees for branch match: %w", err)
	}
	return refs, nil
}

// WorktreeLinkPR is a worktree-to-merge-request link joined to the linked merge
// request's display fields. The fleet snapshot keys these by WorktreeKey to
// overlay branch-matched pull-request metadata onto registered worktrees.
// State and IsDraft are surfaced raw so the snapshot producer can fold an open
// draft into a "draft" display state without re-reading the merge request.
type WorktreeLinkPR struct {
	WorktreeKey string
	Number      int
	State       MergeRequestState
	IsDraft     bool
	Title       string
	CIStatus    string
}

// ListWorktreeLinkPRs returns every worktree link joined to its merge request's
// display fields, ordered by worktree key. It is the snapshot-side read of the
// durable links the branch-match recompute writes; the merge-request join keeps
// state and CI status current as of the last sync without re-deriving the
// branch match at snapshot time.
func (d *DB) ListWorktreeLinkPRs(ctx context.Context) ([]WorktreeLinkPR, error) {
	rows, err := d.ro.QueryContext(ctx, `
		SELECT l.worktree_key, m.number, m.state, m.is_draft, m.title, m.ci_status
		FROM middleman_mr_worktree_links l
		JOIN middleman_merge_requests m ON m.id = l.merge_request_id
		ORDER BY l.worktree_key`)
	if err != nil {
		return nil, fmt.Errorf("list worktree link PRs: %w", err)
	}
	defer rows.Close()

	var prs []WorktreeLinkPR
	for rows.Next() {
		var pr WorktreeLinkPR
		if err := rows.Scan(
			&pr.WorktreeKey, &pr.Number, &pr.State,
			&pr.IsDraft, &pr.Title, &pr.CIStatus,
		); err != nil {
			return nil, fmt.Errorf("scan worktree link PR: %w", err)
		}
		prs = append(prs, pr)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate worktree link PRs: %w", err)
	}
	return prs, nil
}
