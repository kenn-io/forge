package authapi

import (
	"github.com/danielgtaylor/huma/v2"
)

func TerminalAPIConfig() huma.Config {
	config := huma.DefaultConfig("kenn-forge terminal websocket", "0.1.0")
	config.OpenAPIPath = ""
	config.DocsPath = ""
	config.SchemasPath = ""
	return config
}
