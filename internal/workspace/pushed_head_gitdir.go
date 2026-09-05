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
	"sync"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"
	lru "github.com/hashicorp/golang-lru/v2"
)

// gitdirRemoteHeadReader answers the pushed-head observer's read-only
// questions in process through go-git instead of spawning git. The observer
// polls every ready workspace every few seconds, so its steady state must
// cost a few small file reads rather than several forks of a large daemon
// image per workspace.
//
// go-git reads the worktree's HEAD, loose and packed refs (through the
// linked-worktree common directory), and the repository config, with
// config.worktree layered on top when extensions.worktreeConfig is on.
// Layouts it cannot answer faithfully (config include/includeIf, which go-git
// does not expand, or reftable ref storage) are skipped like a detached HEAD:
// no kenn-forge workspace uses them, and spawning git for them would restore
// the churn this reader exists to remove. Upstream repair is the one git
// write, guarded per command by procutil.
//
// remoteURL is the configured remote.<name>.url; url.<base>.insteadOf
// rewriting is not applied. The observer does not consume the URL.
type gitdirRemoteHeadReader struct {
	handles *lru.Cache[string, *worktreeGitHandle]
}

// gitdirHandleCacheSize bounds cached worktrees; a daemon polls tens of
// workspaces, and eviction only costs one re-open.
const gitdirHandleCacheSize = 256

func newGitdirRemoteHeadReader() gitdirRemoteHeadReader {
	handles, err := lru.New[string, *worktreeGitHandle](gitdirHandleCacheSize)
	if err != nil {
		panic(err) // only for a non-positive size
	}
	return gitdirRemoteHeadReader{handles: handles}
}

func (r gitdirRemoteHeadReader) BranchName(_ context.Context, dir string) (string, error) {
	refs, _, err := r.open(dir)
	if err != nil {
		return "", skipUnsupportedGitdir(dir, "branch", err)
	}
	head, err := refs.Reference(plumbing.HEAD)
	if err != nil {
		return "", fmt.Errorf("read HEAD: %w", err)
	}
	// Mirrors `git branch --show-current`: the branch name for a symbolic
	// HEAD under refs/heads, empty when detached.
	if head.Type() == plumbing.SymbolicReference && head.Target().IsBranch() {
		return head.Target().Short(), nil
	}
	return "", nil
}

func (r gitdirRemoteHeadReader) UpstreamState(_ context.Context, dir, branch string) (upstreamState, error) {
	_, cfg, err := r.open(dir)
	if err != nil {
		return upstreamState{}, skipUnsupportedGitdir(dir, "upstream", err)
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

func (r gitdirRemoteHeadReader) RemoteTrackingSHA(_ context.Context, dir, remote, branch string) (string, string, bool, error) {
	trackingRef := "refs/remotes/" + remote + "/" + branch
	refs, _, err := r.open(dir)
	if err != nil {
		return "", trackingRef, false, skipUnsupportedGitdir(dir, "tracking ref", err)
	}
	ref, err := storer.ResolveReference(refs, plumbing.ReferenceName(trackingRef))
	switch {
	case err == nil:
		return ref.Hash().String(), trackingRef, true, nil
	case errors.Is(err, plumbing.ErrReferenceNotFound):
		return "", trackingRef, false, nil
	default:
		return "", trackingRef, false, fmt.Errorf("resolve %s: %w", trackingRef, err)
	}
}

// SetBranchUpstream is the observer's only git write. setBranchUpstream's
// commands each take one procutil slot; wrapping them in an outer acquisition
// would nest and stall until the git timeout whenever the limiter is full.
func (gitdirRemoteHeadReader) SetBranchUpstream(ctx context.Context, dir, branch, remote, mergeRef string) error {
	gitCtx, cancel := context.WithTimeout(ctx, pushedHeadGitTimeout)
	defer cancel()
	return setBranchUpstream(gitCtx, dir, branch, remote, mergeRef)
}

// skipUnsupportedGitdir turns an unsupported-layout error into a silent skip
// (nil error, zero answer) and passes every other error through.
func skipUnsupportedGitdir(dir, question string, err error) error {
	if errors.Is(err, errGitdirUnsupported) {
		slog.Debug("workspace pushed-head observer skipping unsupported repository layout",
			"dir", dir, "question", question, "reason", err)
		return nil
	}
	return err
}

// errGitdirUnsupported marks repository layouts go-git cannot interpret
// faithfully; the observer skips them.
var errGitdirUnsupported = errors.New("repository layout not readable in process")

// worktreeGitHandle is one worktree's resolved layout, go-git reference
// store, and parsed config. Layout and store are fixed for the life of the
// entry; config is re-read only when its files change on disk. Refs are
// never cached here: go-git reads them from disk on every lookup, which is
// what lets the observer notice a push.
type worktreeGitHandle struct {
	gitDir    string
	commonDir string
	refs      storer.ReferenceStorer

	mu          sync.Mutex
	cfg         *config.Config
	sharedStamp fileStamp
	localStamp  fileStamp
}

type fileStamp struct {
	modTime time.Time
	size    int64
	exists  bool
}

func stampFile(path string) (fileStamp, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fileStamp{}, nil
	}
	if err != nil {
		return fileStamp{}, err
	}
	return fileStamp{modTime: info.ModTime(), size: info.Size(), exists: true}, nil
}

// open returns the worktree's reference store and effective config,
// rejecting layouts whose answers go-git would get wrong. A handle that
// fails is dropped so a moved or deleted worktree is re-resolved next time.
func (r gitdirRemoteHeadReader) open(dir string) (storer.ReferenceStorer, *config.Config, error) {
	handle, ok := r.handles.Get(dir)
	if !ok {
		var err error
		handle, err = newWorktreeGitHandle(dir)
		if err != nil {
			return nil, nil, err
		}
		r.handles.Add(dir, handle)
	}
	cfg, err := handle.effectiveConfig()
	if err != nil {
		r.handles.Remove(dir)
		return nil, nil, err
	}
	if cfg.Raw.HasSection("include") || cfg.Raw.HasSection("includeIf") {
		return nil, nil, fmt.Errorf("%w: config uses includes", errGitdirUnsupported)
	}
	if storage := cfg.Raw.Section("extensions").Option("refStorage"); storage != "" && !strings.EqualFold(storage, "files") {
		return nil, nil, fmt.Errorf("%w: ref storage %q", errGitdirUnsupported, storage)
	}
	return handle.refs, cfg, nil
}

// newWorktreeGitHandle resolves the layout and builds the reference store.
//
// The git directory and common directory are resolved here rather than with
// go-git's EnableDotGitCommonDir: that option reads the worktree's
// `commondir` file without closing it, and the observer opens every
// workspace several times a minute, so the daemon would leak a descriptor per
// open and keep linked worktrees undeletable on Windows. The store is used
// directly instead of through gogit.Open because Open rejects any
// version-0 repository declaring extensions.worktreeConfig (its allow list
// is case-mismatched), and ref lookups do not need that validation.
func newWorktreeGitHandle(dir string) (*worktreeGitHandle, error) {
	gitDir, commonDir, err := resolveWorktreeGitDirs(dir)
	if err != nil {
		return nil, err
	}
	var repoFS = osfs.New(gitDir)
	if commonDir != gitDir {
		repoFS = dotgit.NewRepositoryFilesystem(repoFS, osfs.New(commonDir))
	}
	return &worktreeGitHandle{
		gitDir:    gitDir,
		commonDir: commonDir,
		refs:      filesystem.NewStorage(repoFS, cache.NewObjectLRUDefault()),
	}, nil
}

// effectiveConfig returns the shared config with, when
// extensions.worktreeConfig is on, the worktree's own config.worktree layered
// over it the way git does. Managed clones enable that extension to override
// core.bare per linked worktree, and kit's managed merge-request worktrees
// store branch tracking there, so worktree values must win. The parse is
// reused until either file's size or mtime changes.
func (h *worktreeGitHandle) effectiveConfig() (*config.Config, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	sharedPath := filepath.Join(h.commonDir, "config")
	localPath := filepath.Join(h.gitDir, "config.worktree")
	shared, err := stampFile(sharedPath)
	if err != nil {
		return nil, err
	}
	local, err := stampFile(localPath)
	if err != nil {
		return nil, err
	}
	if h.cfg != nil && shared == h.sharedStamp && local == h.localStamp {
		return h.cfg, nil
	}
	cfg, err := readEffectiveConfig(sharedPath, localPath)
	if err != nil {
		return nil, err
	}
	h.cfg, h.sharedStamp, h.localStamp = cfg, shared, local
	return cfg, nil
}

func readEffectiveConfig(sharedPath, localPath string) (*config.Config, error) {
	shared, err := os.ReadFile(sharedPath)
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
	local, err := os.ReadFile(localPath)
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
