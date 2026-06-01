package gitclone

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	gitcmd "go.kenn.io/kit/git/cmd"
	gitremote "go.kenn.io/kit/git/remote"
	"go.kenn.io/middleman/internal/procutil"
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

// ErrNotFound is returned when a git ref or object cannot be resolved.
var ErrNotFound = errors.New("git object not found")

// Manager manages bare git clones for diff computation.
type Manager struct {
	baseDir string            // directory to store clones
	tokens  map[string]string // host -> token (e.g., "github.com" -> "ghp_...")

	// ensureSF deduplicates concurrent EnsureClone calls for the same
	// (host, owner, name). Without it, callers like the periodic syncer,
	// per-PR detail syncs, and workspace setup race each other on the
	// same bare clone and trigger a stampede of identical git fetches,
	// which GitHub's smart-HTTP edge throttles with sporadic 5xx.
	ensureSF singleflight.Group
}

// New creates a Manager that stores bare clones under baseDir.
// tokens maps each host (e.g., "github.com") to its auth token.
// A nil or empty map means all operations proceed without auth.
func New(baseDir string, tokens map[string]string) *Manager {
	return &Manager{baseDir: baseDir, tokens: tokens}
}

// ClonePath returns the filesystem path for a repo's bare clone.
// Path is partitioned by host: {baseDir}/{host}/{owner}/{name}.git
func (m *Manager) ClonePath(host, owner, name string) (string, error) {
	if host == "" && owner == "" {
		// Preserve local fixture clones at {baseDir}/{name}.git while
		// still using kit's path validator for the repository name.
		if _, err := gitremote.ClonePath(m.baseDir, gitremote.Identity{
			Host:  "local",
			Owner: "fixture",
			Name:  name,
		}); err != nil {
			return "", err
		}
		return filepath.Join(m.baseDir, name+".git"), nil
	}
	return gitremote.ClonePath(m.baseDir, gitremote.Identity{
		Host:  host,
		Owner: owner,
		Name:  name,
	})
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
	ctx context.Context, host, owner, name, remoteURL string,
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
	if _, err := m.ClonePath(host, owner, name); err != nil {
		return err
	}
	key := ensureCloneKey(host, owner, name)
	ch := m.ensureSF.DoChan(key, func() (any, error) {
		opCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx), ensureCloneTimeout,
		)
		defer cancel()
		return nil, m.ensureCloneNow(opCtx, host, owner, name, remoteURL)
	})
	select {
	case res := <-ch:
		return res.Err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func ensureCloneKey(host, owner, name string) string {
	return host + "\x00" + owner + "\x00" + name
}

// ensureCloneNow is the unshared inner: it decides whether to create a
// fresh bare clone or refresh an existing one. Always called from
// inside the singleflight slot opened by EnsureClone, which has
// already validated the caller's remoteURL.
func (m *Manager) ensureCloneNow(
	ctx context.Context, host, owner, name, remoteURL string,
) error {
	clonePath, err := m.ClonePath(host, owner, name)
	if err != nil {
		return err
	}

	if _, err := os.Stat(filepath.Join(clonePath, "HEAD")); os.IsNotExist(err) {
		return m.cloneBare(ctx, host, clonePath, remoteURL)
	}
	// On an existing clone, also re-verify the stored origin URL
	// belongs to the expected host: catches a clone whose config
	// was rewritten after creation.
	if out, err := m.git(ctx, host, clonePath, "config", "--get", "remote.origin.url"); err == nil {
		if err := validateRemoteURLIdentity(host, owner, name, strings.TrimSpace(string(out))); err != nil {
			return err
		}
	}
	m.ensureRefspecs(ctx, host, clonePath)
	return m.fetch(ctx, host, clonePath)
}

// Fetch refspecs configured on every bare clone.
//
//   - remoteTrackingRefspec stores origin branches under
//     refs/remotes/origin/* so bare-clone fetches never try to update a local
//     branch that a workspace has checked out.
//   - pullRefspec makes refs/pull/<N>/head available, which is how we resolve
//     PR heads that live on forks.
const (
	legacyBranchRefspec   = "+refs/heads/*:refs/heads/*"
	remoteTrackingRefspec = "+refs/heads/*:refs/remotes/origin/*"
	pullRefspec           = "+refs/pull/*/head:refs/pull/*/head"
)

// defaultRefspecs returns the full list of fetch refspecs every clone should
// have. Used by both cloneBare (fresh clones) and ensureRefspecs (migration).
func defaultRefspecs() []string {
	return []string{remoteTrackingRefspec, pullRefspec}
}

// ensureRefspecs idempotently adds any missing fetch refspecs to an
// existing clone. This upgrades clones created before branch/pull ref
// support was in place, including vanilla `git clone --bare` output with
// no configured fetch refspec at all.
func (m *Manager) ensureRefspecs(
	ctx context.Context, host, clonePath string,
) {
	// `git config --get-all` exits 1 with no output when the key is unset.
	// Treat any read failure as "no existing refspecs" and fall through to
	// the add loop, which is idempotent on its own and will log its own
	// warnings if the add commands fail for a real reason.
	out, _ := m.git(ctx, host, clonePath,
		"config", "--get-all", "remote.origin.fetch")
	existing := make(map[string]bool)
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			existing[line] = true
		}
	}
	if existing[legacyBranchRefspec] {
		if _, err := m.git(
			ctx, host, clonePath,
			"config", "--fixed-value", "--unset-all",
			"remote.origin.fetch", legacyBranchRefspec,
		); err != nil {
			slog.Warn("failed to remove legacy refspec from existing clone",
				"path", clonePath, "refspec", legacyBranchRefspec, "err", err)
		} else {
			delete(existing, legacyBranchRefspec)
		}
	}
	for _, refspec := range defaultRefspecs() {
		if existing[refspec] {
			continue
		}
		if _, err := m.git(ctx, host, clonePath,
			"config", "--add", "remote.origin.fetch", refspec); err != nil {
			slog.Warn("failed to add refspec to existing clone",
				"path", clonePath, "refspec", refspec, "err", err)
		}
	}
}

func (m *Manager) cloneBare(
	ctx context.Context, host, clonePath, remoteURL string,
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
		return m.git(ctx, host, "", "clone", "--bare", remoteURL, clonePath)
	})
	if err != nil {
		return fmt.Errorf("git clone --bare: %w", err)
	}

	// Install fetch refspecs so future fetches pull both branch heads and
	// pull refs. git clone --bare does not install a default refspec.
	// On failure, remove the partial clone so the next call retries.
	for _, refspec := range defaultRefspecs() {
		if _, err := m.git(ctx, host, clonePath,
			"config", "--add", "remote.origin.fetch", refspec); err != nil {
			os.RemoveAll(clonePath)
			return fmt.Errorf("add fetch refspec %q: %w", refspec, err)
		}
	}

	// Fetch immediately after clone so pull refs are available before
	// merge-base computation runs in the same sync cycle.
	return m.fetch(ctx, host, clonePath)
}

func (m *Manager) fetch(
	ctx context.Context, host, clonePath string,
) error {
	// GitHub's smart-HTTP endpoint sporadically returns 5xx on /info/refs.
	// Retry inline so a transient blip does not drop the entire sync cycle.
	_, err := retryTransient(ctx, "git fetch", func() ([]byte, error) {
		return m.git(ctx, host, clonePath, "fetch", "--prune", "origin")
	})
	if err != nil {
		return fmt.Errorf("git fetch: %w", err)
	}
	// set-head -a is networked (it consults the remote's HEAD via
	// /info/refs) and so subject to the same transient 5xx as fetch.
	// Failure is non-fatal — bare clone still works — but retrying
	// reduces stale-HEAD noise across sync cycles.
	_, setHeadErr := retryTransient(ctx, "git remote set-head", func() ([]byte, error) {
		return m.git(ctx, host, clonePath, "remote", "set-head", "origin", "-a")
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
	ctx context.Context, host, owner, name, ref string,
) (string, error) {
	clonePath, err := m.ClonePath(host, owner, name)
	if err != nil {
		return "", err
	}
	out, err := m.git(ctx, host, clonePath, "rev-parse", "--verify", ref)
	if err != nil {
		return "", fmt.Errorf("git rev-parse %s: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// MergeBase computes the merge base between two commits.
func (m *Manager) MergeBase(
	ctx context.Context, host, owner, name, sha1, sha2 string,
) (string, error) {
	clonePath, err := m.ClonePath(host, owner, name)
	if err != nil {
		return "", err
	}
	out, err := m.git(ctx, host, clonePath, "merge-base", sha1, sha2)
	if err != nil {
		return "", fmt.Errorf("git merge-base %s %s: %w", sha1, sha2, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// git runs a git command with auth configured for the given host.
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

func (m *Manager) git(
	ctx context.Context, host, dir string, args ...string,
) ([]byte, error) {
	return m.gitWithInput(ctx, host, dir, nil, args...)
}

func (m *Manager) gitWithInput(
	ctx context.Context, host, dir string, input []byte, args ...string,
) ([]byte, error) {
	runner := m.gitRunner(host)
	var stdin io.Reader
	if input != nil {
		stdin = bytes.NewReader(input)
	}
	release, err := procutil.TryAcquire(ctx, "git subprocess capacity")
	if err != nil {
		return nil, err
	}
	defer release()
	out, stderr, err := runner.Run(ctx, dir, stdin, args...)
	if err != nil {
		msg := string(stderr)
		if isNotFoundError(msg) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, msg)
		}
		return nil, fmt.Errorf("%w: %s", err, msg)
	}
	return out, nil
}

func (m *Manager) gitRunner(host string) gitcmd.Runner {
	// Middleman relies on kit's automation defaults here: inherited GIT_*
	// variables are stripped, global/system config is ignored, and terminal
	// prompts are disabled. Clone/fetch still uses middleman's subprocess
	// limiter above because it shares capacity with the rest of the app.
	runner := gitcmd.New()
	if token := m.tokens[host]; token != "" {
		// GitHub's smart HTTP endpoint expects Basic auth credentials.
		runner = runner.WithBasicAuth("x-access-token", token)
	}
	return runner
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
