package workspace

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	gitcmd "go.kenn.io/kit/git/cmd"
	gitremote "go.kenn.io/kit/git/remote"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/gitclone"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/procutil"
)

// Manager owns middleman's persisted workspace lifecycle.
//
// Its purpose is to turn tracked review items into durable local execution
// contexts backed by a database row, a Git worktree, and a tmux session. It is
// intentionally not a generic host worktree browser or arbitrary Git
// automation layer.
type Manager struct {
	db                     *db.DB
	worktreeDir            string
	clones                 *gitclone.Manager
	locks                  *FileLockManager
	tmuxCmd                []string
	ptyOwner               PtyOwnerClient
	preferPtyOwner         bool
	retryMu                sync.Mutex
	retryQueued            map[string]bool
	runtimeTmuxMu          sync.Mutex
	issueBranchSlugEnabled bool
	summaryCacheMu         sync.RWMutex
	summaryCache           []WorkspaceSummary
	deletedSummaryIDs      map[string]bool
	worktreeBaseResolver   WorktreeBasePathResolver
}

// WorktreeBasePathResolver resolves a tracked remote repository to a
// user-configured local repository that should own new git worktrees.
type WorktreeBasePathResolver func(
	ctx context.Context, platform, platformHost, owner, name string,
) (path string, ok bool, err error)

// CreateIssueOptions controls how issue-backed workspaces choose their branch.
//
// The default path creates middleman's conventional issue branch. When a local
// branch with that name already exists, callers can either ask the manager to
// reuse it or supply a different GitHeadRef.
type CreateIssueOptions struct {
	Provider            string
	GitHeadRef          string
	ReuseExistingBranch bool
}

// IssueWorkspaceBranchConflictError reports that the requested issue-workspace
// branch already exists locally, so the caller must either reuse it or choose
// a different name before a new middleman workspace can be created.
type IssueWorkspaceBranchConflictError struct {
	Branch          string
	SuggestedBranch string
}

func (e *IssueWorkspaceBranchConflictError) Error() string {
	return fmt.Sprintf(
		"issue workspace branch %q already exists; suggested alternative %q",
		e.Branch,
		e.SuggestedBranch,
	)
}

const (
	workspaceSetupStageSetup       = "setup"
	workspaceSetupStageClone       = "clone"
	workspaceSetupStageWorktree    = "worktree"
	workspaceSetupStageTmuxSession = "tmux_session"
	workspaceBranchUnknown         = "__middleman_unknown__"
	tmuxCaptureScrollbackLines     = 160
)

var workspacePersistTimeout = 5 * time.Second
var workspaceCleanupTimeout = 5 * time.Second

var (
	ErrWorkspaceNotFound     = errors.New("workspace not found")
	ErrWorkspaceNotSynced    = errors.New("workspace merge request not synced")
	ErrWorkspaceDuplicate    = errors.New("workspace already exists")
	ErrWorkspaceInvalidState = errors.New("workspace invalid state")
)

type TerminalPaneSnapshot struct {
	Title  string
	Output string
}

// NewManager creates a Manager that stores worktrees under
// worktreeDir.
func NewManager(
	database *db.DB, worktreeDir string,
) *Manager {
	return &Manager{
		db:                     database,
		worktreeDir:            worktreeDir,
		locks:                  NewFileLockManager(),
		retryQueued:            make(map[string]bool),
		issueBranchSlugEnabled: true,
	}
}

// SetIssueBranchSlugEnabled controls whether issue-workspace branch
// names include a slug derived from the issue title. When false, the
// manager keeps the legacy bare middleman/issue-<n> form. Default is
// true, matching the configured default issue_workspace_branch_style.
func (m *Manager) SetIssueBranchSlugEnabled(enabled bool) {
	m.issueBranchSlugEnabled = enabled
}

// SetWorktreeBasePathResolver configures the optional local-repository
// resolver used when a tracked remote repo should create worktrees from a
// user-openable checkout instead of middleman's managed bare clone.
func (m *Manager) SetWorktreeBasePathResolver(resolver WorktreeBasePathResolver) {
	m.worktreeBaseResolver = resolver
}

// defaultIssueBranch returns the middleman issue-workspace branch
// name to use when the caller did not pass an explicit GitHeadRef.
// When the slug style is enabled and the issue has a usable title,
// the bare middleman/issue-<n> is suffixed with a sanitized slug.
func (m *Manager) defaultIssueBranch(issueNumber int, title string) string {
	if m.issueBranchSlugEnabled {
		return issueWorkspaceBranchWithTitle(issueNumber, title)
	}
	return issueWorkspaceBranch(issueNumber)
}

// SetClones sets the git clone manager used for bare clone
// operations. Called after the clone manager is initialized.
func (m *Manager) SetClones(clones *gitclone.Manager) {
	m.clones = clones
}

// withRepoLock acquires a repository-scoped lock, executes the function, and
// releases the lock. The lock is released even if the function panics.
func (m *Manager) withRepoLock(ctx context.Context, lockRoot string, fn func() error) error {
	if err := os.MkdirAll(lockRoot, 0o755); err != nil {
		return fmt.Errorf("prepare worktree lock for %q: %w", lockRoot, err)
	}
	lock, err := m.locks.Acquire(ctx, lockRoot)
	if err != nil {
		return fmt.Errorf("acquire worktree lock for %q: %w", lockRoot, err)
	}
	defer func() {
		if err := lock.Unlock(); err != nil {
			slog.Warn("failed to release worktree lock",
				"path", lockRoot, "err", err)
		}
	}()
	return fn()
}

func (m *Manager) withRepoLockForGitDir(
	ctx context.Context, gitDir string, fn func() error,
) error {
	lockRoot, err := m.worktreeLockRoot(ctx, gitDir)
	if err != nil {
		return err
	}
	return m.withRepoLock(ctx, lockRoot, fn)
}

func (m *Manager) worktreeLockRoot(ctx context.Context, gitDir string) (string, error) {
	bare, err := gitIsBareRepository(ctx, gitDir)
	if err != nil {
		return "", err
	}
	if bare {
		return gitDir, nil
	}
	commonDir, err := worktreeCommonGitDir(ctx, gitDir)
	if err != nil {
		return "", err
	}
	return m.localWorktreeBaseLockRoot(commonDir), nil
}

// SetTmuxCommand sets the command + argv prefix for every tmux
// invocation the manager issues. When nil/empty, the manager uses
// ["tmux"] — preserving today's behavior.
func (m *Manager) SetTmuxCommand(cmd []string) {
	m.tmuxCmd = slices.Clone(cmd)
}

// tmuxExec builds an *exec.Cmd for a tmux invocation: the
// configured prefix + extra args. Defaults to ["tmux"] when
// unconfigured. Returning the *exec.Cmd directly (rather than a
// []string that callers index) keeps the first-element access
// inside this function where the branch structure makes it
// statically safe — NilAway cannot prove safety through an indexed
// slice return.
func (m *Manager) tmuxExec(
	ctx context.Context, extra ...string,
) *exec.Cmd {
	if len(m.tmuxCmd) == 0 {
		return procutil.CommandContext(ctx, "tmux", extra...)
	}
	args := make([]string, 0, len(m.tmuxCmd)-1+len(extra))
	args = append(args, m.tmuxCmd[1:]...)
	args = append(args, extra...)
	return procutil.CommandContext(ctx, m.tmuxCmd[0], args...)
}

// Create persists a PR-backed middleman workspace.
//
// The point of this row is to give a tracked pull request a stable local
// workspace entry that the UI can reopen later, rather than rediscovering local
// Git state on every load. The caller runs Setup in the background to
// materialize the worktree and tmux session.
func (m *Manager) Create(
	ctx context.Context,
	provider, platformHost, owner, name string,
	mrNumber int,
) (*Workspace, error) {
	repo, err := m.workspaceRepo(ctx, provider, platformHost, owner, name)
	if err != nil {
		return nil, fmt.Errorf("look up repo: %w", err)
	}
	if repo == nil {
		return nil, fmt.Errorf("%w: repository not tracked", ErrWorkspaceNotFound)
	}

	mr, err := m.db.GetMergeRequestByRepoIDAndNumber(
		ctx, repo.ID, mrNumber,
	)
	if err != nil {
		return nil, fmt.Errorf("look up merge request: %w", err)
	}
	if mr == nil {
		return nil, fmt.Errorf(
			"%w: merge request %d", ErrWorkspaceNotSynced, mrNumber,
		)
	}

	id, err := newWorkspaceID()
	if err != nil {
		return nil, err
	}

	ws := &Workspace{
		ID:              id,
		Platform:        repo.Platform,
		PlatformHost:    platformHost,
		RepoOwner:       owner,
		RepoName:        name,
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      mrNumber,
		GitHeadRef:      mr.HeadBranch,
		MRHeadRepo:      workspaceHeadRepo(platformHost, owner, name, mr.HeadRepoCloneURL),
		WorkspaceBranch: workspaceBranchUnknown,
		WorktreePath: filepath.Join(
			m.worktreeDir, repo.Platform, platformHost, owner, name,
			fmt.Sprintf("pr-%d", mrNumber),
		),
		TmuxSession:     "middleman-" + id,
		TerminalBackend: m.PreferredTerminalBackend(),
		Status:          "creating",
	}

	if err := m.db.InsertWorkspace(ctx, ws); err != nil {
		if isUniqueConstraintError(err) {
			return nil, fmt.Errorf("%w: %v", ErrWorkspaceDuplicate, err)
		}
		return nil, fmt.Errorf("insert workspace: %w", err)
	}
	return ws, nil
}

// CreateIssue persists an issue-backed middleman workspace.
//
// Unlike PR workspaces, issue workspaces are not tied to a remote head branch.
// They exist to give an issue its own durable local execution context that
// starts from the repo's current origin/HEAD. The caller runs Setup in the
// background to materialize the worktree and tmux session.
func (m *Manager) CreateIssue(
	ctx context.Context,
	platformHost, owner, name string,
	issueNumber int,
	opts CreateIssueOptions,
) (*Workspace, error) {
	repo, err := m.workspaceRepo(ctx, opts.Provider, platformHost, owner, name)
	if err != nil {
		return nil, fmt.Errorf("look up repo: %w", err)
	}
	if repo == nil {
		return nil, fmt.Errorf("repository not tracked")
	}

	issue, err := m.db.GetIssueByRepoIDAndNumber(
		ctx, repo.ID, issueNumber,
	)
	if err != nil {
		return nil, fmt.Errorf("look up issue: %w", err)
	}
	if issue == nil {
		return nil, fmt.Errorf(
			"issue %d not synced yet", issueNumber,
		)
	}

	gitHeadRef := opts.GitHeadRef
	if gitHeadRef == "" {
		gitHeadRef = m.defaultIssueBranch(issueNumber, issue.Title)
	}
	if err := validateLocalBranchName(ctx, "", gitHeadRef); err != nil {
		return nil, err
	}

	workspaceBranch := gitHeadRef
	branchDir, ok, localBase, err := m.issueBranchInspectionDir(
		ctx, repo.Platform, platformHost, owner, name,
		workspaceCloneRemoteURL(repo, platformHost, owner, name),
	)
	if err != nil {
		return nil, err
	}
	if ok {
		branch, err := issueWorkspaceBranchForExistingLocalBranch(
			ctx, branchDir, gitHeadRef, opts.ReuseExistingBranch,
			localBase,
		)
		if err != nil {
			return nil, err
		}
		workspaceBranch = branch
	}

	id, err := newWorkspaceID()
	if err != nil {
		return nil, err
	}

	ws := &Workspace{
		ID:              id,
		Platform:        repo.Platform,
		PlatformHost:    platformHost,
		RepoOwner:       owner,
		RepoName:        name,
		ItemType:        db.WorkspaceItemTypeIssue,
		ItemNumber:      issueNumber,
		GitHeadRef:      gitHeadRef,
		WorkspaceBranch: workspaceBranch,
		WorktreePath: filepath.Join(
			m.worktreeDir, repo.Platform, platformHost, owner, name,
			fmt.Sprintf("issue-%d", issueNumber),
		),
		TmuxSession:     "middleman-" + id,
		TerminalBackend: m.PreferredTerminalBackend(),
		Status:          "creating",
	}

	if err := m.db.InsertWorkspace(ctx, ws); err != nil {
		return nil, fmt.Errorf("insert workspace: %w", err)
	}
	return ws, nil
}

func newWorkspaceID() (string, error) {
	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		return "", fmt.Errorf("generate workspace id: %w", err)
	}
	return hex.EncodeToString(idBytes), nil
}

func workspaceCloneNamespace(platform string) string {
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform == "" || platform == "github" {
		return ""
	}
	return platform
}

func (m *Manager) issueBranchInspectionDir(
	ctx context.Context, platform, platformHost, owner, name, remoteURL string,
) (dir string, ok bool, localBase bool, err error) {
	if baseDir, ok, err := m.localWorktreeBaseDir(ctx, platform, platformHost, owner, name); err != nil || ok {
		return baseDir, ok, ok, err
	}
	if m.clones == nil {
		return "", false, false, nil
	}

	if err := m.clones.EnsureCloneInNamespace(
		ctx, workspaceCloneNamespace(platform), platformHost, owner, name, remoteURL,
	); err != nil {
		return "", false, false, fmt.Errorf("ensure clone: %w", err)
	}

	cloneDir, err := m.clones.ClonePathInNamespace(
		workspaceCloneNamespace(platform), platformHost, owner, name,
	)
	if err != nil {
		return "", false, false, err
	}
	return cloneDir, true, false, nil
}

func workspaceCloneRemoteURL(
	repo *db.Repo, platformHost, owner, name string,
) string {
	if repo != nil {
		if cloneURL := strings.TrimSpace(repo.CloneURL); cloneURL != "" {
			return cloneURL
		}
	}
	return fmt.Sprintf("https://%s/%s/%s.git", platformHost, owner, name)
}

func issueWorkspaceBranchForExistingLocalBranch(
	ctx context.Context, dir, branch string, reuse, localBase bool,
) (string, error) {
	exists, err := localBranchExists(ctx, dir, branch)
	if err != nil {
		return "", fmt.Errorf("inspect local branch: %w", err)
	}
	if !exists {
		return branch, nil
	}
	if reuse && !localBase {
		return "", nil
	}
	if reuse {
		checkedOut, err := localBranchCheckedOut(ctx, dir, branch)
		if err != nil {
			return "", fmt.Errorf("inspect checked out branch: %w", err)
		}
		if !checkedOut {
			return "", nil
		}
	}
	return "", issueWorkspaceBranchConflict(ctx, dir, branch)
}

func issueWorkspaceBranchConflict(
	ctx context.Context, dir, branch string,
) error {
	suggested, err := nextAvailableBranchName(ctx, dir, branch)
	if err != nil {
		return fmt.Errorf("suggest branch name: %w", err)
	}
	return &IssueWorkspaceBranchConflictError{
		Branch:          branch,
		SuggestedBranch: suggested,
	}
}

func workspaceHeadRepo(platformHost, owner, name, cloneURL string) *string {
	if cloneURL == "" {
		return nil
	}
	// MRHeadRepo means "this PR head must be resolved through fork-safe refs"
	// in setup. GitHub also fills head.repo.clone_url for same-repo PRs, so
	// compare clone identities before treating a non-empty URL as fork metadata.
	headRepo := normalizeCloneRepoIdentity(cloneURL)
	baseRepo := strings.ToLower(strings.Join([]string{
		normalizePlatformHostIdentity(platformHost),
		strings.TrimSpace(owner),
		strings.TrimSpace(name),
	}, "/"))
	if headRepo != "" && headRepo == baseRepo {
		return nil
	}
	s := cloneURL
	return &s
}

// Setup clones/fetches the repo, creates the git worktree, starts
// a tmux session, and marks the workspace "ready". On failure it
// rolls back the worktree and sets status to "error".
func (m *Manager) Setup(
	ctx context.Context, ws *Workspace,
) error {
	m.recordSetupEvent(
		ctx,
		ws.ID, workspaceSetupStageSetup, "started",
		"starting workspace setup",
	)
	gitDir, refreshBeforeAdd, err := m.workspaceSetupGitDir(ctx, ws)
	if err != nil {
		return m.failSetup(
			ctx,
			ws.ID, workspaceSetupStageClone, err,
		)
	}

	branch, err := m.addWorktree(ctx, gitDir, refreshBeforeAdd, ws)
	if err != nil {
		return m.failSetup(
			ctx,
			ws.ID, workspaceSetupStageWorktree, err,
		)
	}
	persistedBranch := branch
	if ws.ItemType == db.WorkspaceItemTypeIssue && ws.WorkspaceBranch == "" {
		persistedBranch = ""
	}
	ws.WorkspaceBranch = persistedBranch
	if err := m.updateWorkspaceBranch(
		ctx, ws.ID, persistedBranch,
	); err != nil {
		m.rollbackWorktree(ctx, gitDir, ws, branch)
		return m.failSetup(
			ctx,
			ws.ID, workspaceSetupStageWorktree, err,
		)
	}

	err = m.newTerminalSession(ctx, ws)
	if err != nil {
		m.rollbackWorktree(ctx, gitDir, ws, branch)
		return m.failSetup(
			ctx,
			ws.ID, workspaceSetupStageTmuxSession, err,
		)
	}
	m.recordSetupEvent(
		ctx,
		ws.ID, workspaceSetupStageTmuxSession, "success",
		"terminal session started",
	)

	// Record the final setup event before flipping status: "ready" is
	// the externally visible completion signal, so observers that poll
	// status must never see "ready" while the event log is still
	// missing its last row. failSetup keeps the same event-then-status
	// order on the error path.
	m.recordSetupEvent(
		ctx,
		ws.ID, workspaceSetupStageSetup, "ready",
		"workspace ready",
	)
	if err := m.updateWorkspaceStatus(
		ctx, ws.ID, "ready", nil,
	); err != nil {
		return m.failSetup(
			ctx,
			ws.ID, workspaceSetupStageSetup,
			fmt.Errorf("update status to ready: %w", err),
		)
	}
	return nil
}

func (m *Manager) workspaceSetupGitDir(
	ctx context.Context, ws *Workspace,
) (string, bool, error) {
	if ws.MRHeadRepo == nil {
		if baseDir, ok, err := m.localWorktreeBaseDir(
			ctx, ws.Platform, ws.PlatformHost, ws.RepoOwner, ws.RepoName,
		); err != nil || ok {
			return baseDir, ok, err
		}
	}

	if m.clones == nil {
		return "", false, fmt.Errorf("clone manager not set")
	}

	remoteURL, err := m.workspaceSetupRemoteURL(
		ctx, ws.Platform, ws.PlatformHost, ws.RepoOwner, ws.RepoName,
	)
	if err != nil {
		return "", false, err
	}
	if err := m.clones.EnsureCloneInNamespace(
		ctx, workspaceCloneNamespace(ws.Platform), ws.PlatformHost, ws.RepoOwner,
		ws.RepoName, remoteURL,
	); err != nil {
		return "", false, err
	}

	cloneDir, err := m.clones.ClonePathInNamespace(
		workspaceCloneNamespace(ws.Platform),
		ws.PlatformHost, ws.RepoOwner, ws.RepoName,
	)
	if err != nil {
		return "", false, err
	}
	return cloneDir, false, nil
}

func (m *Manager) workspaceSetupRemoteURL(
	ctx context.Context, platform, platformHost, owner, name string,
) (string, error) {
	repo, err := m.db.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     platform,
		PlatformHost: platformHost,
		Owner:        owner,
		Name:         name,
	})
	if err != nil {
		return "", fmt.Errorf("look up repo clone URL: %w", err)
	}
	return workspaceCloneRemoteURL(repo, platformHost, owner, name), nil
}

func (m *Manager) localWorktreeBaseDir(
	ctx context.Context, platform, platformHost, owner, name string,
) (string, bool, error) {
	if m.worktreeBaseResolver == nil {
		return "", false, nil
	}
	raw, ok, err := m.worktreeBaseResolver(ctx, platform, platformHost, owner, name)
	if err != nil {
		return "", false, err
	}
	raw = strings.TrimSpace(raw)
	if !ok || raw == "" {
		return "", false, nil
	}
	abs, err := ValidateWorktreeBasePath(ctx, raw, platformHost, owner, name)
	if err != nil {
		return "", false, err
	}
	return abs, true, nil
}

func (m *Manager) localWorktreeBaseLockRoot(path string) string {
	sum := sha256.Sum256([]byte(path))
	return filepath.Join(
		m.worktreeDir, ".middleman-worktree-base-locks",
		hex.EncodeToString(sum[:]),
	)
}

// ValidateWorktreeBasePath verifies that path is an existing local Git
// worktree whose origin remote matches the tracked repository identity.
func ValidateWorktreeBasePath(
	ctx context.Context, path, platformHost, owner, name string,
) (string, error) {
	abs, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	evaluated, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("path does not exist: %s", abs)
		}
		return "", fmt.Errorf("resolve symbolic links: %w", err)
	}
	abs = evaluated
	stat, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("path does not exist: %s", abs)
		}
		return "", fmt.Errorf("stat path: %w", err)
	}
	if !stat.IsDir() {
		return "", fmt.Errorf("path is not a directory: %s", abs)
	}
	insideWorkTree, err := gitOutput(ctx, abs, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return "", fmt.Errorf("path is not a git worktree: %w", err)
	}
	if strings.TrimSpace(insideWorkTree) != "true" {
		return "", fmt.Errorf("path is not a git worktree: %s", abs)
	}
	if err := validateNoExecutableLocalGitConfig(ctx, abs); err != nil {
		return "", err
	}
	if err := validateNoExecutableGitHooks(ctx, abs); err != nil {
		return "", err
	}
	if err := validateOriginFetchRefspec(ctx, abs); err != nil {
		return "", err
	}
	if err := validateOriginRemoteURLs(
		ctx, abs, platformHost, owner, name,
	); err != nil {
		return "", err
	}
	return abs, nil
}

func (m *Manager) workspaceRepo(
	ctx context.Context,
	provider, platformHost, owner, name string,
) (*db.Repo, error) {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return m.db.GetRepoByHostOwnerName(ctx, platformHost, owner, name)
	}
	kind, err := platform.NormalizeKind(provider)
	if err != nil {
		return nil, err
	}
	return m.db.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     string(kind),
		PlatformHost: platformHost,
		RepoPath:     owner + "/" + name,
	})
}

func validateNoExecutableLocalGitConfig(ctx context.Context, dir string) error {
	keys, err := localGitConfigKeys(ctx, dir)
	if err != nil {
		return fmt.Errorf("inspect executable local git config: %w", err)
	}
	for _, key := range keys {
		if localGitConfigKeyMayExecute(key) {
			return fmt.Errorf(
				"local git config %q may execute or rewrite git commands",
				key,
			)
		}
	}
	return nil
}

func validateNoExecutableGitHooks(ctx context.Context, dir string) error {
	commonDir, err := worktreeCommonGitDir(ctx, dir)
	if err != nil {
		return fmt.Errorf("inspect git hooks dir: %w", err)
	}
	hooksDir := filepath.Join(commonDir, "hooks")
	entries, err := os.ReadDir(hooksDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read git hooks dir: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), ".sample") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect git hook %q: %w", entry.Name(), err)
		}
		if info.Mode()&0o111 != 0 {
			return fmt.Errorf("git hook %q must not be executable", entry.Name())
		}
	}
	return nil
}

func localGitConfigKeyMayExecute(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	return key == "core.fsmonitor" ||
		key == "core.alternaterefscommand" ||
		key == "core.askpass" ||
		key == "core.gitproxy" ||
		key == "core.hookspath" ||
		key == "core.sshcommand" ||
		key == "credential.helper" ||
		key == "diff.external" ||
		key == "fetch.recursesubmodules" ||
		strings.HasPrefix(key, "http.") ||
		key == "submodule.recurse" ||
		(strings.HasPrefix(key, "credential.") &&
			strings.HasSuffix(key, ".helper")) ||
		(strings.HasPrefix(key, "diff.") &&
			(strings.HasSuffix(key, ".command") ||
				strings.HasSuffix(key, ".textconv"))) ||
		(strings.HasPrefix(key, "filter.") &&
			(strings.HasSuffix(key, ".process") ||
				strings.HasSuffix(key, ".clean") ||
				strings.HasSuffix(key, ".smudge"))) ||
		(strings.HasPrefix(key, "remote.") &&
			strings.HasSuffix(key, ".proxy")) ||
		(strings.HasPrefix(key, "url.") &&
			strings.HasSuffix(key, ".insteadof")) ||
		key == "include.path" ||
		(strings.HasPrefix(key, "includeif.") &&
			strings.HasSuffix(key, ".path")) ||
		(strings.HasPrefix(key, "protocol.") &&
			strings.HasSuffix(key, ".allow"))
}

func validateOriginRemoteURLs(
	ctx context.Context, dir, platformHost, owner, name string,
) error {
	remoteURLs, err := gitConfigValues(ctx, dir, "remote.origin.url")
	if err != nil {
		return fmt.Errorf("read origin remote: %w", err)
	}
	if len(remoteURLs) == 0 {
		return fmt.Errorf("read origin remote: no origin URL configured")
	}
	for _, remoteURL := range remoteURLs {
		if err := validateOriginRemoteURL(
			remoteURL, platformHost, owner, name,
		); err != nil {
			return err
		}
	}
	return nil
}

func validateOriginRemoteURL(
	remoteURL, platformHost, owner, name string,
) error {
	if gitremote.RemoteHost(remoteURL) == "" ||
		gitremote.RemoteRepoPath(remoteURL) == "" {
		return fmt.Errorf(
			"origin remote must include a forge host and repository path",
		)
	}
	if !originRemoteSchemeAllowed(remoteURL) {
		// Never include the raw remote URL: it can embed credentials
		// (http://oauth2:token@host/...) and this error is persisted as
		// workspace error state and returned through the API.
		return fmt.Errorf(
			"origin remote scheme %q is not allowed (host %q)",
			remoteURLScheme(remoteURL), gitremote.RemoteHost(remoteURL),
		)
	}
	if err := gitremote.ValidateRemoteIdentity(gitremote.Identity{
		Host:  platformHost,
		Owner: owner,
		Name:  name,
	}, remoteURL); err != nil {
		return fmt.Errorf("origin remote does not match repository: %w", err)
	}
	return nil
}

// remoteURLScheme returns only the scheme prefix of a remote URL. The rest
// of the URL stays out of error messages because it can embed credentials.
func remoteURLScheme(remoteURL string) string {
	scheme, _, ok := strings.Cut(remoteURL, "://")
	if !ok {
		return ""
	}
	return strings.ToLower(scheme)
}

func originRemoteSchemeAllowed(remoteURL string) bool {
	if !strings.Contains(remoteURL, "://") {
		return true
	}
	parsed, err := url.Parse(remoteURL)
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "", "https", "ssh":
		return true
	case "http":
		return hostIsLoopback(parsed.Host)
	default:
		return false
	}
}

func hostIsLoopback(hostport string) bool {
	host := hostport
	if parsedHost, _, err := net.SplitHostPort(hostport); err == nil {
		host = parsedHost
	}
	host = strings.Trim(strings.ToLower(host), "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validateOriginFetchRefspec(ctx context.Context, dir string) error {
	values, err := gitConfigValues(ctx, dir, "remote.origin.fetch")
	if err != nil {
		return fmt.Errorf("read origin fetch refspec: %w", err)
	}
	for _, value := range values {
		if !originFetchRefspecUpdatesOrigin(value) {
			return fmt.Errorf(
				"origin fetch refspec %q may update unsafe refs",
				value,
			)
		}
	}
	return nil
}

func originFetchRefspecUpdatesOrigin(value string) bool {
	refspec := strings.TrimSpace(value)
	if refspec == "" || strings.HasPrefix(refspec, "^") {
		return false
	}
	refspec = strings.TrimPrefix(refspec, "+")
	src, dst, ok := strings.Cut(refspec, ":")
	if !ok {
		return false
	}
	return strings.HasPrefix(src, "refs/heads/") &&
		strings.HasPrefix(dst, "refs/remotes/origin/")
}

func gitConfigValues(ctx context.Context, dir, key string) ([]string, error) {
	out, err := gitCombinedOutput(ctx, dir, "config", "--get-all", key)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	var values []string
	for line := range strings.SplitSeq(out, "\n") {
		value := strings.TrimSpace(line)
		if value != "" {
			values = append(values, value)
		}
	}
	return values, nil
}

func localGitConfigKeys(ctx context.Context, dir string) ([]string, error) {
	keys, err := localGitConfigKeysForScope(ctx, dir, "--local")
	if err != nil {
		return nil, err
	}
	worktreeKeys, err := localGitConfigKeysForScope(ctx, dir, "--worktree")
	if err != nil {
		return nil, err
	}
	keys = append(keys, worktreeKeys...)
	return keys, nil
}

func localGitConfigKeysForScope(
	ctx context.Context, dir, scope string,
) ([]string, error) {
	out, err := gitCombinedOutput(
		ctx, dir, "config", scope, "--name-only", "--list",
	)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		if scope == "--worktree" &&
			(strings.Contains(out, "extensions.worktreeConfig") ||
				strings.Contains(out, "extension worktreeConfig")) {
			return nil, nil
		}
		return nil, err
	}
	var keys []string
	for line := range strings.SplitSeq(out, "\n") {
		key := strings.TrimSpace(line)
		if key != "" {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

// addWorktree creates the workspace's worktree and branch under the
// per-repo lock. The lock prevents concurrent worktree mutations on
// the same git repository from clobbering each other; see FileLockManager.
func (m *Manager) addWorktree(
	ctx context.Context, cloneDir string, refreshBeforeAdd bool, ws *Workspace,
) (string, error) {
	var branch string
	err := m.withRepoLockForGitDir(ctx, cloneDir, func() error {
		if refreshBeforeAdd {
			if err := m.fetchWorkspaceBase(
				ctx, cloneDir, ws.PlatformHost,
				ws.ItemType == db.WorkspaceItemTypeIssue,
			); err != nil {
				return err
			}
		}
		var addErr error
		branch, addErr = m.addWorktreeLocked(ctx, cloneDir, refreshBeforeAdd, ws)
		return addErr
	})
	return branch, err
}

// addWorktreeLocked runs the worktree-add decision tree. Callers must
// hold the per-repo lock for cloneDir before invoking this function.
func (m *Manager) addWorktreeLocked(
	ctx context.Context, cloneDir string, localBase bool, ws *Workspace,
) (string, error) {
	if ws.ItemType == db.WorkspaceItemTypeIssue {
		return m.addIssueWorktree(ctx, cloneDir, ws)
	}
	branch, err := m.addPreferredWorktree(ctx, cloneDir, localBase, ws)
	if err == nil {
		return branch, nil
	}
	fallbackBranch := syntheticPRWorktreeBranch(ws.ItemNumber)
	startRef := workspaceStartRef(ws)
	fallbackErr := runGitWorktreeAdd(
		ctx, cloneDir, ws.WorktreePath,
		"-b", fallbackBranch, startRef,
	)
	if fallbackErr == nil {
		return fallbackBranch, nil
	}
	return "", fmt.Errorf(
		"preferred branch %q failed: %w; fallback branch %q failed: %w",
		ws.GitHeadRef, err, fallbackBranch, fallbackErr,
	)
}

func (m *Manager) addIssueWorktree(
	ctx context.Context, cloneDir string, ws *Workspace,
) (string, error) {
	workspaceBranch := ws.WorkspaceBranch
	if workspaceBranch == workspaceBranchUnknown {
		workspaceBranch = ws.GitHeadRef
	}
	if workspaceBranch == "" {
		if err := runGitWorktreeAdd(
			ctx, cloneDir, ws.WorktreePath, ws.GitHeadRef,
		); err != nil {
			return "", err
		}
		return "", nil
	}
	startRef := workspaceStartRef(ws)
	if err := runGitWorktreeAdd(
		ctx, cloneDir, ws.WorktreePath,
		"-b", workspaceBranch, startRef,
	); err != nil {
		return "", err
	}
	return workspaceBranch, nil
}

func (m *Manager) addPreferredWorktree(
	ctx context.Context, cloneDir string, localBase bool, ws *Workspace,
) (string, error) {
	if err := validateLocalBranchName(
		ctx, cloneDir, ws.GitHeadRef,
	); err != nil {
		return "", err
	}

	if ws.MRHeadRepo != nil {
		err := runGitWorktreeAdd(
			ctx, cloneDir, ws.WorktreePath,
			"-b", ws.GitHeadRef, workspaceStartRef(ws),
		)
		if err != nil {
			return "", err
		}
		return ws.GitHeadRef, nil
	}

	startRef := workspaceStartRef(ws)
	startSHA, ok, err := gitRefSHA(ctx, cloneDir, startRef)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("start ref %q not found", startRef)
	}

	branchRef := "refs/heads/" + ws.GitHeadRef
	branchSHA, exists, err := gitRefSHA(ctx, cloneDir, branchRef)
	if err != nil {
		return "", err
	}
	if !exists {
		if err := runGitWorktreeAdd(
			ctx, cloneDir, ws.WorktreePath,
			"-b", ws.GitHeadRef, startRef,
		); err != nil {
			return "", err
		}
		if err := setBranchUpstream(
			ctx, ws.WorktreePath, ws.GitHeadRef,
			"origin", "refs/heads/"+ws.GitHeadRef,
		); err != nil {
			cleanupCtx, cancel := cleanupContext(ctx)
			defer cancel()
			_ = runGitWithoutHooks(
				cleanupCtx, cloneDir,
				"worktree", "remove", "--force", ws.WorktreePath,
			)
			_ = runGitWithoutHooks(
				cleanupCtx, cloneDir,
				"branch", "-D", "--", ws.GitHeadRef,
			)
			return "", fmt.Errorf("configure branch upstream: %w", err)
		}
		return ws.GitHeadRef, nil
	}
	if branchSHA != startSHA {
		return "", fmt.Errorf(
			"preferred branch %q points at %s, not %s",
			ws.GitHeadRef, branchSHA, startSHA,
		)
	}
	if localBase {
		checkedOut, err := localBranchCheckedOut(ctx, cloneDir, ws.GitHeadRef)
		if err != nil {
			return "", fmt.Errorf("inspect checked out branch: %w", err)
		}
		if checkedOut {
			return "", fmt.Errorf(
				"preferred branch %q is already checked out",
				ws.GitHeadRef,
			)
		}
	}

	if err := runGitWorktreeAdd(
		ctx, cloneDir, ws.WorktreePath, ws.GitHeadRef,
	); err != nil {
		return "", err
	}

	if !localBase {
		if err := setBranchUpstream(
			ctx, ws.WorktreePath, ws.GitHeadRef,
			"origin", "refs/heads/"+ws.GitHeadRef,
		); err != nil {
			cleanupCtx, cancel := cleanupContext(ctx)
			defer cancel()
			_ = runGitWithoutHooks(
				cleanupCtx, cloneDir,
				"worktree", "remove", "--force", ws.WorktreePath,
			)
			return "", fmt.Errorf("configure branch upstream: %w", err)
		}
	}

	// The branch already existed before this workspace was materialized. Return
	// an empty managed branch so rollback, retry, and delete cleanup remove only
	// the worktree and never delete the user's pre-existing local branch.
	return "", nil
}

func workspaceStartRef(ws *Workspace) string {
	if ws.ItemType == db.WorkspaceItemTypeIssue {
		return "origin/HEAD"
	}
	if ws.MRHeadRepo != nil {
		return fmt.Sprintf("refs/pull/%d/head", ws.ItemNumber)
	}
	return "origin/" + ws.GitHeadRef
}

func syntheticPRWorktreeBranch(mrNumber int) string {
	return fmt.Sprintf("middleman/pr-%d", mrNumber)
}

func setBranchUpstream(
	ctx context.Context,
	worktreePath, branch, remote, mergeRef string,
) error {
	if err := runGitWithoutHooks(
		ctx, worktreePath,
		"config", "branch."+branch+".remote", remote,
	); err != nil {
		return err
	}
	return runGitWithoutHooks(
		ctx, worktreePath,
		"config", "branch."+branch+".merge", mergeRef,
	)
}

func validateLocalBranchName(
	ctx context.Context, dir, branch string,
) error {
	cmd := procutil.CommandContext(
		ctx, "git", "check-ref-format", "--branch", branch,
	)
	if dir == "" {
		// `git check-ref-format --branch` consults cwd when it appears to be
		// inside a worktree. Run repo-independent validation somewhere neutral
		// so a broken launch cwd cannot make a valid branch look invalid.
		dir = os.TempDir()
	}
	cmd.Dir = dir
	out, err := procutil.CombinedOutput(
		ctx, cmd, "git subprocess capacity",
	)
	if err == nil {
		return nil
	}

	msg := strings.TrimSpace(string(out))
	if msg == "" {
		msg = err.Error()
	}
	return fmt.Errorf("invalid branch name %q: %s", branch, msg)
}

// Delete tears down a workspace: kills tmux, removes the git
// worktree and branch, and deletes the DB record.
// If force is false and the worktree has uncommitted changes,
// it returns the dirty file list without deleting.
//
// beforeDestructive is invoked after the dirty preflight passes
// (or is skipped because force=true) and before any destructive
// cleanup. It exists so callers can stop background processes
// that might still write to the worktree — e.g. agent shells
// launched into the workspace — without that cleanup running on
// a 409 dirty rejection. Pass nil if you have nothing to do
// between the preflight and the destructive part.
func (m *Manager) Delete(
	ctx context.Context, id string, force bool,
	beforeDestructive func(context.Context),
) (dirty []string, err error) {
	ws, err := m.db.GetWorkspace(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get workspace: %w", err)
	}
	if ws == nil {
		return nil, ErrWorkspaceNotFound
	}

	if !force {
		files, checkErr := dirtyFiles(ctx, ws.WorktreePath)
		if checkErr != nil {
			// Worktree may be missing/corrupt — surface as a
			// dirty-state response so the UI can offer force-delete.
			return []string{
				fmt.Sprintf("(dirty check failed: %v)", checkErr),
			}, nil
		}
		if len(files) > 0 {
			return files, nil
		}
	}

	if beforeDestructive != nil {
		beforeDestructive(ctx)
	}

	if err := m.cleanupWorkspaceArtifactsForDelete(ctx, ws); err != nil {
		return nil, err
	}

	if err := m.db.DeleteWorkspace(ctx, id); err != nil {
		return nil, fmt.Errorf("delete workspace record: %w", err)
	}
	m.removeWorkspaceSummaryFromCache(id)
	return nil, nil
}

// RequestRetry prepares an errored workspace for another setup
// attempt. If setup is already running, it queues one follow-up retry
// and returns startNow=false. If the workspace is not errored or
// creating, the request is discarded and startNow=false.
func (m *Manager) RequestRetry(
	ctx context.Context, id string,
) (*Workspace, bool, error) {
	ws, err := m.db.GetWorkspace(ctx, id)
	if err != nil {
		return nil, false, fmt.Errorf("get workspace: %w", err)
	}
	if ws == nil {
		return nil, false, ErrWorkspaceNotFound
	}
	started, err := m.db.StartWorkspaceRetry(ctx, ws.ID)
	if err != nil {
		return nil, false, err
	}
	if !started {
		return m.queueRetryOrStartErrored(ctx, id)
	}

	if err := m.prepareWorkspaceRetry(ctx, ws); err != nil {
		m.consumeQueuedRetry(ws.ID)
		return nil, false, err
	}
	return ws, true, nil
}

// StartQueuedRetryIfErrored consumes one queued retry for id. It
// starts the retry only if the workspace is still in error status at
// the time the queue is consumed; otherwise the queued retry is
// discarded.
func (m *Manager) StartQueuedRetryIfErrored(
	ctx context.Context, id string,
) (*Workspace, bool, error) {
	if !m.consumeQueuedRetry(id) {
		return nil, false, nil
	}

	ws, err := m.db.GetWorkspace(ctx, id)
	if err != nil {
		return nil, false, fmt.Errorf("get workspace: %w", err)
	}
	if ws == nil || ws.Status != "error" {
		return ws, false, nil
	}

	started, err := m.db.StartWorkspaceRetry(ctx, id)
	if err != nil {
		return nil, false, err
	}
	if !started {
		return ws, false, nil
	}

	if err := m.prepareWorkspaceRetry(ctx, ws); err != nil {
		m.consumeQueuedRetry(ws.ID)
		return nil, false, err
	}
	return ws, true, nil
}

func (m *Manager) queueRetryOrStartErrored(
	ctx context.Context, id string,
) (*Workspace, bool, error) {
	// Serialize the status re-check with queue consumption. If setup
	// already failed and the worker drained an empty queue, the retry
	// request must start the next setup attempt itself.
	m.retryMu.Lock()
	current, getErr := m.db.GetWorkspace(ctx, id)
	if getErr != nil {
		m.retryMu.Unlock()
		return nil, false, fmt.Errorf(
			"get workspace after retry conflict: %w", getErr,
		)
	}
	if current == nil {
		m.retryMu.Unlock()
		return nil, false, ErrWorkspaceNotFound
	}
	switch current.Status {
	case "creating":
		m.retryQueued[id] = true
		m.retryMu.Unlock()
		return current, false, nil
	case "error":
		delete(m.retryQueued, id)
		m.retryMu.Unlock()
		return m.startWorkspaceRetry(ctx, current)
	default:
		m.retryMu.Unlock()
		return nil, false, fmt.Errorf(
			"%w: workspace is not in error status",
			ErrWorkspaceInvalidState,
		)
	}
}

func (m *Manager) startWorkspaceRetry(
	ctx context.Context, ws *Workspace,
) (*Workspace, bool, error) {
	started, err := m.db.StartWorkspaceRetry(ctx, ws.ID)
	if err != nil {
		return nil, false, err
	}
	if !started {
		return m.queueRetryOrStartErrored(ctx, ws.ID)
	}

	if err := m.prepareWorkspaceRetry(ctx, ws); err != nil {
		m.consumeQueuedRetry(ws.ID)
		return nil, false, err
	}
	return ws, true, nil
}

func (m *Manager) prepareWorkspaceRetry(
	ctx context.Context, ws *Workspace,
) error {
	if err := m.cleanupWorkspaceArtifactsForRetry(ctx, ws); err != nil {
		return m.failSetup(
			ctx,
			ws.ID, workspaceSetupStageSetup,
			fmt.Errorf(
				"cleanup workspace artifacts before retry: %w", err,
			),
		)
	}
	retryBranch := retryWorkspaceBranch(ws)
	if err := m.updateWorkspaceBranch(ctx, ws.ID, retryBranch); err != nil {
		return m.failSetup(
			ctx,
			ws.ID, workspaceSetupStageSetup,
			fmt.Errorf("reset workspace branch before retry: %w", err),
		)
	}
	m.markRetryStarted(ctx, ws, retryBranch)
	return nil
}

func retryWorkspaceBranch(ws *Workspace) string {
	if ws.ItemType == db.WorkspaceItemTypeIssue && ws.WorkspaceBranch == "" {
		return ""
	}
	return workspaceBranchUnknown
}

func (m *Manager) consumeQueuedRetry(id string) bool {
	m.retryMu.Lock()
	defer m.retryMu.Unlock()
	if !m.retryQueued[id] {
		return false
	}
	delete(m.retryQueued, id)
	return true
}

func (m *Manager) markRetryStarted(
	ctx context.Context, ws *Workspace, workspaceBranch string,
) {
	ws.WorkspaceBranch = workspaceBranch
	ws.Status = "creating"
	ws.ErrorMessage = nil
	m.recordSetupEvent(
		ctx,
		ws.ID, workspaceSetupStageSetup, "retrying",
		"retrying workspace setup",
	)
}

func (m *Manager) cleanupWorkspaceArtifactsForRetry(
	ctx context.Context, ws *Workspace,
) error {
	if err := m.cleanupTmuxSession(ctx, ws); err != nil {
		return err
	}

	gitDir, ok, err := m.workspaceCleanupGitDir(ctx, ws)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	return m.withRepoLockForGitDir(ctx, gitDir, func() error {
		if err := runGitWithoutHooks(
			ctx, gitDir,
			"worktree", "remove", "--force", ws.WorktreePath,
		); err != nil && !isGitWorktreeAbsent(err) {
			return fmt.Errorf("remove git worktree: %w", err)
		}
		if ws.WorkspaceBranch == workspaceBranchUnknown {
			if err := deleteWorkspaceBranchStrict(
				ctx, gitDir, workspaceBranchUnknown,
			); err != nil {
				return err
			}
		}
		if err := m.deleteWorkspaceBranchesStrict(
			ctx, gitDir, ws, ws.WorkspaceBranch,
		); err != nil {
			return err
		}
		if err := runGitWithoutHooks(ctx, gitDir, "worktree", "prune"); err != nil {
			return fmt.Errorf("prune git worktrees: %w", err)
		}
		return nil
	})
}

func (m *Manager) cleanupWorkspaceArtifactsForDelete(
	ctx context.Context, ws *Workspace,
) error {
	if err := m.cleanupTmuxSession(ctx, ws); err != nil {
		return err
	}

	gitDir, ok, err := m.workspaceCleanupGitDir(ctx, ws)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	return m.withRepoLockForGitDir(ctx, gitDir, func() error {
		_ = runGitWithoutHooks(
			ctx, gitDir,
			"worktree", "remove", "--force", ws.WorktreePath,
		)
		m.deleteWorkspaceBranches(ctx, gitDir, ws, ws.WorkspaceBranch)
		_ = runGitWithoutHooks(ctx, gitDir, "worktree", "prune")
		return nil
	})
}

func (m *Manager) workspaceCleanupGitDir(
	ctx context.Context, ws *Workspace,
) (string, bool, error) {
	commonDir, err := worktreeCommonGitDir(ctx, ws.WorktreePath)
	if err == nil {
		if gitDirMatchesWorkspaceRepo(ctx, commonDir, ws) {
			owned, err := gitDirOwnsLinkedWorktree(
				ctx, commonDir, ws.WorktreePath,
			)
			if err != nil {
				return "", false, err
			}
			if owned {
				return commonDir, true, nil
			}
		}
	}

	if baseDir, ok, err := m.localWorktreeBaseDir(
		ctx, ws.Platform, ws.PlatformHost, ws.RepoOwner, ws.RepoName,
	); err != nil || ok {
		if err != nil {
			baseDir, ok = "", false
		}
		if !ok && m.clones == nil {
			return baseDir, ok, nil
		}
		if ok {
			owned, err := gitDirOwnsCleanupWorktree(
				ctx, baseDir, ws.WorktreePath,
			)
			if err != nil {
				return "", false, err
			}
			if owned {
				return baseDir, true, nil
			}
		}
	}

	if m.clones != nil {
		cloneDir, err := m.clones.ClonePathInNamespace(
			workspaceCloneNamespace(ws.Platform),
			ws.PlatformHost, ws.RepoOwner, ws.RepoName,
		)
		if err != nil {
			return "", false, err
		}
		ready, err := gitCloneDirReady(cloneDir)
		if err != nil {
			return "", false, err
		}
		if ready {
			owned, err := gitDirOwnsCleanupWorktree(
				ctx, cloneDir, ws.WorktreePath,
			)
			if err != nil {
				return "", false, err
			}
			if owned {
				return cloneDir, true, nil
			}
		}
	}

	return "", false, nil
}

func gitDirOwnsCleanupWorktree(
	ctx context.Context, gitDir, worktreePath string,
) (bool, error) {
	if strings.TrimSpace(worktreePath) == "" {
		return false, nil
	}
	info, err := os.Stat(worktreePath)
	if err != nil {
		if os.IsNotExist(err) {
			return gitDirTracksWorktreePath(ctx, gitDir, worktreePath)
		}
		return false, fmt.Errorf("stat workspace path: %w", err)
	}
	if !info.IsDir() {
		return false, nil
	}

	commonDir, err := worktreeCommonGitDir(ctx, worktreePath)
	if err != nil {
		if isGitWorktreeAbsent(err) {
			return false, nil
		}
		return false, err
	}
	candidateDir, err := canonicalFilesystemPath(gitDir)
	if err != nil {
		return false, fmt.Errorf("resolve git dir: %w", err)
	}
	actualDir, err := canonicalFilesystemPath(commonDir)
	if err != nil {
		return false, fmt.Errorf("resolve workspace git common dir: %w", err)
	}
	if actualDir != candidateDir {
		return false, nil
	}
	return gitDirOwnsLinkedWorktree(ctx, gitDir, worktreePath)
}

func gitDirOwnsLinkedWorktree(
	ctx context.Context, gitDir, worktreePath string,
) (bool, error) {
	commonDir, err := canonicalFilesystemPath(gitDir)
	if err != nil {
		return false, fmt.Errorf("resolve git common dir: %w", err)
	}
	worktreeDir, err := canonicalWorktreeListPath(worktreePath)
	if err != nil {
		return false, fmt.Errorf("resolve workspace path: %w", err)
	}
	if pathContains(worktreeDir, commonDir) {
		return false, nil
	}
	return gitDirTracksWorktreePath(ctx, gitDir, worktreePath)
}

func canonicalFilesystemPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if evaluated, err := filepath.EvalSymlinks(abs); err == nil {
		return evaluated, nil
	}
	return abs, nil
}

func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." ||
		(rel != ".." &&
			!strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

func gitDirMatchesWorkspaceRepo(
	ctx context.Context, dir string, ws *Workspace,
) bool {
	return validateOriginRemoteURLs(
		ctx, dir, ws.PlatformHost, ws.RepoOwner, ws.RepoName,
	) == nil
}

func (m *Manager) cleanupTmuxSession(
	ctx context.Context, ws *Workspace,
) error {
	usesPtyOwner := m.UsesPtyOwnerForWorkspace(ws)
	if usesPtyOwner {
		if m.ptyOwner == nil {
			return fmt.Errorf("pty owner backend unavailable")
		}
		if err := m.ptyOwner.Stop(ctx, ws.TmuxSession); err != nil {
			return fmt.Errorf(
				"stop pty owner session %q: %w", ws.TmuxSession, err,
			)
		}
	}

	type cleanupTarget struct {
		session string
		main    bool
	}
	var sessions []cleanupTarget
	if !usesPtyOwner {
		sessions = append(sessions, cleanupTarget{
			session: ws.TmuxSession,
			main:    true,
		})
	}
	stored, err := m.db.ListWorkspaceRuntimeTmuxSessions(ctx, ws.ID)
	if err != nil {
		return err
	}
	for _, storedSession := range stored {
		sessions = append(sessions, cleanupTarget{
			session: storedSession.TmuxSession,
		})
	}

	var cleanupErrs []error
	for _, target := range sessions {
		if target.session == "" {
			continue
		}
		err := m.killTmuxSession(ctx, target.session)
		if err == nil || isTmuxKillSessionGone(err) {
			continue
		}
		if target.main {
			hasSession, checkErr := m.workspaceHasCreatedTmuxSession(ctx, ws)
			if checkErr != nil {
				cleanupErrs = append(cleanupErrs, checkErr)
				continue
			}
			if !hasSession {
				continue
			}
		}
		cleanupErrs = append(
			cleanupErrs,
			fmt.Errorf("kill tmux session %q: %w", target.session, err),
		)
	}
	if err := errors.Join(cleanupErrs...); err != nil {
		return err
	}
	if err := m.db.DeleteWorkspaceRuntimeSessions(ctx, ws.ID); err != nil {
		return err
	}
	return nil
}

// Get returns a workspace by ID, or nil if not found.
func (m *Manager) Get(
	ctx context.Context, id string,
) (*Workspace, error) {
	return m.db.GetWorkspace(ctx, id)
}

// GetByMRForProvider returns the workspace for a specific provider-scoped MR,
// or nil.
func (m *Manager) GetByMRForProvider(
	ctx context.Context,
	provider, platformHost, owner, name string,
	mrNumber int,
) (*Workspace, error) {
	kind, err := platform.NormalizeKind(provider)
	if err != nil {
		return nil, err
	}
	return m.db.GetWorkspaceByMRForProvider(
		ctx, string(kind), platformHost, owner, name, mrNumber,
	)
}

// GetByIssueForProvider returns the workspace for a specific provider-scoped
// issue, or nil.
func (m *Manager) GetByIssueForProvider(
	ctx context.Context,
	provider, platformHost, owner, name string,
	issueNumber int,
) (*Workspace, error) {
	kind, err := platform.NormalizeKind(provider)
	if err != nil {
		return nil, err
	}
	return m.db.GetWorkspaceByIssueForProvider(
		ctx, string(kind), platformHost, owner, name, issueNumber,
	)
}

// GetSummary returns a workspace with joined MR metadata.
func (m *Manager) GetSummary(
	ctx context.Context, id string,
) (*WorkspaceSummary, error) {
	summary, err := m.db.GetWorkspaceSummary(ctx, id)
	if err != nil {
		return nil, err
	}
	if summary != nil {
		m.upsertWorkspaceSummaryCache(*summary)
	}
	return summary, nil
}

// ListSummaries returns all workspaces with joined MR metadata.
func (m *Manager) ListSummaries(
	ctx context.Context,
) ([]WorkspaceSummary, error) {
	summaries, err := m.db.ListWorkspaceSummaries(ctx)
	if err != nil {
		return nil, err
	}
	if len(summaries) == 0 {
		return m.cachedWorkspaceSummaries(), nil
	}
	return m.setWorkspaceSummaryCache(summaries), nil
}

func (m *Manager) cachedWorkspaceSummaries() []WorkspaceSummary {
	m.summaryCacheMu.RLock()
	defer m.summaryCacheMu.RUnlock()
	return slices.Clone(m.summaryCache)
}

func (m *Manager) setWorkspaceSummaryCache(
	summaries []WorkspaceSummary,
) []WorkspaceSummary {
	m.summaryCacheMu.Lock()
	defer m.summaryCacheMu.Unlock()
	m.summaryCache = filterDeletedWorkspaceSummaries(
		summaries,
		m.deletedSummaryIDs,
	)
	return slices.Clone(m.summaryCache)
}

func (m *Manager) upsertWorkspaceSummaryCache(summary WorkspaceSummary) {
	m.summaryCacheMu.Lock()
	defer m.summaryCacheMu.Unlock()
	if m.deletedSummaryIDs[summary.ID] {
		return
	}
	for i := range m.summaryCache {
		if m.summaryCache[i].ID == summary.ID {
			m.summaryCache[i] = summary
			return
		}
	}
	m.summaryCache = append(m.summaryCache, summary)
}

func (m *Manager) removeWorkspaceSummaryFromCache(id string) {
	m.summaryCacheMu.Lock()
	defer m.summaryCacheMu.Unlock()
	if m.deletedSummaryIDs == nil {
		m.deletedSummaryIDs = make(map[string]bool)
	}
	m.deletedSummaryIDs[id] = true
	m.summaryCache = slices.DeleteFunc(
		m.summaryCache,
		func(summary WorkspaceSummary) bool {
			return summary.ID == id
		},
	)
}

func filterDeletedWorkspaceSummaries(
	summaries []WorkspaceSummary,
	deleted map[string]bool,
) []WorkspaceSummary {
	if len(deleted) == 0 {
		return slices.Clone(summaries)
	}
	out := make([]WorkspaceSummary, 0, len(summaries))
	for _, summary := range summaries {
		if !deleted[summary.ID] {
			out = append(out, summary)
		}
	}
	return out
}

// ReapOrphanTmuxSessions kills middleman-managed tmux sessions that no longer
// correspond to any workspace row. This is a conservative startup cleanup for
// stale sessions left behind by crashes or previous bugs.
func (m *Manager) ReapOrphanTmuxSessions(ctx context.Context) error {
	workspaces, err := m.db.ListWorkspaces(ctx)
	if err != nil {
		return fmt.Errorf("list workspaces: %w", err)
	}
	live := make(map[string]bool, len(workspaces))
	for _, ws := range workspaces {
		if ws.TmuxSession == "" {
			continue
		}
		live[ws.TmuxSession] = true
	}
	storedSessions, err := m.db.ListAllWorkspaceRuntimeTmuxSessions(ctx)
	if err != nil {
		return err
	}
	for _, stored := range storedSessions {
		if stored.TmuxSession != "" {
			live[stored.TmuxSession] = true
		}
	}

	sessions, err := m.listTmuxSessionInfos(ctx)
	if err != nil {
		if isTmuxCommandUnavailable(err) {
			return nil
		}
		return err
	}
	for _, session := range sessions {
		if !isMiddlemanWorkspaceTmuxSessionName(session.name) {
			continue
		}
		if live[session.name] {
			continue
		}
		if session.owner != m.tmuxOwnerMarker() {
			continue
		}
		if err := m.killTmuxSession(ctx, session.name); err != nil &&
			!isTmuxKillSessionGone(err) {
			return fmt.Errorf(
				"kill orphan tmux session %q: %w", session.name, err,
			)
		}
	}
	return nil
}

// PruneMissingTmuxSessions reconciles persisted tmux ownership state against
// the host tmux server. Runtime-session rows whose tmux session was killed
// outside middleman are removed. Ready workspaces whose primary tmux session is
// missing are marked errored so list responses stop probing dead session names
// and the UI can offer retry/delete.
func (m *Manager) PruneMissingTmuxSessions(ctx context.Context) error {
	sessions, err := m.listTmuxSessions(ctx)
	if err != nil {
		return err
	}
	live := make(map[string]bool, len(sessions))
	for _, session := range sessions {
		live[session] = true
	}

	storedSessions, err := m.db.ListAllWorkspaceRuntimeTmuxSessions(ctx)
	if err != nil {
		return err
	}
	for _, stored := range storedSessions {
		if stored.TmuxSession == "" {
			continue
		}
		if live[stored.TmuxSession] {
			continue
		}
		slog.Debug(
			"prune missing runtime tmux session",
			"workspace_id", stored.WorkspaceID,
			"target_key", stored.TargetKey,
			"tmux_session", stored.TmuxSession,
		)
		if _, err := m.db.DeleteWorkspaceRuntimeSessionCreatedAt(
			ctx, stored.WorkspaceID, stored.SessionKey, stored.CreatedAt,
		); err != nil {
			return err
		}
	}

	workspaces, err := m.db.ListWorkspaces(ctx)
	if err != nil {
		return fmt.Errorf("list workspaces: %w", err)
	}
	for _, ws := range workspaces {
		if ws.Status != "ready" ||
			ws.TmuxSession == "" ||
			live[ws.TmuxSession] {
			continue
		}
		if m.usesPtyOwnerForWorkspace(&ws) {
			continue
		}
		msg := fmt.Sprintf(
			"tmux session is no longer running: %s",
			ws.TmuxSession,
		)
		slog.Debug(
			"mark workspace missing tmux session",
			"workspace_id", ws.ID,
			"tmux_session", ws.TmuxSession,
		)
		if err := m.db.UpdateWorkspaceStatus(
			ctx, ws.ID, "error", &msg,
		); err != nil {
			return err
		}
	}
	return nil
}

func isWorkspaceTmuxSessionName(session string) bool {
	const prefix = "middleman-"
	if len(session) != len(prefix)+16 ||
		!strings.HasPrefix(session, prefix) {
		return false
	}
	return isLowerHex(session[len(prefix):])
}

func isMiddlemanWorkspaceTmuxSessionName(session string) bool {
	if isWorkspaceTmuxSessionName(session) {
		return true
	}
	const prefix = "middleman-"
	// Runtime session names intentionally only match the current opaque
	// middleman-<workspace-id>-<target-key-hash> shape. Old readable
	// target suffixes are not supported; stored DB rows are authoritative
	// for restart activity and cleanup.
	if len(session) != len(prefix)+16+1+16 ||
		!strings.HasPrefix(session, prefix) ||
		session[len(prefix)+16] != '-' {
		return false
	}
	return isLowerHex(session[len(prefix):len(prefix)+16]) &&
		isLowerHex(session[len(prefix)+17:])
}

func isLowerHex(value string) bool {
	if value == "" {
		return false
	}
	for _, ch := range value {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

func (m *Manager) tmuxOwnerMarker() string {
	abs, err := filepath.Abs(m.worktreeDir)
	if err != nil {
		abs = m.worktreeDir
	}
	sum := sha256.Sum256([]byte(abs))
	return "middleman:" + hex.EncodeToString(sum[:8])
}

// TmuxOwnerMarker returns the marker used to tag tmux sessions owned by this
// workspace manager.
func (m *Manager) TmuxOwnerMarker() string {
	return m.tmuxOwnerMarker()
}

func (m *Manager) workspaceHasCreatedTmuxSession(
	ctx context.Context, ws *Workspace,
) (bool, error) {
	if ws.Status == "ready" {
		return true, nil
	}

	events, err := m.db.ListWorkspaceSetupEvents(ctx, ws.ID)
	if err != nil {
		return false, fmt.Errorf("list workspace setup events: %w", err)
	}
	for _, event := range events {
		if event.Stage == workspaceSetupStageTmuxSession &&
			event.Outcome == "success" {
			return true, nil
		}
		if event.Stage == workspaceSetupStageSetup &&
			event.Outcome == "ready" {
			return true, nil
		}
	}
	return false, nil
}

// EnsureTmux creates a tmux session if it does not already exist,
// using the manager's configured tmux command prefix.
func (m *Manager) EnsureTmux(
	ctx context.Context, session, cwd string,
) error {
	exists, err := m.tmuxSessionExists(ctx, session)
	if err != nil {
		return fmt.Errorf("tmux has-session: %w", err)
	}
	if exists {
		return nil
	}
	return m.newTmuxSession(ctx, session, cwd)
}

func isTmuxCommandUnavailable(err error) bool {
	return errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist)
}

type tmuxSessionInfo struct {
	name  string
	owner string
}

// tmuxSessionListFormat joins the session name and owner marker with a
// colon. tmux 3.6+ sanitizes control characters in -F output (a literal
// tab prints as "_"), so the separator must be printable. A colon is
// unambiguous because tmux replaces ":" in session names with "_", so no
// live session name can contain one; the owner marker ("middleman:<hex>")
// does, which is why parsing cuts at the first colon only.
const tmuxSessionListFormat = "#{session_name}:#{@middleman_owner}"

func (m *Manager) listTmuxSessionInfos(
	ctx context.Context,
) ([]tmuxSessionInfo, error) {
	cmd := m.tmuxExec(
		ctx,
		"list-sessions", "-F", tmuxSessionListFormat,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := procutil.Run(ctx, cmd, "tmux subprocess capacity")
	if err != nil {
		if isTmuxSessionAbsent(stderr.Bytes(), err) {
			return nil, nil
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return nil, fmt.Errorf("tmux list-sessions: %w: %s", err, msg)
	}
	var sessions []tmuxSessionInfo
	for line := range strings.SplitSeq(stdout.String(), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		name, owner, _ := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		sessions = append(sessions, tmuxSessionInfo{
			name:  name,
			owner: strings.TrimSpace(owner),
		})
	}
	return sessions, nil
}

func (m *Manager) listTmuxSessions(
	ctx context.Context,
) ([]string, error) {
	infos, err := m.listTmuxSessionInfos(ctx)
	if err != nil {
		return nil, err
	}
	sessions := make([]string, 0, len(infos))
	for _, info := range infos {
		sessions = append(sessions, info.name)
	}
	return sessions, nil
}

// RecordRuntimeSession stores a durable runtime session identity and its
// metadata. Normal launched terminals and agents use the session scope.
func (m *Manager) RecordRuntimeSession(
	ctx context.Context,
	session db.WorkspaceRuntimeSession,
) error {
	if session.SessionKey == "" {
		return nil
	}
	return m.db.UpsertWorkspaceRuntimeSession(ctx, &session)
}

func (m *Manager) UpdateRuntimeSessionLabel(
	ctx context.Context,
	workspaceID string,
	sessionKey string,
	label string,
) (bool, error) {
	if sessionKey == "" {
		return false, nil
	}
	return m.db.UpdateWorkspaceRuntimeSessionLabel(
		ctx, workspaceID, sessionKey, label,
	)
}

func (m *Manager) ForgetRuntimeSession(
	ctx context.Context,
	workspaceID string,
	sessionKey string,
) error {
	if sessionKey == "" {
		return nil
	}
	return m.db.DeleteWorkspaceRuntimeSession(ctx, workspaceID, sessionKey)
}

func (m *Manager) ForgetRuntimeSessionCreatedAt(
	ctx context.Context,
	workspaceID string,
	sessionKey string,
	createdAt time.Time,
) (bool, error) {
	if sessionKey == "" {
		return false, nil
	}
	return m.db.DeleteWorkspaceRuntimeSessionCreatedAt(
		ctx, workspaceID, sessionKey, createdAt,
	)
}

func (m *Manager) ForgetRuntimeSessionAfterExit(
	ctx context.Context,
	workspaceID string,
	sessionKey string,
	createdAt time.Time,
	tmuxSession string,
) (bool, error) {
	if sessionKey == "" {
		return false, nil
	}
	tmuxSession = strings.TrimSpace(tmuxSession)
	if tmuxSession != "" {
		exists, err := m.tmuxSessionExists(ctx, tmuxSession)
		if err != nil {
			return false, fmt.Errorf(
				"check exited runtime tmux session %q: %w",
				tmuxSession, err,
			)
		}
		if exists {
			return false, nil
		}
	}
	return m.db.DeleteWorkspaceRuntimeSessionCreatedAt(
		ctx, workspaceID, sessionKey, createdAt,
	)
}

func (m *Manager) RuntimeSessionsForWorkspace(
	ctx context.Context,
	workspaceID string,
) ([]db.WorkspaceRuntimeSession, error) {
	return m.db.ListWorkspaceRuntimeSessions(ctx, workspaceID)
}

func (m *Manager) AllRuntimeSessions(
	ctx context.Context,
) ([]db.WorkspaceRuntimeSession, error) {
	return m.db.ListAllWorkspaceRuntimeSessions(ctx)
}

func (m *Manager) RuntimeSessionKeysForWorkspace(
	ctx context.Context,
	workspaceID string,
) ([]string, error) {
	sessions, err := m.db.ListWorkspaceRuntimeSessions(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(sessions))
	for _, session := range sessions {
		if session.SessionKey != "" {
			keys = append(keys, session.SessionKey)
		}
	}
	return keys, nil
}

// StopStoredRuntimeSessionByKey cleans up a persisted runtime session even
// when the in-memory runtime manager no longer knows about it.
func (m *Manager) StopStoredRuntimeSessionByKey(
	ctx context.Context,
	workspaceID string,
	sessionKey string,
) (bool, error) {
	if sessionKey == "" {
		return false, nil
	}
	stored, err := m.db.ListWorkspaceRuntimeSessions(ctx, workspaceID)
	if err != nil {
		return false, err
	}
	for _, storedSession := range stored {
		if storedSession.SessionKey == sessionKey {
			return m.stopStoredRuntimeSession(ctx, workspaceID, storedSession)
		}
	}
	return false, nil
}

func (m *Manager) stopStoredRuntimeSession(
	ctx context.Context,
	workspaceID string,
	storedSession db.WorkspaceRuntimeSession,
) (bool, error) {
	m.runtimeTmuxMu.Lock()
	defer m.runtimeTmuxMu.Unlock()
	if storedSession.TmuxSession != "" {
		if err := m.killTmuxSession(ctx, storedSession.TmuxSession); err != nil &&
			!isTmuxKillSessionGone(err) {
			return true, fmt.Errorf(
				"kill tmux session %q: %w",
				storedSession.TmuxSession, err,
			)
		}
	} else if m.ptyOwner != nil {
		if err := m.ptyOwner.Stop(ctx, storedSession.SessionKey); err != nil {
			return true, fmt.Errorf(
				"stop pty owner session %q: %w",
				storedSession.SessionKey, err,
			)
		}
	}
	if err := m.db.DeleteWorkspaceRuntimeSession(
		ctx, workspaceID, storedSession.SessionKey,
	); err != nil {
		return true, err
	}
	return true, nil
}

// TmuxSessionsForWorkspace returns the persisted workspace tmux
// session plus stored per-agent sessions. Runtime tmux sessions are
// stored rather than discovered by naming convention so restart
// recovery follows explicit ownership state.
func (m *Manager) TmuxSessionsForWorkspace(
	ctx context.Context,
	workspaceID string,
	baseSession string,
) ([]string, error) {
	stored, err := m.db.ListWorkspaceRuntimeTmuxSessions(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(stored)+1)
	if baseSession != "" {
		seen[baseSession] = true
		out = append(out, baseSession)
	}
	for _, storedSession := range stored {
		session := storedSession.TmuxSession
		if session == "" || seen[session] {
			continue
		}
		seen[session] = true
		out = append(out, session)
	}
	return out, nil
}

// TmuxPaneTitle returns the active pane title for a session. Agents
// can update this via terminal title escape sequences, which tmux
// exposes through the pane_title format.
func (m *Manager) TmuxPaneTitle(
	ctx context.Context, session string,
) (string, error) {
	return m.tmuxPaneTitle(ctx, session)
}

// TerminalPaneSnapshot returns recent terminal output for the backend
// that owns the workspace's primary terminal.
func (m *Manager) TerminalPaneSnapshot(
	ctx context.Context, ws *db.Workspace,
	session string,
) (TerminalPaneSnapshot, error) {
	if ws != nil && session == ws.TmuxSession && m.UsesPtyOwnerForWorkspace(ws) {
		if m.ptyOwner == nil {
			return TerminalPaneSnapshot{}, fmt.Errorf("pty owner backend unavailable")
		}
		status, err := m.ptyOwner.Snapshot(ctx, session)
		if err != nil {
			return TerminalPaneSnapshot{}, err
		}
		return TerminalPaneSnapshot{
			Title:  status.Title,
			Output: string(status.Output),
		}, nil
	}
	return m.tmuxPaneSnapshot(ctx, session)
}

// tmuxPaneSnapshot returns the active pane title and recent pane
// output for passive activity detection.
func (m *Manager) tmuxPaneSnapshot(
	ctx context.Context, session string,
) (TerminalPaneSnapshot, error) {
	title, err := m.tmuxPaneTitle(ctx, session)
	if err != nil {
		return TerminalPaneSnapshot{}, err
	}

	cmd := m.tmuxExec(
		ctx,
		"capture-pane", "-p",
		"-t", session,
		"-S", fmt.Sprintf("-%d", tmuxCaptureScrollbackLines),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = procutil.Run(ctx, cmd, "tmux subprocess capacity")
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return TerminalPaneSnapshot{}, fmt.Errorf(
			"tmux capture-pane: %w: %s", err, msg,
		)
	}
	return TerminalPaneSnapshot{
		Title:  title,
		Output: stdout.String(),
	}, nil
}

func (m *Manager) tmuxPaneTitle(
	ctx context.Context, session string,
) (string, error) {
	cmd := m.tmuxExec(
		ctx,
		"display-message", "-p",
		"-t", session,
		"#{pane_title}",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := procutil.Run(ctx, cmd, "tmux subprocess capacity")
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return "", fmt.Errorf("tmux display-message: %w: %s", err, msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (m *Manager) newTmuxSession(
	ctx context.Context, session, cwd string,
) error {
	shell := userLoginShell()
	cmd := m.tmuxExec(
		ctx,
		"new-session", "-d",
		"-s", session,
		"-c", cwd,
		shell, "-l",
	)
	if err := runBuiltCmd(ctx, cmd); err != nil {
		return err
	}
	if err := m.setTmuxOwnerMarker(ctx, session); err != nil {
		if killErr := m.killTmuxSession(ctx, session); killErr != nil &&
			!isTmuxKillSessionGone(killErr) {
			return fmt.Errorf(
				"set tmux owner marker: %w; cleanup new tmux session: %v",
				err, killErr,
			)
		}
		return fmt.Errorf("set tmux owner marker: %w", err)
	}
	return nil
}

func (m *Manager) setTmuxOwnerMarker(
	ctx context.Context, session string,
) error {
	return runBuiltCmd(
		ctx,
		m.tmuxExec(
			ctx,
			"set-option", "-t", session,
			"@middleman_owner", m.tmuxOwnerMarker(),
		),
	)
}

// tmuxSessionExists runs `tmux has-session` and distinguishes a
// genuine "session absent" signal from a wrapper/binary failure.
// tmux reports session-absent by exiting 1 with one of two
// well-known stderr messages:
//
//	can't find session: <name>
//	no server running on <socket>
//
// Stdout and stderr are captured separately so a wrapper that
// happens to emit those phrases on stdout for unrelated reasons
// cannot masquerade as session-absent. Any other failure — missing
// binary (non-ExitError), wrapper exit codes other than 1, or
// exit-1 without the canonical stderr — propagates so
// misconfiguration surfaces instead of silently falling through to
// new-session through the same broken wrapper.
func (m *Manager) tmuxSessionExists(
	ctx context.Context, session string,
) (bool, error) {
	cmd := m.tmuxExec(ctx, "has-session", "-t", session)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := procutil.Run(ctx, cmd, "tmux subprocess capacity")
	if err == nil {
		return true, nil
	}
	if isTmuxSessionAbsent(stderr.Bytes(), err) {
		return false, nil
	}
	msg := strings.TrimSpace(stderr.String())
	if msg == "" {
		msg = strings.TrimSpace(stdout.String())
	}
	return false, fmt.Errorf("%w: %s", err, msg)
}

// isTmuxSessionAbsent reports whether a has-session failure is
// tmux's documented "session does not exist" signal. Must be both
// exit code 1 AND one of tmux's specific stderr phrases. Plain
// exit 1 is a common generic wrapper/shell failure code, and
// stdout content is not load-bearing — a wrapper could emit
// anything there for unrelated reasons.
func isTmuxSessionAbsent(stderr []byte, err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		return false
	}
	msg := string(stderr)
	return strings.Contains(msg, "can't find session") ||
		strings.Contains(msg, "no server running") ||
		(strings.Contains(msg, "error connecting to") &&
			strings.Contains(msg, "No such file or directory"))
}

func isTmuxKillSessionGone(err error) bool {
	if err == nil {
		return true
	}
	msg := err.Error()
	return isTmuxSessionAbsent([]byte(msg), err) ||
		strings.Contains(msg, "server exited unexpectedly")
}

// killTmuxSession kills a tmux session via the manager's prefix.
// Errors are returned rather than logged — callers decide whether
// to ignore them (Delete ignores; tests assert).
func (m *Manager) killTmuxSession(
	ctx context.Context, session string,
) error {
	return runBuiltCmd(
		ctx, m.tmuxExec(ctx, "kill-session", "-t", session),
	)
}

// userLoginShell resolves the current user's login shell from
// the OS user database (passwd), falling back to $SHELL, then
// /bin/sh.
func userLoginShell() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		if shell := lookupPasswdShell(u.Username); shell != "" {
			return shell
		}
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/sh"
}

func lookupPasswdShell(username string) string {
	cmd := procutil.Command("getent", "passwd", username)
	out, err := procutil.Output(
		context.Background(), cmd, "shell lookup subprocess capacity",
	)
	if err == nil {
		return shellFromPasswdLine(string(out))
	}
	// Fallback: read /etc/passwd directly with exact field match
	// (no grep — avoids regex injection from metacharacters in
	// usernames).
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return ""
	}
	prefix := username + ":"
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.HasPrefix(line, prefix) {
			return shellFromPasswdLine(line)
		}
	}
	return ""
}

func shellFromPasswdLine(line string) string {
	line = strings.TrimSpace(line)
	fields := strings.Split(line, ":")
	if len(fields) < 7 {
		return ""
	}
	shell := strings.TrimSpace(fields[len(fields)-1])
	if shell == "" || shell == "/usr/bin/false" ||
		shell == "/bin/false" || shell == "/sbin/nologin" {
		return ""
	}
	return shell
}

// runGitWithoutHooks executes a git mutation in dir and returns combined
// output on error. It is the only git mutation runner in this package:
// workspace git dirs may be user-owned local worktree bases whose repo-local
// hooks middleman must never run, so there is deliberately no hook-executing
// variant.
func runGitWithoutHooks(ctx context.Context, dir string, args ...string) error {
	_, err := gitCombinedOutput(ctx, dir, gitArgsWithoutHooks(args...)...)
	return err
}

// gitCombinedOutput runs git in dir, returning combined output. On failure
// the returned error includes the trimmed output, and the raw output is
// still returned so callers can inspect it.
func gitCombinedOutput(
	ctx context.Context, dir string, args ...string,
) (string, error) {
	cmd := workspaceGitCommand(ctx, dir, args...)
	out, err := procutil.CombinedOutput(
		ctx, cmd, "git subprocess capacity",
	)
	if err != nil {
		return string(out), fmt.Errorf(
			"%w: %s", err, strings.TrimSpace(string(out)),
		)
	}
	return string(out), nil
}

func gitArgsWithoutHooks(args ...string) []string {
	gitArgs := make([]string, 0, len(args)+2)
	gitArgs = append(gitArgs, "-c", "core.hooksPath=/dev/null")
	return append(gitArgs, args...)
}

func (m *Manager) fetchWorkspaceBase(
	ctx context.Context,
	dir, platformHost string,
	requireOriginHead bool,
) error {
	run := runGitWithoutHooks
	if m.clones != nil {
		run = func(ctx context.Context, dir string, args ...string) error {
			out, err := m.clones.RunGit(ctx, platformHost, dir, args...)
			if err != nil {
				return fmt.Errorf(
					"%w: %s", err, strings.TrimSpace(string(out)),
				)
			}
			return nil
		}
	}
	return fetchWorkspaceBaseWithGit(ctx, run, dir, requireOriginHead)
}

func fetchWorkspaceBaseWithGit(
	ctx context.Context,
	run func(context.Context, string, ...string) error,
	dir string,
	requireOriginHead bool,
) error {
	// The clones-backed run bypasses runGitWithoutHooks, so hook
	// suppression must be applied here as well; on the runGitWithoutHooks
	// path the flag is duplicated, which git tolerates.
	runWithoutHooks := func(ctx context.Context, dir string, args ...string) error {
		return run(ctx, dir, gitArgsWithoutHooks(args...)...)
	}
	if err := runWithoutHooks(
		ctx, dir,
		"fetch", "--prune", "--no-tags", "--recurse-submodules=no",
		"--negotiation-tip=refs/remotes/origin/*", "origin",
		"+refs/heads/*:refs/remotes/origin/*",
	); err != nil {
		return fmt.Errorf("fetch configured worktree base: %w", err)
	}
	if err := refreshWorkspaceBaseOriginHeadWithGit(ctx, runWithoutHooks, dir); err != nil {
		if !requireOriginHead {
			return nil
		}
		return err
	}
	return nil
}

func refreshWorkspaceBaseOriginHeadWithGit(
	ctx context.Context,
	run func(context.Context, string, ...string) error,
	dir string,
) error {
	setHeadErr := run(ctx, dir, "remote", "set-head", "origin", "-a")
	if setHeadErr == nil {
		return nil
	}
	if originHeadRefReady(ctx, dir) {
		return nil
	}
	for _, branch := range []string{"main", "master"} {
		ref := "refs/remotes/origin/" + branch
		if gitRefExists(ctx, dir, ref) {
			if err := run(
				ctx, dir, "symbolic-ref",
				"refs/remotes/origin/HEAD", ref,
			); err != nil {
				return fmt.Errorf(
					"set configured worktree base origin/HEAD: %w", err,
				)
			}
			return nil
		}
	}
	return fmt.Errorf(
		"refresh configured worktree base origin/HEAD: %w", setHeadErr,
	)
}

func originHeadRefReady(ctx context.Context, dir string) bool {
	out, err := gitOutput(
		ctx, dir, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD",
	)
	if err != nil {
		return false
	}
	return gitRefExists(ctx, dir, strings.TrimSpace(out))
}

func gitRefExists(ctx context.Context, dir, ref string) bool {
	cmd := workspaceGitCommand(
		ctx, dir, "show-ref", "--verify", "--quiet", ref,
	)
	err := cmd.Run()
	return err == nil
}

func runGitWorktreeAdd(
	ctx context.Context, dir, worktreePath string, args ...string,
) error {
	gitArgs := make([]string, 0, len(args)+3)
	gitArgs = append(gitArgs, "worktree", "add", worktreePath)
	gitArgs = append(gitArgs, args...)
	return runGitWithoutHooks(ctx, dir, gitArgs...)
}

// runBuiltCmd runs a pre-built exec.Cmd and wraps any failure with
// the combined output. Used for tmux invocations whose *exec.Cmd is
// assembled by tmuxExec so argv[0] access stays inside that helper.
func runBuiltCmd(ctx context.Context, cmd *exec.Cmd) error {
	out, err := procutil.CombinedOutput(
		ctx, cmd, "tmux subprocess capacity",
	)
	if err != nil {
		return fmt.Errorf(
			"%w: %s", err, strings.TrimSpace(string(out)),
		)
	}
	return nil
}

// dirtyFiles returns the list of uncommitted files in a worktree.
func dirtyFiles(
	ctx context.Context, worktreePath string,
) ([]string, error) {
	cmd := workspaceGitCommand(
		ctx, "", "-C", worktreePath, "status", "--porcelain",
	)
	out, err := procutil.Output(
		ctx, cmd, "git subprocess capacity",
	)
	if err != nil {
		return nil, err
	}
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return nil, nil
	}
	var files []string
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			// porcelain format: "XY filename"
			if len(line) > 3 {
				files = append(files, line[3:])
			} else {
				files = append(files, line)
			}
		}
	}
	return files, nil
}

func (m *Manager) setErrorWithContext(
	ctx context.Context, id string, origErr error,
) {
	msg := origErr.Error()
	if err := m.updateWorkspaceStatusWithContext(
		ctx, id, "error", &msg,
	); err != nil {
		slog.Error("failed to set workspace error status",
			"workspace_id", id, "err", err)
	}
}

func (m *Manager) recordSetupEvent(
	ctx context.Context,
	workspaceID, stage, outcome, message string,
) {
	persistCtx, cancel := m.persistenceContext(ctx)
	defer cancel()
	m.recordSetupEventWithContext(
		persistCtx, workspaceID, stage, outcome, message,
	)
}

func (m *Manager) recordSetupEventWithContext(
	ctx context.Context,
	workspaceID, stage, outcome, message string,
) {
	err := m.db.InsertWorkspaceSetupEvent(
		ctx,
		&db.WorkspaceSetupEvent{
			WorkspaceID: workspaceID,
			Stage:       stage,
			Outcome:     outcome,
			Message:     message,
		},
	)
	if err != nil {
		slog.Warn("workspace setup audit insert failed",
			"workspace_id", workspaceID,
			"stage", stage,
			"outcome", outcome,
			"err", err,
		)
	}
}

func (m *Manager) failSetup(
	ctx context.Context,
	workspaceID, stage string, origErr error,
) error {
	wrapped := wrapWorkspaceSetupError(stage, origErr)
	persistCtx, cancel := m.persistenceContext(ctx)
	defer cancel()
	m.recordSetupEventWithContext(
		persistCtx, workspaceID, stage, "failure", wrapped.Error(),
	)
	slog.Error("workspace setup failed",
		"workspace_id", workspaceID,
		"stage", stage,
		"err", wrapped,
	)
	m.setErrorWithContext(persistCtx, workspaceID, wrapped)
	return wrapped
}

func wrapWorkspaceSetupError(stage string, err error) error {
	if procutil.IsResourceExhausted(err) {
		switch stage {
		case workspaceSetupStageClone:
			return fmt.Errorf(
				"ensure clone: host process limit reached while starting git or helper processes: %w",
				err,
			)
		case workspaceSetupStageWorktree:
			return fmt.Errorf(
				"add git worktree: host process limit reached while starting git or helper processes: %w",
				err,
			)
		case workspaceSetupStageTmuxSession:
			return fmt.Errorf(
				"tmux new-session: host process limit reached while starting tmux or shell: %w",
				err,
			)
		}
	}
	switch stage {
	case workspaceSetupStageClone:
		return fmt.Errorf("ensure clone: %w", err)
	case workspaceSetupStageWorktree:
		return fmt.Errorf("add git worktree: %w", err)
	case workspaceSetupStageTmuxSession:
		return fmt.Errorf("tmux new-session: %w", err)
	default:
		return err
	}
}

// rollbackWorktree removes a partially created worktree and its
// branch under the per-repo lock.
func (m *Manager) rollbackWorktree(
	ctx context.Context, cloneDir string, ws *Workspace,
	branch string,
) {
	cleanupCtx, cancel := cleanupContext(ctx)
	defer cancel()
	err := m.withRepoLockForGitDir(cleanupCtx, cloneDir, func() error {
		if err := runGitWithoutHooks(
			cleanupCtx, cloneDir,
			"worktree", "remove", "--force", ws.WorktreePath,
		); err != nil {
			slog.Warn("rollback: worktree remove failed",
				"path", ws.WorktreePath, "err", err)
		}
		m.deleteWorkspaceBranches(cleanupCtx, cloneDir, ws, branch)
		return nil
	})
	if err != nil {
		slog.Warn("rollback: acquire worktree lock failed",
			"path", cloneDir, "err", err)
	}
}

func (m *Manager) deleteWorkspaceBranches(
	ctx context.Context, cloneDir string, ws *Workspace,
	managedBranch string,
) {
	for _, branch := range workspaceBranchCandidates(ws, managedBranch) {
		if err := validateLocalBranchName(
			ctx, cloneDir, branch,
		); err != nil {
			slog.Warn("workspace branch delete skipped",
				"branch", branch, "err", err)
			continue
		}
		if err := runGitWithoutHooks(
			ctx, cloneDir, "branch", "-D", "--", branch,
		); err != nil {
			slog.Warn("workspace branch delete failed",
				"branch", branch, "err", err)
		}
	}
}

func (m *Manager) deleteWorkspaceBranchesStrict(
	ctx context.Context, cloneDir string, ws *Workspace,
	managedBranch string,
) error {
	for _, branch := range workspaceBranchCandidates(ws, managedBranch) {
		if err := deleteWorkspaceBranchStrict(
			ctx, cloneDir, branch,
		); err != nil {
			return err
		}
	}
	return nil
}

func deleteWorkspaceBranchStrict(
	ctx context.Context, cloneDir string, branch string,
) error {
	if err := validateLocalBranchName(
		ctx, cloneDir, branch,
	); err != nil {
		return err
	}
	if err := runGitWithoutHooks(
		ctx, cloneDir, "branch", "-D", "--", branch,
	); err != nil && !isGitBranchAbsent(err) {
		return fmt.Errorf("delete git branch %q: %w", branch, err)
	}
	return nil
}

func isGitWorktreeAbsent(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "is not a working tree") ||
		strings.Contains(msg, "is not a worktree") ||
		strings.Contains(msg, "not a git repository") ||
		strings.Contains(msg, "no such file or directory")
}

func isGitBranchAbsent(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "branch") &&
		strings.Contains(msg, "not found")
}

func gitCloneDirReady(cloneDir string) (bool, error) {
	_, err := os.Stat(filepath.Join(cloneDir, "HEAD"))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("stat git clone dir: %w", err)
}

func isUniqueConstraintError(err error) bool {
	type sqliteCoder interface {
		Code() int
	}
	var coder sqliteCoder
	if !errors.As(err, &coder) {
		return false
	}
	const sqliteConstraintUnique = 2067
	return coder.Code() == sqliteConstraintUnique
}

func workspaceBranchCandidates(
	ws *Workspace, managedBranch string,
) []string {
	if managedBranch == workspaceBranchUnknown {
		if ws.ItemType == db.WorkspaceItemTypeIssue {
			// Trust the persisted branch. The bare-form fallback
			// only applies when GitHeadRef is empty (pre-feature
			// workspaces); a slug-style workspace's bare-form
			// branch may be a user-owned local branch that
			// middleman never created, so cleanup must not delete
			// it as a candidate.
			if ws.GitHeadRef != "" {
				return []string{ws.GitHeadRef}
			}
			return []string{issueWorkspaceBranch(ws.ItemNumber)}
		}
		return []string{syntheticPRWorktreeBranch(ws.ItemNumber)}
	}
	if managedBranch == "" {
		return nil
	}
	return []string{managedBranch}
}

func (m *Manager) persistenceContext(
	ctx context.Context,
) (context.Context, context.CancelFunc) {
	return boundedDetachedContext(ctx, workspacePersistTimeout)
}

func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return boundedDetachedContext(ctx, workspaceCleanupTimeout)
}

func boundedDetachedContext(
	ctx context.Context, timeout time.Duration,
) (context.Context, context.CancelFunc) {
	base := context.WithoutCancel(ctx)
	if deadline, ok := ctx.Deadline(); ok {
		if time.Until(deadline) <= timeout {
			return context.WithDeadline(base, deadline)
		}
	}
	return context.WithTimeout(base, timeout)
}

func (m *Manager) updateWorkspaceStatus(
	ctx context.Context, id, status string, errMsg *string,
) error {
	persistCtx, cancel := m.persistenceContext(ctx)
	defer cancel()
	return m.updateWorkspaceStatusWithContext(
		persistCtx, id, status, errMsg,
	)
}

func (m *Manager) updateWorkspaceStatusWithContext(
	ctx context.Context, id, status string, errMsg *string,
) error {
	return m.db.UpdateWorkspaceStatus(
		ctx, id, status, errMsg,
	)
}

func (m *Manager) updateWorkspaceBranch(
	ctx context.Context, id, branch string,
) error {
	persistCtx, cancel := m.persistenceContext(ctx)
	defer cancel()
	return m.db.UpdateWorkspaceBranch(
		persistCtx, id, branch,
	)
}

func gitRefSHA(
	ctx context.Context, dir, ref string,
) (string, bool, error) {
	out, err := gitCombinedOutput(
		ctx, dir, "rev-parse", "--verify", "--quiet",
		ref+"^{commit}",
	)
	if err == nil {
		return strings.TrimSpace(out), true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return "", false, nil
	}
	return "", false, err
}

func worktreeCommonGitDir(
	ctx context.Context, worktreePath string,
) (string, error) {
	out, err := gitCombinedOutput(
		ctx, worktreePath,
		"rev-parse", "--path-format=absolute", "--git-common-dir",
	)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func gitDirTracksWorktreePath(
	ctx context.Context, gitDir, worktreePath string,
) (bool, error) {
	if strings.TrimSpace(worktreePath) == "" {
		return false, nil
	}
	want, err := canonicalWorktreeListPath(worktreePath)
	if err != nil {
		return false, fmt.Errorf("resolve workspace path: %w", err)
	}
	out, err := gitCombinedOutput(
		ctx, gitDir, "worktree", "list", "--porcelain",
	)
	if err != nil {
		return false, err
	}
	for line := range strings.SplitSeq(out, "\n") {
		path, ok := strings.CutPrefix(line, "worktree ")
		if !ok {
			continue
		}
		got, err := canonicalWorktreeListPath(strings.TrimSpace(path))
		if err != nil {
			return false, fmt.Errorf("resolve tracked worktree path: %w", err)
		}
		if got == want {
			return true, nil
		}
	}
	return false, nil
}

func canonicalWorktreeListPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if evaluated, err := filepath.EvalSymlinks(abs); err == nil {
		return evaluated, nil
	}
	parent := filepath.Dir(abs)
	evaluatedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return abs, nil
	}
	return filepath.Join(evaluatedParent, filepath.Base(abs)), nil
}

func gitIsBareRepository(ctx context.Context, dir string) (bool, error) {
	out, err := gitCombinedOutput(
		ctx, dir, "rev-parse", "--is-bare-repository",
	)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "true", nil
}

func localBranchExists(
	ctx context.Context, dir, branch string,
) (bool, error) {
	cmd := workspaceGitCommand(
		ctx,
		dir,
		"show-ref",
		"--verify",
		"--quiet",
		"refs/heads/"+branch,
	)
	err := procutil.Run(ctx, cmd, "git subprocess capacity")
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func localBranchCheckedOut(
	ctx context.Context, dir, branch string,
) (bool, error) {
	out, err := gitCombinedOutput(
		ctx, dir, "worktree", "list", "--porcelain",
	)
	if err != nil {
		return false, err
	}
	want := "refs/heads/" + branch
	for line := range strings.SplitSeq(out, "\n") {
		got, ok := strings.CutPrefix(line, "branch ")
		if ok && strings.TrimSpace(got) == want {
			return true, nil
		}
	}
	return false, nil
}

func workspaceGitCommand(
	ctx context.Context, dir string, args ...string,
) *exec.Cmd {
	// Keep git process construction centralized so workspace mutations share
	// kit's automation defaults: no inherited GIT_* hook state, no global or
	// system config, and no terminal prompts. Callers remain responsible for
	// wrapping commands in procutil when they need the shared capacity guard.
	return gitcmd.New().Command(ctx, dir, args...)
}

func nextAvailableBranchName(
	ctx context.Context, dir, branch string,
) (string, error) {
	for i := 2; i < 1000; i++ {
		candidate := fmt.Sprintf("%s-%d", branch, i)
		exists, err := localBranchExists(ctx, dir, candidate)
		if err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
	}
	return "", fmt.Errorf(
		"could not find an available branch name derived from %q",
		branch,
	)
}
