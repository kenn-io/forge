package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.kenn.io/kwt/worktree"
)

const hotWorktreeMarkerFile = "kenn-forge-hot-worktree"

type hotWorktreeRepository struct {
	CommonDir     string `json:"common_dir"`
	WorkspacePath string `json:"workspace_path"`
	StartRef      string `json:"start_ref"`
}

// WarmWorktrees prepares one spare for every repository that has had a workspace.
// It uses only existing local refs; setup still owns the fresh fetch and route
// validation. The caller owns scheduling and cancellation.
func (m *Manager) WarmWorktrees(ctx context.Context) error {
	var errs []error
	if err := m.rememberExistingWorkspaceRepositories(ctx); err != nil {
		errs = append(errs, err)
	}
	dir := filepath.Join(m.worktreeDir, ".hot-repositories")
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.Join(append(errs, err)...)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		var repo hotWorktreeRepository
		if err == nil {
			err = json.Unmarshal(data, &repo)
		}
		if err == nil {
			if _, statErr := os.Stat(repo.CommonDir); errors.Is(statErr, os.ErrNotExist) {
				continue // Removing the source checkout makes warming unavailable.
			}
			err = m.prepareHotWorktree(ctx, repo.CommonDir, repo.WorkspacePath, repo.StartRef)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("warm workspace repository %s: %w", entry.Name(), err))
		}
	}
	return errors.Join(errs...)
}

// Keep enrollment outside workspace records so closing the last workspace does
// not stop warming. The old workspace path supplies only destination placement.
func (m *Manager) rememberHotWorktreeRepository(ctx context.Context, gitDir, workspacePath, remote string) error {
	commonDir, err := worktreeCommonGitDir(ctx, gitDir)
	if err != nil {
		return err
	}
	commonDir, err = canonicalFilesystemPath(commonDir)
	if err != nil {
		return err
	}
	data, err := json.Marshal(hotWorktreeRepository{
		CommonDir: commonDir, WorkspacePath: workspacePath, StartRef: remoteShorthandRef(remote, "HEAD"),
	})
	if err != nil {
		return err
	}
	dir := filepath.Join(m.worktreeDir, ".hot-repositories")
	key := sha256.Sum256([]byte(hotWorktreePath(commonDir, workspacePath)))
	name := fmt.Sprintf("%x.json", key[:16])
	if current, err := os.ReadFile(filepath.Join(dir, name)); err == nil && bytes.Equal(current, data) {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeGeneratedFileAtomic(dir, name, data)
}

// Seed enrollment for workspaces created before hot checkouts were enabled.
func (m *Manager) rememberExistingWorkspaceRepositories(ctx context.Context) error {
	if m.db == nil {
		return nil
	}
	workspaces, err := m.db.ListWorkspaces(ctx)
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	var errs []error
	for _, ws := range workspaces {
		if err := ctx.Err(); err != nil {
			return err
		}
		if ws.Status != "ready" {
			continue
		}
		if err := m.verifyWorkspaceRepository(ctx, &ws); err != nil {
			continue
		}
		commonDir, err := worktreeCommonGitDir(ctx, ws.WorktreePath)
		if err != nil {
			continue // A missing checkout belongs to workspace recovery.
		}
		commonDir, err = canonicalFilesystemPath(commonDir)
		if err != nil {
			continue
		}
		pool := hotWorktreePath(commonDir, ws.WorktreePath)
		if seen[pool] {
			continue
		}
		owned, err := m.workspaceRegistrationMatches(ctx, commonDir, ws.WorktreePath, ws.ID)
		if err != nil || !owned {
			continue
		}
		provenance, err := m.existingWorkspaceWorktreeProvenance(ctx, commonDir, &ws)
		if err != nil || !provenance.reusable {
			continue
		}
		seen[pool] = true
		if err := m.rememberHotWorktreeRepository(ctx, commonDir, ws.WorktreePath, provenance.remote); err != nil {
			errs = append(errs, fmt.Errorf("remember workspace repository %s: %w", ws.ID, err))
		}
	}
	return errors.Join(errs...)
}

func hotWorktreePath(commonDir, workspacePath string) string {
	// macOS exposes its temporary volume through both /var and /private/var.
	// Any aliases of the same Git directory must address the same spare.
	if resolved, err := filepath.EvalSymlinks(commonDir); err == nil {
		commonDir = resolved
	}
	sum := sha256.Sum256([]byte(commonDir))
	// Keep the spare on the destination filesystem, including configured local
	// bases whose Git directory may live on a different volume.
	return filepath.Join(filepath.Dir(workspacePath), fmt.Sprintf(".kenn-forge-hot-%x", sum[:8]), "checkout")
}

func (m *Manager) prepareHotWorktree(ctx context.Context, commonDir, workspacePath, startRef string) error {
	repo, err := m.openRepositoryWorktrees(ctx, commonDir)
	if err != nil {
		return err
	}
	return repo.PrepareWarm(ctx, workspaceWarmRequest(commonDir, workspacePath, startRef))
}

func workspaceWarmRequest(commonDir, path, ref string) worktree.WarmRequest {
	if ref == "" {
		ref = "HEAD"
	}
	return worktree.WarmRequest{Path: hotWorktreePath(commonDir, path), PoolLockName: ".kenn-forge-worktree.lock", MarkerFile: hotWorktreeMarkerFile, IdentityFile: workspaceOwnershipMarkerFile, Revision: ref}
}
