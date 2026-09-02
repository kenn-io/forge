package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"
)

// gitdirRemoteHeadReader answers the pushed-head observer's read-only
// questions in process through go-git instead of spawning git. The observer
// polls every ready workspace every few seconds, so its steady state must
// cost a few small file reads rather than several forks of a large daemon
// image per workspace.
//
// go-git reads the worktree's HEAD, loose and packed refs (through the
// linked-worktree common directory), and the repository config. Layouts it
// cannot interpret fall back to git for that question so the observer never
// answers differently from git, only more cheaply: config include/includeIf
// (go-git does not expand includes), reftable ref storage, or files it fails
// to parse.
//
// remoteURL is the configured remote.<name>.url; url.<base>.insteadOf
// rewriting is not applied. The observer does not consume the URL.
type gitdirRemoteHeadReader struct {
	fallback remoteHeadGitReader
}

func (r *gitdirRemoteHeadReader) BranchName(ctx context.Context, dir string) (string, error) {
	refs, _, err := openWorktreeRepository(dir)
	if err == nil {
		var head *plumbing.Reference
		head, err = refs.Reference(plumbing.HEAD)
		if err == nil {
			// Mirrors `git branch --show-current`: the branch name for a
			// symbolic HEAD under refs/heads, empty when detached.
			if head.Type() == plumbing.HashReference {
				return "", nil
			}
			if head.Target().IsBranch() {
				return head.Target().Short(), nil
			}
			return "", nil
		}
	}
	logGitdirFallback(dir, "branch", err)
	return r.fallback.BranchName(ctx, dir)
}

func (r *gitdirRemoteHeadReader) UpstreamState(ctx context.Context, dir, branch string) (upstreamState, error) {
	_, cfg, err := openWorktreeRepository(dir)
	if err != nil {
		logGitdirFallback(dir, "upstream", err)
		return r.fallback.UpstreamState(ctx, dir, branch)
	}
	tracked := cfg.Branches[branch]
	if tracked == nil || tracked.Remote == "" || tracked.Merge == "" {
		return upstreamState{}, nil
	}
	state := upstreamState{
		hasTracking: true,
		remoteName:  tracked.Remote,
		branchName:  strings.TrimPrefix(tracked.Merge.String(), "refs/heads/"),
	}
	if remote := cfg.Remotes[tracked.Remote]; remote != nil && len(remote.URLs) > 0 {
		state.remoteURL = remote.URLs[0]
	}
	return state, nil
}

func (r *gitdirRemoteHeadReader) RemoteTrackingSHA(ctx context.Context, dir, remote, branch string) (string, string, bool, error) {
	trackingRef := "refs/remotes/" + remote + "/" + branch
	refs, _, err := openWorktreeRepository(dir)
	if err == nil {
		var ref *plumbing.Reference
		ref, err = storer.ResolveReference(refs, plumbing.ReferenceName(trackingRef))
		switch {
		case err == nil:
			return ref.Hash().String(), trackingRef, true, nil
		case errors.Is(err, plumbing.ErrReferenceNotFound):
			return "", trackingRef, false, nil
		}
	}
	logGitdirFallback(dir, "tracking ref", err)
	return r.fallback.RemoteTrackingSHA(ctx, dir, remote, branch)
}

func (r *gitdirRemoteHeadReader) SetBranchUpstream(ctx context.Context, dir, branch, remote, mergeRef string) error {
	return r.fallback.SetBranchUpstream(ctx, dir, branch, remote, mergeRef)
}

func logGitdirFallback(dir, question string, reason error) {
	slog.Debug("workspace pushed-head observer reading git state through git",
		"dir", dir, "question", question, "reason", reason)
}

// errGitdirUnsupported marks repository layouts go-git cannot interpret
// faithfully; callers answer through git instead.
var errGitdirUnsupported = errors.New("repository layout not readable in process")

// openWorktreeRepository opens dir as a main or linked worktree and returns
// a reference store over its git directory plus the effective repository
// config, rejecting layouts whose answers go-git would get wrong.
//
// The git directory and common directory are resolved here rather than with
// go-git's EnableDotGitCommonDir: that option reads the worktree's
// `commondir` file without closing it, and the observer opens every
// workspace several times a minute, so the daemon would leak a descriptor per
// open and keep linked worktrees undeletable on Windows. The store is used
// directly instead of through gogit.Open because Open rejects any
// version-0 repository declaring extensions.worktreeConfig (its allow list
// is case-mismatched), and ref lookups do not need that validation.
func openWorktreeRepository(dir string) (storer.ReferenceStorer, *config.Config, error) {
	gitDir, commonDir, err := resolveWorktreeGitDirs(dir)
	if err != nil {
		return nil, nil, err
	}
	cfg, err := effectiveRepositoryConfig(gitDir, commonDir)
	if err != nil {
		return nil, nil, err
	}
	if cfg.Raw.HasSection("include") || cfg.Raw.HasSection("includeIf") {
		return nil, nil, fmt.Errorf("%w: config uses includes", errGitdirUnsupported)
	}
	if storage := cfg.Raw.Section("extensions").Option("refStorage"); storage != "" && !strings.EqualFold(storage, "files") {
		return nil, nil, fmt.Errorf("%w: ref storage %q", errGitdirUnsupported, storage)
	}
	var repoFS = osfs.New(gitDir)
	if commonDir != gitDir {
		repoFS = dotgit.NewRepositoryFilesystem(repoFS, osfs.New(commonDir))
	}
	return filesystem.NewStorage(repoFS, cache.NewObjectLRUDefault()), cfg, nil
}

// effectiveRepositoryConfig reads the shared config and, when
// extensions.worktreeConfig is on, layers the worktree's own config.worktree
// over it the way git does. Managed clones enable that extension to override
// core.bare per linked worktree, so any branch setting stored there must win
// over the shared value.
func effectiveRepositoryConfig(gitDir, commonDir string) (*config.Config, error) {
	shared, err := os.ReadFile(filepath.Join(commonDir, "config"))
	if err != nil {
		return nil, err
	}
	cfg, err := config.ReadConfig(bytes.NewReader(shared))
	if err != nil {
		return nil, err
	}
	extensions := cfg.Raw.Section("extensions")
	if !extensions.HasOption("worktreeConfig") || !gitConfigTrue(extensions.Option("worktreeConfig")) {
		return cfg, nil
	}
	local, err := os.ReadFile(filepath.Join(gitDir, "config.worktree"))
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	// Later assignments win in go-git's config, matching git's precedence.
	return config.ReadConfig(io.MultiReader(bytes.NewReader(shared), strings.NewReader("\n"), bytes.NewReader(local)))
}

// gitConfigTrue applies git-config(1) boolean parsing: yes/on/true, any
// nonzero integer, or a key with no value are true.
func gitConfigTrue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "true", "yes", "on":
		return true
	case "false", "no", "off":
		return false
	}
	n, err := strconv.Atoi(strings.TrimSpace(value))
	return err == nil && n != 0
}

// resolveWorktreeGitDirs locates the worktree's private git directory (a
// `.git` directory, or the target of the `.git` file written by
// `git worktree add`) and the common directory holding refs, packed-refs,
// and config for every worktree of the repository.
func resolveWorktreeGitDirs(worktree string) (gitDir, commonDir string, err error) {
	dotGit := filepath.Join(worktree, ".git")
	info, err := os.Stat(dotGit)
	if err != nil {
		return "", "", err
	}
	gitDir = dotGit
	if !info.IsDir() {
		data, err := os.ReadFile(dotGit)
		if err != nil {
			return "", "", err
		}
		line, _, _ := strings.Cut(string(data), "\n")
		target, ok := strings.CutPrefix(line, "gitdir:")
		if !ok {
			return "", "", fmt.Errorf("%w: %s is neither a directory nor a gitdir file", errGitdirUnsupported, dotGit)
		}
		gitDir = strings.TrimSpace(target)
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(worktree, gitDir)
		}
	}
	commonDir = gitDir
	data, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	switch {
	case err == nil:
		commonDir = strings.TrimSpace(string(data))
		if !filepath.IsAbs(commonDir) {
			commonDir = filepath.Join(gitDir, commonDir)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return "", "", err
	}
	return filepath.Clean(gitDir), filepath.Clean(commonDir), nil
}
