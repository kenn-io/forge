package server

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

type testACPAgentInput struct {
	Body struct {
		Command           []string `json:"command" minItems:"1" nullable:"false"`
		DisableForgeTools bool     `json:"disable_forge_tools,omitempty"`
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
	warning, err := s.runtime.TestACP(ctx, in.Body.Command, in.Body.DisableForgeTools)
	if err != nil {
		result.Valid = false
		result.Message = err.Error()
	} else if warning != "" {
		result.Message += " " + warning
	}
	return &httpapi.BodyOutput[testACPAgentResult]{Body: result}, nil
}

type probeACPOptionsInput struct {
	Body struct {
		TargetKey  string            `json:"target_key" minLength:"1"`
		ACPOptions map[string]string `json:"acp_options,omitempty" doc:"Options to apply before reading the offered ones, keyed by option ID."`
	}
}

type probeACPOptionsResult struct {
	Options []localruntime.ACPConfigOption `json:"options" nullable:"false"`
}

// probeACPOptions reports the session options a configured ACP agent offers
// on this host after the given ones apply, so quick action settings can offer
// real choices. Options that depend on the model reflect the chosen model.
func (s *Server) probeACPOptions(ctx context.Context, in *probeACPOptionsInput) (*httpapi.BodyOutput[probeACPOptionsResult], error) {
	if s.runtime == nil {
		return nil, httpapi.ServiceUnavailable("workspace runtime is unavailable on this host")
	}
	targetKey := strings.ToLower(strings.TrimSpace(in.Body.TargetKey))
	if !slices.ContainsFunc(s.runtime.LaunchTargets(), func(target localruntime.LaunchTarget) bool {
		return target.Key == targetKey && target.Kind == localruntime.LaunchTargetACP
	}) {
		return nil, httpapi.Validation("body.target_key", "target_key is not a configured ACP agent")
	}
	options, err := config.NormalizeACPOptions(in.Body.ACPOptions)
	if err != nil {
		return nil, httpapi.Validation("body.acp_options", err.Error())
	}
	offered, err := s.runtime.ProbeACPOptions(ctx, targetKey, options)
	if err != nil {
		return nil, httpapi.NewProblem(http.StatusBadGateway, httpapi.CodeUpstreamError, "read ACP options: "+err.Error(), nil)
	}
	if offered == nil {
		offered = []localruntime.ACPConfigOption{}
	}
	return &httpapi.BodyOutput[probeACPOptionsResult]{Body: probeACPOptionsResult{Options: offered}}, nil
}
