package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kballard/go-shellquote"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/devbox"
	"go.kenn.io/forge/internal/procutil"
	managed "go.kenn.io/kit/git/managed"
	"go.kenn.io/kwt/worktree"
)

var ErrExecutionIdentity = errors.New("execution worker commit identity is not configured correctly")

func (m *Manager) SetExecutionWorker(worker config.ExecutionWorker, executable string) {
	m.executionWorker = worker
	m.credentialHelper = "!" + shellquote.Join(executable, "devbox", "credential", "--socket", worker.BrokerSocket)
}

func (m *Manager) applyExecutionSource(ctx context.Context, summary *WorkspaceSummary) error {
	if !m.executionWorker.Enabled || summary == nil || summary.ItemType == db.WorkspaceItemTypeAdHoc {
		return nil
	}
	spec, err := m.db.GetWorkspaceLaunchSpec(ctx, summary.ID)
	if err != nil {
		return err
	}
	if spec == nil {
		return nil
	}
	summary.SourceItemVisible = spec.SourceVisible
	summary.SourceTitle, summary.SourceURL = new(spec.SourceTitle), new(spec.SourceURL)
	if spec.Pull != nil {
		summary.MRTitle, summary.MRHeadBranch = new(spec.SourceTitle), new(spec.Pull.HeadBranch)
	}
	return nil
}

func (m *Manager) configureExecutionWorktree(ctx context.Context, dir, remote string) error {
	if !m.executionWorker.Enabled {
		return nil
	}
	commonDir, err := worktreeCommonGitDir(ctx, dir)
	if err != nil {
		return err
	}
	return m.withRepoLockForGitDir(ctx, commonDir, func(scope *worktree.Scope) error {
		for _, args := range [][]string{
			{"config", "--local", "user.useConfigOnly", "true"},
			{"config", "--local", "user.name", m.executionWorker.CommitName},
			{"config", "--local", "user.email", m.executionWorker.CommitEmail},
			{"config", "--local", "credential.useHttpPath", "true"},
			{"config", "--local", "--replace-all", "credential.helper", ""},
			{"config", "--local", "--add", "credential.helper", m.credentialHelper},
		} {
			if _, err := scope.RunGit(ctx, dir, args...); err != nil {
				return fmt.Errorf("configure execution worktree: %w", err)
			}
		}
		if err := m.ValidateExecutionIdentity(ctx, dir); err != nil {
			return err
		}
		branch, err := gitOutput(ctx, dir, "branch", "--show-current")
		if err != nil {
			return err
		}
		branch = strings.TrimSpace(branch)
		if branch != "" {
			tracked, _ := gitConfigValue(ctx, dir, "branch."+branch+".remote")
			if strings.TrimSpace(tracked) == "" {
				// A newly created development branch can be pushed from Forge before
				// it exists remotely. It tracks the workspace's own remote, which a
				// local worktree base may name something other than origin.
				// Existing tracking choices remain intact.
				return scope.SetUpstream(ctx, managed.WorktreeUpstreamOptions{Path: dir, Policy: managed.UpstreamPolicy{Action: managed.UpstreamTrack, Scope: managed.UpstreamRepository, Remote: remote, Ref: "refs/heads/" + branch}})
			}
		}
		return nil
	})
}

func (m *Manager) ValidateExecutionIdentity(ctx context.Context, dir string) error {
	if !m.executionWorker.Enabled {
		return nil
	}
	for _, setting := range []struct{ key, value string }{
		{"user.name", m.executionWorker.CommitName},
		{"user.email", m.executionWorker.CommitEmail},
		{"user.useConfigOnly", "true"},
	} {
		out, err := procutil.Output(ctx, workspaceGitCommand(ctx, dir, "config", "--get", setting.key), "git subprocess capacity")
		if err != nil || strings.TrimSpace(string(out)) != setting.value {
			return fmt.Errorf("%w: %s must match the enrolled developer", ErrExecutionIdentity, setting.key)
		}
	}
	return validateNoExecutableLocalGitConfig(ctx, dir, m.credentialHelper)
}

// ExecutionPushState observes the local branch and upstream without a network mutation.
func (m *Manager) ExecutionPushState(ctx context.Context, summary *WorkspaceSummary) *devbox.PushState {
	if !m.executionWorker.Enabled || summary == nil || summary.Status != "ready" {
		return nil
	}
	upstream, err := currentBranchUpstream(ctx, summary.WorktreePath)
	if err != nil || !m.executionPushRemote(ctx, summary, upstream.remote) {
		return nil
	}
	out, err := gitOutput(ctx, summary.WorktreePath, "rev-parse", "HEAD", "@{upstream}")
	if err != nil {
		return nil
	}
	refs := strings.Fields(out)
	if len(refs) != 2 {
		return nil
	}
	return &devbox.PushState{Repository: summary.RepoOwner + "/" + summary.RepoName, Branch: upstream.branch, OID: refs[0], Pushed: refs[0] == refs[1]}
}

// executionPushRemote reports whether pushes to remote go to the workspace's
// repository. A local worktree base can name that remote something other than
// origin; it then counts only when its fetch and push URLs all match the
// repository identity, since Git pushes to remote.<name>.pushurl when set.
func (m *Manager) executionPushRemote(ctx context.Context, summary *WorkspaceSummary, remote string) bool {
	if remote == originRemoteName {
		return true
	}
	insecure := m.allowsInsecureHTTP(summary.Platform, summary.PlatformHost)
	if validateBaseRemoteURLs(
		ctx, summary.WorktreePath, remote, summary.Platform, summary.PlatformHost,
		summary.RepoOwner, summary.RepoName, insecure,
	) != nil {
		return false
	}
	pushURLs, err := gitConfigValues(ctx, summary.WorktreePath, "remote."+remote+".pushurl")
	if err != nil {
		return false
	}
	for _, pushURL := range pushURLs {
		if validateBaseRemoteURL(
			pushURL, remote, summary.Platform, summary.PlatformHost,
			summary.RepoOwner, summary.RepoName, insecure,
		) != nil {
			return false
		}
	}
	return true
}
