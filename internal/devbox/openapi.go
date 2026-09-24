package devbox

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

// NewControlOpenAPI describes the native registry and Unix broker APIs, which
// run separately from the browser-facing Forge API.
func NewControlOpenAPI() *huma.OpenAPI {
	api := humago.New(http.NewServeMux(), huma.DefaultConfig("Devbox native services", "1"))
	registerBrokerRoutes(api, nil)
	registerRegistryRoutes(api, RegistryConfig{}, nil, nil)
	return api.OpenAPI()
}
