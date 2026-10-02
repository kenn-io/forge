package mcpserver

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listAgentTargetsInput struct{}

type agentTargetRow struct {
	Key            string `json:"key"`
	Label          string `json:"label"`
	Protocol       string `json:"protocol"`
	Source         string `json:"source"`
	Available      bool   `json:"available"`
	DisabledReason string `json:"disabled_reason,omitempty"`
	configOrder    int
}

type listAgentTargetsOutput struct {
	Targets []agentTargetRow `json:"targets"`
}

type listWorkspaceAgentSessionsInput struct {
	WorkspaceID string `json:"workspace_id" jsonschema:"persisted Kenn Forge workspace ID"`
}

type sendAgentMessageInput struct {
	WorkspaceID       string `json:"workspace_id" jsonschema:"persisted Kenn Forge workspace ID"`
	RuntimeSessionKey string `json:"runtime_session_key" jsonschema:"live agent runtime session key"`
	Message           string `json:"message" jsonschema:"message to submit to the running coding agent"`
}

type sendAgentMessageOutput struct {
	WorkspaceID       string `json:"workspace_id"`
	RuntimeSessionKey string `json:"runtime_session_key"`
	TargetKey         string `json:"target_key"`
	MessageBytes      int    `json:"message_bytes"`
	SubmittedAt       string `json:"submitted_at"`
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
	Runtimes []workspaceAgentRuntimeRow `json:"runtimes"`
	Sessions []workspaceAgentSessionRow `json:"sessions"`
}

type workspaceAgentRuntimeRow struct {
	RuntimeSessionKey string `json:"runtime_session_key"`
	TargetKey         string `json:"target_key"`
	Protocol          string `json:"protocol"`
	Status            string `json:"status"`
	CreatedAt         string `json:"created_at"`
	HookObserved      bool   `json:"hook_observed"`
}

func (s *Server) registerAgentTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "kenn_forge_list_agent_targets",
		Meta: mcp.Meta{"ui": map[string]any{"visibility": []string{"model"}}},
		Description: "List configured coding-agent launch targets, including custom targets. " +
			"Unavailable targets remain visible, but command arguments are never returned. " +
			"protocol is terminal for hook-reporting terminal agents and acp for Agent Client Protocol chat agents; " +
			"both can be launched and messaged through these tools.",
	}, wrapTool(s.listAgentTargets))
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "kenn_forge_list_workspace_agent_sessions",
		Meta: mcp.Meta{"ui": map[string]any{"visibility": []string{"model"}}},
		Description: "List live agent runtimes and their fresh coding sessions for one workspace. " +
			"Terminal agents report sessions through hooks; ACP agents report through their ACP connection with agent=acp. " +
			"A runtime with hook_observed=false has launched but has not reported its first session. This is a live projection, not session history.",
	}, wrapTool(s.listWorkspaceAgentSessions))
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "kenn_forge_send_agent_message",
		Meta: mcp.Meta{"ui": map[string]any{"visibility": []string{"model"}}},
		Description: "Submit a follow-up message to one existing live coding-agent runtime. " +
			"Use the workspace ID and runtime session key returned by Forge. " +
			"An ACP agent in the middle of a turn queues the message and runs it after the turn ends.",
	}, wrapTool(s.sendAgentMessage))
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "kenn_forge_spawn_workspace_with_agent",
		Meta: mcp.Meta{"ui": map[string]any{"visibility": []string{"model"}}},
		Description: "Create or reuse a workspace, launch one configured coding agent, submit exactly one initial message, " +
			"and observe the resulting coding session. When agent_target is omitted for a new handoff, the most used " +
			"available agent from the previous 14 days is selected. Resume can continue an existing runtime without " +
			"launching another agent. Partial resources are never cleaned up automatically.",
	}, wrapTool(s.spawnWorkspaceWithAgent))
}

func (s *Server) sendAgentMessage(
	ctx context.Context,
	in sendAgentMessageInput,
) (sendAgentMessageOutput, error) {
	workspaceID := strings.TrimSpace(in.WorkspaceID)
	runtimeSessionKey := strings.TrimSpace(in.RuntimeSessionKey)
	runtime, err := s.backend.GetWorkspaceRuntime(ctx, workspaceID)
	if err != nil {
		return sendAgentMessageOutput{}, err
	}
	liveAgent := slices.ContainsFunc(runtime.Sessions, func(session RuntimeSession) bool {
		return session.Key == runtimeSessionKey && isLiveAgentRuntime(session)
	})
	if !liveAgent {
		return sendAgentMessageOutput{}, errors.New("runtime session is not a live coding agent")
	}
	result, err := s.backend.SubmitAgentMessage(ctx, AgentMessageRequest{
		WorkspaceID: workspaceID, RuntimeSessionKey: runtimeSessionKey, Message: in.Message,
	})
	if err != nil {
		return sendAgentMessageOutput{}, err
	}
	return sendAgentMessageOutput{
		WorkspaceID: workspaceID, RuntimeSessionKey: runtimeSessionKey,
		TargetKey: result.TargetKey, MessageBytes: result.MessageBytes,
		SubmittedAt: formatMCPTime(result.SubmittedAt),
	}, nil
}

func (s *Server) listAgentTargets(
	ctx context.Context,
	_ listAgentTargetsInput,
) (listAgentTargetsOutput, error) {
	targets, err := s.backend.ListLaunchTargets(ctx)
	if err != nil {
		return listAgentTargetsOutput{}, err
	}
	out := listAgentTargetsOutput{Targets: make([]agentTargetRow, 0)}
	for index, target := range targets {
		key := strings.ToLower(strings.TrimSpace(target.Key))
		protocol, ok := agentProtocol(target.Kind)
		if !ok {
			continue
		}
		out.Targets = append(out.Targets, agentTargetRow{
			Key:            key,
			Label:          target.Label,
			Protocol:       protocol,
			Source:         target.Source,
			Available:      target.Available,
			DisabledReason: target.DisabledReason,
			configOrder:    index,
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
	runtime, err := s.backend.GetWorkspaceRuntime(ctx, workspaceID)
	if err != nil {
		return listWorkspaceAgentSessionsOutput{}, err
	}
	sessions, err := s.backend.ListWorkspaceAgentSessions(ctx, workspaceID)
	if err != nil {
		return listWorkspaceAgentSessionsOutput{}, err
	}
	slices.SortFunc(sessions, compareWorkspaceAgentSessions)
	out := listWorkspaceAgentSessionsOutput{
		Runtimes: make([]workspaceAgentRuntimeRow, 0),
		Sessions: make([]workspaceAgentSessionRow, 0, len(sessions)),
	}
	observedRuntimeKeys := make(map[string]struct{}, len(sessions))
	for _, session := range sessions {
		observedRuntimeKeys[session.RuntimeSessionKey] = struct{}{}
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
	for _, session := range runtime.Sessions {
		if !isLiveAgentRuntime(session) {
			continue
		}
		_, hookObserved := observedRuntimeKeys[session.Key]
		protocol, _ := agentProtocol(session.Kind)
		out.Runtimes = append(out.Runtimes, workspaceAgentRuntimeRow{
			RuntimeSessionKey: session.Key,
			TargetKey:         session.TargetKey,
			Protocol:          protocol,
			Status:            session.Status,
			CreatedAt:         formatMCPTime(session.CreatedAt),
			HookObserved:      hookObserved,
		})
	}
	slices.SortFunc(out.Runtimes, func(a, b workspaceAgentRuntimeRow) int {
		if order := strings.Compare(a.CreatedAt, b.CreatedAt); order != 0 {
			return order
		}
		return strings.Compare(a.RuntimeSessionKey, b.RuntimeSessionKey)
	})
	return out, nil
}

// agentProtocol maps a runtime launch kind to the MCP agent protocol. Terminal
// agents are driven through their PTY; ACP agents through their chat session.
func agentProtocol(kind string) (string, bool) {
	switch kind {
	case "agent":
		return "terminal", true
	case "acp":
		return "acp", true
	}
	return "", false
}

func isLiveAgentRuntime(session RuntimeSession) bool {
	_, agent := agentProtocol(session.Kind)
	return agent && (session.Status == "starting" || session.Status == "running")
}

func compareWorkspaceAgentSessions(a, b WorkspaceAgentSession) int {
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
