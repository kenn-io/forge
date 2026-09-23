package configreload

import (
	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/kit/daemon"
)

type DaemonPingResponse struct {
	daemon.PingInfo
	MCPURL string `json:"mcp_url,omitempty"`
}

type DaemonPingOutput = httpapi.BodyOutput[DaemonPingResponse]

func DaemonPingAPIConfig() huma.Config {
	config := huma.DefaultConfig("kenn-forge daemon", "0.1.0")
	config.OpenAPIPath = ""
	config.DocsPath = ""
	config.SchemasPath = ""
	config.Servers = nil
	return config
}
