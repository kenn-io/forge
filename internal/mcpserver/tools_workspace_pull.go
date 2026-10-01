package mcpserver

import (
	"context"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type workspaceScopeKey struct{}

type linkWorkspacePullInput struct {
	WorkspaceID string `json:"workspace_id,omitempty" jsonschema:"workspace ID from generated launch context; omit in a Forge ACP session to use its bound workspace"`
	Number      int    `json:"number" jsonschema:"PR or merge request number in this workspace's repository"`
	URL         string `json:"url" jsonschema:"full canonical PR URL returned by the provider"`
}

type linkWorkspacePullOutput struct {
	WorkspaceID   string `json:"workspace_id"`
	Number        int    `json:"number"`
	URL           string `json:"url"`
	AlreadyLinked bool   `json:"already_linked"`
}

func (s *Server) registerWorkspacePullTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "kenn_forge_link_workspace_pull_request",
		Description: "Register the PR created or worked on in this workspace. Call immediately after creating the PR through a provider CLI or API. " +
			"Forge ACP sessions supply the workspace automatically; other MCP clients pass workspace_id from launch context. " +
			"Supports one PR in the workspace's repository, verifies its URL, and never replaces an existing link. Repeating the same link succeeds. " +
			"A missing PR is synced when possible. This records local attribution, not a provider mutation.",
	}, wrapTool(s.linkWorkspacePull))
}

func (s *Server) linkWorkspacePull(ctx context.Context, in linkWorkspacePullInput) (linkWorkspacePullOutput, error) {
	id := strings.TrimSpace(in.WorkspaceID)
	if scoped, _ := ctx.Value(workspaceScopeKey{}).(string); scoped != "" {
		if id != "" && id != scoped {
			return linkWorkspacePullOutput{}, errors.New("workspace_id does not match this ACP session")
		}
		id = scoped
	}
	if id == "" || in.Number <= 0 || strings.TrimSpace(in.URL) == "" {
		return linkWorkspacePullOutput{}, errors.New("workspace_id, a positive PR number, and its URL are required")
	}
	url := strings.TrimSpace(in.URL)
	alreadyLinked, err := s.backend.LinkWorkspacePullRequest(ctx, id, in.Number, url)
	if err != nil {
		return linkWorkspacePullOutput{}, err
	}
	return linkWorkspacePullOutput{WorkspaceID: id, Number: in.Number, URL: url, AlreadyLinked: alreadyLinked}, nil
}
