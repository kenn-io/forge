package workspaceapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// worktreeGitFingerprint summarizes the git metadata whose change can move a
// workspace's divergence or index state: HEAD, ORIG_HEAD, the index, the
// repository config, packed refs, and every loose ref of the worktree and its
// common git directory. It only stats files, never spawns git, so background
// enrichment can skip a re-probe when nothing changed. Worktree file edits
// that do not touch the index are invisible to it by design; the forced
// re-probe interval bounds how long such a change stays unseen.
func worktreeGitFingerprint(worktreePath string) (string, error) {
	if worktreePath == "" {
		return "", fmt.Errorf("fingerprint worktree: empty path")
	}
	gitDir, err := worktreeGitDirNoExec(worktreePath)
	if err != nil {
		return "", err
	}
	commonDir := gitDir
	if raw, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		common := strings.TrimSpace(string(raw))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitDir, common)
		}
		commonDir = filepath.Clean(common)
	}

	hash := sha256.New()
	record := func(label string, info fs.FileInfo) {
		fmt.Fprintf(hash, "%s\x00%d\x00%d\n", label, info.Size(), info.ModTime().UnixNano())
	}
	statFile := func(dir, name string) {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.IsDir() {
			fmt.Fprintf(hash, "%s/%s\x00absent\n", dir, name)
			return
		}
		record(dir+"/"+name, info)
	}
	walkRefs := func(dir string) {
		root := filepath.Join(dir, "refs")
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return nil
			}
			record(path, info)
			return nil
		})
	}

	for _, name := range []string{"HEAD", "ORIG_HEAD", "index", "config", "packed-refs"} {
		statFile(gitDir, name)
	}
	walkRefs(gitDir)
	if commonDir != gitDir {
		statFile(commonDir, "config")
		statFile(commonDir, "packed-refs")
		walkRefs(commonDir)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// worktreeGitDirNoExec resolves a checkout's git directory from the .git
// entry alone: a directory for a primary checkout, or a "gitdir: <path>" file
// for a linked worktree. It deliberately avoids git rev-parse so fingerprinting
// never spawns a process.
func worktreeGitDirNoExec(worktreePath string) (string, error) {
	dotGit := filepath.Join(worktreePath, ".git")
	info, err := os.Stat(dotGit)
	if err != nil {
		return "", fmt.Errorf("fingerprint worktree: %w", err)
	}
	if info.IsDir() {
		return dotGit, nil
	}
	raw, err := os.ReadFile(dotGit)
	if err != nil {
		return "", fmt.Errorf("fingerprint worktree: %w", err)
	}
	target, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir:")
	if !ok {
		return "", fmt.Errorf("fingerprint worktree: %s is not a gitdir link", dotGit)
	}
	target = strings.TrimSpace(target)
	if !filepath.IsAbs(target) {
		target = filepath.Join(worktreePath, target)
	}
	return filepath.Clean(target), nil
}
