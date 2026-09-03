package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path/filepath"
)

// WorktreeGitFingerprint summarizes the git metadata whose change can move a
// workspace's divergence or index state: HEAD, ORIG_HEAD, the index, the
// repository config (including a linked worktree's config.worktree), packed
// refs, and every loose ref of the worktree and its common git directory. It
// only stats files and never spawns git, so background enrichment can skip a
// re-probe when nothing changed. Worktree file edits that do not touch the
// index are invisible to it by design.
func WorktreeGitFingerprint(worktree string) (string, error) {
	if worktree == "" {
		return "", fmt.Errorf("fingerprint worktree: empty path")
	}
	gitDir, commonDir, err := resolveWorktreeGitDirs(worktree)
	if err != nil {
		return "", fmt.Errorf("fingerprint worktree: %w", err)
	}

	hash := sha256.New()
	record := func(label string, stamp fileStamp) {
		fmt.Fprintf(hash, "%s\x00%v\x00%d\x00%d\n", label, stamp.exists, stamp.size, stamp.modTime.UnixNano())
	}
	stat := func(dir, name string) {
		path := filepath.Join(dir, name)
		stamp, err := stampFile(path)
		if err != nil {
			stamp = fileStamp{}
		}
		record(path, stamp)
	}
	walkRefs := func(dir string) {
		_ = filepath.WalkDir(filepath.Join(dir, "refs"), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return nil
			}
			record(path, fileStamp{modTime: info.ModTime(), size: info.Size(), exists: true})
			return nil
		})
	}

	for _, name := range []string{"HEAD", "ORIG_HEAD", "index", "config", "config.worktree", "packed-refs"} {
		stat(gitDir, name)
	}
	walkRefs(gitDir)
	if commonDir != gitDir {
		stat(commonDir, "config")
		stat(commonDir, "packed-refs")
		walkRefs(commonDir)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
