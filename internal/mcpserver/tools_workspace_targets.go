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
		WorkspaceID string `json:"workspace_id,omitempty" jsonschema:"workspace ID from launch context; omit in a Forge ACP session"`
	}
)

type addWorkspaceTargetInput struct {
	WorkspaceID string       `json:"workspace_id,omitempty"`
	Item        itemRefInput `json:"item"`
	URL         string       `json:"url" jsonschema:"full canonical provider URL"`
}
type addWorkspaceKataTargetInput struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	DaemonID    string `json:"daemon_id"`
	ProjectUID  string `json:"project_uid"`
	IssueUID    string `json:"issue_uid"`
}
type removeWorkspaceTargetInput struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	Type        string `json:"type" jsonschema:"target type from the list: pr, issue or kata"`
	TargetID    int64  `json:"target_id" jsonschema:"explicit target ID from kenn_forge_list_workspace_targets"`
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

func (s *Server) registerWorkspaceTargetTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "kenn_forge_add_workspace_target", Description: "Track a PR or issue in this workspace immediately after creating it or starting work on it. Register every PR in a stack. Use the verified item identity from Forge reads and its canonical URL. ACP supplies the workspace automatically; other clients pass workspace_id. Multiple targets are supported and repeated additions are safe. Tracking never changes the workspace owner, branch, push destination, or lifecycle."}, wrapTool(s.addWorkspaceTarget))
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "kenn_forge_list_workspace_targets", Description: "List this workspace's owning item, branch-associated PR and explicitly tracked PRs, issues and Kata tasks with current cached state. Check this before finishing to find missing links. Unavailable items remain listed. Kata availability is reported separately."}, wrapTool(s.listWorkspaceTargets))
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "kenn_forge_remove_workspace_target", Description: "Remove an explicit tracking link using its type and ID from the target list. This does not delete or close the target, change the workspace owner, or remove an implicit owner or branch association."}, wrapTool(s.removeWorkspaceTarget))
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
		mcp.AddTool(s.mcp, &mcp.Tool{Name: "kenn_forge_add_workspace_kata_target", Description: "Track a Kata task in the current workspace using its daemon, project and issue identity. Available only with a configured remote Kata daemon or a discovered configured local daemon. Repeated additions are safe. Kata retains ownership of the task."}, wrapTool(s.addWorkspaceKataTarget))
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
	item, err := in.Item.itemIdentity()
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
	if !s.backend.KataTargetsAvailable() {
		return WorkspaceTarget{}, errors.New("kata target linking is unavailable")
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
