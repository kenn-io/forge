package localruntime

import (
	"maps"

	acpsdk "github.com/coder/acp-go-sdk"
	"go.kenn.io/forge/internal/runtimelock"
)

func (m *Manager) agentMCPServers() []acpsdk.McpServer {
	if m.agentMCPURL == "" {
		return []acpsdk.McpServer{}
	}
	return []acpsdk.McpServer{{Http: &acpsdk.McpServerHttpInline{
		Name: "kenn-forge", Url: m.agentMCPURL,
		Headers: []acpsdk.HttpHeader{{Name: "Authorization", Value: "Bearer " + m.agentMCPToken}},
	}}}
}

// terminalAgentEnv adds the companion bearer to a terminal agent's
// configured environment under the name Forge's MCP client configuration
// expands, as ACP agents receive it in their MCP headers. Tmux agent panes
// drop the daemon's environment, so a variable exported there never
// arrives. A value the agent configures itself wins.
func (m *Manager) terminalAgentEnv(target LaunchTarget) map[string]string {
	if m.terminalAgentMCPToken == "" || target.DisableForgeTools {
		return target.Env
	}
	env := map[string]string{runtimelock.AuthTokenEnv: m.terminalAgentMCPToken}
	maps.Copy(env, target.Env)
	return env
}
