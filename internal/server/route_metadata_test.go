package server

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var allowedAPITags = map[string]struct{}{
	"Activity":      {},
	"Issues":        {},
	"Projects":      {},
	"Pull Requests": {},
	"Repositories":  {},
	"Roborev":       {},
	"Settings":      {},
	"Stacks":        {},
	"Sync":          {},
	"System":        {},
	"Workspaces":    {},
}

// collectMetadataFailures walks an OpenAPI document and returns one entry per
// missing metadata field on every non-nil operation. The returned slice is
// sorted so failure output is stable across test runs.
//
// The walker checks each operation for a non-empty Summary, a non-empty
// OperationID, exactly one non-empty Tag from the API tag taxonomy, and a
// globally-unique OperationID.
// It deliberately does not consult huma's internal _convenience_summary and
// _convenience_id markers: those markers fire when an explicit value happens
// to match what huma would auto-generate ("List issues" for GET /issues), so
// they are not a reliable signal of "this was never set on purpose". The
// source-level convenience-route test enforces the registration pattern that
// the live OpenAPI document cannot distinguish by value alone.
func collectMetadataFailures(openAPI *huma.OpenAPI) []string {
	var failures []string
	seen := map[string]string{}

	paths := make([]string, 0, len(openAPI.Paths))
	for p := range openAPI.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, path := range paths {
		item := openAPI.Paths[path]
		if item == nil {
			continue
		}
		for _, opRef := range []struct {
			method string
			op     *huma.Operation
		}{
			{http.MethodGet, item.Get},
			{http.MethodPut, item.Put},
			{http.MethodPost, item.Post},
			{http.MethodDelete, item.Delete},
			{http.MethodOptions, item.Options},
			{http.MethodHead, item.Head},
			{http.MethodPatch, item.Patch},
			{http.MethodTrace, item.Trace},
		} {
			op := opRef.op
			if op == nil {
				continue
			}
			label := fmt.Sprintf("%s %s", opRef.method, path)

			if strings.TrimSpace(op.Summary) == "" {
				failures = append(failures, label+": missing Summary")
			}
			if strings.TrimSpace(op.OperationID) == "" {
				failures = append(failures, label+": missing OperationID")
			}
			if len(op.Tags) < 1 {
				failures = append(failures, label+": missing Tags")
			} else {
				for _, tag := range op.Tags {
					if strings.TrimSpace(tag) == "" {
						failures = append(failures, label+": empty Tag")
					}
				}
			}
			if len(op.Tags) > 0 && !usesKnownSingleTag(op.Tags) {
				failures = append(failures,
					label+": expected exactly one tag from the API tag taxonomy")
			}
			if op.OperationID != "" {
				if prior, ok := seen[op.OperationID]; ok {
					failures = append(failures,
						label+": duplicate OperationID with "+prior)
				} else {
					seen[op.OperationID] = label
				}
			}
		}
	}
	return failures
}

func usesKnownSingleTag(tags []string) bool {
	if len(tags) != 1 {
		return false
	}
	_, ok := allowedAPITags[strings.TrimSpace(tags[0])]
	return ok
}

func collectConvenienceRouteMetadataFailures(path string, source []byte) []string {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, source, 0)
	if err != nil {
		return []string{fmt.Sprintf("%s: parse: %v", path, err)}
	}

	var failures []string
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !isHumaConvenienceMethod(selector.Sel.Name) {
			return true
		}
		pkg, ok := selector.X.(*ast.Ident)
		if !ok || pkg.Name != "huma" {
			return true
		}
		for _, arg := range call.Args {
			argCall, ok := arg.(*ast.CallExpr)
			if !ok {
				continue
			}
			ident, ok := argCall.Fun.(*ast.Ident)
			if ok && ident.Name == "documentOperation" {
				return true
			}
		}
		pos := fset.Position(call.Pos())
		failures = append(failures, fmt.Sprintf(
			"%s:%d: huma.%s must use documentOperation for OpenAPI metadata",
			path, pos.Line, selector.Sel.Name,
		))
		return true
	})
	sort.Strings(failures)
	return failures
}

func isHumaConvenienceMethod(name string) bool {
	switch name {
	case "Get", "Post", "Put", "Patch", "Delete", "Head", "Options":
		return true
	default:
		return false
	}
}

// TestHumaContractMetadata asserts that every non-Hidden operation in the
// live OpenAPI document carries an explicit Summary, exactly one known Tag,
// and a unique non-empty OperationID.
func TestHumaContractMetadata(t *testing.T) {
	require := require.New(t)
	openAPI := NewOpenAPI()
	require.NotNil(openAPI)
	require.NotEmpty(openAPI.Paths, "OpenAPI document should expose paths")

	failures := collectMetadataFailures(openAPI)
	assert.Empty(t, failures, strings.Join(failures, "\n"))
}

func TestHumaConvenienceRoutesUseDocumentOperation(t *testing.T) {
	require := require.New(t)

	paths, err := filepath.Glob("*.go")
	require.NoError(err)

	var failures []string
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") || path == "health_routes.go" {
			continue
		}
		source, err := os.ReadFile(path)
		require.NoError(err)
		failures = append(failures,
			collectConvenienceRouteMetadataFailures(path, source)...)
	}
	assert.Empty(t, failures, strings.Join(failures, "\n"))
}

// TestRouteMetadataWalkerCatchesUnannotatedRoute is a teeth-test: it builds
// a tiny in-process huma.API with one convenience-helper route that has no
// metadata callback, runs collectMetadataFailures, and asserts the walker
// reports at least one failure. Catches the case where collectMetadataFailures
// regresses into a no-op (for example by losing the Tags check) and keeps the
// contract test honest if huma changes how its convenience helpers fill in
// default Summary and OperationID values.
func TestRouteMetadataWalkerCatchesUnannotatedRoute(t *testing.T) {
	require := require.New(t)

	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))

	type emptyInput struct{}
	type emptyOutput struct{}
	huma.Get(api, "/unannotated", func(
		_ context.Context, _ *emptyInput,
	) (*emptyOutput, error) {
		return &emptyOutput{}, nil
	})

	failures := collectMetadataFailures(api.OpenAPI())
	require.NotEmpty(failures,
		"walker must flag unannotated routes; got no failures")
}

func TestRouteMetadataWalkerRejectsUnknownOrMultipleTags(t *testing.T) {
	require := require.New(t)

	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))

	type emptyInput struct{}
	type emptyOutput struct{}
	huma.Get(api, "/bad-tag", func(
		_ context.Context, _ *emptyInput,
	) (*emptyOutput, error) {
		return &emptyOutput{}, nil
	}, func(op *huma.Operation) {
		op.OperationID = "bad-tag"
		op.Summary = "Get bad tag"
		op.Tags = []string{"Pull Requests", "Not A Tag"}
	})

	failures := collectMetadataFailures(api.OpenAPI())
	require.Contains(failures,
		"GET /bad-tag: expected exactly one tag from the API tag taxonomy")
}

func TestConvenienceRouteMetadataWalkerRejectsTagOnlyCallback(t *testing.T) {
	require := require.New(t)

	source := []byte(`package server
func (s *Server) register(api huma.API) {
	huma.Get(api, "/tag-only", s.handler, func(op *huma.Operation) {
		op.Tags = []string{"Issues"}
	})
}`)

	failures := collectConvenienceRouteMetadataFailures("sample.go", source)
	require.Contains(failures,
		"sample.go:3: huma.Get must use documentOperation for OpenAPI metadata")
}
