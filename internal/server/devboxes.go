package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/devbox"
	"go.kenn.io/forge/internal/fleet"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/devboxapi"
	"go.kenn.io/forge/internal/server/fleetapi"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/routepolicy"
	"go.kenn.io/forge/internal/server/syncevents"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/terminalwebsocket"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

func (s *Server) devboxController() (*devbox.Connections, error) {
	if s.options.Devboxes == nil || (s.cfg != nil && s.cfg.Fleet.RoleOrDefault() == config.FleetRoleSpoke) {
		return nil, httpapi.ServiceUnavailable("add devboxes on your Forge hub or standalone Forge")
	}
	return s.options.Devboxes, nil
}

func (s *Server) registryURL(requested string) string {
	if requested != "" {
		return requested
	}
	if s.cfg != nil && s.cfg.Devboxes.RegistryURL != "" {
		return s.cfg.Devboxes.RegistryURL
	}
	if s.options.Devboxes != nil {
		return s.options.Devboxes.RegistryURL()
	}
	return ""
}

func (s *Server) registerDevboxAPI(api huma.API) {
	huma.Get(api, "/devboxes", func(ctx context.Context, _ *struct{}) (*httpapi.BodyOutput[[]devbox.Connection], error) {
		connections, err := s.devboxController()
		if err != nil {
			return nil, err
		}
		return &httpapi.BodyOutput[[]devbox.Connection]{Body: connections.List()}, nil
	}, httpapi.DocumentOperation("list-devbox-connections", "List connected devboxes", "Devboxes"))
	huma.Get(api, "/devboxes/discovery", func(ctx context.Context, input *struct {
		Registry string `query:"registry"`
	},
	) (*httpapi.BodyOutput[devbox.Discovery], error) {
		connections, err := s.devboxController()
		if err != nil {
			return nil, err
		}
		discovery, err := connections.Discover(ctx, s.registryURL(input.Registry))
		if err != nil {
			return nil, httpapi.ServiceUnavailable(err.Error())
		}
		return &httpapi.BodyOutput[devbox.Discovery]{Body: discovery}, nil
	}, httpapi.DocumentOperation("discover-devboxes", "Discover your assigned devboxes", "Devboxes"))
	huma.Post(api, "/devboxes", func(ctx context.Context, input *struct {
		Body struct {
			Registry string `json:"registry"`
			HostID   string `json:"host_id"`
		}
	},
	) (*httpapi.BodyOutput[devbox.Connection], error) {
		connections, err := s.devboxController()
		if err != nil {
			return nil, err
		}
		connection, err := connections.Connect(ctx, s.registryURL(input.Body.Registry), input.Body.HostID)
		if err != nil {
			return nil, httpapi.Conflict(httpapi.CodeConflict, err.Error(), nil)
		}
		s.hub.Broadcast(syncevents.Event{Type: "data_changed", Data: struct{}{}})
		return &httpapi.BodyOutput[devbox.Connection]{Body: connection}, nil
	}, httpapi.DocumentOperation("connect-devbox", "Connect your assigned devbox account", "Devboxes"))
	huma.Delete(api, "/devboxes/{connection_id}", func(ctx context.Context, input *struct {
		ConnectionID string `path:"connection_id"`
	},
	) (*struct{}, error) {
		connections, err := s.devboxController()
		if err != nil {
			return nil, err
		}
		if err := connections.Remove(input.ConnectionID); err != nil {
			return nil, httpapi.Internal(err.Error())
		}
		return nil, nil
	}, httpapi.DocumentOperation("disconnect-devbox", "Remove a connection without deleting remote work", "Devboxes"))
	huma.Post(api, "/devboxes/{connection_id}/reconnect", func(ctx context.Context, input *struct {
		ConnectionID string `path:"connection_id"`
	},
	) (*struct{}, error) {
		connections, err := s.devboxController()
		if err != nil {
			return nil, err
		}
		if err := connections.Refresh(ctx, input.ConnectionID); err != nil {
			return nil, httpapi.Conflict(httpapi.CodeConflict, err.Error(), nil)
		}
		return nil, nil
	}, httpapi.DocumentOperation("reconnect-devbox", "Refresh an assigned worker credential and verify identity", "Devboxes"))
	huma.Post(api, "/devboxes/{connection_id}/workspaces", s.createDevboxWorkspace,
		httpapi.DocumentOperation("create-devbox-workspace", "Create a workspace on a connected devbox", "Devboxes"))
	for _, route := range devboxapi.DevboxProxyRoutes {
		s.registerDevboxProxy(api, route)
	}
}

func (s *Server) createDevboxWorkspace(ctx context.Context, input *devboxapi.CreateDevboxWorkspaceInput) (*httpapi.BodyOutput[workspaceapi.WorkspaceResponse], error) {
	connections, err := s.devboxController()
	if err != nil {
		return nil, err
	}
	for _, connection := range connections.List() {
		if connection.ID == input.ConnectionID && connection.Maintenance {
			return nil, httpapi.ServiceUnavailable("this devbox is in maintenance")
		}
	}
	if err := connections.Check(ctx, input.ConnectionID); err != nil {
		return nil, httpapi.Conflict(httpapi.CodeConflict, err.Error(), nil)
	}
	body := input.Body
	repo, err := s.repoResolver.LookupRoute(ctx, body.Provider, body.PlatformHost, body.Owner, body.Name)
	if err != nil {
		return nil, httpapi.ProviderRouteLookupError(err)
	}
	if repo == nil {
		return nil, httpapi.NotFound(httpapi.CodeRepoNotFound, "add the repository to Forge before creating a remote workspace", nil)
	}
	if body.MRNumber == 0 && body.IssueNumber == 0 && strings.TrimSpace(body.Branch) == "" {
		var entropy [8]byte
		if _, err := rand.Read(entropy[:]); err != nil {
			return nil, httpapi.Internal(err.Error())
		}
		body.Branch = "work-" + hex.EncodeToString(entropy[:])
	}
	request := workspaceapi.WorkerCreateRequest{
		Repository: db.WorkspaceLaunchRepository{Provider: repo.Platform, PlatformHost: repo.PlatformHost, PlatformRepoID: repo.PlatformRepoID, Owner: repo.Owner, Name: repo.Name, CloneURL: repo.CloneURL, DefaultBranch: repo.DefaultBranch},
		Branch:     body.Branch, ReuseExistingBranch: body.ReuseExistingBranch,
	}
	if body.MRNumber != 0 || body.IssueNumber != 0 {
		kind, number := db.WorkspaceItemTypePullRequest, body.MRNumber
		if body.IssueNumber != 0 {
			kind, number = db.WorkspaceItemTypeIssue, body.IssueNumber
		}
		spec, err := s.ResolveWorkspaceLaunchSpec(ctx, providerplane.WorkspaceLaunchRequest{
			Repository: providerplane.RepositoryRoute{Provider: repo.Platform, PlatformHost: repo.PlatformHost, Owner: repo.Owner, Name: repo.Name},
			ItemType:   kind, ItemNumber: number, GitHeadRef: body.Branch,
		})
		if err != nil {
			return nil, err
		}
		request.Repository, request.LaunchSpec = spec.Repository, &spec
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, httpapi.Internal(err.Error())
	}
	client, err := connections.WorkerClient(input.ConnectionID)
	if err != nil {
		return nil, err
	}
	var bodyOptions generated.CreateWorkerWorkspaceBody
	if err := json.Unmarshal(raw, &bodyOptions); err != nil {
		return nil, err
	}
	response, err := client.HTTP.CreateWorkerWorkspaceRaw(ctx, client.Transport, &generated.CreateWorkerWorkspaceRequestOptions{Body: &bodyOptions})
	if err != nil {
		return nil, httpapi.ServiceUnavailable("workspace creation response unavailable; refresh devbox workspaces before trying again: " + err.Error())
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		return nil, devboxapi.DevboxResponseProblem(response)
	}
	var workspace workspaceapi.WorkspaceResponse
	if err := json.UnmarshalRead(io.LimitReader(response.Body, 1<<20), &workspace); err != nil {
		return nil, httpapi.Internal(err.Error())
	}
	s.hub.Broadcast(syncevents.Event{Type: "data_changed", Data: struct{}{}})
	return &httpapi.BodyOutput[workspaceapi.WorkspaceResponse]{Body: workspace}, nil
}

func (s *Server) registerDevboxProxy(api huma.API, route devboxapi.DevboxProxyRoute) {
	op := &huma.Operation{OperationID: route.Operation, Method: route.Method, Path: "/devboxes/{connection_id}" + route.Path, Tags: []string{"Devboxes"}, Summary: "Forward an execution operation to its owning devbox"}
	if item := api.OpenAPI().Paths[route.Path]; item != nil {
		source := map[string]*huma.Operation{"GET": item.Get, "POST": item.Post, "DELETE": item.Delete, "PATCH": item.Patch}[route.Method]
		if source != nil {
			op.Parameters = slices.Clone(source.Parameters)
			op.RequestBody, op.Responses, op.Metadata = source.RequestBody, source.Responses, source.Metadata
		}
	}

	op.Parameters = append(op.Parameters, &huma.Param{Name: "connection_id", In: "path", Required: true, Schema: &huma.Schema{Type: "string"}})
	api.OpenAPI().AddOperation(op)
	api.Adapter().Handle(op, func(ctx huma.Context) {
		r, w := humago.Unwrap(ctx)
		connections, err := s.devboxController()
		if err != nil {
			routepolicy.WriteProblemResponse(w, httpapi.NewProblem(503, httpapi.CodeServiceUnavailable, err.Error(), nil))
			return
		}
		if !fleetapi.BufferProxyRequestBody(w, r, 20<<20) {
			return
		}
		path := route.Path
		for _, name := range []string{"id", "session_key"} {
			path = strings.ReplaceAll(path, "{"+name+"}", url.PathEscape(r.PathValue(name)))
		}
		if r.URL.RawQuery != "" {
			path += "?" + r.URL.RawQuery
		}
		refreshContext := r.Method == "POST" && (strings.HasSuffix(route.Path, "/retry") || strings.HasSuffix(route.Path, "/refresh") || strings.HasSuffix(route.Path, "/runtime/agent-handoffs"))
		if r.Method == "POST" && strings.HasSuffix(route.Path, "/runtime/sessions") {
			var input workspaceapi.LaunchWorkspaceRuntimeSessionInput
			body, err := io.ReadAll(r.Body)
			if err == nil {
				err = json.Unmarshal(body, &input.Body)
			}
			if err != nil {
				routepolicy.WriteProblemResponse(w, httpapi.NewProblem(http.StatusBadRequest, httpapi.CodeBadRequest, "invalid session launch request", nil))
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			target := strings.TrimSpace(input.Body.TargetKey)
			// These reserved system targets launch shells; configured targets are agents.
			refreshContext = target != string(localruntime.LaunchTargetShell) && target != string(localruntime.LaunchTargetPlainShell)
		}
		if refreshContext {
			if err := s.refreshDevboxContext(r.Context(), connections, r.PathValue("connection_id"), r.PathValue("id")); err != nil {
				routepolicy.WriteProblemResponse(w, httpapi.NewProblem(409, httpapi.CodeConflict, "Workspace context needs refresh: "+err.Error(), nil))
				return
			}
		}
		response, err := connections.Do(r.Context(), r.PathValue("connection_id"), r.Method, "/api/v1"+path, r.Body, r.Header)
		if err == nil && r.Method == http.MethodGet && response.StatusCode == http.StatusConflict {
			var captured bytes.Buffer
			var problem httpapi.ProblemError
			decodeErr := json.UnmarshalRead(io.TeeReader(io.LimitReader(response.Body, 64<<10), &captured), &problem)
			if decodeErr == nil && problem.Code == httpapi.CodeConflict && problem.Details["reason"] == workspaceapi.WorkspaceContextExpiredReason {
				_ = response.Body.Close()
				if err := s.refreshDevboxContext(r.Context(), connections, r.PathValue("connection_id"), r.PathValue("id")); err != nil {
					routepolicy.WriteProblemResponse(w, httpapi.NewProblem(http.StatusConflict, httpapi.CodeConflict, "Workspace context needs refresh: "+err.Error(), nil))
					return
				}
				// Retry only this read, once. Ordinary reads do not renew context.
				response, err = connections.Do(r.Context(), r.PathValue("connection_id"), r.Method, "/api/v1"+path, nil, r.Header)
			} else {
				// Forward unrelated conflicts unchanged, including bodies beyond the probe limit.
				response.Body = struct {
					io.Reader
					io.Closer
				}{io.MultiReader(&captured, response.Body), response.Body}
			}
		}
		if err != nil {
			routepolicy.WriteProblemResponse(w, httpapi.NewProblem(502, httpapi.CodeUpstreamError, "devbox request failed; reconnect and check operation status: "+err.Error(), nil))
			return
		}
		defer response.Body.Close()
		if response.StatusCode == http.StatusOK && (route.Path == "/workspaces/{id}" || route.Path == "/workspaces/{id}/push" || route.Path == "/workspaces/{id}/refresh") {
			var workspace workspaceapi.WorkspaceResponse
			if err := json.UnmarshalRead(io.LimitReader(response.Body, 1<<20), &workspace); err != nil {
				routepolicy.WriteProblemResponse(w, httpapi.NewProblem(502, httpapi.CodeUpstreamError, "devbox returned an invalid workspace response", nil))
				return
			}
			workspace.CommitAttribution = nil
			if state := workspace.PushState; state != nil && state.Pushed && state.Repository == workspace.RepoOwner+"/"+workspace.RepoName {
				workspace.CommitAttribution = new(connections.CheckAttribution(r.Context(), r.PathValue("connection_id"), *state, s.devboxapi.DevboxAttributionSource(workspace)))
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(response.StatusCode)
			_ = json.MarshalWrite(w, workspace)
			return
		}
		fleetapi.CopyProxyResponseHeaders(w.Header(), response.Header)
		w.WriteHeader(response.StatusCode)
		if strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
			devboxapi.CopyDevboxEvents(w, response.Body)
		} else {
			_, _ = io.Copy(w, response.Body)
		}
	})
}

func (s *Server) refreshDevboxContext(ctx context.Context, connections *devbox.Connections, connectionID, workspaceID string) error {
	client, err := connections.WorkerClient(connectionID)
	if err != nil {
		return err
	}
	response, err := client.HTTP.GetWorkspaceRaw(ctx, client.Transport, &generated.GetWorkspaceRequestOptions{PathParams: &generated.GetWorkspacePath{ID: workspaceID}})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return devboxapi.DevboxResponseProblem(response)
	}
	var current workspaceapi.WorkspaceResponse
	if err := json.UnmarshalRead(io.LimitReader(response.Body, 1<<20), &current); err != nil {
		return err
	}
	if current.ItemType != db.WorkspaceItemTypeIssue && current.ItemType != db.WorkspaceItemTypePullRequest {
		return nil
	}
	spec, err := s.ResolveWorkspaceLaunchSpec(ctx, providerplane.WorkspaceLaunchRequest{
		Repository: providerplane.RepositoryRoute{Provider: current.Repo.Provider, PlatformHost: current.PlatformHost, Owner: current.RepoOwner, Name: current.RepoName},
		ItemType:   current.ItemType, ItemNumber: current.ItemNumber, ItemKey: current.ItemKey, GitHeadRef: current.GitHeadRef,
	})
	if err != nil {
		return err
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	var bodyOptions generated.RefreshWorkerContextBody
	if err := json.Unmarshal(raw, &bodyOptions); err != nil {
		return err
	}
	refreshed, err := client.HTTP.RefreshWorkerContextRaw(ctx, client.Transport, &generated.RefreshWorkerContextRequestOptions{PathParams: &generated.RefreshWorkerContextPath{ID: workspaceID}, Body: &bodyOptions})
	if err != nil {
		return err
	}
	defer refreshed.Body.Close()
	if refreshed.StatusCode >= 300 {
		return devboxapi.DevboxResponseProblem(refreshed)
	}
	return nil
}

func (s *Server) registerDevboxTerminalAPI(api huma.API) {
	for _, route := range []devboxapi.DevboxProxyRoute{
		{Method: "GET", Path: "/workspaces/{id}/terminal", Operation: "connect-devbox-terminal"},
		{Method: "GET", Path: "/workspaces/{id}/runtime/sessions/{session_key}/terminal", Operation: "connect-devbox-session-terminal"},
	} {
		op := &huma.Operation{OperationID: route.Operation, Method: "GET", Path: "/devboxes/{connection_id}" + route.Path, Hidden: true}
		api.Adapter().Handle(op, func(ctx huma.Context) {
			r, w := humago.Unwrap(ctx)
			connections, err := s.devboxController()
			if err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			endpoint, token, err := connections.WorkerEndpoint(r.PathValue("connection_id"))
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			path := route.Path
			for _, name := range []string{"id", "session_key"} {
				path = strings.ReplaceAll(path, "{"+name+"}", url.PathEscape(r.PathValue(name)))
			}
			endpoint = strings.Replace(endpoint, "http", "ws", 1) + "/ws/v1" + path
			if r.URL.RawQuery != "" {
				endpoint += "?" + r.URL.RawQuery
			}
			transport := &http.Transport{Proxy: nil, ResponseHeaderTimeout: 30 * time.Second}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			peer, _, err := terminalwebsocket.Dial(r.Context(), endpoint, http.Header{"Authorization": {"Bearer " + token}}, client)
			if err != nil {
				http.Error(w, "devbox terminal unavailable; reconnect and retry attachment", http.StatusBadGateway)
				return
			}
			defer peer.Close(websocket.StatusNormalClosure, "controller detached")
			local, err := terminalwebsocket.Accept(w, r)
			if err != nil {
				return
			}
			defer local.Close(websocket.StatusNormalClosure, "controller detached")
			fleetapi.BridgeWebSocketProxy(r.Context(), local, peer)
		})
	}
}

func (s *Server) devboxSnapshots(ctx context.Context, timeout time.Duration) []fleet.PeerResult {
	connections := s.options.Devboxes
	if connections == nil {
		return nil
	}
	items := connections.List()
	results := make([]fleet.PeerResult, len(items))
	var wg sync.WaitGroup
	for i, connection := range items {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			result := fleet.PeerResult{NodeID: fleet.NodeID("devbox:" + connection.ID), Name: connection.Name, Role: fleet.RoleDevbox, ObservedAt: time.Now().UTC().Format(time.RFC3339)}
			result.Maintenance = connection.Maintenance
			defer func() { results[i] = result }()
			client, err := connections.WorkerClient(connection.ID)
			if err != nil {
				result.Err = new(err.Error())
				return
			}
			response, err := client.HTTP.GetWorkerSnapshotRaw(ctx, client.Transport)
			if err != nil {
				result.Err = new("devbox offline: " + err.Error())
				return
			}
			defer response.Body.Close()
			if response.StatusCode != 200 {
				result.Err = new("devbox needs reconnect or setup: " + response.Status)
				return
			}
			var raw fleet.RawSnapshot
			if err := json.UnmarshalRead(io.LimitReader(response.Body, 32<<20), &raw); err != nil {
				result.Err = new(err.Error())
				return
			}
			if string(raw.NodeID) != connection.NodeID {
				result.Err = new("devbox identity mismatch; reconnect required")
				return
			}
			// Projection IDs describe execution targets, separately from the worker's durable identity.
			raw.NodeID, raw.BaseURL = result.NodeID, ""
			result.Raw, result.Reachable, result.Platform = &raw, true, raw.Host.Platform
		})
	}
	wg.Wait()
	return results
}
