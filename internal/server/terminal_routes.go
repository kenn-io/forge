package server

import "github.com/danielgtaylor/huma/v2"

func (s *Server) registerTerminalAPI(api huma.API, _ []string) {
	if !s.options.ExecutionWorker {
		s.fleetAPI.RegisterTerminal(api)
		s.registerDevboxTerminalAPI(api)
	}
	if s.workspaces != nil {
		s.workspaceAPI.RegisterTerminal(api)
	}
}
