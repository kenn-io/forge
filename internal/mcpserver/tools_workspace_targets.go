package mcpserver

import (
	"context"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type (
	workspaceScopeKey     struct{}
	workspaceTargetsInput struct {
		WorkspaceID string `json:"workspace_id,omitempty" jsonschema:"omit in Forge ACP sessions"`
	}
)

type addWorkspaceTargetInput struct {
	WorkspaceID string       `json:"workspace_id,omitempty"`
	Item        itemRefInput `json:"item"`
	URL         string       `json:"url" jsonschema:"canonical URL"`
}
type addWorkspaceKataTargetInput struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	DaemonID    string `json:"daemon_id"`
	ProjectUID  string `json:"project_uid"`
	IssueUID    string `json:"issue_uid"`
}
type removeWorkspaceTargetInput struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	Type        string `json:"type" jsonschema:"pr, issue or kata"`
	TargetID    int64  `json:"target_id"`
}
type removeWorkspaceTargetOutput struct {
	Removed bool `json:"removed"`
}

func workspaceScope(ctx context.Context, id string) (string, error) {
	id = strings.TrimSpace(id)
	if scoped, _ := ctx.Value(workspaceScopeKey{}).(string); scoped != "" {
		if id != "" && id != scoped {
			return "", errors.New("workspace_id does not match this ACP session")
		}
		id = scoped
	}
	if id == "" {
		return "", errors.New("workspace_id is required outside a Forge ACP session")
	}
	return id, nil
}

// Workspace target results use an open object schema: repository identities add
// fields during JSON marshaling that Go struct schema inference cannot see.
// A successful tracking write must not fail on additional response properties.
func (s *Server) registerWorkspaceTargetTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "kenn_forge_add_workspace_target", OutputSchema: map[string]any{"type": "object"}, Description: "Track a PR or issue right after you create it or start work on it; every PR in a stack. The workspace's own item is already tracked. A successful add needs no confirming list. Tracking never changes the workspace owner or push destination."}, wrapTool(s.addWorkspaceTarget))
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "kenn_forge_list_workspace_targets", OutputSchema: map[string]any{"type": "object"}, Description: "List this workspace's owning item and tracked PRs, issues and Kata tasks; when you do not know which PR or issue this workspace is for, call it instead of gh pr view."}, wrapTool(s.listWorkspaceTargets))
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "kenn_forge_remove_workspace_target", Description: "Remove an explicit tracking link by type and ID; never closes the item."}, wrapTool(s.removeWorkspaceTarget))
}

// Refresh the catalog at each HTTP request so adding or removing Kata configuration
// changes tools/list without restarting Forge. Tool execution also checks availability.
func (s *Server) syncKataTargetTool() {
	s.kataToolMu.Lock()
	defer s.kataToolMu.Unlock()
	available := s.backend.KataTargetsAvailable()
	if available == s.kataToolEnabled {
		return
	}
	if available {
		mcp.AddTool(s.mcp, &mcp.Tool{Name: "kenn_forge_add_workspace_kata_target", OutputSchema: map[string]any{"type": "object"}, Description: "Track a Kata task. daemon_id is Forge's Kata daemon ID from the workspace context or a listed Kata target; never guess it."}, wrapTool(s.addWorkspaceKataTarget))
	} else {
		s.mcp.RemoveTools("kenn_forge_add_workspace_kata_target")
	}
	s.kataToolEnabled = available
}

func (s *Server) addWorkspaceTarget(ctx context.Context, in addWorkspaceTargetInput) (WorkspaceTarget, error) {
	id, err := workspaceScope(ctx, in.WorkspaceID)
	if err != nil {
		return WorkspaceTarget{}, err
	}
	item, err := s.itemIdentity(ctx, in.Item, "item.")
	if err != nil {
		return WorkspaceTarget{}, err
	}
	return s.backend.AddWorkspaceTarget(ctx, id, WorkspaceTargetRequest{Item: item, URL: strings.TrimSpace(in.URL)})
}

func (s *Server) addWorkspaceKataTarget(ctx context.Context, in addWorkspaceKataTargetInput) (WorkspaceTarget, error) {
	id, err := workspaceScope(ctx, in.WorkspaceID)
	if err != nil {
		return WorkspaceTarget{}, err
	}
	return s.backend.AddWorkspaceTarget(ctx, id, WorkspaceTargetRequest{Kata: &WorkspaceKataTarget{DaemonID: in.DaemonID, ProjectUID: in.ProjectUID, IssueUID: in.IssueUID}})
}

func (s *Server) listWorkspaceTargets(ctx context.Context, in workspaceTargetsInput) (WorkspaceTargets, error) {
	id, err := workspaceScope(ctx, in.WorkspaceID)
	if err != nil {
		return WorkspaceTargets{}, err
	}
	return s.backend.ListWorkspaceTargets(ctx, id)
}

func (s *Server) removeWorkspaceTarget(ctx context.Context, in removeWorkspaceTargetInput) (removeWorkspaceTargetOutput, error) {
	id, err := workspaceScope(ctx, in.WorkspaceID)
	if err != nil {
		return removeWorkspaceTargetOutput{}, err
	}
	err = s.backend.RemoveWorkspaceTarget(ctx, id, in.Type, in.TargetID)
	return removeWorkspaceTargetOutput{Removed: err == nil}, err
}
