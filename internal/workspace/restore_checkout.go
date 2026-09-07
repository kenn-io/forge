package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// The caller holds the repository lock and has verified the exact stale
// registration and an absent destination. Restore the saved index, retaining
// staged changes and detached HEADs as well as branch commits.
func restoreMissingWorkspaceCheckout(ctx context.Context, gitDir, metadataDir string, ws *Workspace) (err error) {
	if err := os.MkdirAll(filepath.Dir(ws.WorktreePath), 0o755); err != nil {
		return err
	}
	if err := os.Mkdir(ws.WorktreePath, 0o755); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			// Only remove the directory this attempt created. Keep the original
			// Git registration, including HEAD and index, for another retry.
			err = errors.Join(err, os.RemoveAll(ws.WorktreePath))
		}
	}()
	if err := os.WriteFile(filepath.Join(ws.WorktreePath, ".git"), []byte("gitdir: "+metadataDir+"\n"), 0o644); err != nil {
		return err
	}
	if err := runGitWithoutHooks(ctx, ws.WorktreePath, "checkout-index", "--all"); err != nil {
		return fmt.Errorf("restore missing workspace files: %w", err)
	}
	return writeWorkspaceOwnershipMarker(ctx, gitDir, ws)
}
