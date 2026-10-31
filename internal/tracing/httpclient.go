package tracing

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// HTTPTransport wraps base, or http.DefaultTransport when base is nil, so
// each request it sends gets an OpenTelemetry client span named after the
// method and host. It sends no trace context: third-party APIs cannot join
// Forge traces, and an added header must not change provider responses.
//
// Wrap the transport that reaches the network, beneath caches, budgets,
// and authentication, so the span measures the remote call alone.
func HTTPTransport(base http.RoundTripper) http.RoundTripper {
	return newClientTransport(base, propagation.NewCompositeTextMapPropagator())
}

// PeerHTTPTransport is HTTPTransport for requests to other Forge daemons.
// It also sends the global trace context and baggage, so the peer's server
// spans join the caller's trace.
func PeerHTTPTransport(base http.RoundTripper) http.RoundTripper {
	// otelhttp's default is the global propagator, which delegates to the
	// one telemetry.Init installs even when this transport is built first.
	return newClientTransport(base, nil)
}

func newClientTransport(base http.RoundTripper, propagators propagation.TextMapPropagator) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	options := []otelhttp.Option{
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Host
		}),
	}
	if propagators != nil {
		options = append(options, otelhttp.WithPropagators(propagators))
	}
	return &clientTransport{
		base:   base,
		traced: otelhttp.NewTransport(spanURLRedactor{base: base}, options...),
	}
}

type clientSpanKey struct{}

// clientTransport sends untraced requests straight to base, so a request
// already inside a client span, such as a peer transport wrapped around a
// traced one, gets one span, and long-lived streams get none.
type clientTransport struct {
	base   http.RoundTripper
	traced http.RoundTripper
}

func (t *clientTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Context().Value(clientSpanKey{}) != nil || longLived(r) {
		return t.base.RoundTrip(r)
	}
	return t.traced.RoundTrip(r)
}

// CloseIdleConnections forwards to base, so http.Client.CloseIdleConnections
// still drains the wrapped connection pool.
func (t *clientTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// longLived reports whether r opens a connection that lives as long as the
// caller wants: a protocol upgrade such as a WebSocket, or a stream in one
// of the media types the server also treats as long-lived
// (internal/server/routepolicy/transport_inventory.go::isLongLivedMediaType).
// A span would last until the stream ends.
func longLived(r *http.Request) bool {
	if r.Header.Get("Upgrade") != "" {
		return true
	}
	for _, accept := range r.Header.Values("Accept") {
		for mediaRange := range strings.SplitSeq(accept, ",") {
			mediaType, _, _ := strings.Cut(mediaRange, ";")
			switch strings.ToLower(strings.TrimSpace(mediaType)) {
			case "text/event-stream", "application/x-ndjson":
				return true
			}
		}
	}
	return false
}

// spanURLRedactor runs inside the client span. otelhttp records the query
// string in url.full, and queries can carry tokens or signatures, so it
// replaces url.full with the scheme, host, and path. It also marks the
// context so a traced transport beneath it adds no second span.
type spanURLRedactor struct {
	base http.RoundTripper
}

func (t spanURLRedactor) RoundTrip(r *http.Request) (*http.Response, error) {
	redacted := url.URL{Scheme: r.URL.Scheme, Host: r.URL.Host, Path: r.URL.Path, RawPath: r.URL.RawPath}
	trace.SpanFromContext(r.Context()).SetAttributes(semconv.URLFull(redacted.String()))
	return t.base.RoundTrip(r.WithContext(context.WithValue(r.Context(), clientSpanKey{}, struct{}{})))
}
