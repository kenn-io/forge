package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"go.kenn.io/forge/internal/db"
)

// ExternalWorktreePR is the open pull request a worktree outside Forge holds
// the head branch of.
type ExternalWorktreePR struct {
	WorktreePath string
	Branch       string
	Identity     db.PlatformIdentity
	Number       int
}

// FindExternalWorktreePR finds the pull request whose head the checkout at cwd
// holds. A remote URL names only a route, so a match also needs the pull
// request's head commit in the checkout's history: a reused branch name on an
// unrelated checkout never matches. Anything ambiguous matches nothing.
func (m *PRMonitor) FindExternalWorktreePR(
	ctx context.Context, cwd string, identify func(remoteURL string) *db.PlatformIdentity,
) (*ExternalWorktreePR, error) {
	root, err := externalWorktreeRoot(ctx, cwd)
	if err != nil || root == "" {
		return nil, err
	}
	branch, err := gitBranchName(ctx, root)
	if err != nil {
		return nil, err
	}
	if branch == "" {
		return nil, nil
	}
	remotes, err := gitOutput(ctx, root, "remote")
	if err != nil {
		return nil, fmt.Errorf("list remotes: %w", err)
	}
	var found *ExternalWorktreePR
	seen := make(map[db.PlatformIdentity]bool)
	for remote := range strings.FieldsSeq(remotes) {
		url, err := gitRemoteURL(ctx, root, remote)
		if err != nil {
			return nil, fmt.Errorf("read remote %s: %w", remote, err)
		}
		identity := identify(url)
		if identity == nil || seen[*identity] {
			continue
		}
		seen[*identity] = true
		mr, err := m.detectWorktreePR(ctx, &Workspace{
			Platform: identity.Platform, PlatformHost: identity.Host,
			RepoOwner: identity.Owner, RepoName: identity.Name,
			ItemType: db.WorkspaceItemTypeAdHoc, WorktreePath: root,
		})
		if err != nil {
			return nil, err
		}
		if mr == nil || !worktreeHoldsCommit(ctx, root, mr.PlatformHeadSHA) {
			continue
		}
		if found != nil {
			return nil, nil
		}
		found = &ExternalWorktreePR{WorktreePath: root, Branch: branch, Identity: *identity, Number: mr.Number}
	}
	return found, nil
}

// externalWorktreeRoot returns the resolved root of the checkout holding cwd, or
// empty when cwd is in no checkout.
func externalWorktreeRoot(ctx context.Context, cwd string) (string, error) {
	output, err := gitOutput(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		if _, ok := errors.AsType[*exec.ExitError](err); ok || errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("find checkout root: %w", err)
	}
	root, err := filepath.EvalSymlinks(strings.TrimSpace(output))
	if err != nil {
		return "", fmt.Errorf("resolve checkout root: %w", err)
	}
	return root, nil
}

// worktreeHoldsCommit reports whether sha is in the history of the checkout's
// HEAD. A provider that reports no head commit leaves only the branch to go by.
func worktreeHoldsCommit(ctx context.Context, root, sha string) bool {
	sha = strings.TrimSpace(sha)
	if sha == "" {
		return true
	}
	_, err := gitOutput(ctx, root, "merge-base", "--is-ancestor", sha, "HEAD")
	return err == nil
}

// setupExternalWorktree readies a workspace on a worktree Forge did not create.
// It changes nothing in the checkout: no fetch, branch move, or Git config.
func (m *Manager) setupExternalWorktree(ctx context.Context, ws *Workspace) error {
	info, err := os.Lstat(ws.WorktreePath)
	if err != nil {
		return m.failSetup(ctx, ws.ID, workspaceSetupStageWorktree, fmt.Errorf("external worktree: %w", err))
	}
	if !info.IsDir() {
		return m.failSetup(ctx, ws.ID, workspaceSetupStageWorktree, fmt.Errorf("external worktree %s is not a directory", ws.WorktreePath))
	}
	root, err := externalWorktreeRoot(ctx, ws.WorktreePath)
	if err != nil {
		return m.failSetup(ctx, ws.ID, workspaceSetupStageWorktree, err)
	}
	if root != ws.WorktreePath {
		return m.failSetup(ctx, ws.ID, workspaceSetupStageWorktree, fmt.Errorf("external worktree %s is no longer a checkout root", ws.WorktreePath))
	}
	branch, err := gitBranchName(ctx, root)
	if err != nil {
		return m.failSetup(ctx, ws.ID, workspaceSetupStageWorktree, err)
	}
	if branch != ws.WorkspaceBranch {
		return m.failSetup(ctx, ws.ID, workspaceSetupStageWorktree, fmt.Errorf("external worktree %s moved from branch %q to %q", ws.WorktreePath, ws.WorkspaceBranch, branch))
	}
	if err := m.EnsureTerminal(ctx, ws); err != nil {
		return m.failSetup(ctx, ws.ID, workspaceSetupStageTmuxSession, err)
	}
	m.recordSetupEvent(ctx, ws.ID, workspaceSetupStageSetup, "ready", "workspace ready")
	if err := m.updateWorkspaceStatus(ctx, ws.ID, "ready", nil); err != nil {
		return m.failSetup(ctx, ws.ID, workspaceSetupStageSetup, fmt.Errorf("update status to ready: %w", err))
	}
	ws.Status = "ready"
	return nil
}
