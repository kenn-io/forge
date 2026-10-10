package server

import (
	"context"
	"encoding/json/v2"
	"io"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/server/devboxapi"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
)

func (s *Server) registerDevboxTargetAPI(api huma.API) {
	huma.Get(api, "/devboxes/{connection_id}/workspaces/{id}/targets", s.listDevboxWorkspaceTargets,
		httpapi.DocumentOperation("list-devbox-workspace-targets", "Read worker targets with controller metadata", "Devboxes"))
	huma.Put(api, "/devboxes/{connection_id}/workspaces/{id}/targets", s.updateDevboxWorkspaceTarget,
		httpapi.DocumentOperation("update-devbox-workspace-target", "Update a target on its owning devbox", "Devboxes"))
}

func (s *Server) updateDevboxWorkspaceTarget(ctx context.Context, in *struct {
	ConnectionID string `path:"connection_id"`
	ID           string `path:"id"`
	Body         workspaceapi.WorkspaceTargetSelection
},
) (*struct{}, error) {
	connections, err := s.devboxController()
	if err != nil {
		return nil, err
	}
	client, err := connections.WorkerClient(in.ConnectionID)
	if err != nil {
		return nil, err
	}
	identity := in.Body.Repository
	if !identity.Valid() {
		return nil, httpapi.Validation("body.repository", "verified repository identity is required")
	}
	request := workspaceapi.WorkerWorkspaceTargetRequest{
		Repository: httpapi.RepoRefResponse{Provider: identity.Provider, PlatformHost: identity.PlatformHost, Key: identity.Key},
		Type:       in.Body.Type, Number: in.Body.Number, Hidden: in.Body.Hidden,
	}
	if !in.Body.Hidden {
		repo, metadata, err := s.workspaceAPI.ResolveWorkspaceTargetService(ctx, in.Body)
		if err != nil {
			return nil, err
		}
		request.Repository = httpapi.RepoRefResponse{Provider: repo.Platform, PlatformHost: repo.PlatformHost, Key: repo.Key, Owner: repo.Owner, Name: repo.Name, RepoPath: repo.RepoPath}
		request.URL = metadata.URL
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, httpapi.Internal("encode worker target failed")
	}
	var body generated.UpdateWorkerWorkspaceTargetBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, httpapi.Internal("encode worker target failed")
	}
	response, err := client.HTTP.UpdateWorkerWorkspaceTargetRaw(ctx, client.Transport, &generated.UpdateWorkerWorkspaceTargetRequestOptions{
		PathParams: &generated.UpdateWorkerWorkspaceTargetPath{ID: in.ID}, Body: &body,
	})
	if err != nil {
		return nil, httpapi.ServiceUnavailable(err.Error())
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return nil, devboxapi.DevboxResponseProblem(response)
	}
	return nil, nil
}

func (s *Server) listDevboxWorkspaceTargets(ctx context.Context, in *struct {
	ConnectionID string `path:"connection_id"`
	ID           string `path:"id"`
},
) (*httpapi.BodyOutput[workspaceapi.WorkspaceTargetsResponse], error) {
	connections, err := s.devboxController()
	if err != nil {
		return nil, err
	}
	client, err := connections.WorkerClient(in.ConnectionID)
	if err != nil {
		return nil, err
	}
	response, err := client.HTTP.ListWorkspaceTargetsRaw(ctx, client.Transport, &generated.ListWorkspaceTargetsRequestOptions{PathParams: &generated.ListWorkspaceTargetsPath{ID: in.ID}})
	if err != nil {
		return nil, httpapi.ServiceUnavailable(err.Error())
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, devboxapi.DevboxResponseProblem(response)
	}
	var targets workspaceapi.WorkspaceTargetsResponse
	if err := json.UnmarshalRead(io.LimitReader(response.Body, 20<<20), &targets); err != nil {
		return nil, httpapi.Internal("read worker targets failed")
	}
	for i := range targets.Targets {
		target := &targets.Targets[i]
		if target.Repo == nil || target.Type == "kata" {
			continue
		}
		repo, metadata, err := s.workspaceAPI.ResolveWorkspaceTargetService(ctx, workspaceapi.WorkspaceTargetSelection{Repository: target.Repo.Identity(), Type: target.Type, Number: target.Number})
		target.Unavailable = err != nil
		if err != nil {
			continue
		}
		target.Repo = &httpapi.RepoRefResponse{Provider: repo.Platform, PlatformHost: repo.PlatformHost, Key: repo.Key, Owner: repo.Owner, Name: repo.Name, RepoPath: repo.RepoPath}
		target.URL, target.Title, target.State = metadata.URL, metadata.Title, metadata.State
	}
	return &httpapi.BodyOutput[workspaceapi.WorkspaceTargetsResponse]{Body: targets}, nil
}
