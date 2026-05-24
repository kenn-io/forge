package gitclone

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ResolveDefaultBranch resolves the preferred remote branch in the clone.
// If preferred is empty or stale, it falls back to the origin/HEAD symref.
func (m *Manager) ResolveDefaultBranch(
	ctx context.Context,
	host, owner, name, preferred string,
) (branch string, ref string, err error) {
	dir, err := m.ClonePath(host, owner, name)
	if err != nil {
		return "", "", err
	}

	preferred = strings.TrimSpace(preferred)
	if preferred != "" {
		for _, candidate := range branchActivityRefCandidates(preferred) {
			if sha, err := m.resolveRefInDir(ctx, host, dir, candidate); err == nil {
				return defaultBranchNameForResolvedCandidate(preferred, candidate), sha, nil
			} else if !isMissingRefError(err) {
				return "", "", fmt.Errorf("resolve preferred default branch %s: %w", preferred, err)
			}
		}
	}

	out, err := m.git(ctx, host, dir,
		"symbolic-ref", "--quiet", "refs/remotes/origin/HEAD",
	)
	if err != nil {
		return "", "", fmt.Errorf("resolve origin HEAD: %w", err)
	}
	remoteRef := strings.TrimSpace(string(out))
	branch, ok := strings.CutPrefix(remoteRef, "refs/remotes/origin/")
	if !ok || branch == "" || branch == "HEAD" {
		return "", "", fmt.Errorf("resolve origin HEAD: %w", ErrNotFound)
	}
	sha, err := m.resolveRefInDir(ctx, host, dir, remoteRef)
	if err != nil {
		return "", "", fmt.Errorf("resolve origin HEAD target %s: %w", remoteRef, err)
	}
	return branch, sha, nil
}

func defaultBranchNameForResolvedCandidate(preferred, candidate string) string {
	if branch, ok := strings.CutPrefix(preferred, "origin/"); ok &&
		branch != "" &&
		candidate == remoteBranchRef(branch) {
		return branch
	}
	return preferred
}

// ResolveRef resolves a branch, ref, or SHA in the clone to a commit SHA.
// Plain branch names prefer origin's remote-tracking refs.
func (m *Manager) ResolveRef(
	ctx context.Context,
	host, owner, name, ref string,
) (string, error) {
	dir, err := m.ClonePath(host, owner, name)
	if err != nil {
		return "", err
	}
	refName, err := m.resolveBranchActivityRef(ctx, host, dir, ref)
	if err != nil {
		return "", err
	}
	return m.resolveRefInDir(ctx, host, dir, refName)
}

// ResolveCommit resolves objectID directly to a commit SHA without branch
// fallback.
func (m *Manager) ResolveCommit(
	ctx context.Context,
	host, owner, name, objectID string,
) (string, error) {
	dir, err := m.ClonePath(host, owner, name)
	if err != nil {
		return "", err
	}
	return m.resolveRefInDir(ctx, host, dir, objectID)
}

// IsAncestor reports whether ancestor is reachable from descendant.
func (m *Manager) IsAncestor(
	ctx context.Context,
	host, owner, name, ancestor, descendant string,
) (bool, error) {
	dir, err := m.ClonePath(host, owner, name)
	if err != nil {
		return false, err
	}
	_, err = m.git(ctx, host, dir,
		"merge-base", "--is-ancestor", ancestor, descendant,
	)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check ancestor %s..%s: %w", ancestor, descendant, err)
}

// ListBranchCommitsSince returns first-parent commits for a branch/ref newest
// first. afterSHA takes precedence over since when both are provided.
func (m *Manager) ListBranchCommitsSince(
	ctx context.Context,
	host, owner, name, ref string,
	since time.Time,
	afterSHA string,
	maxCount int,
) ([]Commit, error) {
	dir, err := m.ClonePath(host, owner, name)
	if err != nil {
		return nil, err
	}
	refName, err := m.resolveBranchActivityRef(ctx, host, dir, ref)
	if err != nil {
		return nil, err
	}

	args := []string{"log", "--first-parent", "--format=" + commitLogFormat}
	if maxCount > 0 {
		args = append(args, "--max-count="+strconv.Itoa(maxCount))
	}
	if afterSHA != "" {
		args = append(args, afterSHA+".."+refName)
	} else {
		args = append(args, "--since="+since.UTC().Format(time.RFC3339), refName)
	}
	out, err := m.git(ctx, host, dir, args...)
	if err != nil {
		return nil, fmt.Errorf("list branch commits %s: %w", ref, err)
	}
	return parseCommitLog(out)
}

func (m *Manager) resolveBranchActivityRef(
	ctx context.Context,
	host, dir, ref string,
) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("resolve ref: %w", ErrNotFound)
	}
	var lastErr error
	for _, candidate := range branchActivityRefCandidates(ref) {
		if _, err := m.resolveRefInDir(ctx, host, dir, candidate); err == nil {
			return candidate, nil
		} else {
			lastErr = err
			if !isMissingRefError(err) {
				return "", fmt.Errorf("resolve ref %s: %w", ref, err)
			}
		}
	}
	if lastErr != nil {
		return "", fmt.Errorf("resolve ref %s: %w", ref, lastErr)
	}
	return "", fmt.Errorf("resolve ref %s: %w", ref, ErrNotFound)
}

func branchActivityRefCandidates(ref string) []string {
	var candidates []string
	add := func(candidate string) {
		if slices.Contains(candidates, candidate) {
			return
		}
		candidates = append(candidates, candidate)
	}
	if !strings.HasPrefix(ref, "refs/") {
		add(remoteBranchRef(ref))
	}
	if branch, ok := strings.CutPrefix(ref, "origin/"); ok && branch != "" {
		add(remoteBranchRef(branch))
	}
	add(ref)
	return candidates
}

func remoteBranchRef(branch string) string {
	return "refs/remotes/origin/" + branch
}

func isMissingRefError(err error) bool {
	if errors.Is(err, ErrNotFound) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "needed a single revision")
}

func (m *Manager) resolveRefInDir(
	ctx context.Context,
	host, dir, ref string,
) (string, error) {
	out, err := m.git(ctx, host, dir,
		"rev-parse", "--verify", "--end-of-options", ref+"^{commit}",
	)
	if err != nil {
		return "", normalizeMissingCommitError(err)
	}
	return strings.TrimSpace(string(out)), nil
}

func normalizeMissingCommitError(err error) error {
	if isMissingRefError(err) {
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	return err
}
