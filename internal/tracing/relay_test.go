package tracing

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTraceRelayForwardsToTheDaemonCollector(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	var gotPath, gotAuth, gotType, gotBody string
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotPath, gotAuth, gotType, gotBody = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), string(body)
	}))
	t.Cleanup(collector.Close)
	env := map[string]string{
		"OTEL_TRACES_EXPORTER":        "otlp",
		"OTEL_EXPORTER_OTLP_ENDPOINT": collector.URL + "/",
		"OTEL_EXPORTER_OTLP_HEADERS":  "Authorization=Bearer%20collector-token",
	}

	relay := newTraceRelay(func(key string) string { return env[key] })
	require.NotNil(relay)
	require.NoError(relay.Forward(t.Context(), []byte(`{"resourceSpans":[]}`)))

	assert.Equal("/v1/traces", gotPath)
	assert.Equal("Bearer collector-token", gotAuth)
	assert.Equal("application/json", gotType)
	assert.JSONEq(`{"resourceSpans":[]}`, gotBody)
}

func TestTraceRelayReportsCollectorRejection(t *testing.T) {
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(collector.Close)
	env := map[string]string{"OTEL_TRACES_EXPORTER": "otlp", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": collector.URL + "/custom"}

	relay := newTraceRelay(func(key string) string { return env[key] })

	require.Error(t, relay.Forward(t.Context(), []byte(`{}`)))
}

func TestTraceRelayNeedsAnOTLPHTTPTraceExporter(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"no exporter":  {},
		"other export": {"OTEL_TRACES_EXPORTER": "console"},
		"grpc":         {"OTEL_TRACES_EXPORTER": "otlp", "OTEL_EXPORTER_OTLP_PROTOCOL": "grpc"},
	} {
		assert.Nil(t, newTraceRelay(func(key string) string { return env[key] }), name)
	}
}

func TestTraceRelaySendsOnlyTheHeadersTheDaemonExporterSends(t *testing.T) {
	assert := assert.New(t)
	var got http.Header
	collector := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
	}))
	t.Cleanup(collector.Close)
	env := map[string]string{
		"OTEL_TRACES_EXPORTER":              "otlp",
		"OTEL_EXPORTER_OTLP_ENDPOINT":       collector.URL,
		"OTEL_EXPORTER_OTLP_HEADERS":        "X-Generic-Secret=generic",
		"OTEL_EXPORTER_OTLP_TRACES_HEADERS": "Authorization=Basic a+b%3D",
	}

	require.NoError(t, newTraceRelay(func(key string) string { return env[key] }).Forward(t.Context(), []byte(`{}`)))

	assert.Equal("Basic a+b=", got.Get("Authorization"))
	assert.Empty(got.Get("X-Generic-Secret"))
}
