package server

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/server/activityapi"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/roborevapi"
	"go.kenn.io/forge/internal/server/routepolicy"
	"go.kenn.io/forge/internal/server/workspaceapi"
)

// RegisteredTransportOperations returns every documented REST operation. The
// list is sorted by operation ID and rejects duplicate IDs so ownership can be
// a closed, one-entry-per-operation table.
func RegisteredTransportOperations() ([]routepolicy.RegisteredTransportOperation, error) {
	openAPI := NewOpenAPI()
	operations := make([]routepolicy.RegisteredTransportOperation, 0)
	seen := make(map[string]string)
	paths := make([]string, 0, len(openAPI.Paths))
	for path := range openAPI.Paths {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		item := openAPI.Paths[path]
		if item == nil {
			continue
		}
		for _, candidate := range []struct {
			method    string
			operation *huma.Operation
		}{
			{http.MethodGet, item.Get},
			{http.MethodPut, item.Put},
			{http.MethodPost, item.Post},
			{http.MethodDelete, item.Delete},
			{http.MethodOptions, item.Options},
			{http.MethodHead, item.Head},
			{http.MethodPatch, item.Patch},
			{http.MethodTrace, item.Trace},
		} {
			if candidate.operation == nil {
				continue
			}
			id := strings.TrimSpace(candidate.operation.OperationID)
			if id == "" {
				return nil, fmt.Errorf("%s %s has no operation ID", candidate.method, path)
			}
			canonicalPath := "/api/v1" + path
			if previous, duplicate := seen[id]; duplicate {
				return nil, fmt.Errorf(
					"duplicate operation ID %q: %s and %s %s",
					id, previous, candidate.method, canonicalPath,
				)
			}
			seen[id] = candidate.method + " " + canonicalPath
			scope, peerCallable := federationauth.RouteScope(
				candidate.method, canonicalPath,
			)
			operations = append(operations, routepolicy.RegisteredTransportOperation{
				ID: id, Method: candidate.method, Path: canonicalPath,
				Tags:         slices.Clone(candidate.operation.Tags),
				PeerCallable: peerCallable,
				PeerScope:    scope,
			})
		}
	}
	sort.Slice(operations, func(i, j int) bool {
		return operations[i].ID < operations[j].ID
	})
	return operations, nil
}

// NewTransportInventory builds the long-lived transport contract from the
// same Huma route registrations used by the server.
func NewTransportInventory() (routepolicy.TransportInventory, error) {
	mux := http.NewServeMux()
	recorder := &routepolicy.TransportRecorder{}
	s := wiredServer(&Server{cfg: &config.Config{}})

	api := routepolicy.NewRecordingAPI(mux, "/api/v1", activityapi.ApiConfig("/"), recorder, "")
	s.registerAPI(api)
	restAdapter := api.Adapter().(*routepolicy.RecordingAdapter)
	restAdapter.DefaultTransport = routepolicy.TransportWebSocket
	s.fleetAPI.RegisterTerminal(api)
	workspaceapi.RegisterTerminalInventory(api)
	s.registerDevboxTerminalAPI(api)
	restAdapter.DefaultTransport = ""

	wsAPI := routepolicy.NewRecordingAPI(
		mux, "/ws/v1", authapi.TerminalAPIConfig(), recorder, routepolicy.TransportWebSocket,
	)
	s.fleetAPI.RegisterTerminal(wsAPI)
	workspaceapi.RegisterTerminalInventory(wsAPI)
	s.registerDevboxTerminalAPI(wsAPI)

	roborevAPI := routepolicy.NewRecordingAPI(
		mux, "/api", roborevapi.RoborevProxyAPIConfig(), recorder, "",
	)
	s.roborevapi.RegisterRoborevProxyAPI(roborevAPI)

	if len(recorder.Errs) > 0 {
		return routepolicy.TransportInventory{}, recorder.Errs[0]
	}
	routes, err := routepolicy.NormalizeTransportRoutes(recorder.Routes)
	if err != nil {
		return routepolicy.TransportInventory{}, err
	}
	return routepolicy.TransportInventory{
		SchemaVersion: routepolicy.TransportInventorySchemaVersion,
		Routes:        routes,
	}, nil
}
