package settingsservertest

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/doordash-oss/oapi-codegen-dd/v3/pkg/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/telemetry"
	"go.kenn.io/forge/internal/testutil/dbtest"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	servertest "go.kenn.io/forge/internal/testutil/servertest"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

const telemetryEventsPath = "/api/v1/telemetry/events"

type telemetryServerKind int

const (
	optedOutServer telemetryServerKind = iota
	nilCaptureServer
	authServer
)

// telemetryRoute is one server under test and how often its capture handler was reached.
type telemetryRoute struct {
	srv     *server.Server
	reached *atomic.Int32
}

func newTelemetryRoute(t *testing.T, kind telemetryServerKind) telemetryRoute {
	t.Helper()
	t.Setenv(telemetry.EnabledEnv, "0")
	reporter, err := telemetry.NewReporter(telemetry.Options{})
	require.NoError(t, err)
	reached := &atomic.Int32{}
	capture := reporter.CaptureHandler()
	counted := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		capture.ServeHTTP(w, r)
	})
	switch kind {
	case nilCaptureServer:
		return telemetryRoute{srv: servertest.NewTelemetryTestServer(t, nil), reached: reached}
	case authServer:
		srv := server.New(dbtest.Open(t), nil, nil, "/", nil, server.ServerOptions{
			DaemonAccess:     authapi.DaemonAccessOptions{Token: "local-secret", RequireAPIAuth: true},
			TelemetryCapture: counted,
		})
		t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
		return telemetryRoute{srv: srv, reached: reached}
	default:
		return telemetryRoute{srv: servertest.NewTelemetryTestServer(t, counted), reached: reached}
	}
}

// failingBody yields a partial body and then a read error.
type failingBody struct{ sent bool }

func (b *failingBody) Read(p []byte) (int, error) {
	if b.sent {
		return 0, errors.New("connection reset")
	}
	b.sent = true
	return copy(p, `{"event":`), nil
}

// failingBodyClient is a generated client whose requests reach the server with a body that fails mid-read.
func failingBodyClient(t *testing.T, srv *server.Server) *apiclient.Client {
	t.Helper()
	client, err := apiclient.NewWithHTTPClient("http://forge.test", &http.Client{
		Transport: serverfake.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
			serverReq := httptest.NewRequestWithContext(req.Context(), req.Method, req.URL.String(), &failingBody{})
			serverReq.Header = req.Header.Clone()
			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, serverReq)
			return rr.Result(), nil
		}),
	})
	require.NoError(t, err)
	return client
}

func captureWithClient(t *testing.T, client *apiclient.Client, event string) (*generated.CaptureTelemetryEventResp, error) {
	t.Helper()
	return client.HTTP.CaptureTelemetryEventWithResponse(t.Context(), &generated.CaptureTelemetryEventRequestOptions{
		Body: &generated.CaptureTelemetryEventBody{Event: event},
	})
}

func TestCaptureTelemetryEventRoute(t *testing.T) {
	oversizedProperty := `{"event":"app_opened","properties":{"pad":"` + strings.Repeat("x", 64<<10+1) + `"}}`
	trailingWhitespace := `{"event":"app_opened"}` + strings.Repeat(" ", 64<<10)

	tests := []struct {
		name          string
		kind          telemetryServerKind
		body          string
		chunked       bool
		contentType   string
		noContentType bool
		authorization string
		traced        bool
		// check runs instead of the raw request when set.
		check       func(t *testing.T, route telemetryRoute)
		wantStatus  int
		wantBody    string
		wantProblem bool
		wantReached int32
	}{
		{name: "app_opened is accepted while opted out", body: `{"event":"app_opened"}`, wantStatus: http.StatusAccepted, wantBody: `{"status":"disabled"}`, wantReached: 1},
		{name: "blank event", body: `{"event":"   "}`, wantStatus: http.StatusBadRequest, wantBody: "unsupported telemetry event\n", wantReached: 1},
		{name: "malformed JSON", body: `{"event":`, wantStatus: http.StatusBadRequest, wantBody: "invalid telemetry request\n", wantReached: 1},
		{name: "app_loaded is retired", body: `{"event":"app_loaded"}`, wantStatus: http.StatusBadRequest, wantBody: "unsupported telemetry event\n", wantReached: 1},
		{name: "unknown event", body: `{"event":"repo_opened"}`, wantStatus: http.StatusBadRequest, wantBody: "unsupported telemetry event\n", wantReached: 1},
		{name: "daemon_active is not a UI event", body: `{"event":"daemon_active"}`, wantStatus: http.StatusBadRequest, wantBody: "unsupported telemetry event\n", wantReached: 1},
		{name: "oversized property", body: oversizedProperty, wantStatus: http.StatusRequestEntityTooLarge, wantProblem: true},
		{name: "trailing whitespace past the limit with unknown length", body: trailingWhitespace, chunked: true, wantStatus: http.StatusRequestEntityTooLarge, wantProblem: true},
		{name: "text/plain content type", body: `{"event":"app_opened"}`, contentType: "text/plain", wantStatus: http.StatusUnsupportedMediaType, wantProblem: true},
		{name: "JSON with charset", body: `{"event":"app_opened"}`, contentType: "application/json; charset=utf-8", wantStatus: http.StatusAccepted, wantBody: `{"status":"disabled"}`, wantReached: 1},
		{name: "+json suffix", body: `{"event":"app_opened"}`, contentType: "application/vnd.test+json", wantStatus: http.StatusUnsupportedMediaType, wantProblem: true},
		{name: "merge-patch+json suffix", body: `{"event":"app_opened"}`, contentType: "application/merge-patch+json", wantStatus: http.StatusUnsupportedMediaType, wantProblem: true},
		{name: "absent content type", body: `{"event":"app_opened"}`, noContentType: true, wantStatus: http.StatusUnsupportedMediaType, wantProblem: true},
		{name: "nil capture admits no event", kind: nilCaptureServer, body: `{"event":"app_opened"}`, wantStatus: http.StatusBadRequest, wantBody: "unsupported telemetry event\n"},
		{name: "auth without token", kind: authServer, body: `{"event":"app_opened"}`, wantStatus: http.StatusUnauthorized, wantProblem: true},
		{name: "auth with wrong token", kind: authServer, body: `{"event":"app_opened"}`, authorization: "Bearer wrong-token", wantStatus: http.StatusUnauthorized, wantProblem: true},
		{name: "auth with token", kind: authServer, body: `{"event":"app_opened"}`, authorization: "Bearer local-secret", wantStatus: http.StatusAccepted, wantBody: `{"status":"disabled"}`, wantReached: 1},
		{name: "route runs the API middlewares", body: `{"event":"app_opened"}`, traced: true, wantStatus: http.StatusAccepted, wantBody: `{"status":"disabled"}`, wantReached: 1},
		{name: "generated client accepts app_opened", check: func(t *testing.T, route telemetryRoute) {
			response, err := captureWithClient(t, servertest.SetupTestClient(t, route.srv), "app_opened")
			require.NoError(t, err)
			require.NotNil(t, response.JSON202)
			assert.Equal(t, "disabled", response.JSON202.Status)
		}, wantReached: 1},
		{name: "generated client keeps a plain-text 400 raw", check: func(t *testing.T, route telemetryRoute) {
			response, err := captureWithClient(t, servertest.SetupTestClient(t, route.srv), "app_loaded")
			apiErr, ok := errors.AsType[*runtime.ClientAPIError](err)
			require.True(t, ok, "error %v", err)
			assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode())
			require.NotNil(t, response)
			assert.Nil(t, response.Error)
			assert.Equal(t, "unsupported telemetry event\n", string(response.Body))
		}, wantReached: 1},
		{name: "generated client decodes a body-read problem", check: func(t *testing.T, route telemetryRoute) {
			response, err := captureWithClient(t, failingBodyClient(t, route.srv), "app_opened")
			require.Error(t, err)
			require.NotNil(t, response)
			require.NotNil(t, response.Error)
			require.NotNil(t, response.Error.Status)
			assert.Equal(t, int64(http.StatusBadRequest), *response.Error.Status)
		}},
		{name: "generated client decodes a daemon 401 problem", kind: authServer, check: func(t *testing.T, route telemetryRoute) {
			response, err := captureWithClient(t, servertest.SetupTestClient(t, route.srv), "app_opened")
			require.Error(t, err)
			require.NotNil(t, response)
			require.NotNil(t, response.Error)
			require.NotNil(t, response.Error.Status)
			assert.Equal(t, int64(http.StatusUnauthorized), *response.Error.Status)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			var recorder *tracetest.SpanRecorder
			if tt.traced {
				recorder = recordSpans(t)
			}
			route := newTelemetryRoute(t, tt.kind)
			if tt.check != nil {
				tt.check(t, route)
				assert.Equal(tt.wantReached, route.reached.Load())
				return
			}

			var body io.Reader = strings.NewReader(tt.body)
			if tt.chunked {
				body = io.MultiReader(body)
			}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, telemetryEventsPath, body)
			if !tt.noContentType {
				req.Header.Set("Content-Type", "application/json")
			}
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			if tt.authorization != "" {
				req.Header.Set("Authorization", tt.authorization)
			}
			if tt.traced {
				req.Header.Set("baggage", "workspace.id=ws-telemetry")
			}
			rr := httptest.NewRecorder()
			route.srv.ServeHTTP(rr, req)

			assert.Equal(tt.wantStatus, rr.Code, rr.Body.String())
			if tt.wantProblem {
				assert.Equal("application/problem+json", rr.Header().Get("Content-Type"))
			}
			if strings.HasPrefix(tt.wantBody, "{") {
				assert.JSONEq(tt.wantBody, rr.Body.String())
			} else if tt.wantBody != "" {
				assert.Equal(tt.wantBody, rr.Body.String())
			}
			assert.Equal(tt.wantReached, route.reached.Load())
			if tt.traced {
				assertBaggageOnSpan(t, recorder, "POST "+telemetryEventsPath, "ws-telemetry")
			}
		})
	}
}

// recordSpans installs a span recorder and baggage propagation for the test.
func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	prevProp := otel.GetTextMapPropagator()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
		otel.SetTextMapPropagator(prevProp)
	})
	return recorder
}

// assertBaggageOnSpan checks the Huma OTel middleware copied baggage onto the route's span.
func assertBaggageOnSpan(t *testing.T, recorder *tracetest.SpanRecorder, name, workspaceID string) {
	t.Helper()
	for _, span := range recorder.Ended() {
		if span.Name() != name {
			continue
		}
		for _, kv := range span.Attributes() {
			if kv.Key == "workspace.id" && kv.Value.Type() == attribute.STRING {
				assert.Equal(t, workspaceID, kv.Value.AsString())
				return
			}
		}
	}
	assert.Fail(t, "no span carried the request baggage", name)
}
