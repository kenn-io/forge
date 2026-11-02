package server

import (
	"context"
	"errors"
	"net/http"

	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/spokeapi"
	"go.kenn.io/forge/internal/workspace"
)

// hubWorkspacePullStack reads agent-context stacks from the hub, because a
// spoke's database holds no PR or stack rows. The hub answers 404 for a PR
// outside any stack.
func hubWorkspacePullStack(source *spokeapi.HubProviderSource) workspace.PullStackSource {
	return func(
		ctx context.Context, ws workspace.WorkspaceSummary, number int,
	) (*workspace.AgentStackContext, error) {
		stack, err := source.GetPullStack(ctx, mcpserver.ItemIdentity{
			Provider: ws.Platform, PlatformHost: ws.PlatformHost,
			Owner: ws.RepoOwner, Name: ws.RepoName, Number: number,
		})
		if problem, ok := errors.AsType[*httpapi.ProblemError](err); ok &&
			problem.Status == http.StatusNotFound {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		result := &workspace.AgentStackContext{Native: stack.Native}
		for _, member := range stack.Members {
			result.Members = append(result.Members, workspace.AgentStackMember{
				Number: member.Number, State: member.State, Draft: member.IsDraft,
				BaseBranch: member.BaseBranch,
			})
		}
		return result, nil
	}
}
