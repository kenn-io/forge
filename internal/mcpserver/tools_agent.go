package mcpserver

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.kenn.io/kit/agenthook"
)

type listAgentTargetsInput struct{}

type agentTargetRow struct {
	Key            string `json:"key"`
	Label          string `json:"label"`
	Source         string `json:"source"`
	Available      bool   `json:"available"`
	DisabledReason string `json:"disabled_reason,omitempty"`
}

type listAgentTargetsOutput struct {
	Targets []agentTargetRow `json:"targets"`
}

type daemonLaunchTarget struct {
	Key            string   `json:"key"`
	Label          string   `json:"label"`
	Kind           string   `json:"kind"`
	Source         string   `json:"source"`
	Command        []string `json:"command"`
	Available      bool     `json:"available"`
	DisabledReason string   `json:"disabled_reason"`
}

type daemonSettings struct {
	LaunchTargets []daemonLaunchTarget `json:"launch_targets"`
}

type listWorkspaceAgentSessionsInput struct {
	WorkspaceID string `json:"workspace_id" jsonschema:"persisted Kenn Forge workspace ID"`
}

type agentInitialMessageRow struct {
	State        string `json:"state"`
	MessageBytes int    `json:"message_bytes"`
	DeliveredAt  string `json:"delivered_at,omitempty"`
}

type workspaceAgentSessionRow struct {
	Agent             string                  `json:"agent"`
	SessionID         string                  `json:"session_id"`
	RuntimeSessionKey string                  `json:"runtime_session_key"`
	TargetKey         string                  `json:"target_key"`
	State             string                  `json:"state"`
	UpdatedAt         string                  `json:"updated_at"`
	InitialMessage    *agentInitialMessageRow `json:"initial_message,omitempty"`
}

type listWorkspaceAgentSessionsOutput struct {
	Sessions []workspaceAgentSessionRow `json:"sessions"`
}

type daemonAgentInitialMessage struct {
	State        string     `json:"state"`
	MessageBytes int        `json:"message_bytes"`
	DeliveredAt  *time.Time `json:"delivered_at"`
}

type daemonWorkspaceAgentSession struct {
	Agent             string                     `json:"agent"`
	SessionID         string                     `json:"session_id"`
	RuntimeSessionKey string                     `json:"runtime_session_key"`
	TargetKey         string                     `json:"target_key"`
	State             string                     `json:"state"`
	UpdatedAt         time.Time                  `json:"updated_at"`
	InitialMessage    *daemonAgentInitialMessage `json:"initial_message"`
}

type daemonWorkspaceAgentSessions struct {
	Sessions []daemonWorkspaceAgentSession `json:"sessions"`
}

func (s *Server) registerAgentTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "kenn_forge_list_agent_targets",
		Description: "List configured launch targets that can report supported coding-agent hook sessions. " +
			"Unavailable targets remain visible, but command arguments are never returned.",
	}, wrapTool(s.listAgentTargets))
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "kenn_forge_list_workspace_agent_sessions",
		Description: "List fresh hook-authoritative coding sessions joined to live agent runtimes for one workspace. " +
			"This is a live projection, not session history.",
	}, wrapTool(s.listWorkspaceAgentSessions))
}

func (s *Server) listAgentTargets(
	ctx context.Context,
	_ listAgentTargetsInput,
) (listAgentTargetsOutput, error) {
	var settings daemonSettings
	if err := s.daemon.getJSON(ctx, "/api/v1/settings", nil, &settings); err != nil {
		return listAgentTargetsOutput{}, err
	}
	supported := make(map[string]struct{})
	for _, profile := range agenthook.Profiles() {
		supported[string(profile.Agent)] = struct{}{}
	}
	out := listAgentTargetsOutput{Targets: make([]agentTargetRow, 0)}
	for _, target := range settings.LaunchTargets {
		key := strings.ToLower(strings.TrimSpace(target.Key))
		if target.Kind != "agent" {
			continue
		}
		if _, ok := supported[key]; !ok {
			continue
		}
		out.Targets = append(out.Targets, agentTargetRow{
			Key:            key,
			Label:          target.Label,
			Source:         target.Source,
			Available:      target.Available,
			DisabledReason: target.DisabledReason,
		})
	}
	slices.SortFunc(out.Targets, func(a, b agentTargetRow) int {
		return strings.Compare(a.Key, b.Key)
	})
	return out, nil
}

func (s *Server) listWorkspaceAgentSessions(
	ctx context.Context,
	in listWorkspaceAgentSessionsInput,
) (listWorkspaceAgentSessionsOutput, error) {
	workspaceID := strings.TrimSpace(in.WorkspaceID)
	if workspaceID == "" {
		return listWorkspaceAgentSessionsOutput{}, fmt.Errorf("workspace_id is required")
	}
	var response daemonWorkspaceAgentSessions
	if err := s.daemon.getJSON(
		ctx, "/api/v1/workspaces/"+seg(workspaceID)+"/agent-sessions", nil, &response,
	); err != nil {
		return listWorkspaceAgentSessionsOutput{}, err
	}
	slices.SortFunc(response.Sessions, compareDaemonAgentSessions)
	out := listWorkspaceAgentSessionsOutput{
		Sessions: make([]workspaceAgentSessionRow, 0, len(response.Sessions)),
	}
	for _, session := range response.Sessions {
		row := workspaceAgentSessionRow{
			Agent:             session.Agent,
			SessionID:         session.SessionID,
			RuntimeSessionKey: session.RuntimeSessionKey,
			TargetKey:         session.TargetKey,
			State:             session.State,
			UpdatedAt:         formatMCPTime(session.UpdatedAt),
		}
		if session.InitialMessage != nil {
			row.InitialMessage = &agentInitialMessageRow{
				State:        session.InitialMessage.State,
				MessageBytes: session.InitialMessage.MessageBytes,
			}
			if session.InitialMessage.DeliveredAt != nil {
				row.InitialMessage.DeliveredAt = formatMCPTime(
					*session.InitialMessage.DeliveredAt,
				)
			}
		}
		out.Sessions = append(out.Sessions, row)
	}
	return out, nil
}

func compareDaemonAgentSessions(a, b daemonWorkspaceAgentSession) int {
	if order := b.UpdatedAt.Compare(a.UpdatedAt); order != 0 {
		return order
	}
	if order := strings.Compare(a.Agent, b.Agent); order != 0 {
		return order
	}
	if order := strings.Compare(a.SessionID, b.SessionID); order != 0 {
		return order
	}
	return strings.Compare(a.RuntimeSessionKey, b.RuntimeSessionKey)
}
