package fleet

import "path/filepath"

// NormPath returns an absolute, cleaned path used as a worktree or project
// identity. It falls back to a cleaned relative path when the working
// directory cannot be resolved. Producers of scoped keys must normalize paths
// through this single function so a worktree's key is identical wherever it is
// built — the snapshot reader and any link writer included.
func NormPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return filepath.Clean(abs)
}

// WorktreeScopedKey returns the fleet scoped key for a worktree at the given
// on-disk path. It is the canonical "worktree:" + normalized-path form the
// snapshot uses as a worktree's identity; durable worktree links key off the
// same value so a link resolves to exactly one snapshot worktree.
func WorktreeScopedKey(path string) string {
	return "worktree:" + NormPath(path)
}
