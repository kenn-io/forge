package server

import (
	"context"

	"go.kenn.io/forge/internal/server/httpapi"
)

type testACPAgentInput struct {
	Body struct {
		Command []string `json:"command" minItems:"1" nullable:"false"`
	}
}

type testACPAgentResult struct {
	Valid   bool   `json:"valid"`
	Message string `json:"message"`
}

func (s *Server) testACPAgent(ctx context.Context, in *testACPAgentInput) (*httpapi.BodyOutput[testACPAgentResult], error) {
	result := testACPAgentResult{Valid: true, Message: "ACP connection verified on this host."}
	if s.runtime == nil {
		return &httpapi.BodyOutput[testACPAgentResult]{Body: testACPAgentResult{Message: "Workspace runtime is unavailable on this host."}}, nil
	}
	if err := s.runtime.TestACP(ctx, in.Body.Command); err != nil {
		result.Valid = false
		result.Message = err.Error()
	}
	return &httpapi.BodyOutput[testACPAgentResult]{Body: result}, nil
}
