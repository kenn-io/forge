package server

import (
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

func (s *Server) registerKataProxyAPI(api huma.API) {
	proxy := s.kataProxy()
	for _, method := range []string{
		http.MethodGet,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
		http.MethodOptions,
		http.MethodHead,
	} {
		op := &huma.Operation{
			OperationID: "proxy-kata-" + strings.ToLower(method),
			Method:      method,
			Path:        "/kata/proxy/",
			Hidden:      true,
		}
		api.Adapter().Handle(op, func(ctx huma.Context) {
			r, w := humago.Unwrap(ctx)
			proxy.ServeHTTP(w, r)
		})
	}
}
