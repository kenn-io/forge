package localruntime

import acpsdk "github.com/coder/acp-go-sdk"

func (m *Manager) agentMCPServers() []acpsdk.McpServer {
	if m.agentMCPURL == "" {
		return []acpsdk.McpServer{}
	}
	return []acpsdk.McpServer{{Http: &acpsdk.McpServerHttpInline{
		Name: "kenn-forge", Url: m.agentMCPURL,
		Headers: []acpsdk.HttpHeader{{Name: "Authorization", Value: "Bearer " + m.agentMCPToken}},
	}}}
}
