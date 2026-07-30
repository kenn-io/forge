package server

import (
	"context"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/server/httpapi"
)

type healthOutput = httpapi.BodyOutput[healthResponse]

type healthResponse struct {
	Status string `json:"status"`
}

func healthAPIConfig() huma.Config {
	config := huma.DefaultConfig("kenn-forge health", "0.1.0")
	config.OpenAPIPath = ""
	config.DocsPath = ""
	config.SchemasPath = ""
	config.Servers = nil
	return config
}

func (s *Server) registerHealthAPI(api huma.API) {
	huma.Get(api, "/healthz", s.healthz)
	huma.Get(api, "/livez", s.livez)
}

func (s *Server) livez(_ context.Context, _ *struct{}) (*healthOutput, error) {
	return &healthOutput{
		Body: healthResponse{Status: "ok"},
	}, nil
}

func (s *Server) healthz(ctx context.Context, _ *struct{}) (*healthOutput, error) {
	if s.db == nil {
		return nil, httpapi.ServiceUnavailable("database unavailable")
	}

	var probe int
	if err := s.db.ReadDB().QueryRowContext(ctx, "SELECT 1").Scan(&probe); err != nil {
		return nil, httpapi.ServiceUnavailable("database unavailable")
	}

	return &healthOutput{
		Body: healthResponse{Status: "ok"},
	}, nil
}
