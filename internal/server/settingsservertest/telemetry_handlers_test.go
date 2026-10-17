package settingsservertest

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/telemetryapi"
	telemetrypkg "go.kenn.io/forge/internal/telemetry"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	servertest "go.kenn.io/forge/internal/testutil/servertest"
	"go.kenn.io/kit/telemetry/posthog"
)

func TestCaptureTelemetryEvent_UsesKitReportResult(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	for _, tc := range []struct {
		name   string
		status posthog.Status
		err    error
		code   int
	}{
		{"queued", posthog.StatusQueued, nil, http.StatusAccepted},
		{"skipped", posthog.StatusSkipped, nil, http.StatusAccepted},
		{"disabled", posthog.StatusDisabled, nil, http.StatusAccepted},
		{"invalid property", "", posthog.ErrInvalidProperty, http.StatusBadRequest},
		{"storage failure", "", errors.New("storage unavailable"), http.StatusInternalServerError},
		{"queue failure", "", errors.New("queue full"), http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			telemetry := &serverfake.FakeTelemetry{EnabledValue: true, ReportStatus: tc.status, ReportError: tc.err}
			srv := servertest.NewTelemetryTestServer(t, telemetry)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/telemetry/events", strings.NewReader(`{"event":"screen_viewed","properties":{"screen":"activity","surface":"web"}}`))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, req)
			assert.Equal(tc.code, rr.Code)
			if tc.err != nil {
				assert.Empty(telemetry.Event)
				return
			}
			var body telemetryapi.TelemetryEventResponse
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
			assert.Equal(string(tc.status), body.Status)
			assert.Equal("screen_viewed", telemetry.Event)
			assert.Equal(map[string]any{"screen": "activity", "surface": "web"}, telemetry.Properties)
		})
	}
}

func TestCaptureTelemetryEvent_QueuesEvent(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)

	telemetry := &serverfake.FakeTelemetry{EnabledValue: true}
	srv := servertest.NewTelemetryTestServer(t, telemetry)

	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodPost,
		"/api/v1/telemetry/events",
		strings.NewReader(`{"event":"app_opened","properties":{"surface":"web"}}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	assert.Equal(http.StatusAccepted, rr.Code)
	assert.Equal("app_opened", telemetry.Event)
	assert.Equal(map[string]any{"surface": "web"}, telemetry.Properties)

	var body telemetryapi.TelemetryEventResponse
	err := json.NewDecoder(rr.Body).Decode(&body)
	require.NoError(err)
	assert.Equal("queued", body.Status)
}

func TestCaptureTelemetryEvent_ReturnsDisabledWhenTelemetryUnavailable(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	for _, tc := range []struct {
		name      string
		telemetry *serverfake.FakeTelemetry
		payload   string
		code      int
	}{
		{"unavailable", nil, `{"event":"app_opened"}`, http.StatusAccepted},
		{"disabled", &serverfake.FakeTelemetry{EnabledValue: false}, `{"event":"app_opened"}`, http.StatusAccepted},
		{"invalid duration unavailable", nil, `{"event":"session_ended","properties":{}}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			srv := servertest.NewTelemetryTestServer(t, tc.telemetry)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/telemetry/events", strings.NewReader(tc.payload))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, req)
			assert.Equal(tc.code, rr.Code)
			if tc.code == http.StatusBadRequest {
				assert.Contains(rr.Body.String(), "unsupported or missing session duration")
				return
			}
			var body telemetryapi.TelemetryEventResponse
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
			assert.Equal("disabled", body.Status)
			if tc.telemetry != nil {
				assert.Empty(tc.telemetry.Event)
			}
		})
	}
}

func TestCaptureTelemetryEvent_SessionDuration(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	for _, tc := range []struct {
		name, properties string
		valid            bool
	}{
		{"under_1m", `{"duration_bucket":"under_1m"}`, true},
		{"missing", `{}`, false},
		{"unknown", `{"duration_bucket":"all_day"}`, false},
		{"null", `{"duration_bucket":null}`, false},
		{"number", `{"duration_bucket":31}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			telemetry := &serverfake.FakeTelemetry{EnabledValue: true}
			srv := servertest.NewTelemetryTestServer(t, telemetry)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/telemetry/events", strings.NewReader(`{"event":"session_ended","properties":`+tc.properties+`}`))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, req)
			if !tc.valid {
				assert.Equal(http.StatusBadRequest, rr.Code)
				assert.Contains(rr.Body.String(), "unsupported or missing session duration")
				assert.Empty(telemetry.Event)
				return
			}
			assert.Equal(http.StatusAccepted, rr.Code)
			assert.Equal("session_ended", telemetry.Event)
			assert.Equal(tc.name, telemetry.Properties["duration_bucket"])
		})
	}
}

func TestCaptureTelemetryEvent_CoreActions(t *testing.T) {
	t.Setenv(telemetrypkg.EnabledEnv, "1")
	t.Setenv("KENN_FORGE_TELEMETRY_ENABLED", "1")
	for _, tc := range []struct {
		event, property string
		values          []string
	}{
		{"pr_viewed", "provider", []string{"github", "gitlab", "forgejo", "gitea", "bitbucket"}},
		{"workspace_opened", "item_type", []string{"pull_request", "issue", "kata_task", "adhoc"}},
		{"search_empty", "surface", []string{"web"}},
		{"error_shown", "kind", []string{"auth", "rate_limited", "not_found", "network", "provider", "other"}},
	} {
		t.Run(tc.event, func(t *testing.T) {
			type capturedEvent struct {
				Event      string         `json:"event"`
				Properties map[string]any `json:"properties"`
			}
			batches := make(chan []capturedEvent, 1)
			collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var batch struct {
					Batch []capturedEvent `json:"batch"`
				}
				if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				batches <- batch.Batch
				w.WriteHeader(http.StatusOK)
			}))
			defer collector.Close()
			reporter, err := posthog.NewReporter(posthog.Options{
				APIKey: "test-key", Endpoint: collector.URL, Application: "kenn-forge", EnvPrefix: "KENN_FORGE", Source: "backend", DistinctID: "test-install",
			}, telemetrypkg.KitAllowedEvents("backend")...)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, reporter.Close()) })
			srv := server.New(serverfake.OpenTestDB(t), nil, nil, "/", nil, server.ServerOptions{Telemetry: reporter})
			t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
			var values []any
			for _, value := range tc.values {
				values = append(values, value)
			}
			if tc.event != "search_empty" {
				values = append(values, "unknown", 23)
			}
			for _, value := range values {
				payload, err := json.Marshal(map[string]any{"event": tc.event, "properties": map[string]any{tc.property: value, "surface": "web", "repo": "acme/widgets", "query": "private query", "message": "private error", "path": "/private/path", "id": 42}})
				require.NoError(t, err)
				req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/telemetry/events", strings.NewReader(string(payload)))
				req.Header.Set("Content-Type", "application/json")
				rr := httptest.NewRecorder()
				srv.ServeHTTP(rr, req)
				require.Equal(t, http.StatusAccepted, rr.Code, rr.Body.String())
				var response telemetryapi.TelemetryEventResponse
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response))
				assert.Equal(t, "queued", response.Status)
			}
			require.NoError(t, reporter.Close())
			select {
			case events := <-batches:
				require.Len(t, events, len(values))
				for i, event := range events {
					assert.Equal(t, tc.event, event.Event)
					assert.Equal(t, "web", event.Properties["surface"])
					assert.NotContains(t, event.Properties, "repo")
					assert.NotContains(t, event.Properties, "query")
					assert.NotContains(t, event.Properties, "message")
					assert.NotContains(t, event.Properties, "path")
					assert.NotContains(t, event.Properties, "id")
					if i < len(tc.values) {
						assert.Equal(t, tc.values[i], event.Properties[tc.property])
					} else if tc.event == "error_shown" {
						assert.Equal(t, "other", event.Properties[tc.property])
					} else {
						assert.NotContains(t, event.Properties, tc.property)
					}
				}
			case <-time.After(5 * time.Second):
				require.FailNow(t, "telemetry collector received no batch")
			}
		})
	}
}

func TestCaptureTelemetryEventRejectsDaemonOutcome(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	reporter := &serverfake.FakeTelemetry{EnabledValue: true}
	srv := servertest.NewTelemetryTestServer(t, reporter)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/telemetry/events", strings.NewReader(`{"event":"sync_failed","properties":{"reason":"auth"}}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Empty(t, reporter.Event)
}
