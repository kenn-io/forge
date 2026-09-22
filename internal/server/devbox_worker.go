package server

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/devbox"
	"go.kenn.io/forge/internal/fleet"
	"go.kenn.io/forge/internal/server/httpapi"
)

type workerIdentity = devbox.WorkerIdentity

func (s *Server) registerWorkerAPI(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "get-execution-worker", Method: http.MethodGet, Path: "/worker",
		Tags: []string{"Devboxes"}, Summary: "Get authenticated execution worker identity",
	}, func(context.Context, *struct{}) (*httpapi.BodyOutput[workerIdentity], error) {
		return &httpapi.BodyOutput[workerIdentity]{Body: workerIdentity{
			NodeID: s.options.FederationSpokeID, UID: s.cfg.ExecutionWorker.UID,
			GitHubUserID: s.cfg.ExecutionWorker.GitHubUserID, Role: "execution", Protocol: 1,
		}}, nil
	})
	huma.Get(api, "/worker/snapshot", func(ctx context.Context, _ *struct{}) (*httpapi.BodyOutput[fleet.RawSnapshot], error) {
		raw, err := s.fleetAPI.LocalSnapshot(ctx)
		if err != nil {
			return nil, httpapi.Internal(err.Error())
		}
		return &httpapi.BodyOutput[fleet.RawSnapshot]{Body: raw}, nil
	}, httpapi.DocumentOperation("get-worker-snapshot", "Read this account's execution inventory", "Devboxes"))
	s.workspaceAPI.RegisterWorker(api)
}
