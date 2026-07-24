package server

import "github.com/danielgtaylor/huma/v2"

func terminalAPIConfig() huma.Config {
	config := huma.DefaultConfig("middleman terminal websocket", "0.1.0")
	config.OpenAPIPath = ""
	config.DocsPath = ""
	config.SchemasPath = ""
	return config
}

func (s *Server) registerTerminalAPI(api huma.API, _ []string) {
	s.fleetAPI.RegisterTerminal(api)
	s.workspaceAPI.RegisterTerminal(api)
}
