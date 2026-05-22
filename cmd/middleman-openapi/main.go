package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"go.kenn.io/middleman/internal/server"
)

func main() {
	var out string
	var version string
	var format string
	flag.StringVar(&out, "out", "frontend/openapi/openapi.yaml", "path to write the generated OpenAPI document")
	flag.StringVar(&version, "version", "3.1", "OpenAPI version to write: 3.1 or 3.0")
	flag.StringVar(&format, "format", "auto", "OpenAPI format to write: auto, json, or yaml")
	flag.Parse()

	openAPI := server.NewOpenAPI()
	spec, err := renderSpec(openAPI, version, resolveFormat(out, format))
	if err != nil {
		fmt.Fprintf(os.Stderr, "render openapi: %v\n", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir %s: %v\n", filepath.Dir(out), err)
		os.Exit(1)
	}

	if err := os.WriteFile(out, ensureTrailingNewline(spec), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", out, err)
		os.Exit(1)
	}
}

func prettyJSON(spec []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.Indent(&buf, spec, "", "  "); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func resolveFormat(out, format string) string {
	if format != "auto" {
		return format
	}
	switch filepath.Ext(out) {
	case ".yaml", ".yml":
		return "yaml"
	default:
		return "json"
	}
}

func renderSpec(openAPI interface {
	MarshalJSON() ([]byte, error)
	Downgrade() ([]byte, error)
	YAML() ([]byte, error)
	DowngradeYAML() ([]byte, error)
}, version, format string) ([]byte, error) {
	switch format {
	case "json":
		var (
			spec []byte
			err  error
		)
		switch version {
		case "3.1":
			spec, err = openAPI.MarshalJSON()
		case "3.0":
			spec, err = openAPI.Downgrade()
		default:
			return nil, fmt.Errorf("unsupported openapi version %q", version)
		}
		if err != nil {
			return nil, fmt.Errorf("marshal openapi %s: %w", version, err)
		}
		spec, err = prettyJSON(spec)
		if err != nil {
			return nil, fmt.Errorf("format openapi %s: %w", version, err)
		}
		return spec, nil
	case "yaml":
		switch version {
		case "3.1":
			return openAPI.YAML()
		case "3.0":
			return openAPI.DowngradeYAML()
		default:
			return nil, fmt.Errorf("unsupported openapi version %q", version)
		}
	default:
		return nil, fmt.Errorf("unsupported openapi format %q", format)
	}
}

func ensureTrailingNewline(spec []byte) []byte {
	spec = bytes.TrimRight(spec, "\n")
	return append(spec, '\n')
}
