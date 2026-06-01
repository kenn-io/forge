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
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	gitcmd "go.kenn.io/kit/git/cmd"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/gitclone"
	"go.kenn.io/middleman/internal/procutil"
	"go.kenn.io/middleman/internal/workspace/localruntime"
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
}

// CreateIssueOptions controls how issue-backed workspaces choose their branch.
//
// The default path creates middleman's conventional issue branch. When a local
// branch with that name already exists, callers can either ask the manager to
// reuse it or supply a different GitHeadRef.
type CreateIssueOptions struct {
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

// withRepoLock acquires a lock for the clone directory, executes the function,
// and releases the lock. The lock is released even if the function panics.
func (m *Manager) withRepoLock(ctx context.Context, cloneDir string, fn func() error) error {
	lock, err := m.locks.Acquire(ctx, cloneDir)
	if err != nil {
		return fmt.Errorf("acquire worktree lock for %q: %w", cloneDir, err)
	}
	defer func() {
		if err := lock.Unlock(); err != nil {
			slog.Warn("failed to release worktree lock",
				"path", cloneDir, "err", err)
		}
	}()
	return fn()
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
	platformHost, owner, name string,
	mrNumber int,
) (*Workspace, error) {
	repo, err := m.db.GetRepoByHostOwnerName(
		ctx, platformHost, owner, name,
	)
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
		PlatformHost:    platformHost,
		RepoOwner:       owner,
		RepoName:        name,
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      mrNumber,
		GitHeadRef:      mr.HeadBranch,
		MRHeadRepo:      workspaceHeadRepo(platformHost, owner, name, mr.HeadRepoCloneURL),
		WorkspaceBranch: workspaceBranchUnknown,
		WorktreePath: filepath.Join(
			m.worktreeDir, platformHost, owner, name,
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
	repo, err := m.db.GetRepoByHostOwnerName(
		ctx, platformHost, owner, name,
	)
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
	if m.clones != nil {
		remoteURL := fmt.Sprintf(
			"https://%s/%s/%s.git",
			platformHost, owner, name,
		)
		if err := m.clones.EnsureClone(
			ctx, platformHost, owner, name, remoteURL,
		); err != nil {
			return nil, fmt.Errorf("ensure clone: %w", err)
		}

		cloneDir, err := m.clones.ClonePath(platformHost, owner, name)
		if err != nil {
			return nil, err
		}
		exists, err := localBranchExists(ctx, cloneDir, gitHeadRef)
		if err != nil {
			return nil, fmt.Errorf("inspect local branch: %w", err)
		}
		if exists {
			if opts.ReuseExistingBranch {
				workspaceBranch = ""
			} else {
				suggested, err := nextAvailableBranchName(
					ctx, cloneDir, gitHeadRef,
				)
				if err != nil {
					return nil, fmt.Errorf(
						"suggest branch name: %w",
						err,
					)
				}
				return nil, &IssueWorkspaceBranchConflictError{
					Branch:          gitHeadRef,
					SuggestedBranch: suggested,
				}
			}
		}
	}

	id, err := newWorkspaceID()
	if err != nil {
		return nil, err
	}

	ws := &Workspace{
		ID:              id,
		PlatformHost:    platformHost,
		RepoOwner:       owner,
		RepoName:        name,
		ItemType:        db.WorkspaceItemTypeIssue,
		ItemNumber:      issueNumber,
		GitHeadRef:      gitHeadRef,
		WorkspaceBranch: workspaceBranch,
		WorktreePath: filepath.Join(
			m.worktreeDir, platformHost, owner, name,
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
	if m.clones == nil {
		return m.failSetup(
			ctx,
			ws.ID, workspaceSetupStageClone,
			fmt.Errorf("clone manager not set"),
		)
	}

	remoteURL := fmt.Sprintf(
		"https://%s/%s/%s.git",
		ws.PlatformHost, ws.RepoOwner, ws.RepoName,
	)

	if err := m.clones.EnsureClone(
		ctx, ws.PlatformHost, ws.RepoOwner,
		ws.RepoName, remoteURL,
	); err != nil {
		return m.failSetup(
			ctx,
			ws.ID, workspaceSetupStageClone, err,
		)
	}

	cloneDir, err := m.clones.ClonePath(
		ws.PlatformHost, ws.RepoOwner, ws.RepoName,
	)
	if err != nil {
		return m.failSetup(
			ctx,
			ws.ID, workspaceSetupStageClone, err,
		)
	}

	branch, err := m.addWorktree(ctx, cloneDir, ws)
	if err != nil {
		return m.failSetup(
			ctx,
			ws.ID, workspaceSetupStageWorktree, err,
		)
	}
	ws.WorkspaceBranch = branch
	if err := m.updateWorkspaceBranch(
		ctx, ws.ID, branch,
	); err != nil {
		m.rollbackWorktree(ctx, cloneDir, ws, branch)
		return m.failSetup(
			ctx,
			ws.ID, workspaceSetupStageWorktree, err,
		)
	}

	err = m.newTerminalSession(ctx, ws)
	if err != nil {
		m.rollbackWorktree(ctx, cloneDir, ws, branch)
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

	if err := m.updateWorkspaceStatus(
		ctx, ws.ID, "ready", nil,
	); err != nil {
		return m.failSetup(
			ctx,
			ws.ID, workspaceSetupStageSetup,
			fmt.Errorf("update status to ready: %w", err),
		)
	}
	m.recordSetupEvent(
		ctx,
		ws.ID, workspaceSetupStageSetup, "ready",
		"workspace ready",
	)
	return nil
}

// addWorktree creates the workspace's worktree and branch under the
// per-repo lock. The lock prevents concurrent worktree mutations on
// the same bare clone from clobbering each other; see FileLockManager.
func (m *Manager) addWorktree(
	ctx context.Context, cloneDir string, ws *Workspace,
) (string, error) {
	var branch string
	err := m.withRepoLock(ctx, cloneDir, func() error {
		var addErr error
		branch, addErr = m.addWorktreeLocked(ctx, cloneDir, ws)
		return addErr
	})
	return branch, err
}

// addWorktreeLocked runs the worktree-add decision tree. Callers must
// hold the per-repo lock for cloneDir before invoking this function.
func (m *Manager) addWorktreeLocked(
	ctx context.Context, cloneDir string, ws *Workspace,
) (string, error) {
	if ws.ItemType == db.WorkspaceItemTypeIssue {
		return m.addIssueWorktree(ctx, cloneDir, ws)
	}
	if branch, err := m.addPreferredWorktree(ctx, cloneDir, ws); err == nil {
		return branch, nil
	} else {
		fallbackBranch := syntheticPRWorktreeBranch(ws.ItemNumber)
		startRef := workspaceStartRef(ws)
		fallbackErr := runGit(
			ctx, cloneDir,
			"worktree", "add", ws.WorktreePath,
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
}

func (m *Manager) addIssueWorktree(
	ctx context.Context, cloneDir string, ws *Workspace,
) (string, error) {
	if ws.WorkspaceBranch == "" {
		if err := runGit(
			ctx, cloneDir,
			"worktree", "add", ws.WorktreePath, ws.GitHeadRef,
		); err != nil {
			return "", err
		}
		return ws.GitHeadRef, nil
	}
	startRef := workspaceStartRef(ws)
	if err := runGit(
		ctx, cloneDir,
		"worktree", "add", ws.WorktreePath,
		"-b", ws.WorkspaceBranch, startRef,
	); err != nil {
		return "", err
	}
	return ws.WorkspaceBranch, nil
}

func (m *Manager) addPreferredWorktree(
	ctx context.Context, cloneDir string, ws *Workspace,
) (string, error) {
	if err := validateLocalBranchName(
		ctx, cloneDir, ws.GitHeadRef,
	); err != nil {
		return "", err
	}

	if ws.MRHeadRepo != nil {
		err := runGit(
			ctx, cloneDir,
			"worktree", "add", ws.WorktreePath,
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
		if err := runGit(
			ctx, cloneDir,
			"worktree", "add", ws.WorktreePath,
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
			_ = runGit(
				cleanupCtx, cloneDir,
				"worktree", "remove", "--force", ws.WorktreePath,
			)
			_ = runGit(
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

	if err := runGit(
		ctx, cloneDir,
		"worktree", "add", ws.WorktreePath, ws.GitHeadRef,
	); err != nil {
		return "", err
	}

	if err := setBranchUpstream(
		ctx, ws.WorktreePath, ws.GitHeadRef,
		"origin", "refs/heads/"+ws.GitHeadRef,
	); err != nil {
		cleanupCtx, cancel := cleanupContext(ctx)
		defer cancel()
		_ = runGit(
			cleanupCtx, cloneDir,
			"worktree", "remove", "--force", ws.WorktreePath,
		)
		return "", fmt.Errorf("configure branch upstream: %w", err)
	}

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
	if err := runGit(
		ctx, worktreePath,
		"config", "branch."+branch+".remote", remote,
	); err != nil {
		return err
	}
	return runGit(
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
	if dir != "" {
		cmd.Dir = dir
	}
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
	if err := m.updateWorkspaceBranch(
		ctx, ws.ID, workspaceBranchUnknown,
	); err != nil {
		return m.failSetup(
			ctx,
			ws.ID, workspaceSetupStageSetup,
			fmt.Errorf("reset workspace branch before retry: %w", err),
		)
	}
	m.markRetryStarted(ctx, ws)
	return nil
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

func (m *Manager) markRetryStarted(ctx context.Context, ws *Workspace) {
	ws.WorkspaceBranch = workspaceBranchUnknown
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

	if m.clones == nil {
		return nil
	}

	cloneDir, err := m.clones.ClonePath(
		ws.PlatformHost, ws.RepoOwner, ws.RepoName,
	)
	if err != nil {
		return err
	}
	ready, err := gitCloneDirReady(cloneDir)
	if err != nil {
		return err
	}
	if !ready {
		return nil
	}

	return m.withRepoLock(ctx, cloneDir, func() error {
		if err := runGit(
			ctx, cloneDir,
			"worktree", "remove", "--force", ws.WorktreePath,
		); err != nil && !isGitWorktreeAbsent(err) {
			return fmt.Errorf("remove git worktree: %w", err)
		}
		if err := m.deleteWorkspaceBranchesStrict(
			ctx, cloneDir, ws, ws.WorkspaceBranch,
		); err != nil {
			return err
		}
		if err := runGit(ctx, cloneDir, "worktree", "prune"); err != nil {
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

	if m.clones == nil {
		return nil
	}

	cloneDir, err := m.clones.ClonePath(
		ws.PlatformHost, ws.RepoOwner, ws.RepoName,
	)
	if err != nil {
		return err
	}
	// If the clone is missing — manually removed, or never created
	// because Setup failed before EnsureClone returned — there is
	// nothing to clean up under the lock, and trying to acquire it
	// would fail at file open. Match the retry-path's behavior and
	// fall through to a successful no-op delete.
	ready, err := gitCloneDirReady(cloneDir)
	if err != nil {
		return err
	}
	if !ready {
		return nil
	}

	return m.withRepoLock(ctx, cloneDir, func() error {
		_ = runGit(
			ctx, cloneDir,
			"worktree", "remove", "--force", ws.WorktreePath,
		)
		m.deleteWorkspaceBranches(ctx, cloneDir, ws, ws.WorkspaceBranch)
		_ = runGit(ctx, cloneDir, "worktree", "prune")
		return nil
	})
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
	stored, err := m.db.ListWorkspaceTmuxSessions(ctx, ws.ID)
	if err != nil {
		return err
	}
	for _, storedSession := range stored {
		sessions = append(sessions, cleanupTarget{
			session: storedSession.SessionName,
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
	if err := m.db.DeleteWorkspaceTmuxSessions(ctx, ws.ID); err != nil {
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

// GetByMR returns the workspace for a specific MR, or nil.
func (m *Manager) GetByMR(
	ctx context.Context,
	platformHost, owner, name string,
	mrNumber int,
) (*Workspace, error) {
	return m.db.GetWorkspaceByMR(
		ctx, platformHost, owner, name, mrNumber,
	)
}

// GetByIssue returns the workspace for a specific issue, or nil.
func (m *Manager) GetByIssue(
	ctx context.Context,
	platformHost, owner, name string,
	issueNumber int,
) (*Workspace, error) {
	return m.db.GetWorkspaceByIssue(
		ctx, platformHost, owner, name, issueNumber,
	)
}

// GetSummary returns a workspace with joined MR metadata.
func (m *Manager) GetSummary(
	ctx context.Context, id string,
) (*WorkspaceSummary, error) {
	return m.db.GetWorkspaceSummary(ctx, id)
}

// ListSummaries returns all workspaces with joined MR metadata.
func (m *Manager) ListSummaries(
	ctx context.Context,
) ([]WorkspaceSummary, error) {
	return m.db.ListWorkspaceSummaries(ctx)
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
	storedSessions, err := m.db.ListAllWorkspaceTmuxSessions(ctx)
	if err != nil {
		return err
	}
	for _, stored := range storedSessions {
		if stored.SessionName != "" {
			live[stored.SessionName] = true
		}
	}

	sessions, err := m.listTmuxSessions(ctx)
	if err != nil {
		if isTmuxCommandUnavailable(err) {
			return nil
		}
		return err
	}
	for _, session := range sessions {
		if !isMiddlemanWorkspaceTmuxSessionName(session) {
			continue
		}
		if live[session] {
			continue
		}
		owned, err := m.tmuxSessionOwnedByThisManager(ctx, session)
		if err != nil {
			return err
		}
		if !owned {
			continue
		}
		if err := m.killTmuxSession(ctx, session); err != nil &&
			!isTmuxKillSessionGone(err) {
			return fmt.Errorf(
				"kill orphan tmux session %q: %w", session, err,
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

	storedSessions, err := m.db.ListAllWorkspaceTmuxSessions(ctx)
	if err != nil {
		return err
	}
	for _, stored := range storedSessions {
		if stored.SessionName == "" {
			continue
		}
		if live[stored.SessionName] {
			continue
		}
		slog.Debug(
			"prune missing runtime tmux session",
			"workspace_id", stored.WorkspaceID,
			"target_key", stored.TargetKey,
			"tmux_session", stored.SessionName,
		)
		if err := m.db.DeleteWorkspaceTmuxSession(
			ctx, stored.WorkspaceID, stored.SessionName,
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

func (m *Manager) tmuxSessionOwnedByThisManager(
	ctx context.Context, session string,
) (bool, error) {
	cmd := m.tmuxExec(
		ctx,
		"show-options", "-qv", "-t", session,
		"@middleman_owner",
	)
	out, err := procutil.Output(
		ctx, cmd, "tmux subprocess capacity",
	)
	if err != nil {
		if procutil.IsResourceExhausted(err) {
			return false, err
		}
		return false, nil
	}
	return strings.TrimSpace(string(out)) == m.tmuxOwnerMarker(), nil
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

func (m *Manager) listTmuxSessions(
	ctx context.Context,
) ([]string, error) {
	cmd := m.tmuxExec(ctx, "list-sessions", "-F", "#{session_name}")
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
	var sessions []string
	for line := range strings.SplitSeq(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			sessions = append(sessions, line)
		}
	}
	return sessions, nil
}

// RecordRuntimeTmuxSession records a tmux-backed runtime launch so
// activity probing and cleanup survive an application restart.
func (m *Manager) RecordRuntimeTmuxSession(
	ctx context.Context,
	workspaceID string,
	sessionName string,
	targetKey string,
	createdAt time.Time,
) error {
	if sessionName == "" {
		return nil
	}
	m.runtimeTmuxMu.Lock()
	defer m.runtimeTmuxMu.Unlock()
	return m.db.UpsertWorkspaceTmuxSession(ctx, &db.WorkspaceTmuxSession{
		WorkspaceID: workspaceID,
		SessionName: sessionName,
		TargetKey:   targetKey,
		CreatedAt:   createdAt,
	})
}

// ForgetRuntimeTmuxSession removes a stored tmux-backed runtime
// launch after an explicit stop succeeds.
func (m *Manager) ForgetRuntimeTmuxSession(
	ctx context.Context,
	workspaceID string,
	sessionName string,
) error {
	if sessionName == "" {
		return nil
	}
	m.runtimeTmuxMu.Lock()
	defer m.runtimeTmuxMu.Unlock()
	return m.db.DeleteWorkspaceTmuxSession(ctx, workspaceID, sessionName)
}

// ForgetMissingRuntimeTmuxSession removes a stored runtime tmux session only
// after tmux reports that the session no longer exists.
func (m *Manager) ForgetMissingRuntimeTmuxSession(
	ctx context.Context,
	workspaceID string,
	sessionName string,
	createdAt time.Time,
) (bool, error) {
	if sessionName == "" {
		return false, nil
	}
	m.runtimeTmuxMu.Lock()
	defer m.runtimeTmuxMu.Unlock()
	exists, err := m.tmuxSessionExists(ctx, sessionName)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	return m.db.DeleteWorkspaceTmuxSessionCreatedAt(
		ctx, workspaceID, sessionName, createdAt,
	)
}

// StopStoredRuntimeTmuxSession cleans up a persisted runtime tmux session even
// when the in-memory runtime manager no longer knows about it.
func (m *Manager) StopStoredRuntimeTmuxSession(
	ctx context.Context,
	workspaceID string,
	targetKey string,
) (bool, error) {
	if targetKey == "" {
		return false, nil
	}
	m.runtimeTmuxMu.Lock()
	defer m.runtimeTmuxMu.Unlock()
	stored, err := m.db.ListWorkspaceTmuxSessions(ctx, workspaceID)
	if err != nil {
		return false, err
	}
	for _, storedSession := range stored {
		if storedSession.TargetKey != targetKey ||
			storedSession.SessionName == "" {
			continue
		}
		if err := m.killTmuxSession(
			ctx, storedSession.SessionName,
		); err != nil && !isTmuxKillSessionGone(err) {
			return true, fmt.Errorf(
				"kill tmux session %q: %w",
				storedSession.SessionName, err,
			)
		}
		if err := m.db.DeleteWorkspaceTmuxSession(
			ctx, workspaceID, storedSession.SessionName,
		); err != nil {
			return true, err
		}
		return true, nil
	}
	return false, nil
}

// StopStoredRuntimeTmuxSessionByKey cleans up a persisted runtime tmux session
// addressed by the public localruntime session key.
func (m *Manager) StopStoredRuntimeTmuxSessionByKey(
	ctx context.Context,
	workspaceID string,
	sessionKey string,
) (bool, error) {
	if sessionKey == "" {
		return false, nil
	}
	stored, err := m.db.ListWorkspaceTmuxSessions(ctx, workspaceID)
	if err != nil {
		return false, err
	}
	for _, storedSession := range stored {
		if localruntime.SessionKey(workspaceID, storedSession.TargetKey) == sessionKey {
			return m.StopStoredRuntimeTmuxSession(
				ctx, workspaceID, storedSession.TargetKey,
			)
		}
	}
	return false, nil
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
	stored, err := m.db.ListWorkspaceTmuxSessions(ctx, workspaceID)
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
		session := storedSession.SessionName
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

// runGit executes a git command in dir and returns combined
// output on error.
func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := workspaceGitCommand(ctx, dir, args...)
	out, err := procutil.CombinedOutput(
		ctx, cmd, "git subprocess capacity",
	)
	if err != nil {
		return fmt.Errorf(
			"%w: %s", err, strings.TrimSpace(string(out)),
		)
	}
	return nil
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
	err := m.withRepoLock(cleanupCtx, cloneDir, func() error {
		if err := runGit(
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
		if err := runGit(
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
		if err := validateLocalBranchName(
			ctx, cloneDir, branch,
		); err != nil {
			return err
		}
		if err := runGit(
			ctx, cloneDir, "branch", "-D", "--", branch,
		); err != nil && !isGitBranchAbsent(err) {
			return fmt.Errorf("delete git branch %q: %w", branch, err)
		}
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
	cmd := workspaceGitCommand(
		ctx, dir, "rev-parse", "--verify", "--quiet",
		ref+"^{commit}",
	)
	out, err := procutil.CombinedOutput(
		ctx, cmd, "git subprocess capacity",
	)
	if err == nil {
		return strings.TrimSpace(string(out)), true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return "", false, nil
	}
	return "", false, fmt.Errorf(
		"%w: %s", err, strings.TrimSpace(string(out)),
	)
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
