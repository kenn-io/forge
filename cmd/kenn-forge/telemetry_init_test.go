package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"go.kenn.io/forge/internal/tracing"
)

// Daemon clients are built before initTelemetry runs, and the exporter's own
// requests must not trace themselves.
func TestTelemetryExportsClientSpansFromTransportsBuiltBeforeInit(t *testing.T) {
	require := require.New(t)
	var mu sync.Mutex
	var exported []string
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || r.URL.Path != "/v1/traces" {
			http.Error(w, "unexpected export", http.StatusBadRequest)
			return
		}
		var batch coltracepb.ExportTraceServiceRequest
		if err := proto.Unmarshal(body, &batch); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, resource := range batch.GetResourceSpans() {
			for _, scope := range resource.GetScopeSpans() {
				for _, span := range scope.GetSpans() {
					exported = append(exported, span.GetKind().String()+" "+span.GetName())
				}
			}
		}
	}))
	t.Cleanup(collector.Close)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(provider.Close)
	for key, value := range map[string]string{
		"OTEL_TRACES_EXPORTER":        "otlp",
		"OTEL_METRICS_EXPORTER":       "none",
		"OTEL_LOGS_EXPORTER":          "none",
		"OTEL_EXPORTER_OTLP_ENDPOINT": collector.URL,
		"OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
		"OTEL_BSP_SCHEDULE_DELAY":     "10",
	} {
		t.Setenv(key, value)
	}
	previousTracers, previousMeters, previousPropagator := otel.GetTracerProvider(), otel.GetMeterProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(previousTracers)
		otel.SetMeterProvider(previousMeters)
		otel.SetTextMapPropagator(previousPropagator)
	})
	client := &http.Client{Transport: tracing.HTTPTransport(nil)}

	stop, err := initTelemetry(t.Context(), "kenn-forge-test")
	require.NoError(err)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, provider.URL, nil)
	require.NoError(err)
	response, err := client.Do(request)
	require.NoError(err)
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(err)
	require.NoError(response.Body.Close())
	require.Eventually(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(exported) > 0
	}, 5*time.Second, 10*time.Millisecond)
	// A traced export would have ended its own span by now; shutdown flushes it.
	stop()

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{
		tracepb.Span_SPAN_KIND_CLIENT.String() + " GET " + strings.TrimPrefix(provider.URL, "http://"),
	}, exported)
}
