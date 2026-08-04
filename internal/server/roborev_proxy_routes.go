package server

import (
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"go.kenn.io/forge/internal/server/httpapi"
)

func roborevProxyAPIConfig() huma.Config {
	config := huma.DefaultConfig("kenn-forge roborev proxy", "0.1.0")
	config.OpenAPIPath = ""
	config.DocsPath = ""
	config.SchemasPath = ""
	return config
}

func (s *Server) registerRoborevProxyAPI(api huma.API) {
	proxy := roborevProxy(s.cfg.RoborevEndpoint())
	for _, method := range []string{
		http.MethodGet,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
		http.MethodOptions,
		http.MethodHead,
		http.MethodConnect,
		http.MethodTrace,
	} {
		op := &huma.Operation{
			OperationID: "proxy-roborev-" + strings.ToLower(method),
			Method:      method,
			Path:        "/roborev/",
			Hidden:      true,
		}
		switch method {
		case http.MethodGet:
			httpapi.AddTransportRoutes(op,
				httpapi.TransportRoute{
					Method:    http.MethodGet,
					Path:      "/api/roborev/api/stream/events",
					Transport: httpapi.TransportHTTPStream,
					Accept:    "application/x-ndjson",
				},
				httpapi.TransportRoute{
					Method:    http.MethodGet,
					Path:      "/api/roborev/api/job/output",
					Transport: httpapi.TransportHTTPStream,
					Accept:    "application/x-ndjson",
					Query:     map[string]string{"stream": "1"},
				},
			)
		case http.MethodPost:
			httpapi.AddTransportRoutes(op, httpapi.TransportRoute{
				Method:    http.MethodPost,
				Path:      "/api/roborev/api/sync/now",
				Transport: httpapi.TransportHTTPStream,
				Accept:    "application/x-ndjson",
				Query:     map[string]string{"stream": "1"},
			})
		}
		api.Adapter().Handle(op, func(ctx huma.Context) {
			r, w := humago.Unwrap(ctx)
			accepted, err := httpapi.ValidateTransportAccept(op, r)
			if err != nil {
				http.Error(w, "invalid transport metadata", http.StatusInternalServerError)
				return
			}
			if !accepted {
				http.Error(w, "streaming route requires an explicit Accept header", http.StatusNotAcceptable)
				return
			}
			proxy.ServeHTTP(w, r)
		})
	}
}
