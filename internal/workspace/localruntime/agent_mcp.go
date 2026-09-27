package localruntime

type ACPMCPHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
type ACPMCPServer struct {
	Type    string         `json:"type"`
	Name    string         `json:"name"`
	URL     string         `json:"url"`
	Headers []ACPMCPHeader `json:"headers"`
}

func (m *Manager) agentMCPServers() []ACPMCPServer {
	if m.agentMCPURL == "" {
		return nil
	}
	return []ACPMCPServer{{Type: "http", Name: "kenn-forge", URL: m.agentMCPURL, Headers: []ACPMCPHeader{{Name: "Authorization", Value: "Bearer " + m.agentMCPToken}}}}
}
