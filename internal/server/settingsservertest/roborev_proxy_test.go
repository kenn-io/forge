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
	tailscaleHeaders := map[string]string{
		"Tailscale-User-Login":       "user@example.com",
		"Tailscale-User-Name":        "Example User",
		"Tailscale-User-Profile-Pic": "https://example.com/profile.png",
		"Tailscale-App-Capabilities": "{}",
	}
	for _, mode := range []struct{ name, key string }{
		{name: "unkeyed"},
		{name: "keyed", key: strings.Repeat("ab", 32)},
	} {
		t.Run(mode.name, func(t *testing.T) {
			assert := assert.New(t)
			t.Setenv("KENN_FORGE_ROBOREV_AUTH_KEY", mode.key)
			const record = "{\"line_type\":\"stdout\",\"text\":\"review started\"}\n"
			daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				want := ""
				if mode.key != "" {
					want = "Bearer " + mode.key
				}
				assert.Equal(want, r.Header.Get("Authorization"))
				assert.Empty(r.Header.Get("Cookie"))
				for name := range tailscaleHeaders {
					assert.Empty(r.Header.Get(name), name)
				}
				switch r.URL.Path {
				case "/reviews/api/status":
					_, _ = io.WriteString(w, `{"version":"1.2.3"}`)
				case "/reviews/api/repos":
					_, _ = io.WriteString(w, `{"repos":[],"total_count":0}`)
				case "/reviews/api/job/output":
					w.Header().Set("Content-Type", "application/x-ndjson")
					_, _ = io.WriteString(w, record)
				default:
					http.NotFound(w, r)
				}
			}))
			defer daemon.Close()
			srv := servertest.SetupTestServerWithRoborev(t, daemon.URL+"/reviews")
			for _, tc := range []struct{ path, accept, wantBody string }{
				{"/api/v1/roborev/status", "application/json", `"available":true`},
				{"/api/v1/roborev/configured-repositories", "application/json", `"repositories":[]`},
				{"/api/roborev/api/job/output?job_id=1&stream=1", "application/x-ndjson", record},
			} {
				req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, nil)
				req.Host = "127.0.0.1:8091"
				req.RemoteAddr = "127.0.0.1:12345"
				req.Header.Set("Authorization", "Bearer forge-caller-secret")
				req.Header.Set("Cookie", "forge_session=caller-cookie")
				req.Header.Set("Accept", tc.accept)
				for name, value := range tailscaleHeaders {
					req.Header.Set(name, value)
				}
				rr := httptest.NewRecorder()
				srv.ServeHTTP(rr, req)
				require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
				assert.Contains(rr.Body.String(), tc.wantBody)
			}
		})
	}
}

func TestRoborevIncorrectCredentialIsNotRetried(t *testing.T) {
	assert := assert.New(t)
	key := strings.Repeat("ab", 32)
	t.Setenv("KENN_FORGE_ROBOREV_AUTH_KEY", key)
	const unauthorized = `{"error":"unauthorized"}`
	var calls atomic.Int64
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("Bearer "+key, r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/api/status":
			assert.Equal(http.MethodGet, r.Method)
		case "/api/job/rerun":
			assert.Equal(http.MethodPost, r.Method)
			var body struct {
				JobID int64 `json:"job_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid rerun payload", http.StatusBadRequest)
				return
			}
			assert.Equal(int64(1), body.JobID)
		default:
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, unauthorized)
	}))
	defer daemon.Close()
	srv := servertest.SetupTestServerWithRoborev(t, daemon.URL)
	status := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/roborev/status", nil)
	require.Equal(t, http.StatusOK, status.Code, status.Body.String())
	var resp roborevapi.RoborevStatusResponse
	require.NoError(t, json.NewDecoder(status.Body).Decode(&resp))
	assert.False(resp.Available)
	assert.Equal(int64(1), calls.Load())

	rerun := testutil.DoJSON(t, srv, http.MethodPost, "/api/roborev/api/job/rerun", map[string]int{"job_id": 1})
	assert.Equal(http.StatusUnauthorized, rerun.Code)
	assert.JSONEq(unauthorized, rerun.Body.String())
	assert.Equal(int64(2), calls.Load())
}
