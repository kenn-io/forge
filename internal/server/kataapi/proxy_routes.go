package kataapi

import (
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"go.kenn.io/forge/internal/server/httpapi"
)

func (h *Handler) registerKataProxyAPI(api huma.API) {
	proxy := h.kataProxy()
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
		if method == http.MethodGet {
			httpapi.AddTransportRoutes(op, httpapi.TransportRoute{
				Method:    http.MethodGet,
				Path:      "/api/v1/kata/proxy/api/v1/events/stream",
				Transport: httpapi.TransportHTTPStream,
				Accept:    "text/event-stream",
			})
		}
		api.Adapter().Handle(op, func(ctx huma.Context) {
			r, w := humago.Unwrap(ctx)
			proxy.ServeHTTP(w, r)
		})
	}
}
