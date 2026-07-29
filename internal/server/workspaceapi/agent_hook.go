package workspaceapi

import (
	"context"
	"log/slog"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/kit/agenthook"
	"go.kenn.io/middleman/internal/agentactivity"
)

type receiveAgentHookInput struct {
	Agent             string                  `path:"agent"`
	RuntimeSessionKey string                  `header:"X-Middleman-Runtime-Session-Key"`
	Body              agentactivity.HookEvent `json:"body"`
}

type agentHookResponse struct {
	HookOutput *agentHookOutput `json:"hook_output,omitempty"`
}

type agentHookOutput struct {
	HookSpecificOutput agentHookSpecificOutput `json:"hookSpecificOutput"`
}

type agentHookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

type receiveAgentHookOutput struct {
	Body agentHookResponse
}

func (s *Handler) receiveAgentHook(
	ctx context.Context, input *receiveAgentHookInput,
) (*receiveAgentHookOutput, error) {
	integration, err := agenthook.ParseAgent(input.Agent)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	if err := s.agentActivity.HandleEvent(input.Body, input.RuntimeSessionKey); err != nil {
		slog.Warn("record agent hook activity", "err", err)
	}

	output := &receiveAgentHookOutput{}
	if integration != agenthook.AgentClaude ||
		input.Body.HookEventName != "SessionStart" || s.workspaces == nil {
		return output, nil
	}
	startContext, err := s.workspaces.RenderAgentContextForWorktree(ctx, input.Body.CWD)
	if err != nil {
		slog.Warn("render agent hook workspace context", "err", err)
		return output, nil
	}
	if strings.TrimSpace(startContext) == "" {
		return output, nil
	}
	output.Body.HookOutput = &agentHookOutput{
		HookSpecificOutput: agentHookSpecificOutput{
			HookEventName:     "SessionStart",
			AdditionalContext: startContext,
		},
	}
	return output, nil
}
