package workspace

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/providerplane"
)

const (
	WorkspaceLaunchSpecVersion          = db.WorkspaceLaunchSpecVersion
	WorkspaceLaunchSpecMigrationVersion = db.WorkspaceLaunchSpecMigrationVersion
	WorkspaceLaunchSpecVisibilityLease  = db.WorkspaceLaunchSpecVisibilityLease
)

var (
	ErrLaunchSpecRefreshRequired = db.ErrLaunchSpecRefreshRequired
	ErrLaunchSpecSourceHidden    = db.ErrLaunchSpecSourceHidden
	ErrLaunchSpecResolverMissing = errors.New("workspace launch specification resolver is not configured")
)

type WorkspaceLaunchRepository = db.WorkspaceLaunchRepository
type WorkspaceLaunchPull = db.WorkspaceLaunchPull
type WorkspaceLaunchSpec = db.WorkspaceLaunchSpec
type UnpreparedWorkspace = db.UnpreparedWorkspace

// LaunchSpecRefreshError reports an expired or missing provider-fact lease
// whose hub refresh could not complete. It remains identifiable as
// ErrLaunchSpecRefreshRequired while preserving the underlying cause.
type LaunchSpecRefreshError struct {
	Cause error
}

func (e *LaunchSpecRefreshError) Error() string {
	if e == nil || e.Cause == nil {
		return ErrLaunchSpecRefreshRequired.Error()
	}
	return fmt.Sprintf("%s: %v", ErrLaunchSpecRefreshRequired, e.Cause)
}

func (e *LaunchSpecRefreshError) Unwrap() []error {
	if e == nil || e.Cause == nil {
		return []error{ErrLaunchSpecRefreshRequired}
	}
	return []error{ErrLaunchSpecRefreshRequired, e.Cause}
}

// LaunchSpecErrorRetryable reports whether retrying after hub
// recovery can make the validation succeed.
func LaunchSpecErrorRetryable(err error) bool {
	var refresh *LaunchSpecRefreshError
	return errors.As(err, &refresh)
}

func (m *Manager) launchSpecNow() time.Time {
	if m != nil && m.now != nil {
		return m.now().UTC()
	}
	return time.Now().UTC()
}

func providerBackedWorkspace(workspace *Workspace) bool {
	return workspace != nil &&
		(workspace.ItemType == db.WorkspaceItemTypePullRequest ||
			workspace.ItemType == db.WorkspaceItemTypeIssue)
}

func validateLaunchSpecForCreation(spec WorkspaceLaunchSpec) error {
	return providerplane.ValidateWorkspaceLaunchSpecResponse(
		providerplane.WorkspaceLaunchRequest{
			Repository: providerplane.RepositoryRoute{
				Provider:     spec.Repository.Provider,
				PlatformHost: spec.Repository.PlatformHost,
				Owner:        spec.Repository.Owner,
				Name:         spec.Repository.Name,
			},
			ItemType: spec.ItemType, ItemNumber: spec.ItemNumber,
			ItemKey: spec.ItemKey, GitHeadRef: spec.GitHeadRef,
		},
		spec,
	)
}

// RequireWorkspaceLaunchSpec validates the persisted provider facts for one
// lifecycle operation and renews an expired visibility lease through the
// configured hub. It is the lease-checking seam used by setup and
// provider-backed Git operations.
func (m *Manager) RequireWorkspaceLaunchSpec(
	ctx context.Context, workspace *Workspace,
) (*WorkspaceLaunchSpec, error) {
	if workspace == nil {
		return nil, ErrWorkspaceNotFound
	}
	if !providerBackedWorkspace(workspace) {
		return nil, nil
	}
	if m == nil || m.db == nil {
		return nil, &LaunchSpecRefreshError{Cause: ErrLaunchSpecResolverMissing}
	}
	if workspace.RepoID == 0 {
		return nil, ErrWorkspaceRepositoryUnresolved
	}
	spec, err := m.db.GetWorkspaceLaunchSpec(ctx, workspace.ID)
	if err != nil {
		return nil, fmt.Errorf("read workspace launch specification: %w", err)
	}
	if spec == nil {
		spec, err = m.refreshWorkspaceLaunchSpec(
			ctx, workspace, missingLaunchSpecSeed(*workspace),
		)
		if err != nil {
			return nil, err
		}
	}
	routeChanged, err := m.validateWorkspaceLaunchSpec(ctx, workspace, *spec)
	if err != nil {
		return nil, err
	}
	if routeChanged {
		spec, err = m.refreshWorkspaceLaunchSpec(ctx, workspace, *spec)
		if err != nil {
			return nil, err
		}
	}
	visibilityErr := spec.RequireVisible(m.launchSpecNow())
	switch {
	case visibilityErr == nil:
		// Continue below and project the persisted trust facts.
	case errors.Is(visibilityErr, ErrLaunchSpecSourceHidden):
		return nil, visibilityErr
	case errors.Is(visibilityErr, ErrLaunchSpecRefreshRequired):
		spec, err = m.refreshWorkspaceLaunchSpec(ctx, workspace, *spec)
		if err != nil {
			return nil, err
		}
	default:
		return nil, visibilityErr
	}
	if _, err := m.validateWorkspaceLaunchSpec(ctx, workspace, *spec); err != nil {
		return nil, err
	}
	if err := validateLaunchSpecForCreation(*spec); err != nil {
		return nil, fmt.Errorf("validate workspace launch Git identity: %w", err)
	}
	if err := m.applyWorkspaceLaunchSpec(ctx, workspace, *spec); err != nil {
		return nil, err
	}
	return spec, nil
}

func (m *Manager) validateWorkspaceLaunchSpec(
	ctx context.Context, workspace *Workspace, spec WorkspaceLaunchSpec,
) (bool, error) {
	if workspace.RepoID == 0 {
		return false, ErrWorkspaceRepositoryUnresolved
	}
	repo, err := m.db.GetActiveRepoByID(ctx, workspace.RepoID)
	if err != nil {
		return false, fmt.Errorf("resolve workspace launch repository: %w", err)
	}
	if repo == nil ||
		!strings.EqualFold(repo.Platform, spec.Repository.Provider) ||
		!strings.EqualFold(repo.PlatformHost, spec.Repository.PlatformHost) ||
		strings.TrimSpace(repo.PlatformRepoID) != strings.TrimSpace(spec.Repository.PlatformRepoID) {
		return false, fmt.Errorf(
			"%w: workspace launch specification repository identity changed",
			db.ErrRepositoryRouteFenceChanged,
		)
	}
	routeChanged := !strings.EqualFold(repo.Owner, spec.Repository.Owner) ||
		!strings.EqualFold(repo.Name, spec.Repository.Name)
	comparison := *workspace
	comparison.Platform = spec.Repository.Provider
	comparison.PlatformHost = spec.Repository.PlatformHost
	comparison.RepoOwner = spec.Repository.Owner
	comparison.RepoName = spec.Repository.Name
	if err := spec.ValidateWorkspace(comparison); err != nil {
		return false, fmt.Errorf("validate workspace launch specification: %w", err)
	}
	return routeChanged, nil
}

// RefreshWorkspaceLaunchSpec forces a hub refresh even while the
// current visibility lease remains valid. Manual workspace refresh uses this
// to update provider facts instead of treating a valid lease as fresh data.
func (m *Manager) RefreshWorkspaceLaunchSpec(
	ctx context.Context, workspace *Workspace,
) (*WorkspaceLaunchSpec, error) {
	if workspace == nil {
		return nil, ErrWorkspaceNotFound
	}
	if !providerBackedWorkspace(workspace) {
		return nil, nil
	}
	if m == nil || m.db == nil {
		return nil, &LaunchSpecRefreshError{Cause: ErrLaunchSpecResolverMissing}
	}
	current, err := m.db.GetWorkspaceLaunchSpec(ctx, workspace.ID)
	if err != nil {
		return nil, fmt.Errorf("read workspace launch specification: %w", err)
	}
	if current == nil {
		seed := missingLaunchSpecSeed(*workspace)
		current = &seed
	}
	refreshed, err := m.refreshWorkspaceLaunchSpec(ctx, workspace, *current)
	if err != nil {
		return nil, err
	}
	if err := m.applyWorkspaceLaunchSpec(ctx, workspace, *refreshed); err != nil {
		return nil, err
	}
	return refreshed, nil
}

func (m *Manager) refreshWorkspaceLaunchSpec(
	ctx context.Context, workspace *Workspace, current WorkspaceLaunchSpec,
) (*WorkspaceLaunchSpec, error) {
	if m.launchSpecResolver == nil {
		return nil, &LaunchSpecRefreshError{Cause: ErrLaunchSpecResolverMissing}
	}
	refreshed, err := m.launchSpecResolver.RefreshWorkspaceLaunchSpec(ctx, current)
	if err != nil {
		return nil, &LaunchSpecRefreshError{Cause: err}
	}
	if err := validateLaunchSpecForCreation(refreshed); err != nil {
		return nil, fmt.Errorf("validate refreshed workspace launch Git identity: %w", err)
	}
	if err := refreshed.RequireVisible(m.launchSpecNow()); err != nil {
		return nil, err
	}
	updatedWorkspace, err := m.db.PutRefreshedWorkspaceLaunchSpec(
		ctx, workspace.ID, refreshed,
	)
	if err != nil {
		return nil, fmt.Errorf("persist refreshed workspace launch specification: %w", err)
	}
	*workspace = *updatedWorkspace
	return &refreshed, nil
}

func missingLaunchSpecSeed(workspace Workspace) WorkspaceLaunchSpec {
	return WorkspaceLaunchSpec{
		Version: WorkspaceLaunchSpecVersion,
		Repository: WorkspaceLaunchRepository{
			Provider: workspace.Platform, PlatformHost: workspace.PlatformHost,
			Owner: workspace.RepoOwner, Name: workspace.RepoName,
		},
		ItemType: workspace.ItemType, ItemNumber: workspace.ItemNumber,
		ItemKey: workspace.ItemKey, GitHeadRef: workspace.GitHeadRef,
	}
}

func (m *Manager) applyWorkspaceLaunchSpec(
	ctx context.Context, workspace *Workspace, spec WorkspaceLaunchSpec,
) error {
	if workspace.ItemType != db.WorkspaceItemTypePullRequest {
		return nil
	}
	headRepo := workspaceHeadRepoFromLaunchSpec(spec)
	if sameStringPointer(workspace.MRHeadRepo, headRepo) {
		return nil
	}
	if err := m.db.UpdateWorkspaceMRHeadRepo(ctx, workspace.ID, headRepo); err != nil {
		return fmt.Errorf("persist workspace launch head repository: %w", err)
	}
	workspace.MRHeadRepo = headRepo
	return nil
}

func workspaceHeadRepoFromLaunchSpec(spec WorkspaceLaunchSpec) *string {
	if spec.Pull == nil || spec.Pull.HeadRepoKind == "same_repo" {
		return nil
	}
	value := ""
	if spec.Pull.HeadRepoKind == "fork" {
		value = spec.Pull.HeadRepoCloneURL
	}
	return &value
}

func sameStringPointer(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func launchRequest(
	provider, platformHost, owner, name, itemType string,
	itemNumber int,
	gitHeadRef string,
	issueBranchSlug bool,
) providerplane.WorkspaceLaunchRequest {
	return providerplane.WorkspaceLaunchRequest{
		Repository: providerplane.RepositoryRoute{
			Provider: provider, PlatformHost: platformHost,
			Owner: owner, Name: name,
		},
		ItemType: itemType, ItemNumber: itemNumber,
		ItemKey: strconv.Itoa(itemNumber), GitHeadRef: strings.TrimSpace(gitHeadRef),
		IssueBranchSlug: issueBranchSlug,
	}
}

func launchSpecSummary(workspace Workspace, spec WorkspaceLaunchSpec) WorkspaceSummary {
	summary := WorkspaceSummary{Workspace: workspace}
	summary.RepoPlatformID = spec.Repository.PlatformRepoID
	summary.SourceItemVisible = spec.SourceVisible
	if spec.SourceTitle != "" {
		summary.SourceTitle = &spec.SourceTitle
	}
	if spec.SourceURL != "" {
		summary.SourceURL = &spec.SourceURL
	}
	if spec.Pull != nil {
		headBranch := spec.Pull.HeadBranch
		summary.MRHeadBranch = &headBranch
		summary.MRHeadRepo = workspaceHeadRepoFromLaunchSpec(spec)
		if spec.SourceTitle != "" {
			summary.MRTitle = &spec.SourceTitle
		}
	}
	return summary
}

func (m *Manager) lifecycleSummary(
	ctx context.Context, workspace *Workspace,
) (*WorkspaceSummary, error) {
	if workspace == nil {
		return nil, nil
	}
	summary := WorkspaceSummary{Workspace: *workspace}
	summary.SourceItemVisible = true
	summary.AssociatedPRVisible = workspace.AssociatedPRNumber != nil
	kataMetadata := workspace.KataMetadata
	if workspace.ItemType == db.WorkspaceItemTypeKataTask &&
		kataMetadata != nil && strings.TrimSpace(kataMetadata.Title) != "" {
		title := kataMetadata.Title
		summary.SourceTitle = &title
		summary.MRTitle = &title
	}
	if !providerBackedWorkspace(workspace) {
		return &summary, nil
	}
	if m == nil || m.db == nil {
		return nil, errors.New("workspace launch specification store is unavailable")
	}
	spec, err := m.db.GetWorkspaceLaunchSpec(ctx, workspace.ID)
	if err != nil {
		return nil, fmt.Errorf("read workspace launch specification: %w", err)
	}
	if spec == nil {
		return &summary, nil
	}
	if _, err := m.validateWorkspaceLaunchSpec(ctx, workspace, *spec); err != nil {
		return nil, err
	}
	if err := validateLaunchSpecForCreation(*spec); err != nil {
		return nil, fmt.Errorf("validate workspace launch Git identity: %w", err)
	}
	projected := launchSpecSummary(*workspace, *spec)
	projected.AssociatedPRVisible = summary.AssociatedPRVisible
	return &projected, nil
}
