package workspace

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const hotWorktreeMarkerFile = "kenn-forge-hot-worktree"

// WarmWorktrees prepares one spare for each repository with a ready workspace.
// It uses only existing local refs; setup still owns the fresh fetch and route
// validation. The caller owns scheduling and cancellation.
func (m *Manager) WarmWorktrees(ctx context.Context) error {
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
		owned, err := workspaceRegistrationMatches(ctx, commonDir, ws.WorktreePath, ws.ID)
		if err != nil || !owned {
			continue
		}
		provenance, err := m.existingWorkspaceWorktreeProvenance(ctx, commonDir, &ws)
		if err != nil || !provenance.reusable {
			continue
		}
		seen[pool] = true
		if err := m.prepareHotWorktree(ctx, commonDir, ws.WorktreePath, remoteShorthandRef(provenance.remote, "HEAD")); err != nil {
			errs = append(errs, fmt.Errorf("warm workspace repository %s: %w", ws.ID, err))
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
	hot := hotWorktreePath(commonDir, workspacePath)
	// Builders use their own lock. The expensive checkout must never hold the
	// repository mutation lock needed by foreground workspace creation.
	return m.withRepoLock(ctx, filepath.Dir(hot), func() error {
		var metadataDir string
		fill := false
		if err := m.withRepoLockForGitDir(ctx, commonDir, func() error {
			if _, err := os.Lstat(hot); err == nil {
				state, metadata, err := hotWorktreeState(ctx, commonDir, hot)
				if err != nil {
					return err
				}
				// Resume only our interrupted preparation. Ready or foreign
				// checkouts are left in place, including any user edits.
				metadataDir, fill = metadata, state == "preparing"
				if fill {
					// Canceled Git processes can leave index.lock behind. The
					// fill lock serializes builders; only this private,
					// incomplete index is rebuilt.
					if err := os.Remove(filepath.Join(metadata, "index.lock")); err != nil && !errors.Is(err, os.ErrNotExist) {
						return err
					}
				}
				return nil
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := removeStaleWorktreeRegistrationMetadata(ctx, commonDir, hot); err != nil {
				return err
			}
			if err := runGitWorktreeAdd(ctx, commonDir, hot, "--detach", "--no-checkout", startRef); err != nil {
				return err
			}
			var err error
			metadataDir, err = worktreeGitDir(ctx, hot)
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(metadataDir, hotWorktreeMarkerFile), []byte("preparing\n"), 0o600); err != nil {
				return err
			}
			fill = true
			return nil
		}); err != nil || !fill {
			return err
		}
		if err := runGitWithoutHooks(ctx, hot, "-c", "submodule.recurse=false", "reset", "--hard", "HEAD"); err != nil {
			return err
		}
		return m.withRepoLockForGitDir(ctx, commonDir, func() error {
			return os.WriteFile(filepath.Join(metadataDir, hotWorktreeMarkerFile), []byte("ready\n"), 0o600)
		})
	})
}

// hotWorktreeState accepts only our detached, registered checkout. Its marker
// lives in Git's administrative directory, outside repository-controlled files.
func hotWorktreeState(ctx context.Context, commonDir, hot string) (string, string, error) {
	live, err := gitDirHasLiveWorktree(ctx, commonDir, hot)
	if err != nil || !live {
		return "", "", err
	}
	metadata, err := worktreeGitDir(ctx, hot)
	if err != nil {
		return "", "", err
	}
	state, err := os.ReadFile(filepath.Join(metadata, hotWorktreeMarkerFile))
	if errors.Is(err, os.ErrNotExist) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	if _, err := os.Lstat(filepath.Join(metadata, workspaceOwnershipMarkerFile)); !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	branch, err := worktreeCurrentBranch(ctx, hot)
	if err != nil || branch != "" {
		return "", "", err
	}
	return strings.TrimSpace(string(state)), metadata, nil
}

// tryHotWorktree runs under the caller's repository lock, just like worktree
// add. A cache miss follows the normal creation path. Existing checkout reuse
// and registered-checkout recovery bypass this seam.
func tryHotWorktree(ctx context.Context, gitDir, workspacePath string, args ...string) (bool, error) {
	if len(args) != 1 && (len(args) != 2 || args[0] != "--detach") {
		return false, nil
	}
	started := time.Now()
	commonDir, err := worktreeCommonGitDir(ctx, gitDir)
	if err != nil {
		return false, err
	}
	commonDir, err = canonicalFilesystemPath(commonDir)
	if err != nil {
		return false, err
	}
	hot := hotWorktreePath(commonDir, workspacePath)
	if _, err := os.Lstat(hot); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	state, _, err := hotWorktreeState(ctx, commonDir, hot)
	if err != nil || state != "ready" {
		return false, err
	}
	dirty, err := gitCombinedOutput(ctx, hot, "status", "--porcelain", "--untracked-files=all", "--ignored")
	if err != nil || strings.TrimSpace(dirty) != "" {
		return false, err
	}
	checkout := append([]string{"-c", "submodule.recurse=false", "checkout", "--no-guess"}, args...)
	if err := runGitWithoutHooks(ctx, hot, checkout...); err != nil {
		cleanupCtx, cancel := cleanupContext(ctx)
		defer cancel()
		// A canceled switch can leave both an index lock and partially
		// written files. Discard only the clean, owned spare we just claimed;
		// the next warming pass creates its replacement.
		removeErr := runGitWithoutHooks(cleanupCtx, commonDir, "worktree", "remove", "--force", hot)
		return true, errors.Join(err, removeErr)
	}
	if err := runGitWithoutHooks(ctx, commonDir, "worktree", "move", hot, workspacePath); err != nil {
		cleanupCtx, cancel := cleanupContext(ctx)
		defer cancel()
		// Release the requested branch if the destination could not be used.
		detachErr := runGitWithoutHooks(cleanupCtx, hot, "-c", "submodule.recurse=false", "checkout", "--detach")
		return true, errors.Join(err, detachErr)
	}
	slog.Info("workspace claimed hot worktree", "path", workspacePath, "duration_ms", time.Since(started).Milliseconds())
	return true, nil
}
