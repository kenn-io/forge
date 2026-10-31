package tracing

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

// TraceRelay forwards OTLP/HTTP JSON trace batches from the browser to the
// collector the daemon's own trace exporter uses, so browser spans need no
// route to the collector of their own.
type TraceRelay struct {
	url     string
	headers http.Header
	client  *http.Client
}

// NewTraceRelayFromEnv reads the standard OpenTelemetry exporter environment.
// It returns nil unless traces are exported over OTLP/HTTP, the only protocol
// a browser batch can be forwarded to as it is.
func NewTraceRelayFromEnv() *TraceRelay {
	return newTraceRelay(os.Getenv)
}

func newTraceRelay(getenv func(string) string) *TraceRelay {
	if !slices.Contains(strings.Split(getenv("OTEL_TRACES_EXPORTER"), ","), "otlp") {
		return nil
	}
	protocol := firstNonEmpty(getenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL"), getenv("OTEL_EXPORTER_OTLP_PROTOCOL"), "http/protobuf")
	if !strings.HasPrefix(protocol, "http/") {
		return nil
	}
	endpoint := getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	if endpoint == "" {
		endpoint = strings.TrimSuffix(firstNonEmpty(getenv("OTEL_EXPORTER_OTLP_ENDPOINT"), "http://localhost:4318"), "/") + "/v1/traces"
	}
	// As in the daemon's exporter, trace headers replace the generic ones and
	// only values are percent-decoded.
	headers := http.Header{}
	for pair := range strings.SplitSeq(firstNonEmpty(getenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS"), getenv("OTEL_EXPORTER_OTLP_HEADERS")), ",") {
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value, err := url.PathUnescape(value)
		if err == nil && key != "" {
			headers.Set(key, strings.TrimSpace(value))
		}
	}
	// OTLP exporters default to a 10s export timeout. The client stays
	// untraced, or every forwarded batch would add a span to the next one.
	return &TraceRelay{url: endpoint, headers: headers, client: &http.Client{Timeout: 10 * time.Second}}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// Forward posts one OTLP JSON trace batch to the collector.
func (r *TraceRelay) Forward(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header = r.headers.Clone()
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("collector answered %s", resp.Status)
	}
	return nil
}
