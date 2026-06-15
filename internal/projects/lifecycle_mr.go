package projects

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"go.kenn.io/middleman/internal/platform"
)

// MergeRequestWorktreeOptions parameterizes CreateWorktreeFromMergeRequest.
// ProjectRoot, Branch, and Number are required. HeadBranch and
// HeadRepoCloneURL come from the synced merge-request metadata;
// ProjectRepoIdentity is the CloneURLIdentity-normalized identity of the
// project's own repository, used to recognize same-repo merge requests.
type MergeRequestWorktreeOptions struct {
	ProjectRoot  string
	Branch       string
	Path         string
	BaseDir      string
	SetupScript  string
	WorktreeName string

	Number              int
	HeadBranch          string
	HeadRepoCloneURL    string
	ProjectRepoIdentity string
	// Platform is the project's platform kind ("github", "gitlab", ...);
	// it selects the remote ref that carries the merge request head when
	// the head must be fetched by number rather than by branch.
	Platform string
}

// mergeRequestRemoteTarget describes how to materialize a merge request
// head locally: the fetch that makes the checkout ref available, the ref
// the new branch starts at, an optional second fetch that makes upstream
// tracking possible, and the tracking remote/merge-ref pair.
type mergeRequestRemoteTarget struct {
	checkoutFetch    []string
	checkoutRef      string
	trackingFetch    []string
	trackingRemote   string
	trackingMergeRef string
}

// CreateWorktreeFromMergeRequest materializes a merge request head as a new
// worktree: it fetches the head (from the project's own branch, the fork,
// or the platform pull ref), creates a new local branch on it, configures
// upstream tracking when possible (non-fatally skipping it when the fork
// cannot be fetched), and runs the optional setup hook. Failures after the
// worktree exists roll it back.
func CreateWorktreeFromMergeRequest(
	ctx context.Context, opts MergeRequestWorktreeOptions,
) (CreateWorktreeResult, error) {
	root, branch, err := requireRootAndBranch(
		opts.ProjectRoot, opts.Branch,
	)
	if err != nil {
		return CreateWorktreeResult{}, err
	}
	if opts.Number < 1 {
		return CreateWorktreeResult{}, fmt.Errorf(
			"merge request number is required",
		)
	}
	if err := validateBranchName(ctx, root, branch); err != nil {
		return CreateWorktreeResult{}, err
	}
	hookScript, err := resolveHookScript(root, opts.SetupScript)
	if err != nil {
		return CreateWorktreeResult{}, err
	}
	path, err := resolveWorktreeDestination(
		root, branch, opts.Path, opts.BaseDir,
	)
	if err != nil {
		return CreateWorktreeResult{}, err
	}

	target, err := prepareMergeRequestRemote(ctx, root, opts)
	if err != nil {
		return CreateWorktreeResult{}, err
	}

	if out, err := runLifecycleGit(
		ctx, root, target.checkoutFetch...,
	); err != nil {
		return CreateWorktreeResult{}, classifyWorktreeGitError(out, err)
	}
	trackingEnabled := target.trackingRemote != "" &&
		target.trackingMergeRef != ""
	if trackingEnabled && len(target.trackingFetch) > 0 {
		// The tracking fetch is best-effort: a fork that has vanished
		// or is unreachable must not block importing via the pull ref.
		if _, err := runLifecycleGit(
			ctx, root, target.trackingFetch...,
		); err != nil {
			trackingEnabled = false
		}
	}

	// --no-track: tracking is configured explicitly below; without it git
	// auto-tracks the remote-tracking start point (e.g. the read-only
	// pull ref), which is wrong for pushes.
	if out, err := runLifecycleGit(
		ctx, root,
		"worktree", "add", path, "--no-track", "-b", branch,
		"--", target.checkoutRef,
	); err != nil {
		return CreateWorktreeResult{}, classifyWorktreeGitError(out, err)
	}

	if trackingEnabled {
		if err := configureMergeRequestTracking(
			ctx, root, path, branch, target,
		); err != nil {
			rollbackCreatedWorktree(ctx, root, path, branch, true)
			return CreateWorktreeResult{}, err
		}
	}

	result := CreateWorktreeResult{
		Path: path, Branch: branch, BranchCreated: true,
	}
	if hookScript != "" {
		if hookErr := runLifecycleHook(
			ctx, hookScript, root, path, branch, opts.WorktreeName,
		); hookErr != nil {
			rollbackCreatedWorktree(ctx, root, path, branch, true)
			return CreateWorktreeResult{}, hookErr
		}
		result.HookRan = true
		result.HookScript = hookScript
	}
	return result, nil
}

// prepareMergeRequestRemote decides how to fetch the merge request head.
// Same-repo heads fetch the branch from origin directly with tracking;
// fork heads fetch the checkout from the platform pull ref while tracking
// a dedicated fork remote; heads with no clone URL fetch the pull ref with
// no tracking.
func prepareMergeRequestRemote(
	ctx context.Context, root string, opts MergeRequestWorktreeOptions,
) (mergeRequestRemoteTarget, error) {
	headBranch := strings.TrimSpace(opts.HeadBranch)
	if headBranch == "" {
		headBranch = "merge-request"
	}

	cloneURL := strings.TrimSpace(opts.HeadRepoCloneURL)
	sameRepo := cloneURL != "" && opts.ProjectRepoIdentity != "" &&
		strings.EqualFold(
			CloneURLIdentity(cloneURL), opts.ProjectRepoIdentity,
		)
	if sameRepo {
		return mergeRequestRemoteTarget{
			checkoutFetch: []string{
				"fetch", "origin",
				fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s",
					headBranch, headBranch),
			},
			checkoutRef:      "origin/" + headBranch,
			trackingRemote:   "origin",
			trackingMergeRef: "refs/heads/" + headBranch,
		}, nil
	}

	// The platform's merge-request head ref (refs/pull/<n>/head on GitHub,
	// Forgejo, and Gitea; refs/merge-requests/<n>/head on GitLab) carries
	// the head commit regardless of where the head branch lives.
	headRef := platform.MergeRequestHeadRef(
		platform.Kind(opts.Platform), opts.Number,
	)
	localRef := strings.TrimPrefix(headRef, "refs/")
	pullRefFetch := []string{
		"fetch", "origin",
		fmt.Sprintf("+%s:refs/remotes/origin/%s", headRef, localRef),
	}
	pullRef := "origin/" + localRef

	if cloneURL == "" {
		return mergeRequestRemoteTarget{
			checkoutFetch: pullRefFetch,
			checkoutRef:   pullRef,
		}, nil
	}

	preferredName := sanitizeRemoteName(ownerFromCloneURL(cloneURL))
	if preferredName == "" {
		preferredName = "mr-head"
	}
	remoteName, err := ensureFetchRemote(ctx, root, preferredName, cloneURL)
	if err != nil {
		return mergeRequestRemoteTarget{}, err
	}
	return mergeRequestRemoteTarget{
		checkoutFetch: pullRefFetch,
		checkoutRef:   pullRef,
		trackingFetch: []string{
			"fetch", remoteName,
			fmt.Sprintf("+refs/heads/%s:refs/remotes/%s/%s",
				headBranch, remoteName, headBranch),
		},
		trackingRemote:   remoteName,
		trackingMergeRef: "refs/heads/" + headBranch,
	}, nil
}

// configureMergeRequestTracking points the imported branch at its upstream
// using worktree-scoped config, so `git push`/`git pull` in the worktree
// target the merge request head without affecting the primary checkout.
func configureMergeRequestTracking(
	ctx context.Context,
	root, worktreePath, branch string,
	target mergeRequestRemoteTarget,
) error {
	steps := []struct {
		dir  string
		args []string
	}{
		{root, []string{"config", "extensions.worktreeConfig", "true"}},
		{worktreePath, []string{
			"config", "branch." + branch + ".remote", target.trackingRemote,
		}},
		{worktreePath, []string{
			"config", "branch." + branch + ".merge", target.trackingMergeRef,
		}},
		{worktreePath, []string{
			"config", "--worktree", "push.default", "upstream",
		}},
	}
	for _, step := range steps {
		if out, err := runLifecycleGit(
			ctx, step.dir, step.args...,
		); err != nil {
			return fmt.Errorf(
				"configure merge request tracking (git %s): %w: %s",
				strings.Join(step.args, " "), err,
				strings.TrimSpace(string(out)),
			)
		}
	}
	return nil
}

// ensureFetchRemote returns the name of a remote pointing at cloneURL,
// adding one when absent. Name collisions with other URLs fall back to
// numbered suffixes.
func ensureFetchRemote(
	ctx context.Context, root, preferredName, cloneURL string,
) (string, error) {
	const maxAttempts = 100
	candidate := preferredName
	for attempt := 2; attempt < maxAttempts+2; attempt++ {
		existing, err := runLifecycleGit(
			ctx, root, "config", "--get",
			"remote."+candidate+".url",
		)
		if err != nil {
			// The remote does not exist yet; claim the name.
			if out, addErr := runLifecycleGit(
				ctx, root, "remote", "add", candidate, cloneURL,
			); addErr != nil {
				return "", fmt.Errorf(
					"add remote %s: %w: %s", candidate, addErr,
					strings.TrimSpace(string(out)),
				)
			}
			return candidate, nil
		}
		existingURL := strings.TrimSpace(string(existing))
		if existingURL == cloneURL ||
			CloneURLIdentity(existingURL) == CloneURLIdentity(cloneURL) {
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s-%d", preferredName, attempt)
	}
	return "", fmt.Errorf(
		"no unique remote name for %s after %d attempts",
		cloneURL, maxAttempts,
	)
}

var remoteNameSanitizer = regexp.MustCompile(`[^A-Za-z0-9]+`)

// sanitizeRemoteName folds a repository owner into a git-safe remote name.
func sanitizeRemoteName(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	return strings.Trim(
		remoteNameSanitizer.ReplaceAllString(trimmed, "-"), "-",
	)
}

// CloneURLIdentity normalizes a clone URL to a comparable identity:
// "host/owner/name" for URL and scp-like forms (lowercased host, ".git"
// stripped), or the trimmed input for anything else (such as local paths).
func CloneURLIdentity(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}
	if strings.Contains(trimmed, "://") {
		parsed, err := url.Parse(trimmed)
		if err == nil && parsed.Host != "" {
			path := strings.TrimPrefix(parsed.Path, "/")
			path = strings.TrimSuffix(path, ".git")
			if path != "" {
				return strings.ToLower(parsed.Host) + "/" + path
			}
		}
	}
	if atIndex := strings.Index(trimmed, "@"); atIndex >= 0 {
		if colonIndex := strings.Index(trimmed[atIndex:], ":"); colonIndex >= 0 {
			colonIndex += atIndex
			host := strings.ToLower(trimmed[atIndex+1 : colonIndex])
			path := strings.TrimSuffix(trimmed[colonIndex+1:], ".git")
			if path != "" {
				return host + "/" + path
			}
		}
	}
	return trimmed
}

// ownerFromCloneURL extracts the repository owner segment from a clone
// URL identity ("host/owner/name"). Identities without that shape (local
// paths) yield "".
func ownerFromCloneURL(rawURL string) string {
	identity := CloneURLIdentity(rawURL)
	if _, after, ok := strings.Cut(identity, "/"); ok {
		if owner, _, ok := strings.Cut(after, "/"); ok && owner != "" {
			return owner
		}
	}
	return ""
}
