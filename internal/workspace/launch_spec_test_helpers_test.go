package workspace

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/providerplane"
)

type databaseLaunchSpecResolver struct {
	db  *db.DB
	now func() time.Time
}

func (r databaseLaunchSpecResolver) ResolveWorkspaceLaunchSpec(
	ctx context.Context, request providerplane.WorkspaceLaunchRequest,
) (db.WorkspaceLaunchSpec, error) {
	var repo *db.Repo
	var err error
	if strings.TrimSpace(request.PlatformRepoID) != "" {
		entry, lookupErr := r.db.GetRepositoryByProviderID(
			ctx, request.Repository.Provider, request.Repository.PlatformHost,
			request.PlatformRepoID,
		)
		err = lookupErr
		if entry != nil {
			resolved := entry.Repository
			repo = &resolved
		}
	} else {
		repo, err = r.db.GetRepoByIdentity(ctx, db.RepoIdentity{
			Platform: request.Repository.Provider, PlatformHost: request.Repository.PlatformHost,
			Owner: request.Repository.Owner, Name: request.Repository.Name,
		})
	}
	if err != nil {
		return db.WorkspaceLaunchSpec{}, err
	}
	if repo == nil {
		return db.WorkspaceLaunchSpec{}, fmt.Errorf("%w: repository not tracked", ErrWorkspaceNotFound)
	}
	now := time.Now().UTC()
	if r.now != nil {
		now = r.now().UTC()
	}
	cloneURL := strings.TrimSpace(repo.CloneURL)
	if cloneURL == "" {
		cloneURL = "https://" + repo.PlatformHost + "/" + repo.Owner + "/" + repo.Name + ".git"
	}
	defaultBranch := strings.TrimSpace(repo.DefaultBranch)
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	spec := db.WorkspaceLaunchSpec{
		Version: db.WorkspaceLaunchSpecVersion,
		Repository: db.WorkspaceLaunchRepository{
			Provider: repo.Platform, PlatformHost: repo.PlatformHost,
			PlatformRepoID: repo.PlatformRepoID, Owner: repo.Owner, Name: repo.Name,
			CloneURL: cloneURL, DefaultBranch: defaultBranch,
		},
		ItemType: request.ItemType, ItemNumber: request.ItemNumber,
		ItemKey: request.ItemKey, GitHeadRef: strings.TrimSpace(request.GitHeadRef),
		IssuedAt: now, SourceVisibleUntil: now.Add(db.WorkspaceLaunchSpecVisibilityLease),
	}
	if spec.ItemKey == "" {
		spec.ItemKey = strconv.Itoa(spec.ItemNumber)
	}
	switch spec.ItemType {
	case db.WorkspaceItemTypePullRequest:
		pull, err := r.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, spec.ItemNumber)
		if err != nil {
			return db.WorkspaceLaunchSpec{}, err
		}
		if pull == nil {
			return db.WorkspaceLaunchSpec{}, fmt.Errorf(
				"%w: merge request %d", ErrWorkspaceNotSynced, spec.ItemNumber,
			)
		}
		visible, err := r.db.GetVisibleMergeRequestByRepoIDAndNumber(ctx, repo.ID, spec.ItemNumber)
		if err != nil {
			return db.WorkspaceLaunchSpec{}, err
		}
		spec.SourceVisible = visible != nil
		if spec.GitHeadRef == "" {
			spec.GitHeadRef = pull.HeadBranch
		}
		headRepo := WorkspaceHeadRepo(
			repo.Platform, repo.PlatformHost, repo.Owner, repo.Name,
			pull.HeadRepoCloneURL,
		)
		spec.Pull = &db.WorkspaceLaunchPull{
			HeadBranch: pull.HeadBranch, SnapshotRevision: pull.SnapshotRevision,
			HeadRepoKind: "same_repo",
		}
		if headRepo != nil && *headRepo == "" {
			spec.Pull.HeadRepoKind = "unknown"
		} else if headRepo != nil {
			spec.Pull.HeadRepoKind = "fork"
			spec.Pull.HeadRepoCloneURL = *headRepo
		}
	case db.WorkspaceItemTypeIssue:
		issue, err := r.db.GetIssueByRepoIDAndNumber(ctx, repo.ID, spec.ItemNumber)
		if err != nil {
			return db.WorkspaceLaunchSpec{}, err
		}
		if issue == nil {
			return db.WorkspaceLaunchSpec{}, fmt.Errorf("issue %d not synced yet", spec.ItemNumber)
		}
		visible, err := r.db.GetVisibleIssueByRepoIDAndNumber(ctx, repo.ID, spec.ItemNumber)
		if err != nil {
			return db.WorkspaceLaunchSpec{}, err
		}
		spec.SourceVisible = visible != nil
		if spec.GitHeadRef == "" {
			spec.GitHeadRef = IssueWorkspaceBranch(
				spec.ItemNumber, issue.Title, request.IssueBranchSlug,
			)
		}
	default:
		return db.WorkspaceLaunchSpec{}, fmt.Errorf("unsupported item type %q", spec.ItemType)
	}
	return spec, spec.Validate()
}

func (r databaseLaunchSpecResolver) RefreshWorkspaceLaunchSpec(
	ctx context.Context, current db.WorkspaceLaunchSpec,
) (db.WorkspaceLaunchSpec, error) {
	return r.ResolveWorkspaceLaunchSpec(ctx, providerplane.WorkspaceLaunchRequest{
		Repository: providerplane.RepositoryRoute{
			Provider: current.Repository.Provider, PlatformHost: current.Repository.PlatformHost,
			Owner: current.Repository.Owner, Name: current.Repository.Name,
		},
		PlatformRepoID: current.Repository.PlatformRepoID,
		ItemType:       current.ItemType, ItemNumber: current.ItemNumber,
		ItemKey: current.ItemKey, GitHeadRef: current.GitHeadRef,
	})
}

func newTestManager(t testing.TB, database *db.DB, worktreeDir string) *Manager {
	t.Helper()
	manager := NewManager(database, worktreeDir)
	if database != nil {
		manager.SetLaunchSpecResolver(databaseLaunchSpecResolver{db: database})
	}
	return manager
}
