package routepolicy

import (
	"fmt"
	"maps"
	"net/http"
	"sort"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/server/httpapi"
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

type TransportRecorder struct {
	Routes []TransportRoute
	Errs   []error
}

type RecordingAdapter struct {
	huma.Adapter
	prefix           string
	DefaultTransport TransportKind
	recorder         *TransportRecorder
}

func (a *RecordingAdapter) Handle(
	op *huma.Operation,
	handler func(huma.Context),
) {
	a.recorder.record(a.prefix, a.DefaultTransport, op)
	a.Adapter.Handle(op, handler)
}

func (r *TransportRecorder) record(
	prefix string,
	defaultTransport TransportKind,
	op *huma.Operation,
) {
	if defaultTransport != "" {
		r.Routes = append(r.Routes, TransportRoute{
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
			r.Routes = append(r.Routes, TransportRoute{
				Method: op.Method, Path: prefix + op.Path,
				Transport: TransportHTTPStream, Accept: mediaType,
			})
		}
	}
	annotated, err := httpapi.TransportRoutes(op)
	if err != nil {
		r.Errs = append(r.Errs, fmt.Errorf("%s %s: %w", op.Method, prefix+op.Path, err))
		return
	}
	r.Routes = append(r.Routes, annotated...)
}

func isLongLivedMediaType(value string) bool {
	switch strings.ToLower(strings.TrimSpace(strings.SplitN(value, ";", 2)[0])) {
	case "text/event-stream", "application/x-ndjson":
		return true
	default:
		return false
	}
}

func NewRecordingAPI(
	mux *http.ServeMux,
	prefix string,
	config huma.Config,
	recorder *TransportRecorder,
	defaultTransport TransportKind,
) huma.API {
	adapter := &RecordingAdapter{
		Adapter: humago.NewAdapter(mux, prefix),
		prefix:  prefix, DefaultTransport: defaultTransport, recorder: recorder,
	}
	return huma.NewAPI(config, adapter)
}

func NormalizeTransportRoutes(routes []TransportRoute) ([]TransportRoute, error) {
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

func (inventory TransportInventory) MatchesHTTPStream(request *http.Request) bool {
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
