package httpapi

import (
	"fmt"
	"maps"
	"slices"

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
