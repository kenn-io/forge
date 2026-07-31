package gitclone

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/tokenauth"
	gitcmd "go.kenn.io/kit/git/cmd"
	gitremote "go.kenn.io/kit/git/remote"
	"golang.org/x/sync/singleflight"
)

// ensureCloneTimeout caps how long a single bare-clone create-or-fetch
// is allowed to run inside the singleflight slot. The slot is detached
// from caller cancellation so one canceled waiter cannot abort work for
// others; the timeout is what prevents a stuck git subprocess from
// holding the slot forever. Generous enough to cover large initial
// clones over slow links, short enough to recover from a wedged
// network connection inside one sync interval.
const ensureCloneTimeout = 15 * time.Minute

const repositoryIncarnationNamespace = "repository-incarnations"

// ErrNotFound is returned when a git ref or object cannot be resolved.
var ErrNotFound = errors.New("git object not found")

// RouteResolver selects mutation-capable credentials for managed Git.
type RouteResolver interface {
	SourceForRepo(platform, host, owner, name string) tokenauth.Source
	FallbackSource(host string) tokenauth.Source
}

// HostSources adapts host fallback sources for providers without repository
// credential routes.
type HostSources map[string]tokenauth.Source

func (s HostSources) SourceForRepo(_, host, _, _ string) tokenauth.Source {
	return s[host]
}

func (s HostSources) FallbackSource(host string) tokenauth.Source {
	return s[host]
}

// Manager manages bare git clones for diff computation.
type Manager struct {
	baseDir string
	routes  RouteResolver

	// ensureSF deduplicates concurrent EnsureClone calls for the same
	// (host, owner, name). Without it, callers like the periodic syncer,
	// per-PR detail syncs, and workspace setup race each other on the
	// same bare clone and trigger a stampede of identical git fetches,
	// which GitHub's smart-HTTP edge throttles with sporadic 5xx.
	ensureSF singleflight.Group

	repoBrowserRefreshSF singleflight.Group
	repoBrowserMu        sync.Mutex
	repoBrowserRepos     map[string]RepoBrowserRepoRef
	repoBrowserRefreshFn func(context.Context, RepoBrowserRepoRef) error
	credentialAliasesMu  sync.RWMutex
	credentialAliases    map[string]CredentialAlias
	localReadTestMu      sync.RWMutex
	beforeLocalReadTest  func()
}

// CredentialAlias selects configured credentials independently from the
// current provider route used as the remote endpoint.
type CredentialAlias struct {
	Platform        string
	Host            string
	Owner           string
	Name            string
	CredentialOwner string
	CredentialName  string
}

// New creates a Manager that stores bare clones under baseDir. A nil resolver
// means all operations proceed without auth.
func New(baseDir string, routes RouteResolver) *Manager {
	return &Manager{
		baseDir:           baseDir,
		routes:            routes,
		repoBrowserRepos:  make(map[string]RepoBrowserRepoRef),
		credentialAliases: make(map[string]CredentialAlias),
	}
}

// ReplaceCredentialAliases atomically replaces managed-Git credential
// bindings for provider-side repository renames.
func (m *Manager) ReplaceCredentialAliases(aliases []CredentialAlias) {
	if m == nil {
		return
	}
	next := make(map[string]CredentialAlias, len(aliases))
	for _, alias := range aliases {
		if strings.TrimSpace(alias.CredentialOwner) == "" ||
			strings.TrimSpace(alias.CredentialName) == "" {
			continue
		}
		next[credentialAliasKey(
			alias.Platform, alias.Host, alias.Owner, alias.Name,
		)] = alias
	}
	m.credentialAliasesMu.Lock()
	m.credentialAliases = next
	m.credentialAliasesMu.Unlock()
}

func credentialAliasKey(platform, host, owner, name string) string {
	return strings.ToLower(strings.TrimSpace(platform)) + "\x00" +
		strings.ToLower(strings.TrimSpace(host)) + "\x00" +
		strings.ToLower(strings.TrimSpace(owner)) + "\x00" +
		strings.ToLower(strings.TrimSpace(name))
}

// ClonePath returns the filesystem path for a repo's bare clone.
// Path is partitioned by host: {baseDir}/{host}/{owner}/{name}.git
func (m *Manager) ClonePath(
	platform, host, owner, name string,
) (string, error) {
	binding := cloneStorageBinding{
		namespace: cloneNamespaceForPlatform(platform),
		host:      host,
		owner:     owner,
		name:      name,
	}
	return m.ClonePathInNamespace(
		binding.namespace,
		binding.host,
		binding.owner,
		binding.name,
	)
}

type cloneStorageBinding struct {
	namespace string
	host      string
	owner     string
	name      string
}

// RepositoryClone is an immutable handle to one repository incarnation's
// managed bare clone. Routing coordinates select credentials and the remote;
// repositoryID alone selects local storage.
type RepositoryClone struct {
	manager      *Manager
	repositoryID int64
	platform     string
	host         string
	owner        string
	name         string
}

// RepositoryClone returns an immutable clone handle for one internal
// repository ID.
func (m *Manager) RepositoryClone(
	repositoryID int64,
	platform, host, owner, name string,
) RepositoryClone {
	return RepositoryClone{
		manager:      m,
		repositoryID: repositoryID,
		platform:     platform,
		host:         host,
		owner:        owner,
		name:         name,
	}
}

func (r RepositoryClone) storage() (cloneStorageBinding, error) {
	if r.manager == nil {
		return cloneStorageBinding{}, fmt.Errorf("repository clone manager is required")
	}
	if r.repositoryID <= 0 {
		return cloneStorageBinding{}, fmt.Errorf("repository clone id must be positive")
	}
	return cloneStorageBinding{
		namespace: repositoryIncarnationNamespace,
		host:      "local",
		owner:     "repositories",
		name:      fmt.Sprintf("repo-%d", r.repositoryID),
	}, nil
}

// ClonePath returns the incarnation-scoped bare clone path.
func (r RepositoryClone) ClonePath() (string, error) {
	storage, err := r.storage()
	if err != nil {
		return "", err
	}
	return r.manager.ClonePathInNamespace(
		storage.namespace,
		storage.host,
		storage.owner,
		storage.name,
	)
}

// EnsureClone creates or refreshes this repository incarnation's bare clone.
func (r RepositoryClone) EnsureClone(
	ctx context.Context,
	remoteURL string,
) error {
	if err := validateRemoteURLIdentity(
		r.host, r.owner, r.name, remoteURL,
	); err != nil {
		return err
	}
	storage, err := r.storage()
	if err != nil {
		return err
	}
	return r.manager.ensureCloneInStorage(
		ctx,
		storage,
		r.platform,
		r.host,
		r.owner,
		r.name,
		remoteURL,
	)
}

// RevParse resolves a ref inside this repository incarnation.
func (r RepositoryClone) RevParse(
	ctx context.Context,
	ref string,
) (string, error) {
	clonePath, err := r.ClonePath()
	if err != nil {
		return "", err
	}
	return r.manager.revParseInDir(ctx, clonePath, ref)
}

// MergeBase computes a merge base inside this repository incarnation.
func (r RepositoryClone) MergeBase(
	ctx context.Context,
	sha1, sha2 string,
) (string, error) {
	clonePath, err := r.ClonePath()
	if err != nil {
		return "", err
	}
	return r.manager.mergeBaseInDir(ctx, clonePath, sha1, sha2)
}

func cloneNamespaceForPlatform(platform string) string {
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform == "" || platform == "github" {
		return ""
	}
	return platform
}

// ClonePathInNamespace returns the filesystem path for a repo's bare clone
// inside an additional storage namespace. The namespace is only a local
// partition; host validation and git authentication still use host.
func (m *Manager) ClonePathInNamespace(
	namespace, host, owner, name string,
) (string, error) {
	namespace = strings.TrimSpace(namespace)
	if namespace != "" {
		if err := validateCloneNamespace(namespace); err != nil {
			return "", err
		}
		return clonePath(filepath.Join(m.baseDir, namespace), host, owner, name)
	}
	return clonePath(m.baseDir, host, owner, name)
}

func clonePath(baseDir, host, owner, name string) (string, error) {
	if host == "" && owner == "" {
		// Preserve local fixture clones at {baseDir}/{name}.git while
		// still using kit's path validator for the repository name.
		if _, err := gitremote.ClonePath(baseDir, gitremote.Identity{
			Host:  "local",
			Owner: "fixture",
			Name:  name,
		}); err != nil {
			return "", err
		}
		return filepath.Join(baseDir, name+".git"), nil
	}
	return gitremote.ClonePath(baseDir, gitremote.Identity{
		Host:  host,
		Owner: owner,
		Name:  name,
	})
}

func validateCloneNamespace(namespace string) error {
	if namespace == "." || namespace == ".." {
		return fmt.Errorf("unsafe clone namespace %q", namespace)
	}
	for _, r := range namespace {
		if (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '.' || r == '_' || r == '-' {
			continue
		}
		return fmt.Errorf("unsafe clone namespace %q", namespace)
	}
	return nil
}

// EnsureClone creates or fetches a bare clone for the given repo.
// remoteURL is the HTTPS clone URL (e.g., https://github.com/owner/name.git).
// On first call, clones the repo. On subsequent calls, fetches updates.
//
// Concurrent callers for the same (host, owner, name) share a single
// underlying clone/fetch via singleflight so PR detail syncs, the
// periodic syncer, and workspace setup do not stampede the same bare
// clone with duplicate git operations.
//
// The shared runner uses a context detached from any individual
// caller's cancellation, capped at ensureCloneTimeout, so one canceled
// waiter cannot abort the in-flight work but a stuck git subprocess
// cannot hold the slot forever either. Callers whose own context is
// already canceled on entry short-circuit without ever taking the
// slot.
func (m *Manager) EnsureClone(
	ctx context.Context, platform, host, owner, name, remoteURL string,
) error {
	binding := cloneStorageBinding{
		namespace: cloneNamespaceForPlatform(platform),
		host:      host,
		owner:     owner,
		name:      name,
	}
	return m.ensureCloneInStorage(
		ctx,
		binding,
		platform,
		host,
		owner,
		name,
		remoteURL,
	)
}

// EnsureCloneInNamespace creates or fetches a bare clone in a local storage
// namespace while still validating and authenticating against host.
func (m *Manager) EnsureCloneInNamespace(
	ctx context.Context, namespace, platform, host, owner, name, remoteURL string,
) error {
	return m.ensureCloneInStorage(
		ctx,
		cloneStorageBinding{
			namespace: strings.TrimSpace(namespace),
			host:      host,
			owner:     owner,
			name:      name,
		},
		platform,
		host,
		owner,
		name,
		remoteURL,
	)
}

func (m *Manager) ensureCloneInStorage(
	ctx context.Context,
	storage cloneStorageBinding,
	platform, host, owner, name, remoteURL string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Validate per-caller inputs before entering the singleflight
	// slot. remoteURL is not part of the slot key (we dedup by
	// repo identity, not URL spelling), so without an up-front
	// check a follower with a malformed URL could inherit the
	// leader's success — or a valid caller could inherit the
	// leader's validation error.
	if err := validateRemoteURLIdentity(host, owner, name, remoteURL); err != nil {
		return err
	}
	if _, err := m.ClonePathInNamespace(
		storage.namespace,
		storage.host,
		storage.owner,
		storage.name,
	); err != nil {
		return err
	}
	key := ensureCloneKey(
		storage.namespace,
		storage.host,
		storage.owner,
		storage.name,
	)
	ch := m.ensureSF.DoChan(key, func() (any, error) {
		opCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx), ensureCloneTimeout,
		)
		defer cancel()
		return nil, m.ensureCloneNowInStorage(
			opCtx,
			storage,
			platform,
			host,
			owner,
			name,
			remoteURL,
		)
	})
	select {
	case res := <-ch:
		return res.Err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func ensureCloneKey(namespace, host, owner, name string) string {
	return namespace + "\x00" + host + "\x00" + owner + "\x00" + name
}

// ensureCloneNow is the unshared inner: it decides whether to create a
// fresh bare clone or refresh an existing one. Always called from
// inside the singleflight slot opened by EnsureClone, which has
// already validated the caller's remoteURL.
func (m *Manager) ensureCloneNowInStorage(
	ctx context.Context,
	storage cloneStorageBinding,
	platform, host, owner, name, remoteURL string,
) error {
	clonePath, err := m.ClonePathInNamespace(
		storage.namespace,
		storage.host,
		storage.owner,
		storage.name,
	)
	if err != nil {
		return err
	}

	if _, err := os.Stat(filepath.Join(clonePath, "HEAD")); os.IsNotExist(err) {
		return m.cloneBare(
			ctx, platform, host, owner, name, clonePath, remoteURL,
		)
	}
	// On an existing clone, also re-verify the stored origin URL
	// belongs to the expected host: catches a clone whose config
	// was rewritten after creation.
	if out, err := m.git(ctx, clonePath, "config", "--get", "remote.origin.url"); err == nil {
		storedOrigin := strings.TrimSpace(string(out))
		if err := validateRemoteURLIdentity(host, owner, name, storedOrigin); err != nil {
			// The repository-incarnation path is selected from the active
			// repository's immutable internal ID. That binding authorizes an
			// origin update after a provider-side rename even after restart,
			// when the prior route is no longer present in memory.
			if storage.namespace != repositoryIncarnationNamespace {
				return err
			}
			if _, setErr := m.git(
				ctx,
				clonePath,
				"remote",
				"set-url",
				"origin",
				remoteURL,
			); setErr != nil {
				return fmt.Errorf(
					"update managed clone origin after repository rename: %w",
					setErr,
				)
			}
		}
	}
	m.ensureRefspecs(ctx, clonePath)
	return m.fetch(ctx, platform, host, owner, name, clonePath)
}

// Fetch refspecs configured on every bare clone.
//
//   - remoteTrackingRefspec stores origin branches under
//     refs/remotes/origin/* so bare-clone fetches never try to update a local
//     branch that a workspace has checked out.
//   - pullRefspec makes refs/pull/<N>/head available, which is how we resolve
//     PR heads that live on forks.
//   - gitlabMergeRequestRefspec is intentionally not a default refspec. GitLab
//     MR heads are fetched one at a time by explicit workspace operations.
const (
	legacyBranchRefspec       = "+refs/heads/*:refs/heads/*"
	remoteTrackingRefspec     = "+refs/heads/*:refs/remotes/origin/*"
	pullRefspec               = "+refs/pull/*/head:refs/pull/*/head"
	gitlabMergeRequestRefspec = "+refs/merge-requests/*/head:refs/merge-requests/*/head"
)

// defaultRefspecs returns the full list of fetch refspecs every clone should
// have. Used by both cloneBare (fresh clones) and ensureRefspecs (migration).
func defaultRefspecs() []string {
	return []string{
		remoteTrackingRefspec,
		pullRefspec,
	}
}

// ensureRefspecs idempotently adds any missing fetch refspecs to an
// existing clone. This upgrades clones created before branch/pull ref
// support was in place, including vanilla `git clone --bare` output with
// no configured fetch refspec at all.
func (m *Manager) ensureRefspecs(
	ctx context.Context, clonePath string,
) {
	// `git config --get-all` exits 1 with no output when the key is unset.
	// Treat any read failure as "no existing refspecs" and fall through to
	// the add loop, which is idempotent on its own and will log its own
	// warnings if the add commands fail for a real reason.
	out, _ := m.git(ctx, clonePath,
		"config", "--get-all", "remote.origin.fetch")
	existing := make(map[string]bool)
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			existing[line] = true
		}
	}
	if existing[legacyBranchRefspec] {
		if _, err := m.git(
			ctx, clonePath,
			"config", "--fixed-value", "--unset-all",
			"remote.origin.fetch", legacyBranchRefspec,
		); err != nil {
			slog.Warn("failed to remove legacy refspec from existing clone",
				"path", clonePath, "refspec", legacyBranchRefspec, "err", err)
		} else {
			delete(existing, legacyBranchRefspec)
		}
	}
	if existing[gitlabMergeRequestRefspec] {
		if _, err := m.git(
			ctx, clonePath,
			"config", "--fixed-value", "--unset-all",
			"remote.origin.fetch", gitlabMergeRequestRefspec,
		); err != nil {
			slog.Warn("failed to remove unbounded GitLab MR refspec from existing clone",
				"path", clonePath, "refspec", gitlabMergeRequestRefspec, "err", err)
		} else {
			delete(existing, gitlabMergeRequestRefspec)
		}
	}
	for _, refspec := range defaultRefspecs() {
		if existing[refspec] {
			continue
		}
		if _, err := m.git(ctx, clonePath,
			"config", "--add", "remote.origin.fetch", refspec); err != nil {
			slog.Warn("failed to add refspec to existing clone",
				"path", clonePath, "refspec", refspec, "err", err)
		}
	}
}

func (m *Manager) cloneBare(
	ctx context.Context, platform, host, owner, name, clonePath, remoteURL string,
) error {
	if err := os.MkdirAll(filepath.Dir(clonePath), 0o755); err != nil {
		return fmt.Errorf("mkdir for clone: %w", err)
	}
	slog.Info("cloning bare repo", "path", clonePath)
	// Initial clones hit the same flaky smart-HTTP /info/refs that
	// fetches do, so wrap the clone command in the same retry helper.
	// git clone refuses to write into a non-empty destination, so a
	// partial directory from a previous failed attempt would poison
	// every retry — sweep it out before re-running.
	_, err := retryTransient(ctx, "git clone --bare", func() ([]byte, error) {
		if err := os.RemoveAll(clonePath); err != nil {
			return nil, fmt.Errorf("cleanup partial clone: %w", err)
		}
		return m.gitCloneBare(
			ctx, platform, host, owner, name, clonePath, remoteURL,
		)
	})
	if err != nil {
		return fmt.Errorf("git clone --bare: %w", err)
	}

	// Install fetch refspecs so future fetches pull both branch heads and
	// pull refs. git clone --bare does not install a default refspec.
	// On failure, remove the partial clone so the next call retries.
	for _, refspec := range defaultRefspecs() {
		if _, err := m.git(ctx, clonePath,
			"config", "--add", "remote.origin.fetch", refspec); err != nil {
			os.RemoveAll(clonePath)
			return fmt.Errorf("add fetch refspec %q: %w", refspec, err)
		}
	}

	// Fetch immediately after clone so pull refs are available before
	// merge-base computation runs in the same sync cycle.
	return m.fetch(ctx, platform, host, owner, name, clonePath)
}

func (m *Manager) fetch(
	ctx context.Context, platform, host, owner, name, clonePath string,
) error {
	// GitHub's smart-HTTP endpoint sporadically returns 5xx on /info/refs.
	// Retry inline so a transient blip does not drop the entire sync cycle.
	_, err := retryTransient(ctx, "git fetch", func() ([]byte, error) {
		return m.gitNetworked(
			ctx, m.sourceForRepo(platform, host, owner, name), host, clonePath, nil,
			"fetch", "--prune", "--no-tags", "origin",
		)
	})
	if err != nil {
		return fmt.Errorf("git fetch: %w", err)
	}
	// set-head -a is networked (it consults the remote's HEAD via
	// /info/refs) and so subject to the same transient 5xx as fetch.
	// Failure is non-fatal — bare clone still works — but retrying
	// reduces stale-HEAD noise across sync cycles.
	_, setHeadErr := retryTransient(ctx, "git remote set-head", func() ([]byte, error) {
		return m.gitNetworked(
			ctx, m.sourceForRepo(platform, host, owner, name), host, clonePath, nil,
			"remote", "set-head", "origin", "-a",
		)
	})
	if setHeadErr != nil {
		slog.Warn("failed to repair origin HEAD",
			"path", clonePath, "err", setHeadErr)
	}
	return nil
}

// RevParse resolves a git ref to its SHA. Returns an empty string if the ref
// does not exist.
func (m *Manager) RevParse(
	ctx context.Context, platform, host, owner, name, ref string,
) (string, error) {
	clonePath, err := m.ClonePath(platform, host, owner, name)
	if err != nil {
		return "", err
	}
	return m.revParseInDir(ctx, clonePath, ref)
}

func (m *Manager) revParseInDir(
	ctx context.Context,
	clonePath, ref string,
) (string, error) {
	out, err := m.git(ctx, clonePath, "rev-parse", "--verify", ref)
	if err != nil {
		return "", fmt.Errorf("git rev-parse %s: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// MergeBase computes the merge base between two commits.
func (m *Manager) MergeBase(
	ctx context.Context, platform, host, owner, name, sha1, sha2 string,
) (string, error) {
	clonePath, err := m.ClonePath(platform, host, owner, name)
	if err != nil {
		return "", err
	}
	return m.mergeBaseInDir(ctx, clonePath, sha1, sha2)
}

func (m *Manager) mergeBaseInDir(
	ctx context.Context,
	clonePath, sha1, sha2 string,
) (string, error) {
	out, err := m.git(ctx, clonePath, "merge-base", sha1, sha2)
	if err != nil {
		return "", fmt.Errorf("git merge-base %s %s: %w", sha1, sha2, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func validateRemoteURLHost(expectedHost, remoteURL string) error {
	return gitremote.ValidateRemoteHost(expectedHost, remoteURL)
}

func validateRemoteURLIdentity(expectedHost, owner, name, remoteURL string) error {
	return gitremote.ValidateRemoteIdentity(gitremote.Identity{
		Host:  expectedHost,
		Owner: owner,
		Name:  name,
	}, remoteURL)
}

// git runs a local git command against an already-cloned bare repo and
// returns its stdout. Local reads (diff, log, rev-parse, merge-base,
// cat-file, config) never contact the remote, so they run without resolving
// or attaching a credential. Decoupling them from the token source keeps
// commit and diff views working during token rotation, when a token file can
// be briefly missing and resolving it would otherwise error. Networked
// operations go through gitNetworked instead.
func (m *Manager) git(
	ctx context.Context, dir string, args ...string,
) ([]byte, error) {
	m.localReadTestMu.RLock()
	hook := m.beforeLocalReadTest
	m.localReadTestMu.RUnlock()
	if hook != nil {
		hook()
	}
	return m.gitWithInput(ctx, dir, nil, args...)
}

// SetBeforeLocalReadForTest installs a hook immediately before local Git
// reads. It exists only to coordinate concurrency regression tests.
func (m *Manager) SetBeforeLocalReadForTest(fn func()) func() {
	m.localReadTestMu.Lock()
	previous := m.beforeLocalReadTest
	m.beforeLocalReadTest = fn
	m.localReadTestMu.Unlock()
	return func() {
		m.localReadTestMu.Lock()
		m.beforeLocalReadTest = previous
		m.localReadTestMu.Unlock()
	}
}

// RunGitForRepo runs a networked Git command with the repository route's
// mutation-capable credential.
func (m *Manager) RunGitForRepo(
	ctx context.Context, platform, host, owner, name, dir string, args ...string,
) ([]byte, error) {
	source := m.sourceForRepo(platform, host, owner, name)
	if source != nil {
		if err := m.validateOriginIdentity(ctx, dir, host, owner, name); err != nil {
			return nil, err
		}
	}
	return m.gitNetworked(ctx, source, host, dir, nil, args...)
}

// RunGitForHost runs a genuinely ownerless networked Git command with the
// host fallback credential.
func (m *Manager) RunGitForHost(
	ctx context.Context, host, dir string, args ...string,
) ([]byte, error) {
	return m.gitNetworked(ctx, m.fallbackSource(host), host, dir, nil, args...)
}

func (m *Manager) validateOriginIdentity(
	ctx context.Context, dir, host, owner, name string,
) error {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	rewrites, err := m.git(ctx, dir, "config", "--local", "--get-regexp", `^url\..*\.(insteadOf|pushInsteadOf)$`)
	if err == nil && strings.TrimSpace(string(rewrites)) != "" {
		return fmt.Errorf("authenticated git rejects repository-local URL rewrites")
	}
	for _, key := range []string{"remote.origin.url", "remote.origin.pushurl"} {
		out, err := m.git(ctx, dir, "config", "--get-all", key)
		if err != nil {
			if key == "remote.origin.pushurl" {
				continue
			}
			return fmt.Errorf("read %s before authenticated git: %w", key, err)
		}
		urls := strings.Fields(string(out))
		if len(urls) == 0 && key == "remote.origin.pushurl" {
			continue
		}
		for _, url := range urls {
			if err := validateRemoteURLIdentity(host, owner, name, url); err != nil {
				return fmt.Errorf("validate %s before authenticated git: %w", key, err)
			}
		}
	}
	return nil
}

func (m *Manager) gitWithInput(
	ctx context.Context, dir string, input []byte, args ...string,
) ([]byte, error) {
	out, stderr, err := runGitCommand(ctx, newGitRunner(), dir, input, args...)
	if err != nil {
		return nil, wrapGitError(err, stderr)
	}
	return out, nil
}

func (m *Manager) gitCloneBare(
	ctx context.Context, platform, host, owner, name, clonePath, remoteURL string,
) ([]byte, error) {
	return m.gitNetworked(
		ctx, m.sourceForRepo(platform, host, owner, name), host, "",
		func() error {
			if err := os.RemoveAll(clonePath); err != nil {
				return fmt.Errorf("cleanup partial clone before auth retry: %w", err)
			}
			return nil
		},
		"clone", "--bare", remoteURL, clonePath,
	)
}

// gitNetworked runs a git command that contacts the remote (clone, fetch,
// remote set-head). It resolves the host credential and attaches it, then on
// an authentication failure invalidates the source and retries once — the
// recovery path when a token rotates or expires mid-operation.
// cleanupBeforeAuthRetry, when set, runs between attempts; clone uses it to
// sweep the partial destination git refuses to overwrite.
func (m *Manager) gitNetworked(
	ctx context.Context,
	source tokenauth.Source,
	host, dir string,
	cleanupBeforeAuthRetry func() error,
	args ...string,
) ([]byte, error) {
	out, stderr, err := m.runGitAuthed(ctx, source, host, dir, args...)
	if err == nil {
		return out, nil
	}
	wrapped := wrapGitError(err, stderr)
	if isAuthGitError(wrapped) && invalidateTokenSource(source) {
		if cleanupBeforeAuthRetry != nil {
			if err := cleanupBeforeAuthRetry(); err != nil {
				return nil, err
			}
		}
		out, stderr, err = m.runGitAuthed(ctx, source, host, dir, args...)
		if err == nil {
			return out, nil
		}
		wrapped = wrapGitError(err, stderr)
	}
	return nil, wrapped
}

// runGitAuthed builds a runner with the host credential attached and runs the
// command. Networked git has no stdin, so it takes no input.
func (m *Manager) runGitAuthed(
	ctx context.Context, source tokenauth.Source, host, dir string, args ...string,
) ([]byte, []byte, error) {
	runner, err := m.gitRunnerAuthed(ctx, source, host)
	if err != nil {
		return nil, nil, err
	}
	return runGitCommand(ctx, runner, dir, nil, args...)
}

// runGitCommand runs git in dir with the given runner, bounded by the shared
// subprocess limiter. The limiter covers every git invocation — local reads
// and networked clone/fetch alike — because they all draw on the same process
// capacity as the rest of the app.
func runGitCommand(
	ctx context.Context, runner gitcmd.Runner, dir string, input []byte, args ...string,
) ([]byte, []byte, error) {
	var stdin io.Reader
	if input != nil {
		stdin = bytes.NewReader(input)
	}
	release, err := procutil.TryAcquire(ctx, "git subprocess capacity")
	if err != nil {
		return nil, nil, err
	}
	defer release()
	return runner.Run(ctx, dir, stdin, args...)
}

func wrapGitError(err error, stderr []byte) error {
	msg := tokenauth.RedactKnownSecrets(string(stderr))
	if isNotFoundError(msg) {
		return fmt.Errorf("%w: %s", ErrNotFound, msg)
	}
	errMsg := tokenauth.RedactKnownSecrets(err.Error())
	wrapped := gitCommandError{
		message: errMsg + ": " + msg,
		cause:   safeGitErrorCause(err),
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		wrapped.exitCode = exitErr.ExitCode()
		wrapped.hasExitCode = true
	}
	return wrapped
}

type gitCommandError struct {
	message     string
	cause       error
	exitCode    int
	hasExitCode bool
}

func (e gitCommandError) Error() string {
	return e.message
}

func (e gitCommandError) Unwrap() error {
	return e.cause
}

func (e gitCommandError) ExitCode() (int, bool) {
	return e.exitCode, e.hasExitCode
}

func safeGitErrorCause(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, tokenauth.ErrMissingToken):
		return tokenauth.ErrMissingToken
	default:
		return nil
	}
}

func gitExitCode(err error) (int, bool) {
	var exitErr interface {
		ExitCode() (int, bool)
	}
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 0, false
}

func invalidateTokenSource(source tokenauth.Source) bool {
	if source == nil {
		return false
	}
	source.Invalidate()
	return true
}

// newGitRunner returns a runner with kit's automation defaults: inherited
// GIT_* variables are stripped, global/system config is ignored, and terminal
// prompts are disabled.
func newGitRunner() gitcmd.Runner {
	return gitcmd.New()
}

func (m *Manager) sourceForRepo(
	platform, host, owner, name string,
) tokenauth.Source {
	if m.routes == nil {
		return nil
	}
	key := credentialAliasKey(platform, host, owner, name)
	m.credentialAliasesMu.RLock()
	alias, ok := m.credentialAliases[key]
	m.credentialAliasesMu.RUnlock()
	if ok {
		owner = alias.CredentialOwner
		name = alias.CredentialName
	}
	return m.routes.SourceForRepo(platform, host, owner, name)
}

func (m *Manager) fallbackSource(host string) tokenauth.Source {
	if m.routes == nil {
		return nil
	}
	return m.routes.FallbackSource(host)
}

// gitRunnerAuthed returns a runner with the selected token attached for
// networked operations. With no source configured it returns the plain runner.
func (m *Manager) gitRunnerAuthed(
	ctx context.Context, source tokenauth.Source, host string,
) (gitcmd.Runner, error) {
	runner := newGitRunner()
	if source == nil {
		return runner, nil
	}
	token, err := source.Token(ctx)
	if err != nil {
		return runner, fmt.Errorf("resolve git token for host %s: %w", host, err)
	}
	if token != "" {
		// GitHub's smart HTTP endpoint expects Basic auth credentials.
		runner = runner.WithBasicAuth("x-access-token", token)
	}
	return runner, nil
}

// isNotFoundError checks if git stderr indicates a missing object or ref.
func isNotFoundError(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "unknown revision") ||
		strings.Contains(s, "bad object") ||
		strings.Contains(s, "not a valid object name") ||
		strings.Contains(s, "not a valid commit name") ||
		strings.Contains(s, "does not exist")
}

func isAuthGitError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "authentication failed") ||
		strings.Contains(msg, "could not read username") ||
		strings.Contains(msg, "terminal prompts disabled")
}
