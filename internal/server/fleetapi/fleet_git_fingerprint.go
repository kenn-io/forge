package fleetapi

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"hash/fnv"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// gitDirs is the on-disk layout of one checkout: gitDir holds the
// per-worktree files (HEAD, index) and commonDir holds what every worktree of
// the repository shares (refs, packed-refs, config, worktrees/). They are the
// same directory for a main checkout or a bare repository.
type gitDirs struct {
	gitDir    string
	commonDir string
}

// resolveGitDirs locates a checkout's git directories from the filesystem
// alone, so the fleet monitors can decide whether anything changed without
// spawning git. It understands a main checkout (.git directory), a linked
// worktree (.git file pointing at <common>/worktrees/<name>), and a bare
// repository (HEAD and objects directly under the path).
func resolveGitDirs(path string) (gitDirs, error) {
	dotGit := filepath.Join(path, ".git")
	info, err := os.Stat(dotGit)
	switch {
	case err == nil && info.IsDir():
		return withCommonDir(dotGit)
	case err == nil:
		gitDir, err := readGitDirPointer(dotGit, path)
		if err != nil {
			return gitDirs{}, err
		}
		return withCommonDir(gitDir)
	case !errors.Is(err, fs.ErrNotExist):
		return gitDirs{}, fmt.Errorf("stat %s: %w", dotGit, err)
	}
	if _, err := os.Stat(filepath.Join(path, "HEAD")); err != nil {
		return gitDirs{}, fmt.Errorf("%s is not a git checkout", path)
	}
	if info, err := os.Stat(filepath.Join(path, "objects")); err != nil || !info.IsDir() {
		return gitDirs{}, fmt.Errorf("%s is not a git checkout", path)
	}
	return withCommonDir(path)
}

// readGitDirPointer parses a .git file ("gitdir: <path>") and resolves a
// relative target against the checkout directory.
func readGitDirPointer(dotGit, base string) (string, error) {
	f, err := os.Open(dotGit)
	if err != nil {
		return "", err
	}
	defer f.Close()
	line, err := bufio.NewReader(io.LimitReader(f, 4096)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read %s: %w", dotGit, err)
	}
	target, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
	if !ok {
		return "", fmt.Errorf("%s is not a gitdir pointer", dotGit)
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return "", fmt.Errorf("%s has an empty gitdir pointer", dotGit)
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(base, target)
	}
	return filepath.Clean(target), nil
}

// withCommonDir pairs a git directory with its common directory, following
// the optional "commondir" file a linked worktree's git directory carries.
func withCommonDir(gitDir string) (gitDirs, error) {
	dirs := gitDirs{gitDir: gitDir, commonDir: gitDir}
	raw, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return dirs, nil
		}
		return gitDirs{}, fmt.Errorf("read commondir for %s: %w", gitDir, err)
	}
	common := strings.TrimSpace(string(raw))
	if common == "" {
		return dirs, nil
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(gitDir, common)
	}
	dirs.commonDir = filepath.Clean(common)
	return dirs, nil
}

// worktreeStatsFingerprint summarizes every git-directory input the stats
// sampler's diff and divergence probes depend on: the checked-out commit
// (HEAD), staged content (index), and every ref the merge base or upstream can
// resolve through (loose refs and packed-refs). Two equal fingerprints mean
// the sampled numbers cannot have changed through git, so the probes and the
// database write can be skipped. Unstaged edits to tracked files and new
// untracked files do not touch the git directory and are deliberately outside
// this fingerprint; the on-demand refresh path measures them unconditionally.
func worktreeStatsFingerprint(path string) (string, error) {
	dirs, err := resolveGitDirs(path)
	if err != nil {
		return "", err
	}
	h := newGitFingerprintHasher()
	h.file("HEAD", filepath.Join(dirs.gitDir, "HEAD"))
	h.file("index", filepath.Join(dirs.gitDir, "index"))
	h.file("packed-refs", filepath.Join(dirs.commonDir, "packed-refs"))
	h.tree("refs", filepath.Join(dirs.commonDir, "refs"))
	if dirs.gitDir != dirs.commonDir {
		h.tree("worktree-refs", filepath.Join(dirs.gitDir, "refs"))
	}
	return h.sum(), nil
}

// projectDiscoveryFingerprint summarizes what discovery reads for a registered
// project: the linked-worktree registry (worktrees/), the refs the default
// branch resolves through, HEAD, and config (init.defaultBranch, remotes).
func projectDiscoveryFingerprint(root string) (string, error) {
	dirs, err := resolveGitDirs(root)
	if err != nil {
		return "", err
	}
	h := newGitFingerprintHasher()
	h.file("HEAD", filepath.Join(dirs.gitDir, "HEAD"))
	h.file("config", filepath.Join(dirs.commonDir, "config"))
	h.file("packed-refs", filepath.Join(dirs.commonDir, "packed-refs"))
	h.tree("refs", filepath.Join(dirs.commonDir, "refs"))
	h.tree("worktrees", filepath.Join(dirs.commonDir, "worktrees"))
	return h.sum(), nil
}

// gitFingerprintHasher folds file metadata into one digest. Only sizes and
// modification times are read, never content, so a fingerprint costs a handful
// of stat calls plus one directory walk per refs tree.
type gitFingerprintHasher struct {
	h hash.Hash
}

func newGitFingerprintHasher() *gitFingerprintHasher {
	return &gitFingerprintHasher{h: fnv.New128a()}
}

func (g *gitFingerprintHasher) file(label, path string) {
	info, err := os.Stat(path)
	if err != nil {
		g.write(label, "missing")
		return
	}
	g.stamp(label, info)
}

func (g *gitFingerprintHasher) tree(label, root string) {
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root && errors.Is(err, fs.ErrNotExist) {
				g.write(label, "missing")
				return filepath.SkipAll
			}
			g.write(label+"/"+path, "error")
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			g.write(label+"/"+path, "error")
			return nil
		}
		g.stamp(label+"/"+path, info)
		return nil
	})
	if err != nil {
		g.write(label, "walk-error")
	}
}

func (g *gitFingerprintHasher) stamp(label string, info fs.FileInfo) {
	g.write(label,
		strconv.FormatInt(info.Size(), 10)+":"+
			strconv.FormatInt(info.ModTime().UnixNano(), 10)+":"+
			strconv.FormatBool(info.IsDir()),
	)
}

// write folds one label/value pair into the digest; hash.Hash writers never
// return an error.
func (g *gitFingerprintHasher) write(label, value string) {
	_, _ = io.WriteString(g.h, label)
	_, _ = g.h.Write([]byte{0})
	_, _ = io.WriteString(g.h, value)
	_, _ = g.h.Write([]byte{0})
}

func (g *gitFingerprintHasher) sum() string {
	return hex.EncodeToString(g.h.Sum(nil))
}
