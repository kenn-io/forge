package server

import (
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
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
		api.Adapter().Handle(op, func(ctx huma.Context) {
			r, w := humago.Unwrap(ctx)
			proxy.ServeHTTP(w, r)
		})
	}
}
