package settingsservertest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/server/roborevapi"
	"go.kenn.io/forge/internal/testutil"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	servertest "go.kenn.io/forge/internal/testutil/servertest"
)

func TestRoborevHealthProbeAvailable(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)

	daemon := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/status" {
				w.Header().Set(
					"Content-Type", "application/json",
				)
				_, _ = w.Write(
					[]byte(`{"version":"1.2.3"}`),
				)
				return
			}
			http.NotFound(w, r)
		},
	))
	defer daemon.Close()

	srv := servertest.SetupTestServerWithRoborev(t, daemon.URL)

	rr := testutil.DoJSON(
		t, srv, http.MethodGet,
		"/api/v1/roborev/status", nil)

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp roborevapi.RoborevStatusResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.True(resp.Available)
	assert.Equal("1.2.3", resp.Version)
	assert.Equal(daemon.URL, resp.Endpoint)
}

func TestRoborevHealthProbeUnavailable(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)

	srv := servertest.SetupTestServerWithRoborev(t, "http://127.0.0.1:1")

	rr := testutil.DoJSON(
		t, srv, http.MethodGet,
		"/api/v1/roborev/status", nil)

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp roborevapi.RoborevStatusResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.False(resp.Available)
	assert.Empty(resp.Version)
}

func TestRoborevAuthenticatedRequests(t *testing.T) {
	for _, key := range []string{"", strings.Repeat("ab", 32)} {
		t.Run(key, func(t *testing.T) {
			assert := assert.New(t)
			t.Setenv("KENN_FORGE_ROBOREV_AUTH_KEY", key)
			var calls atomic.Int64
			daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				want := ""
				if key != "" {
					want = "Bearer " + key
				}
				assert.Equal(want, r.Header.Get("Authorization"))
				assert.Empty(r.Header.Get("Cookie"))
				switch r.URL.Path {
				case "/api/status":
					_, _ = io.WriteString(w, `{"version":"1.2.3"}`)
				case "/api/repos":
					_, _ = io.WriteString(w, `{"repos":[],"total_count":0}`)
				default:
					w.Header().Set("Content-Type", "application/x-ndjson")
					_, _ = io.WriteString(w, "{\"ok\":true}\n")
				}
			}))
			defer daemon.Close()
			srv := servertest.SetupTestServerWithRoborev(t, daemon.URL)
			for _, tc := range []struct{ method, path string }{
				{http.MethodGet, "/api/v1/roborev/status"},
				{http.MethodGet, "/api/v1/roborev/configured-repositories"},
				{http.MethodPost, "/api/roborev/api/jobs/1/retry"},
				{http.MethodGet, "/api/roborev/api/stream/events"},
				{http.MethodGet, "/api/roborev/api/job/output?job_id=1&stream=1"},
				{http.MethodPost, "/api/roborev/api/sync/now?stream=1"},
			} {
				req := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil)
				req.Header.Set("Authorization", "Bearer forge-caller-secret")
				req.Header.Set("Cookie", "forge_session=caller-cookie")
				req.Header.Set("Accept", "application/json")
				if strings.HasPrefix(tc.path, "/api/roborev/") {
					req.Header.Set("Accept", "application/x-ndjson")
				}
				rr := httptest.NewRecorder()
				srv.ServeHTTP(rr, req)
				require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
				if tc.path == "/api/v1/roborev/status" {
					assert.Contains(rr.Body.String(), `"available":true`)
				}
			}
			assert.Equal(int64(6), calls.Load())
		})
	}
}

func TestRoborevStatusRejectsUnauthorizedJSON(t *testing.T) {
	t.Setenv("KENN_FORGE_ROBOREV_AUTH_KEY", strings.Repeat("ab", 32))
	var calls atomic.Int64
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"version":"denied"}`)
	}))
	defer daemon.Close()
	srv := servertest.SetupTestServerWithRoborev(t, daemon.URL)
	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/roborev/status", nil)
	var resp roborevapi.RoborevStatusResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.False(t, resp.Available)
	assert.Empty(t, resp.Version)
	assert.Equal(t, int64(1), calls.Load())
}

func TestRoborevIncorrectCredentialIsNotRetried(t *testing.T) {
	t.Setenv("KENN_FORGE_ROBOREV_AUTH_KEY", strings.Repeat("ab", 32))
	var calls atomic.Int64
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("cd", 32) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"unauthorized"}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer daemon.Close()
	srv := servertest.SetupTestServerWithRoborev(t, daemon.URL)
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/api/roborev/api/jobs/1/retry", http.StatusUnauthorized},
		{"/api/v1/roborev/configured-repositories", http.StatusServiceUnavailable},
	} {
		method := http.MethodGet
		if tc.status == http.StatusUnauthorized {
			method = http.MethodPost
		}
		rr := testutil.DoJSON(t, srv, method, tc.path, nil)
		assert.Equal(t, tc.status, rr.Code, rr.Body.String())
	}
	assert.Equal(t, int64(2), calls.Load())
}
