package github

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	platformgithub "go.kenn.io/forge/platform/github"
)

func TestProviderReadsGetClientSpansUnderTheCaller(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	// A configured global propagator proves provider requests opt out of it.
	previous := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })

	var mu sync.Mutex
	var traceparents, validators []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/repos/owner/repo/pulls" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		traceparents = append(traceparents, r.Header.Get("traceparent"))
		validators = append(validators, r.Header.Get("If-None-Match"))
		mu.Unlock()
		if r.Header.Get("If-None-Match") == `"pulls-v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"pulls-v1"`)
		_, _ = w.Write([]byte(`[{"number":7,"state":"open"}]`))
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(testTokenSource("secret-token"), "github.com", nil, nil, WithBaseURLForTesting(server.URL))
	require.NoError(err)

	recorder := tracetest.NewSpanRecorder()
	ctx, parent := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)).Tracer("test").Start(t.Context(), "sync")
	first, err := client.ListOpenPullRequests(ctx, "owner", "repo")
	require.NoError(err)
	_, err = client.ListOpenPullRequests(ctx, "owner", "repo")
	parent.End()

	require.Len(first, 1)
	assert.Equal(7, first[0].GetNumber())
	assert.True(platformgithub.IsNotModified(err), "the second read reports the unchanged list: %v", err)
	assert.Equal([]string{"", `"pulls-v1"`}, validators, "the second read stays conditional")
	assert.Equal([]string{"", ""}, traceparents, "providers get no trace context")

	var statuses []int64
	for _, span := range recorder.Ended() {
		if span.SpanKind() != trace.SpanKindClient {
			continue
		}
		assert.Equal("GET "+strings.TrimPrefix(server.URL, "http://"), span.Name())
		assert.Equal(parent.SpanContext().SpanID(), span.Parent().SpanID())
		for _, attr := range span.Attributes() {
			assert.NotContains(attr.Value.String(), "secret-token", attr.Key)
			switch attr.Key {
			case "url.full":
				assert.Equal(server.URL+"/api/v3/repos/owner/repo/pulls", attr.Value.AsString())
			case "http.response.status_code":
				statuses = append(statuses, attr.Value.AsInt64())
			}
		}
	}
	assert.Equal([]int64{http.StatusOK, http.StatusNotModified}, statuses)
}
