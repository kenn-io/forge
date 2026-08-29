package server

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
)

const TransportInventorySchemaVersion = 1

type TransportRoute = httpapi.TransportRoute
type TransportKind = httpapi.TransportKind

const (
	TransportHTTPStream = httpapi.TransportHTTPStream
	TransportWebSocket  = httpapi.TransportWebSocket
)

// TransportInventory describes every registered long-lived route.
type TransportInventory struct {
	SchemaVersion int              `json:"schema_version"`
	Routes        []TransportRoute `json:"routes"`
}

// RegisteredTransportOperation is one documented REST operation from the
// same Huma registration graph used by the live server.
type RegisteredTransportOperation struct {
	ID           string
	Method       string
	Path         string
	Tags         []string
	PeerCallable bool
	PeerScope    federationauth.Scope
}

// RegisteredTransportOperations returns every documented REST operation. The
// list is sorted by operation ID and rejects duplicate IDs so ownership can be
// a closed, one-entry-per-operation table.
func RegisteredTransportOperations() ([]RegisteredTransportOperation, error) {
	openAPI := NewOpenAPI()
	operations := make([]RegisteredTransportOperation, 0)
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
			operations = append(operations, RegisteredTransportOperation{
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

type transportRecorder struct {
	routes []TransportRoute
	errs   []error
}

type recordingAdapter struct {
	huma.Adapter
	prefix           string
	defaultTransport TransportKind
	recorder         *transportRecorder
}

func (a *recordingAdapter) Handle(
	op *huma.Operation,
	handler func(huma.Context),
) {
	a.recorder.record(a.prefix, a.defaultTransport, op)
	a.Adapter.Handle(op, handler)
}

func (r *transportRecorder) record(
	prefix string,
	defaultTransport TransportKind,
	op *huma.Operation,
) {
	if defaultTransport != "" {
		r.routes = append(r.routes, TransportRoute{
			Method: op.Method, Path: prefix + op.Path,
			Transport: defaultTransport,
		})
	}
	for status, response := range op.Responses {
		if !strings.HasPrefix(status, "2") || response == nil {
			continue
		}
		for mediaType := range response.Content {
			if !isLongLivedMediaType(mediaType) {
				continue
			}
			r.routes = append(r.routes, TransportRoute{
				Method: op.Method, Path: prefix + op.Path,
				Transport: TransportHTTPStream, Accept: mediaType,
			})
		}
	}
	annotated, err := httpapi.TransportRoutes(op)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s %s: %w", op.Method, prefix+op.Path, err))
		return
	}
	r.routes = append(r.routes, annotated...)
}

func isLongLivedMediaType(value string) bool {
	switch strings.ToLower(strings.TrimSpace(strings.SplitN(value, ";", 2)[0])) {
	case "text/event-stream", "application/x-ndjson":
		return true
	default:
		return false
	}
}

func newRecordingAPI(
	mux *http.ServeMux,
	prefix string,
	config huma.Config,
	recorder *transportRecorder,
	defaultTransport TransportKind,
) huma.API {
	adapter := &recordingAdapter{
		Adapter: humago.NewAdapter(mux, prefix),
		prefix:  prefix, defaultTransport: defaultTransport, recorder: recorder,
	}
	return huma.NewAPI(config, adapter)
}

// NewTransportInventory builds the long-lived transport contract from the
// same Huma route registrations used by the server.
func NewTransportInventory() (TransportInventory, error) {
	mux := http.NewServeMux()
	recorder := &transportRecorder{}
	s := &Server{cfg: &config.Config{}}

	api := newRecordingAPI(mux, "/api/v1", apiConfig("/"), recorder, "")
	s.registerAPI(api)
	restAdapter := api.Adapter().(*recordingAdapter)
	restAdapter.defaultTransport = TransportWebSocket
	s.fleetAPI.RegisterTerminal(api)
	workspaceapi.RegisterTerminalInventory(api)
	restAdapter.defaultTransport = ""

	wsAPI := newRecordingAPI(
		mux, "/ws/v1", terminalAPIConfig(), recorder, TransportWebSocket,
	)
	s.fleetAPI.RegisterTerminal(wsAPI)
	workspaceapi.RegisterTerminalInventory(wsAPI)

	roborevAPI := newRecordingAPI(
		mux, "/api", roborevProxyAPIConfig(), recorder, "",
	)
	s.registerRoborevProxyAPI(roborevAPI)

	if len(recorder.errs) > 0 {
		return TransportInventory{}, recorder.errs[0]
	}
	routes, err := normalizeTransportRoutes(recorder.routes)
	if err != nil {
		return TransportInventory{}, err
	}
	return TransportInventory{
		SchemaVersion: TransportInventorySchemaVersion,
		Routes:        routes,
	}, nil
}

func normalizeTransportRoutes(routes []TransportRoute) ([]TransportRoute, error) {
	normalized := make([]TransportRoute, 0, len(routes))
	seen := map[string]struct{}{}
	for _, route := range routes {
		route.Method = strings.ToUpper(strings.TrimSpace(route.Method))
		route.Path = strings.TrimSpace(route.Path)
		route.Accept = strings.ToLower(strings.TrimSpace(route.Accept))
		route.Query = maps.Clone(route.Query)
		if route.Method == "" {
			return nil, fmt.Errorf("transport route has empty method")
		}
		if !strings.HasPrefix(route.Path, "/") || strings.HasPrefix(route.Path, "//") {
			return nil, fmt.Errorf("%s %s: route requires an absolute path", route.Method, route.Path)
		}
		if strings.ContainsAny(route.Path, "\\\x00#") || strings.Contains(route.Path, "://") {
			return nil, fmt.Errorf("%s %s: invalid transport path", route.Method, route.Path)
		}
		switch route.Transport {
		case TransportHTTPStream:
			if route.Accept == "" {
				return nil, fmt.Errorf("%s %s: HTTP stream requires accept", route.Method, route.Path)
			}
		case TransportWebSocket:
			if route.Accept != "" {
				return nil, fmt.Errorf("%s %s: WebSocket must not declare accept", route.Method, route.Path)
			}
		default:
			return nil, fmt.Errorf("%s %s: unsupported transport %q", route.Method, route.Path, route.Transport)
		}
		for key, value := range route.Query {
			if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
				return nil, fmt.Errorf("%s %s: transport query requires non-empty keys and values", route.Method, route.Path)
			}
		}
		identity := transportRouteIdentity(route)
		if _, ok := seen[identity]; ok {
			return nil, fmt.Errorf("duplicate transport route: %s %s", route.Method, route.Path)
		}
		seen[identity] = struct{}{}
		normalized = append(normalized, route)
	}
	sort.Slice(normalized, func(i, j int) bool {
		return transportRouteIdentity(normalized[i]) <
			transportRouteIdentity(normalized[j])
	})
	return normalized, nil
}

func transportRouteIdentity(route TransportRoute) string {
	query := make([]string, 0, len(route.Query))
	for key, value := range route.Query {
		query = append(query, key+"="+value)
	}
	sort.Strings(query)
	return strings.Join([]string{
		route.Method, route.Path, string(route.Transport), route.Accept,
		strings.Join(query, "&"),
	}, "\x00")
}

func (inventory TransportInventory) matchesHTTPStream(request *http.Request) bool {
	for _, route := range inventory.Routes {
		if route.Transport != TransportHTTPStream ||
			route.Method != request.Method || route.Path != request.URL.Path {
			continue
		}
		matches := true
		for key, value := range route.Query {
			if request.URL.Query().Get(key) != value {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}
