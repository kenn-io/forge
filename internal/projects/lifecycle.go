package projects

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	gitenv "go.kenn.io/kit/git/env"
	"go.kenn.io/middleman/internal/procutil"
)

// Sentinel errors for worktree lifecycle failures the HTTP layer maps to
// distinct problem codes. They are wrapped with operation detail; match
// with errors.Is.
var (
	// ErrWorktreeDestinationExists reports that the worktree target path
	// already exists on disk or is already used by another worktree.
	ErrWorktreeDestinationExists = errors.New(
		"worktree destination already exists",
	)
	// ErrBranchInUse reports that the branch is checked out in another
	// worktree, so it can be neither attached nor deleted.
	ErrBranchInUse = errors.New(
		"branch is checked out in another worktree",
	)
	// ErrInvalidBranchName reports a branch name git rejects
	// (`git check-ref-format --branch`).
	ErrInvalidBranchName = errors.New("invalid branch name")
	// ErrHookOutsideProject reports a lifecycle hook script path that
	// resolves outside the project tree. Hooks are arbitrary executables;
	// confining them to the project keeps a registry entry from running
	// code elsewhere on the machine.
	ErrHookOutsideProject = errors.New(
		"lifecycle hook script resolves outside the project",
	)
)

// HookError reports a lifecycle hook script that ran and exited non-zero.
type HookError struct {
	Script   string
	ExitCode int
	Stderr   string
}

func (e *HookError) Error() string {
	return fmt.Sprintf(
		"%s failed with exit code %d: %s", e.Script, e.ExitCode, e.Stderr,
	)
}

// Lifecycle hook environment variable names. Hook scripts receive the
// worktree identity through these in addition to the inherited environment.
const (
	hookEnvWorktreeName = "MIDDLEMAN_WORKTREE_NAME"
	hookEnvWorktreePath = "MIDDLEMAN_WORKTREE_PATH"
	hookEnvProjectRoot  = "MIDDLEMAN_PROJECT_ROOT"
	hookEnvBranch       = "MIDDLEMAN_BRANCH"
)

// CreateWorktreeOptions parameterizes CreateWorktreeOnDisk. ProjectRoot and
// Branch are required; everything else is optional. Lifecycle script paths
// arrive per call: the caller owns config sourcing (project files, app
// settings) and middleman owns execution.
type CreateWorktreeOptions struct {
	// ProjectRoot is the repository checkout git commands run in.
	ProjectRoot string
	// Branch is the branch to attach or create.
	Branch string
	// Path is the worktree destination. When empty it derives from
	// BaseDir (default "<ProjectRoot>-worktrees") plus the slash-slugged
	// branch name.
	Path string
	// BaseDir overrides the derivation base used when Path is empty.
	BaseDir string
	// BaseRef, when set, forces creation of a new Branch starting at this
	// ref (git worktree add <path> -b <branch> -- <ref>). When empty, an
	// existing local Branch is attached and a missing one is created from
	// HEAD.
	BaseRef string
	// SetupScript, when set, runs in the new worktree after git work
	// succeeds. Relative paths resolve against ProjectRoot; the resolved
	// path must stay inside the project tree. A non-zero exit rolls the
	// worktree (and any branch this call created) back.
	SetupScript string
	// WorktreeName is the display name exported to hook scripts; defaults
	// to Branch.
	WorktreeName string
}

// CreateWorktreeResult reports what CreateWorktreeOnDisk did.
type CreateWorktreeResult struct {
	Path   string
	Branch string
	// BranchCreated reports whether this call created the branch (as
	// opposed to attaching a pre-existing local branch). Callers rolling
	// the git work back must pass it to RollbackCreatedWorktree so a
	// pre-existing branch is never force-deleted.
	BranchCreated bool
	HookRan       bool
	HookScript    string
}

// CreateWorktreeOnDisk performs the git side of worktree creation: it
// derives and validates the destination, runs `git worktree add`
// (attaching an existing branch or creating a new one), and runs the
// optional setup hook. On hook failure the worktree — and the branch this
// call created — are rolled back so a retry does not trip
// ErrWorktreeDestinationExists.
func CreateWorktreeOnDisk(
	ctx context.Context, opts CreateWorktreeOptions,
) (CreateWorktreeResult, error) {
	root, branch, err := requireRootAndBranch(
		opts.ProjectRoot, opts.Branch,
	)
	if err != nil {
		return CreateWorktreeResult{}, err
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

	branchExisted := localBranchExists(ctx, root, branch)
	var args []string
	switch {
	case opts.BaseRef != "":
		// Double-dash keeps a ref-like branch argument from being
		// parsed as a path and vice versa.
		args = []string{
			"worktree", "add", path, "-b", branch, "--", opts.BaseRef,
		}
	case branchExisted:
		args = []string{"worktree", "add", path, branch}
	default:
		args = []string{"worktree", "add", "-b", branch, path}
	}
	if out, err := runLifecycleGit(ctx, root, args...); err != nil {
		return CreateWorktreeResult{}, classifyWorktreeGitError(out, err)
	}

	result := CreateWorktreeResult{
		Path: path, Branch: branch, BranchCreated: !branchExisted,
	}
	if hookScript != "" {
		hookErr := runLifecycleHook(
			ctx, hookScript, root, path, branch, opts.WorktreeName,
		)
		if hookErr != nil {
			rollbackCreatedWorktree(
				ctx, root, path, branch, !branchExisted,
			)
			return CreateWorktreeResult{}, hookErr
		}
		result.HookRan = true
		result.HookScript = hookScript
	}
	return result, nil
}

// RemoveWorktreeOptions parameterizes RemoveWorktreeFromDisk. ProjectRoot
// and Path are required.
type RemoveWorktreeOptions struct {
	ProjectRoot string
	Path        string
	// Branch is the branch deleted when RemoveBranch is set; an empty
	// branch (detached HEAD) makes RemoveBranch a no-op.
	Branch string
	// Force passes --force to git worktree remove so dirty or locked
	// worktrees still go. Policy checks (refusing dirty removal without
	// force) belong to the caller.
	Force        bool
	RemoveBranch bool
	// TeardownScript, when set, runs in the worktree before removal.
	// Relative paths resolve against ProjectRoot and must stay inside the
	// project tree. A non-zero exit aborts the removal. The hook is
	// skipped when the worktree path is already gone.
	TeardownScript string
	// WorktreeName is the display name exported to hook scripts; defaults
	// to Branch.
	WorktreeName string
}

// RemoveWorktreeResult reports what RemoveWorktreeFromDisk did.
type RemoveWorktreeResult struct {
	HookRan    bool
	HookScript string
}

// RemoveWorktreeFromDisk performs the git side of worktree removal: it
// runs the optional teardown hook, removes the worktree (or prunes the
// stale registration when the path is already gone), and optionally
// deletes the branch.
func RemoveWorktreeFromDisk(
	ctx context.Context, opts RemoveWorktreeOptions,
) (RemoveWorktreeResult, error) {
	root, err := absRequired(opts.ProjectRoot, "project root")
	if err != nil {
		return RemoveWorktreeResult{}, err
	}
	path, err := absRequired(opts.Path, "worktree path")
	if err != nil {
		return RemoveWorktreeResult{}, err
	}
	hookScript, err := resolveHookScript(root, opts.TeardownScript)
	if err != nil {
		return RemoveWorktreeResult{}, err
	}

	pathExists := true
	if _, statErr := os.Stat(path); statErr != nil {
		if !os.IsNotExist(statErr) {
			return RemoveWorktreeResult{}, fmt.Errorf(
				"stat worktree path: %w", statErr,
			)
		}
		pathExists = false
	}

	result := RemoveWorktreeResult{}
	if hookScript != "" && pathExists {
		if hookErr := runLifecycleHook(
			ctx, hookScript, root, path, opts.Branch, opts.WorktreeName,
		); hookErr != nil {
			return RemoveWorktreeResult{}, hookErr
		}
		result.HookRan = true
		result.HookScript = hookScript
	}

	if pathExists {
		args := []string{"worktree", "remove"}
		if opts.Force {
			args = append(args, "--force")
		}
		args = append(args, path)
		if out, err := runLifecycleGit(ctx, root, args...); err != nil {
			return result, classifyWorktreeGitError(out, err)
		}
	} else {
		// The directory is gone but git may still hold a stale
		// registration that would block branch deletion and re-creation.
		if out, err := runLifecycleGit(
			ctx, root, "worktree", "prune",
		); err != nil {
			return result, classifyWorktreeGitError(out, err)
		}
	}

	if opts.RemoveBranch && strings.TrimSpace(opts.Branch) != "" {
		if out, err := runLifecycleGit(
			ctx, root, "branch", "-D", "--", opts.Branch,
		); err != nil {
			return result, classifyWorktreeGitError(out, err)
		}
	}
	return result, nil
}

// WorktreeIsDirty reports whether the worktree at path has uncommitted
// changes (staged, unstaged, or untracked).
func WorktreeIsDirty(ctx context.Context, path string) (bool, error) {
	out, err := runLifecycleGit(ctx, path, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf(
			"check worktree dirty state: %w: %s",
			err, strings.TrimSpace(string(out)),
		)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

func requireRootAndBranch(
	rawRoot, rawBranch string,
) (string, string, error) {
	root, err := absRequired(rawRoot, "project root")
	if err != nil {
		return "", "", err
	}
	branch := strings.TrimSpace(rawBranch)
	if branch == "" {
		return "", "", fmt.Errorf("branch is required")
	}
	return root, branch, nil
}

func absRequired(raw, label string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", label, err)
	}
	return abs, nil
}

func validateBranchName(
	ctx context.Context, root, branch string,
) error {
	if _, err := runLifecycleGit(
		ctx, root, "check-ref-format", "--branch", branch,
	); err != nil {
		return fmt.Errorf("%w: %q", ErrInvalidBranchName, branch)
	}
	return nil
}

// resolveHookScript resolves a caller-supplied hook script path against the
// project root and rejects paths that escape it. Both sides of the
// containment check are canonicalized through symlink resolution so a
// symlink inside the project cannot smuggle in a script that lives outside
// it. An empty raw path means no hook and resolves to "".
func resolveHookScript(projectRoot, raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	resolved := trimmed
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(projectRoot, resolved)
	}
	resolved = filepath.Clean(resolved)
	if !pathWithinRoot(canonicalizePath(projectRoot), canonicalizePath(resolved)) {
		return "", fmt.Errorf("%w: %q", ErrHookOutsideProject, raw)
	}
	return resolved, nil
}

// canonicalizePath resolves symlinks when the path exists; a path that does
// not exist yet (or cannot be resolved) keeps its lexical form, which fails
// later at execution time rather than here.
func canonicalizePath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

func pathWithinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveWorktreeDestination returns the validated absolute worktree
// destination. An explicit path wins; otherwise the destination derives
// from baseDir (default "<root>-worktrees") plus the slash-slugged branch.
// The destination must not already exist.
func resolveWorktreeDestination(
	root, branch, explicitPath, baseDir string,
) (string, error) {
	var dest string
	if strings.TrimSpace(explicitPath) != "" {
		abs, err := filepath.Abs(strings.TrimSpace(explicitPath))
		if err != nil {
			return "", fmt.Errorf("resolve worktree path: %w", err)
		}
		dest = abs
	} else {
		base := strings.TrimSpace(baseDir)
		if base == "" {
			base = root + "-worktrees"
		}
		if err := os.MkdirAll(base, 0o755); err != nil {
			return "", fmt.Errorf("create worktree base dir: %w", err)
		}
		// Canonicalize the base so derived paths agree with what git
		// and discovery report (macOS /tmp vs /private/tmp).
		if resolved, err := filepath.EvalSymlinks(base); err == nil {
			base = resolved
		}
		slug := strings.ReplaceAll(branch, "/", "-")
		abs, err := filepath.Abs(filepath.Join(base, slug))
		if err != nil {
			return "", fmt.Errorf("resolve worktree path: %w", err)
		}
		dest = abs
	}
	if _, err := os.Stat(dest); err == nil {
		return "", fmt.Errorf(
			"%w: %s", ErrWorktreeDestinationExists, dest,
		)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat worktree destination: %w", err)
	}
	return dest, nil
}

func localBranchExists(ctx context.Context, root, branch string) bool {
	_, err := runLifecycleGit(
		ctx, root, "show-ref", "--verify", "--quiet",
		"refs/heads/"+branch,
	)
	return err == nil
}

func runLifecycleGit(
	ctx context.Context, dir string, args ...string,
) ([]byte, error) {
	cmd := procutil.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = gitenv.StripAll(os.Environ())
	return procutil.CombinedOutput(ctx, cmd, "worktree lifecycle git")
}

// classifyWorktreeGitError maps well-known git stderr phrases onto the
// package sentinels so the HTTP layer can answer with distinct problem
// codes instead of a generic failure.
func classifyWorktreeGitError(out []byte, err error) error {
	detail := strings.TrimSpace(string(out))
	switch {
	// "'X' is already checked out at ..." (older git, worktree add),
	// "'X' is already used by worktree at ..." (worktree add),
	// "cannot delete branch 'X' used by worktree at ..." (branch -D).
	case strings.Contains(detail, "is already checked out at"),
		strings.Contains(detail, "used by worktree at"):
		return fmt.Errorf("%w: %s", ErrBranchInUse, detail)
	case strings.Contains(detail, "already exists"):
		return fmt.Errorf("%w: %s", ErrWorktreeDestinationExists, detail)
	}
	return fmt.Errorf("git: %w: %s", err, detail)
}

// runLifecycleHook executes a hook script in the worktree directory with
// the lifecycle environment, honoring ctx cancellation and the shared
// subprocess limiter. Stdout is discarded; stderr is captured into the
// HookError a non-zero exit produces.
func runLifecycleHook(
	ctx context.Context,
	script, projectRoot, worktreePath, branch, worktreeName string,
) error {
	name := strings.TrimSpace(worktreeName)
	if name == "" {
		name = branch
	}
	cmd := procutil.CommandContext(ctx, script)
	cmd.Dir = worktreePath
	cmd.Env = append(
		os.Environ(),
		hookEnvWorktreeName+"="+name,
		hookEnvWorktreePath+"="+worktreePath,
		hookEnvProjectRoot+"="+projectRoot,
		hookEnvBranch+"="+branch,
	)
	cmd.Stdout = io.Discard
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := procutil.Run(ctx, cmd, "worktree lifecycle hook"); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return &HookError{
				Script:   script,
				ExitCode: exitErr.ExitCode(),
				Stderr:   strings.TrimSpace(stderr.String()),
			}
		}
		return fmt.Errorf("run lifecycle hook %s: %w", script, err)
	}
	return nil
}

// RollbackCreatedWorktree best-effort unwinds a worktree created earlier
// in the same operation, for callers whose post-create step (registry
// insert) failed after CreateWorktreeOnDisk succeeded.
func RollbackCreatedWorktree(
	ctx context.Context, root, path, branch string, deleteBranch bool,
) {
	rollbackCreatedWorktree(ctx, root, path, branch, deleteBranch)
}

// rollbackCreatedWorktree best-effort unwinds a worktree this call just
// created after its setup hook failed. The branch is deleted only when
// this call created it; the original hook error is what the caller
// surfaces.
func rollbackCreatedWorktree(
	ctx context.Context, root, path, branch string, deleteBranch bool,
) {
	_, _ = runLifecycleGit(
		ctx, root, "worktree", "remove", "--force", path,
	)
	if deleteBranch {
		_, _ = runLifecycleGit(ctx, root, "branch", "-D", "--", branch)
	}
}
