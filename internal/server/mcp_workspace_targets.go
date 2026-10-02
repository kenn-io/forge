package server

import (
	"context"

	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/internal/server/mcpapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/platform"
)

func (b mcpBackend) KataTargetsAvailable() bool {
	return b.server.workspaceAPI != nil && b.server.workspaceAPI.KataTargetsAvailable()
}

func (b mcpBackend) AddWorkspaceTarget(ctx context.Context, id string, in mcpserver.WorkspaceTargetRequest) (mcpserver.WorkspaceTarget, error) {
	request := workspaceapi.WorkspaceTargetInput{Type: in.Item.Type, Number: in.Item.Number, URL: in.URL}
	if in.Kata != nil {
		request.Type = "kata"
		request.Kata = &workspaceapi.WorkspaceKataTarget{DaemonID: in.Kata.DaemonID, ProjectUID: in.Kata.ProjectUID, IssueUID: in.Kata.IssueUID}
	} else {
		repo, err := b.resolveWorkspaceRepository(ctx, mcpapi.ItemRepositoryIdentity(in.Item))
		if err != nil {
			return mcpserver.WorkspaceTarget{}, err
		}
		request.Repository = &platform.RepositoryIdentity{Provider: repo.Repo.Platform, PlatformHost: repo.Repo.PlatformHost, Key: repo.Repo.Key}
	}
	target, err := b.server.workspaceAPI.AddWorkspaceTargetService(ctx, id, request)
	if err != nil {
		return mcpserver.WorkspaceTarget{}, mcpapi.McpBackendMutationError(err)
	}
	return mcpWorkspaceTarget(target), nil
}

func (b mcpBackend) ListWorkspaceTargets(ctx context.Context, id string) (mcpserver.WorkspaceTargets, error) {
	result, err := b.server.workspaceAPI.ListWorkspaceTargetsService(ctx, id)
	if err != nil {
		return mcpserver.WorkspaceTargets{}, mcpapi.McpBackendError(err)
	}
	out := mcpserver.WorkspaceTargets{Targets: make([]mcpserver.WorkspaceTarget, 0, len(result.Targets)), KataAvailable: result.KataAvailable}
	for _, target := range result.Targets {
		out.Targets = append(out.Targets, mcpWorkspaceTarget(target))
	}
	return out, nil
}

func (b mcpBackend) RemoveWorkspaceTarget(ctx context.Context, id, kind string, targetID int64) error {
	return mcpapi.McpBackendMutationError(b.server.workspaceAPI.RemoveWorkspaceTargetService(ctx, id, kind, targetID))
}

func mcpWorkspaceTarget(target workspaceapi.WorkspaceTarget) mcpserver.WorkspaceTarget {
	out := mcpserver.WorkspaceTarget{ID: target.ID, Type: target.Type, Number: target.Number, URL: target.URL, Title: target.Title, State: target.State, Source: target.Source, Unavailable: target.Unavailable}
	if target.Repo != nil {
		repo := mcpapi.RepositoryIdentityFromResponse(*target.Repo)
		out.Repository = &repo
	}
	if target.Kata != nil {
		out.Kata = &mcpserver.WorkspaceKataTarget{DaemonID: target.Kata.DaemonID, ProjectUID: target.Kata.ProjectUID, IssueUID: target.Kata.IssueUID, Reference: target.Kata.Reference}
	}
	return out
}
