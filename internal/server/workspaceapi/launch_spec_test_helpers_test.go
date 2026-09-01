package workspaceapi

import (
	"context"
	"strconv"
	"time"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/workspace"
)

type stubLaunchSpecResolver struct {
	resolve           func(context.Context, providerplane.WorkspaceLaunchRequest) (db.WorkspaceLaunchSpec, error)
	refresh           func(context.Context, db.WorkspaceLaunchSpec) (db.WorkspaceLaunchSpec, error)
	mergeRequestFacts *MergeRequestWorktreeFacts
}

func (r stubLaunchSpecResolver) ResolveWorkspaceLaunchSpec(
	ctx context.Context, request providerplane.WorkspaceLaunchRequest,
) (db.WorkspaceLaunchSpec, error) {
	if r.resolve != nil {
		return r.resolve(ctx, request)
	}
	return workspaceLaunchSpecForRequest(request, time.Now().UTC()), nil
}

func (r stubLaunchSpecResolver) RefreshWorkspaceLaunchSpec(
	ctx context.Context, current db.WorkspaceLaunchSpec,
) (db.WorkspaceLaunchSpec, error) {
	if r.refresh != nil {
		return r.refresh(ctx, current)
	}
	request := providerplane.WorkspaceLaunchRequest{
		Repository: providerplane.RepositoryRoute{
			Provider: current.Repository.Provider, PlatformHost: current.Repository.PlatformHost,
			Owner: current.Repository.Owner, Name: current.Repository.Name,
		},
		ItemType: current.ItemType, ItemNumber: current.ItemNumber,
		ItemKey: current.ItemKey, GitHeadRef: current.GitHeadRef,
	}
	return workspaceLaunchSpecForRequest(request, time.Now().UTC()), nil
}

func (r stubLaunchSpecResolver) ResolveMergeRequestWorktreeFacts(
	context.Context, providerplane.RepositoryRoute, int,
) (MergeRequestWorktreeFacts, error) {
	if r.mergeRequestFacts == nil {
		return MergeRequestWorktreeFacts{}, providerplane.ErrHubUnavailable
	}
	return *r.mergeRequestFacts, nil
}

func workspaceLaunchSpecForRequest(
	request providerplane.WorkspaceLaunchRequest, issuedAt time.Time,
) db.WorkspaceLaunchSpec {
	gitHeadRef := request.GitHeadRef
	if gitHeadRef == "" {
		if request.ItemType == db.WorkspaceItemTypeIssue {
			gitHeadRef = workspace.IssueWorkspaceBranch(
				request.ItemNumber, "Test issue", request.IssueBranchSlug,
			)
		} else {
			gitHeadRef = "feature"
		}
	}
	spec := db.WorkspaceLaunchSpec{
		Version: db.WorkspaceLaunchSpecVersion,
		Repository: db.WorkspaceLaunchRepository{
			Provider:       request.Repository.Provider,
			PlatformHost:   request.Repository.PlatformHost,
			PlatformRepoID: "repo-" + request.Repository.Owner + "-" + request.Repository.Name,
			Owner:          request.Repository.Owner, Name: request.Repository.Name,
			CloneURL: "https://" + request.Repository.PlatformHost + "/" +
				request.Repository.Owner + "/" + request.Repository.Name + ".git",
			DefaultBranch: "main",
		},
		ItemType: request.ItemType, ItemNumber: request.ItemNumber,
		ItemKey: strconv.Itoa(request.ItemNumber), GitHeadRef: gitHeadRef,
		SourceVisible: true, IssuedAt: issuedAt,
		SourceVisibleUntil: issuedAt.Add(db.WorkspaceLaunchSpecVisibilityLease),
	}
	if request.ItemType == db.WorkspaceItemTypePullRequest {
		spec.Pull = &db.WorkspaceLaunchPull{
			HeadBranch: "feature", HeadRepoKind: "same_repo", SnapshotRevision: 1,
		}
	}
	return spec
}
