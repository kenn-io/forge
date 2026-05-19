package server

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// collectMetadataFailures walks an OpenAPI document and returns one entry per
// missing metadata field on every non-nil operation. The returned slice is
// sorted so failure output is stable across test runs.
//
// The walker checks each operation for a non-empty Summary, a non-empty
// OperationID, at least one non-empty Tag, and a globally-unique OperationID.
// It deliberately does not consult huma's internal _convenience_summary and
// _convenience_id markers: those markers fire when an explicit value happens
// to match what huma would auto-generate ("List issues" for GET /issues), so
// they are not a reliable signal of "this was never set on purpose". The
// non-empty Tag check carries that load instead, because huma never sets a
// default tag list.
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

// TestHumaContractMetadata asserts that every non-Hidden operation in the
// live OpenAPI document carries an explicit Summary, at least one Tag, and a
// unique non-empty OperationID.
func TestHumaContractMetadata(t *testing.T) {
	require := require.New(t)
	openAPI := NewOpenAPI()
	require.NotNil(openAPI)
	require.NotEmpty(openAPI.Paths, "OpenAPI document should expose paths")

	failures := collectMetadataFailures(openAPI)
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
