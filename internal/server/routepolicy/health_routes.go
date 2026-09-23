package routepolicy

import (
	"context"
	"runtime/debug"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/server/httpapi"
)

type healthOutput = httpapi.BodyOutput[HealthResponse]

type HealthResponse struct {
	Status   string `json:"status"`
	Version  string `json:"version"`
	Revision string `json:"revision"`
	Modified bool   `json:"modified"`
}

func (s *Handlers) healthyResponse() HealthResponse {
	version, commit := "", ""
	if s.BuildVersion != nil {
		version = s.BuildVersion()
	}
	if s.BuildCommit != nil {
		commit = s.BuildCommit()
	}
	return HealthyResponse(version, commit)
}

func HealthyResponse(version, commit string) HealthResponse {
	build, _ := debug.ReadBuildInfo()
	return HealthResponseForBuild(version, commit, build)
}

func HealthResponseForBuild(version, commit string, build *debug.BuildInfo) HealthResponse {
	response := HealthResponse{Status: "ok", Version: version, Revision: commit}
	if build != nil {
		for _, setting := range build.Settings {
			switch setting.Key {
			case "vcs.revision":
				response.Revision = setting.Value
			case "vcs.modified":
				response.Modified = setting.Value == "true"
			}
		}
	}
	return response
}

func HealthAPIConfig() huma.Config {
	config := huma.DefaultConfig("kenn-forge health", "0.1.0")
	config.OpenAPIPath = ""
	config.DocsPath = ""
	config.SchemasPath = ""
	config.Servers = nil
	return config
}

func (s *Handlers) RegisterHealthAPI(api huma.API) {
	huma.Get(api, "/healthz", s.healthz)
	huma.Get(api, "/livez", s.livez)
}

func (s *Handlers) livez(_ context.Context, _ *struct{}) (*healthOutput, error) {
	return &healthOutput{
		Body: s.healthyResponse(),
	}, nil
}

func (s *Handlers) healthz(ctx context.Context, _ *struct{}) (*healthOutput, error) {
	if s.Db == nil {
		return nil, httpapi.ServiceUnavailable("database unavailable")
	}

	var probe int
	if err := s.Db.ReadDB().QueryRowContext(ctx, "SELECT 1").Scan(&probe); err != nil {
		return nil, httpapi.ServiceUnavailable("database unavailable")
	}

	return &healthOutput{
		Body: s.healthyResponse(),
	}, nil
}
