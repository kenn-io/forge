package httpapi

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

// TransportKind identifies a long-lived HTTP or upgraded transport.
type TransportKind string

const (
	TransportHTTPStream TransportKind = "http-stream"
	TransportWebSocket  TransportKind = "websocket"
)

// TransportRoute is the machine-readable contract for one long-lived route.
type TransportRoute struct {
	Method    string            `json:"method"`
	Path      string            `json:"path"`
	Transport TransportKind     `json:"transport"`
	Accept    string            `json:"accept,omitempty"`
	Query     map[string]string `json:"query,omitempty"`
}

const transportRoutesMetadataKey = "_forge_transport_routes"

// AddTransportRoutes attaches proxy-only route variants to the Huma operation
// that registers their catch-all handler.
func AddTransportRoutes(op *huma.Operation, routes ...TransportRoute) {
	if op.Metadata == nil {
		op.Metadata = map[string]any{}
	}
	op.Metadata[transportRoutesMetadataKey] = slices.Clone(routes)
}

// TransportRoutes returns an isolated copy of routes attached to op.
func TransportRoutes(op *huma.Operation) ([]TransportRoute, error) {
	if op.Metadata == nil {
		return nil, nil
	}
	raw, ok := op.Metadata[transportRoutesMetadataKey]
	if !ok {
		return nil, nil
	}
	routes, ok := raw.([]TransportRoute)
	if !ok {
		return nil, fmt.Errorf("transport metadata has type %T", raw)
	}
	cloned := slices.Clone(routes)
	for i := range cloned {
		cloned[i].Query = maps.Clone(cloned[i].Query)
	}
	return cloned, nil
}

// ValidateTransportAccept reports whether a request satisfies the media type
// declared by a matching proxy-only stream variant.
func ValidateTransportAccept(op *huma.Operation, request *http.Request) (bool, error) {
	routes, err := TransportRoutes(op)
	if err != nil {
		return false, err
	}
	for _, route := range routes {
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
			return acceptsMediaType(request.Header.Get("Accept"), route.Accept), nil
		}
	}
	return true, nil
}

func acceptsMediaType(header string, required string) bool {
	for mediaRange := range strings.SplitSeq(header, ",") {
		parts := strings.Split(mediaRange, ";")
		if !strings.EqualFold(strings.TrimSpace(parts[0]), required) {
			continue
		}
		accepted := true
		for _, parameter := range parts[1:] {
			name, value, ok := strings.Cut(parameter, "=")
			if !ok || !strings.EqualFold(strings.TrimSpace(name), "q") {
				continue
			}
			quality, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			if err != nil || quality <= 0 {
				accepted = false
			}
		}
		if accepted {
			return true
		}
	}
	return false
}
