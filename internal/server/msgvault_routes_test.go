package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/config"
	ptyownerruntime "go.kenn.io/middleman/internal/ptyowner/runtime"
	"go.kenn.io/middleman/internal/workspace/localruntime"
)

type msgvaultHealthWire struct {
	Configured   bool           `json:"configured"`
	OK           bool           `json:"ok"`
	Status       string         `json:"status,omitempty"`
	StatusDetail string         `json:"status_detail,omitempty"`
	Modes        []string       `json:"modes"`
	Features     map[string]any `json:"features"`
	URL          string         `json:"url,omitempty"`
	APIKeyEnv    string         `json:"api_key_env,omitempty"`
}

type msgvaultRuntimeOwner struct {
	startedStripEnvVars []string
	pty                 *msgvaultRuntimePTY
}

type msgvaultRuntimePTY struct {
	output chan []byte
	done   chan struct{}
}

func (m *msgvaultRuntimeOwner) HasState(string) bool {
	return m.pty != nil
}

func (m *msgvaultRuntimeOwner) Attach(context.Context, string) (ptyownerruntime.PTY, error) {
	return m.pty, nil
}

func (m *msgvaultRuntimeOwner) Start(
	_ context.Context,
	_ string,
	_ string,
	_ []string,
	stripEnvVars []string,
) (ptyownerruntime.PTY, error) {
	m.startedStripEnvVars = append([]string(nil), stripEnvVars...)
	m.pty = &msgvaultRuntimePTY{
		output: make(chan []byte),
		done:   make(chan struct{}),
	}
	return m.pty, nil
}

func (m *msgvaultRuntimeOwner) Stop(context.Context, string) error {
	if m.pty != nil {
		m.pty.Close()
	}
	return nil
}

func (p *msgvaultRuntimePTY) Output() <-chan []byte { return p.output }
func (p *msgvaultRuntimePTY) Done() <-chan struct{} { return p.done }
func (p *msgvaultRuntimePTY) Write([]byte) error    { return nil }
func (p *msgvaultRuntimePTY) Resize(int, int) error { return nil }
func (p *msgvaultRuntimePTY) ExitCode() int         { return 0 }

func (p *msgvaultRuntimePTY) Close() {
	select {
	case <-p.done:
	default:
		close(p.done)
		close(p.output)
	}
}

func setupMsgvaultRouteServer(t *testing.T, cfg *config.Config) *Server {
	t.Helper()
	srv := New(openTestDB(t), nil, nil, "/", cfg, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	return srv
}

func setupPersistentMsgvaultRouteServer(t *testing.T, cfg *config.Config) (*Server, string) {
	t.Helper()
	if cfg == nil {
		cfg = &config.Config{}
	}
	cfg.SyncInterval = "5m"
	cfg.Host = "127.0.0.1"
	cfg.Port = 8091
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, cfg.Save(cfgPath))
	loaded, err := config.Load(cfgPath)
	require.NoError(t, err)
	srv := NewWithConfig(openTestDB(t), nil, nil, nil, loaded, cfgPath, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	return srv, cfgPath
}

func doMsgvaultJSON(t *testing.T, srv *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	setAcceptedHostForServerTest(req, srv)
	req.RemoteAddr = "127.0.0.1:12345"
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(middlemanCSRFHeaderName, "1")
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

func doMsgvaultRaw(t *testing.T, srv *Server, method, path, remoteAddr, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	return doMsgvaultRawWithCSRF(t, srv, method, path, remoteAddr, contentType, body, true)
}

func doMsgvaultRawWithoutCSRF(t *testing.T, srv *Server, method, path, remoteAddr, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	return doMsgvaultRawWithCSRF(t, srv, method, path, remoteAddr, contentType, body, false)
}

func doMsgvaultRawWithCSRF(
	t *testing.T,
	srv *Server,
	method,
	path,
	remoteAddr,
	contentType,
	body string,
	withCSRF bool,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	setAcceptedHostForServerTest(req, srv)
	req.RemoteAddr = remoteAddr
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if method != http.MethodGet && withCSRF {
		req.Header.Set(middlemanCSRFHeaderName, "1")
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

func decodeMsgvaultHealth(t *testing.T, rr *httptest.ResponseRecorder) msgvaultHealthWire {
	t.Helper()
	var body msgvaultHealthWire
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	return body
}

func decodeMsgvaultHealthMap(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	return body
}

func decodeMsgvaultProblem(t *testing.T, rr *httptest.ResponseRecorder) ProblemError {
	t.Helper()
	var body ProblemError
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	return body
}

func configuredMsgvaultRouteServer(t *testing.T, upstream http.Handler) (*Server, func()) {
	t.Helper()
	srv := httptest.NewServer(upstream)
	t.Setenv("MSGVAULT_API_KEY_TEST", "secret-key")
	server := setupMsgvaultRouteServer(t, &config.Config{
		Msgvault: &config.Msgvault{URL: srv.URL, APIKeyEnv: "MSGVAULT_API_KEY_TEST"},
	})
	return server, srv.Close
}

func msgvaultOKUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/stats":
			_, _ = w.Write([]byte(`{"total_messages":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestMsgvaultHealthAbsentConfig(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv := setupMsgvaultRouteServer(t, &config.Config{})

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/health", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	body := decodeMsgvaultHealth(t, rr)
	assert.False(body.Configured)
	assert.False(body.OK)
	assert.Empty(body.Status)
	assert.Empty(body.URL)
	assert.Empty(body.APIKeyEnv)
	assert.Empty(body.Modes)
	assert.False(body.Features["threads_endpoint"].(bool))
}

func TestMsgvaultDoesNotMountLegacyMessageAPIRoutes(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	srv := setupMsgvaultRouteServer(t, &config.Config{})

	tests := []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodGet, path: "/api/v1/mail/health"},
		{method: http.MethodGet, path: "/api/v1/mail/search"},
		{method: http.MethodGet, path: "/api/v1/mail/messages/1001"},
		{method: http.MethodGet, path: "/api/v1/mail/threads/1001"},
		{method: http.MethodPost, path: "/api/v1/mail/configure", body: `{"url":"http://127.0.0.1:9","api_key_env":"MSGVAULT_API_KEY"}`},
		{method: http.MethodGet, path: "/api/v1/mail/saved-searches"},
		{method: http.MethodPut, path: "/api/v1/mail/saved-searches", body: `{"searches":[]}`},
	}

	for _, tt := range tests {
		rr := doMsgvaultRaw(t, srv, tt.method, tt.path, "127.0.0.1:12345", "application/json", tt.body)
		require.Equal(http.StatusNotFound, rr.Code, tt.path)
	}

	openAPI := NewOpenAPI()
	_, hasMsgvaultHealth := openAPI.Paths["/msgvault/health"]
	_, hasSavedSearches := openAPI.Paths["/messages/saved-searches"]
	assert.True(hasMsgvaultHealth)
	assert.True(hasSavedSearches)
	for _, tt := range tests {
		assert.NotContains(openAPI.Paths, strings.TrimPrefix(tt.path, "/api/v1"))
	}
}

func TestMsgvaultHealthAbsentOmitsCapabilityMetadata(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv := setupMsgvaultRouteServer(t, &config.Config{})

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/health", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	body := decodeMsgvaultHealthMap(t, rr)
	assert.NotContains(body, "url")
	assert.NotContains(body, "api_key_env")
}

func TestMsgvaultHealthOKProbesUpstream(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	var statsAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/stats":
			statsAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"total_messages":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	t.Setenv("MSGVAULT_API_KEY_TEST", "secret-key")
	srv := setupMsgvaultRouteServer(t, &config.Config{
		Msgvault: &config.Msgvault{URL: upstream.URL, APIKeyEnv: "MSGVAULT_API_KEY_TEST"},
	})

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/health", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	body := decodeMsgvaultHealth(t, rr)
	assert.True(body.Configured)
	assert.True(body.OK)
	assert.Equal("ok", body.Status)
	assert.Equal([]string{"fts"}, body.Modes)
	assert.Equal(upstream.URL, body.URL)
	assert.Equal("MSGVAULT_API_KEY_TEST", body.APIKeyEnv)
	assert.Equal("Bearer secret-key", statsAuth)
	assert.True(body.Features["threads_endpoint"].(bool))
	assert.False(body.Features["mutations"].(bool))
}

func TestMsgvaultHealthDown(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(err)
	addr := listener.Addr().String()
	require.NoError(listener.Close())

	t.Setenv("MSGVAULT_API_KEY_TEST", "secret-key")
	srv := setupMsgvaultRouteServer(t, &config.Config{
		Msgvault: &config.Msgvault{URL: "http://" + addr, APIKeyEnv: "MSGVAULT_API_KEY_TEST"},
	})

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/health", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	body := decodeMsgvaultHealth(t, rr)
	assert.True(body.Configured)
	assert.False(body.OK)
	assert.Equal("down", body.Status)
	assert.Empty(body.Modes)
	assert.False(body.Features["threads_endpoint"].(bool))
}

func TestMsgvaultHealthUnauthorized(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/stats":
			http.Error(w, `{"error":"bad_key"}`, http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	t.Setenv("MSGVAULT_API_KEY_TEST", "wrong-key")
	srv := setupMsgvaultRouteServer(t, &config.Config{
		Msgvault: &config.Msgvault{URL: upstream.URL, APIKeyEnv: "MSGVAULT_API_KEY_TEST"},
	})

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/health", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	body := decodeMsgvaultHealth(t, rr)
	assert.True(body.Configured)
	assert.False(body.OK)
	assert.Equal("unauthorized", body.Status)
	assert.Empty(body.Modes)
	assert.False(body.Features["threads_endpoint"].(bool))
}

func TestMsgvaultHealthMisconfiguredEchoesValidCapabilityMetadata(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	t.Setenv("MSGVAULT_API_KEY_TEST", "")
	srv := setupMsgvaultRouteServer(t, &config.Config{
		Msgvault: &config.Msgvault{
			URL:       "https://msgvault.example.com",
			APIKeyEnv: "MSGVAULT_API_KEY_TEST",
		},
	})

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/health", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	body := decodeMsgvaultHealth(t, rr)
	assert.True(body.Configured)
	assert.False(body.OK)
	assert.Equal("misconfigured", body.Status)
	assert.Equal("https://msgvault.example.com", body.URL)
	assert.Equal("MSGVAULT_API_KEY_TEST", body.APIKeyEnv)
	assert.Empty(body.Modes)
	assert.False(body.Features["threads_endpoint"].(bool))
}

func TestMsgvaultHealthDoesNotEchoUnsafeManualURL(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	t.Setenv("MSGVAULT_API_KEY_TEST", "secret-key")
	srv := setupMsgvaultRouteServer(t, &config.Config{
		Msgvault: &config.Msgvault{
			URL:       "https://user:pass@msgvault.example.com?token=secret",
			APIKeyEnv: "MSGVAULT_API_KEY_TEST",
		},
	})

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/health", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	body := decodeMsgvaultHealth(t, rr)
	assert.True(body.Configured)
	assert.False(body.OK)
	assert.Equal("misconfigured", body.Status)
	assert.Empty(body.URL)
	assert.Equal("MSGVAULT_API_KEY_TEST", body.APIKeyEnv)
}

func TestMsgvaultHealthInvalidStoredURLOmitsURL(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv := setupMsgvaultRouteServer(t, &config.Config{
		Msgvault: &config.Msgvault{
			URL:       "not a url",
			APIKeyEnv: "MSGVAULT_API_KEY",
		},
	})

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/health", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	body := decodeMsgvaultHealthMap(t, rr)
	assert.NotContains(body, "url")
	assert.Equal("MSGVAULT_API_KEY", body["api_key_env"])
}

func TestMsgvaultHealthInvalidStoredEnvNameOmitsEnvKey(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	t.Setenv("lowercase_bad", "")
	srv := setupMsgvaultRouteServer(t, &config.Config{
		Msgvault: &config.Msgvault{
			URL:       "https://msgvault.example.com",
			APIKeyEnv: "lowercase_bad",
		},
	})

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/health", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	body := decodeMsgvaultHealthMap(t, rr)
	assert.Equal("misconfigured", body["status"])
	assert.Equal("https://msgvault.example.com", body["url"])
	assert.NotContains(body, "api_key_env")
}

func TestMsgvaultHealthCachesWithinTTL(t *testing.T) {
	require := require.New(t)
	var probes atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/stats":
			probes.Add(1)
			_, _ = w.Write([]byte(`{}`))
		case "/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	t.Setenv("MSGVAULT_API_KEY_TEST", "secret-key")
	srv := setupMsgvaultRouteServer(t, &config.Config{
		Msgvault: &config.Msgvault{URL: upstream.URL, APIKeyEnv: "MSGVAULT_API_KEY_TEST"},
	})

	for range 5 {
		rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/health", nil)
		require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	}

	Assert.Equal(t, int32(1), probes.Load())
}

func TestMsgvaultHealthCacheUnderConcurrency(t *testing.T) {
	require := require.New(t)
	var probes atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/stats":
			probes.Add(1)
			time.Sleep(20 * time.Millisecond)
			_, _ = w.Write([]byte(`{}`))
		default:
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}
	}))
	defer upstream.Close()
	t.Setenv("MSGVAULT_API_KEY_TEST", "secret-key")
	srv := setupMsgvaultRouteServer(t, &config.Config{
		Msgvault: &config.Msgvault{URL: upstream.URL, APIKeyEnv: "MSGVAULT_API_KEY_TEST"},
	})

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/health", nil)
			require.Equal(http.StatusOK, rr.Code, rr.Body.String())
		})
	}
	wg.Wait()

	Assert.Equal(t, int32(1), probes.Load())
}

func TestMsgvaultSearchFtsReturnsPaginatableShape(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health", "/api/v1/stats":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/search":
			assert.Equal("project", r.URL.Query().Get("q"))
			assert.Equal("fts", r.URL.Query().Get("mode"))
			assert.Equal("Bearer secret-key", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{
				"query":"project","total":42,"page":1,"page_size":20,
				"messages":[{"id":101,"conversation_id":1001,"subject":"Project sync","from":"alice@example.com","to":["bob@example.com"],"sent_at":"2026-05-15T10:00:00Z","snippet":"...","labels":["work"],"has_attachments":false,"size_bytes":2048}]
			}`))
		default:
			http.NotFound(w, r)
		}
	})
	srv, cleanup := configuredMsgvaultRouteServer(t, upstream)
	defer cleanup()

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/search?q=project", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var body struct {
		Query       string           `json:"query"`
		Mode        string           `json:"mode"`
		Total       int              `json:"total"`
		Page        int              `json:"page"`
		PageSize    int              `json:"page_size"`
		Paginatable bool             `json:"paginatable"`
		Messages    []map[string]any `json:"messages"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal("project", body.Query)
	assert.Equal("fts", body.Mode)
	assert.Equal(42, body.Total)
	assert.Equal(1, body.Page)
	assert.Equal(20, body.PageSize)
	assert.True(body.Paginatable)
	require.Len(body.Messages, 1)
	first := body.Messages[0]
	assert.InDelta(101, first["id"], 0)
	assert.Empty(first["cc"])
	assert.Empty(first["bcc"])
	assert.Contains(first, "deleted_at")
	assert.Nil(first["deleted_at"])
}

func TestMsgvaultSearchRejectsUnsupportedMode(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/search" {
			assert.Fail("unsupported search mode was forwarded upstream")
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	srv, cleanup := configuredMsgvaultRouteServer(t, upstream)
	defer cleanup()

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/search?q=x&mode=vector", nil)

	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
	problem := decodeMsgvaultProblem(t, rr)
	assert.Equal(CodeBadRequest, problem.Code)
	assert.Equal("modeUnsupported", problem.Details["reason"])
}

func TestMsgvaultSearchNotConfiguredIs503(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv := setupMsgvaultRouteServer(t, &config.Config{})

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/search?q=x", nil)

	require.Equal(http.StatusServiceUnavailable, rr.Code, rr.Body.String())
	problem := decodeMsgvaultProblem(t, rr)
	assert.Equal(CodeServiceUnavailable, problem.Code)
	assert.Equal("notConfigured", problem.Details["reason"])
}

func TestMsgvaultSearchNilUpstreamPayloadIs502(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health", "/api/v1/stats":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/search":
			_, _ = w.Write([]byte(`null`))
		default:
			http.NotFound(w, r)
		}
	})
	srv, cleanup := configuredMsgvaultRouteServer(t, upstream)
	defer cleanup()

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/search?q=x", nil)

	require.Equal(http.StatusBadGateway, rr.Code, rr.Body.String())
	problem := decodeMsgvaultProblem(t, rr)
	assert.Equal(CodeUpstreamError, problem.Code)
	assert.Equal("upstreamMalformed", problem.Details["reason"])
}

func TestMsgvaultMessageSanitizesBodyHTML(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health", "/api/v1/stats":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/messages/42":
			_, _ = w.Write([]byte(`{
				"id":42,"subject":"Hi","from":"alice@example.com","to":["bob@example.com"],
				"sent_at":"2026-05-15T10:00:00Z","labels":[],"has_attachments":false,"size_bytes":0,
				"body":"text body","body_html":"<p>html body</p><script>alert(1)</script>","attachments":[]
			}`))
		default:
			http.NotFound(w, r)
		}
	})
	srv, cleanup := configuredMsgvaultRouteServer(t, upstream)
	defer cleanup()

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/messages/42", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var body map[string]any
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal("text body", body["body"])
	html, ok := body["body_html"].(string)
	require.True(ok, "body_html should be present")
	assert.NotEmpty(html)
	assert.NotContains(html, "<script")
	assert.Contains(body, "deleted_at")
	assert.Nil(body["deleted_at"])
}

func TestMsgvaultMessageNilUpstreamPayloadIs502(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health", "/api/v1/stats":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/messages/42":
			_, _ = w.Write([]byte(`null`))
		default:
			http.NotFound(w, r)
		}
	})
	srv, cleanup := configuredMsgvaultRouteServer(t, upstream)
	defer cleanup()

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/messages/42", nil)

	require.Equal(http.StatusBadGateway, rr.Code, rr.Body.String())
	problem := decodeMsgvaultProblem(t, rr)
	assert.Equal(CodeUpstreamError, problem.Code)
	assert.Equal("upstreamMalformed", problem.Details["reason"])
}

func TestMsgvaultMessageExposesRemoteImageToken(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health", "/api/v1/stats":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/messages/1001":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":              1001,
				"conversation_id": 1001,
				"subject":         "Remote image",
				"from":            "alice@example.com",
				"to":              []string{"bob@example.com"},
				"sent_at":         "2026-05-15T10:00:00Z",
				"labels":          []string{},
				"has_attachments": false,
				"size_bytes":      0,
				"body":            "hello",
				"body_html":       `<p>hello</p><script>alert(1)</script><img src="http://example.com/img.png">`,
				"attachments":     []any{},
			})
		default:
			http.NotFound(w, r)
		}
	})
	srv, cleanup := configuredMsgvaultRouteServer(t, upstream)
	defer cleanup()

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/messages/1001", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var body map[string]any
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	html, ok := body["body_html"].(string)
	require.True(ok, "body_html should be present")
	assert.NotContains(html, "<script")
	assert.Contains(html, `data-remote-image-idx="0"`)
	assert.InDelta(float64(1), body["remote_image_count"], 0)
	token, ok := body["remote_image_token"].(string)
	require.True(ok, "remote_image_token should be present")
	assert.Len(token, 32)
	assert.NotContains(body, "html_sanitization_failed")
	assert.Equal("hello", body["body"])
}

func TestMsgvaultMessageWithoutRemoteImagesOmitsToken(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health", "/api/v1/stats":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/messages/3001":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":              3001,
				"conversation_id": 3001,
				"subject":         "No remote image",
				"from":            "alice@example.com",
				"to":              []string{"bob@example.com"},
				"sent_at":         "2026-05-15T10:00:00Z",
				"labels":          []string{},
				"has_attachments": false,
				"size_bytes":      0,
				"body":            "hello world",
				"body_html":       `<p>hello <a href="http://example.com/y">world</a></p>`,
				"attachments":     []any{},
			})
		default:
			http.NotFound(w, r)
		}
	})
	srv, cleanup := configuredMsgvaultRouteServer(t, upstream)
	defer cleanup()

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/messages/3001", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var body map[string]any
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	html, ok := body["body_html"].(string)
	require.True(ok, "body_html should be present")
	assert.Contains(html, "hello")
	assert.NotContains(body, "remote_image_count")
	assert.NotContains(body, "remote_image_token")
}

func TestMsgvaultMessageSanitizationFailedFallsBack(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	huge := strings.Repeat("<p>x</p>", 1<<18)
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health", "/api/v1/stats":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/messages/2001":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":              2001,
				"conversation_id": 2001,
				"subject":         "Fallback",
				"from":            "alice@example.com",
				"to":              []string{"bob@example.com"},
				"sent_at":         "2026-05-15T10:00:00Z",
				"labels":          []string{},
				"has_attachments": false,
				"size_bytes":      0,
				"body":            "fallback text",
				"body_html":       huge,
				"attachments":     []any{},
			})
		default:
			http.NotFound(w, r)
		}
	})
	srv, cleanup := configuredMsgvaultRouteServer(t, upstream)
	defer cleanup()

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/messages/2001", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var body map[string]any
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(true, body["html_sanitization_failed"])
	assert.NotContains(body, "body_html")
	assert.Equal("fallback text", body["body"])
}

func TestMsgvaultConfigureBumpsSanitizerAndPurgesCache(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	t.Setenv("MSGVAULT_API_KEY_TEST", "secret-key")
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health", "/api/v1/stats":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/messages/9001":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":              9001,
				"conversation_id": 9001,
				"subject":         "Rotation",
				"from":            "alice@example.com",
				"to":              []string{"bob@example.com"},
				"sent_at":         "2026-05-15T10:00:00Z",
				"labels":          []string{},
				"has_attachments": false,
				"size_bytes":      0,
				"body":            "x",
				"body_html":       `<img src="http://example.com/x">`,
				"attachments":     []any{},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstreamSrv.Close()
	srv, _ := setupPersistentMsgvaultRouteServer(t, &config.Config{
		Msgvault: &config.Msgvault{URL: upstreamSrv.URL, APIKeyEnv: "MSGVAULT_API_KEY_TEST"},
	})

	firstRR := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/messages/9001", nil)
	require.Equal(http.StatusOK, firstRR.Code, firstRR.Body.String())
	var firstBody map[string]any
	require.NoError(json.NewDecoder(firstRR.Body).Decode(&firstBody))
	firstToken, ok := firstBody["remote_image_token"].(string)
	require.True(ok, "first remote_image_token should be present")
	require.NotEmpty(firstToken)

	configureRR := doMsgvaultJSON(t, srv, http.MethodPost, "/api/v1/msgvault/configure", map[string]any{
		"url":         upstreamSrv.URL,
		"api_key_env": "MSGVAULT_API_KEY_TEST",
	})
	require.Equal(http.StatusOK, configureRR.Code, configureRR.Body.String())

	secondRR := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/messages/9001", nil)
	require.Equal(http.StatusOK, secondRR.Code, secondRR.Body.String())
	var secondBody map[string]any
	require.NoError(json.NewDecoder(secondRR.Body).Decode(&secondBody))
	secondToken, ok := secondBody["remote_image_token"].(string)
	require.True(ok, "second remote_image_token should be present")
	assert.NotEmpty(secondToken)
	assert.NotEqual(firstToken, secondToken)

	staleTokenRR := doMsgvaultJSON(t, srv, http.MethodGet,
		"/api/v1/msgvault/messages/9001/remote-image/"+firstToken+"/0", nil)
	assert.Equal(http.StatusNotFound, staleTokenRR.Code, staleTokenRR.Body.String())
}

func TestMsgvaultMessageCIDImagesUseBasePath(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health", "/api/v1/stats":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/messages/42":
			_, _ = w.Write([]byte(`{
				"id":42,"subject":"Hi","from":"alice@example.com","to":["bob@example.com"],
				"sent_at":"2026-05-15T10:00:00Z","labels":[],"has_attachments":false,"size_bytes":0,
				"body":"text body","body_html":"<img src=\"cid:logo\">","attachments":[]
			}`))
		default:
			http.NotFound(w, r)
		}
	})
	upstreamSrv := httptest.NewServer(upstream)
	defer upstreamSrv.Close()
	t.Setenv("MSGVAULT_API_KEY_TEST", "secret-key")
	srv := New(openTestDB(t), nil, nil, "/middleman/", &config.Config{
		Msgvault: &config.Msgvault{URL: upstreamSrv.URL, APIKeyEnv: "MSGVAULT_API_KEY_TEST"},
	}, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/middleman/api/v1/msgvault/messages/42", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var body map[string]any
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	html, ok := body["body_html"].(string)
	require.True(ok, "body_html should be present")
	assert.Contains(html, `/middleman/api/v1/msgvault/messages/42/inline?cid=logo`)
}

func TestMsgvaultMessageBadIDIs422(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	upstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	srv, cleanup := configuredMsgvaultRouteServer(t, upstream)
	defer cleanup()

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/messages/not-a-number", nil)

	require.Equal(http.StatusUnprocessableEntity, rr.Code, rr.Body.String())
	problem := decodeMsgvaultProblem(t, rr)
	assert.Equal(CodeValidationError, problem.Code)
}

func TestMsgvaultInlineStreamsAndPassesContentType(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health", "/api/v1/stats":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/messages/42/inline":
			assert.Equal("logo", r.URL.Query().Get("cid"))
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte{0x89, 0x50, 0x4e, 0x47})
		default:
			http.NotFound(w, r)
		}
	})
	srv, cleanup := configuredMsgvaultRouteServer(t, upstream)
	defer cleanup()

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/messages/42/inline?cid=logo", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal("image/png", rr.Header().Get("Content-Type"))
	assert.Equal("nosniff", rr.Header().Get("X-Content-Type-Options"))
	assert.Equal("private, max-age=31536000, immutable", rr.Header().Get("Cache-Control"))
	assert.Equal("4", rr.Header().Get("Content-Length"))
	body, err := io.ReadAll(rr.Body)
	require.NoError(err)
	assert.Equal([]byte{0x89, 0x50, 0x4e, 0x47}, body)
}

func TestMsgvaultInlineRejectsUnsafeContentType(t *testing.T) {
	for _, contentType := range []string{"text/html", "image/svg+xml", "application/javascript"} {
		t.Run(contentType, func(t *testing.T) {
			assert := Assert.New(t)
			require := require.New(t)
			upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/health", "/api/v1/stats":
					_, _ = w.Write([]byte(`{"status":"ok"}`))
				case "/api/v1/messages/42/inline":
					w.Header().Set("Content-Type", contentType)
					_, _ = w.Write([]byte("<script>alert(1)</script>"))
				default:
					http.NotFound(w, r)
				}
			})
			srv, cleanup := configuredMsgvaultRouteServer(t, upstream)
			defer cleanup()

			rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/messages/42/inline?cid=x", nil)

			require.Equal(http.StatusUnsupportedMediaType, rr.Code, rr.Body.String())
			problem := decodeMsgvaultProblem(t, rr)
			assert.Equal(CodeBadRequest, problem.Code)
			assert.Equal("inlineTypeNotAllowed", problem.Details["reason"])
		})
	}
}

func TestMsgvaultInlineMissingCidIs400(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	upstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	srv, cleanup := configuredMsgvaultRouteServer(t, upstream)
	defer cleanup()

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/messages/42/inline", nil)

	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
	problem := decodeMsgvaultProblem(t, rr)
	assert.Equal(CodeBadRequest, problem.Code)
	assert.Equal("missingCID", problem.Details["reason"])
}

func TestMsgvaultAggregatesTranslatesQ(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	var seenSearchQuery, seenHideDeleted string
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health", "/api/v1/stats":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/aggregates":
			seenSearchQuery = r.URL.Query().Get("search_query")
			seenHideDeleted = r.URL.Query().Get("hide_deleted")
			_, _ = w.Write([]byte(`{"view_type":"senders","rows":[]}`))
		default:
			http.NotFound(w, r)
		}
	})
	srv, cleanup := configuredMsgvaultRouteServer(t, upstream)
	defer cleanup()

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/aggregates?view_type=senders&q=label%3AInbox", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal("label:Inbox", seenSearchQuery)
	assert.Equal("true", seenHideDeleted)
}

func TestMsgvaultAggregatesNilUpstreamPayloadIs502(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health", "/api/v1/stats":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/aggregates":
			_, _ = w.Write([]byte(`null`))
		default:
			http.NotFound(w, r)
		}
	})
	srv, cleanup := configuredMsgvaultRouteServer(t, upstream)
	defer cleanup()

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/aggregates?view_type=senders", nil)

	require.Equal(http.StatusBadGateway, rr.Code, rr.Body.String())
	problem := decodeMsgvaultProblem(t, rr)
	assert.Equal(CodeUpstreamError, problem.Code)
	assert.Equal("upstreamMalformed", problem.Details["reason"])
}

func TestMsgvaultAggregatesHideDeletedFalseRespected(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	var seenHideDeleted string
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health", "/api/v1/stats":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/aggregates":
			seenHideDeleted = r.URL.Query().Get("hide_deleted")
			_, _ = w.Write([]byte(`{"view_type":"senders","rows":[]}`))
		default:
			http.NotFound(w, r)
		}
	})
	srv, cleanup := configuredMsgvaultRouteServer(t, upstream)
	defer cleanup()

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/aggregates?view_type=senders&hide_deleted=false", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal("false", seenHideDeleted)
}

func TestMsgvaultAggregatesMissingViewTypeIs400(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	upstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	srv, cleanup := configuredMsgvaultRouteServer(t, upstream)
	defer cleanup()

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/aggregates", nil)

	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
	problem := decodeMsgvaultProblem(t, rr)
	assert.Equal(CodeBadRequest, problem.Code)
	assert.Equal("missingViewType", problem.Details["reason"])
}

func TestMsgvaultThreadPinsSortOrder(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	var seenSort, seenDirection, seenConversationID string
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health", "/api/v1/stats":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/messages/filter":
			seenSort = r.URL.Query().Get("sort")
			seenDirection = r.URL.Query().Get("direction")
			seenConversationID = r.URL.Query().Get("conversation_id")
			_, _ = w.Write([]byte(`{"messages":[{"id":1,"conversation_id":1001,"subject":"first"}]}`))
		default:
			http.NotFound(w, r)
		}
	})
	srv, cleanup := configuredMsgvaultRouteServer(t, upstream)
	defer cleanup()

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/threads/1001?sort=size&direction=desc", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal("date", seenSort)
	assert.Equal("asc", seenDirection)
	assert.Equal("1001", seenConversationID)
	var body struct {
		ConversationID int64            `json:"conversation_id"`
		Messages       []map[string]any `json:"messages"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(int64(1001), body.ConversationID)
	require.Len(body.Messages, 1)
}

func TestMsgvaultThreadNormalizesMessages(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health", "/api/v1/stats":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/messages/filter":
			_, _ = w.Write([]byte(`{"messages":[{"id":1,"conversation_id":1001,"subject":"sparse"}]}`))
		default:
			http.NotFound(w, r)
		}
	})
	srv, cleanup := configuredMsgvaultRouteServer(t, upstream)
	defer cleanup()

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/threads/1001", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var body struct {
		Messages []map[string]any `json:"messages"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	require.Len(body.Messages, 1)
	first := body.Messages[0]
	for _, key := range []string{"to", "cc", "bcc", "labels"} {
		value, present := first[key]
		assert.True(present, "%s should be present", key)
		assert.Empty(value, "%s should be an empty array", key)
	}
}

func TestMsgvaultThreadNilUpstreamPayloadIs502(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health", "/api/v1/stats":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/messages/filter":
			_, _ = w.Write([]byte(`null`))
		default:
			http.NotFound(w, r)
		}
	})
	srv, cleanup := configuredMsgvaultRouteServer(t, upstream)
	defer cleanup()

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/threads/1001", nil)

	require.Equal(http.StatusBadGateway, rr.Code, rr.Body.String())
	problem := decodeMsgvaultProblem(t, rr)
	assert.Equal(CodeUpstreamError, problem.Code)
	assert.Equal("upstreamMalformed", problem.Details["reason"])
}

func TestMsgvaultThreadBadConversationIDIs422(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	upstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	srv, cleanup := configuredMsgvaultRouteServer(t, upstream)
	defer cleanup()

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/threads/not-a-number", nil)

	require.Equal(http.StatusUnprocessableEntity, rr.Code, rr.Body.String())
	problem := decodeMsgvaultProblem(t, rr)
	assert.Equal(CodeValidationError, problem.Code)
}

func TestMsgvaultConfigureHappyPathPersistsAndHotReloads(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	t.Setenv("MSGVAULT_API_KEY_TEST", "secret-key")
	upstream := msgvaultOKUpstream(t)
	defer upstream.Close()
	srv, cfgPath := setupPersistentMsgvaultRouteServer(t, nil)

	rr := doMsgvaultJSON(t, srv, http.MethodPost, "/api/v1/msgvault/configure", map[string]any{
		"url":         upstream.URL + "/",
		"api_key_env": "MSGVAULT_API_KEY_TEST",
	})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	body := decodeMsgvaultHealth(t, rr)
	assert.True(body.Configured)
	assert.True(body.OK)
	assert.Equal("ok", body.Status)
	assert.Equal(upstream.URL, body.URL)
	assert.Equal("MSGVAULT_API_KEY_TEST", body.APIKeyEnv)

	reloaded, err := config.Load(cfgPath)
	require.NoError(err)
	require.NotNil(reloaded.Msgvault)
	assert.Equal(upstream.URL, reloaded.Msgvault.URL)
	assert.Equal("MSGVAULT_API_KEY_TEST", reloaded.Msgvault.APIKeyEnv)
	raw, err := os.ReadFile(cfgPath)
	require.NoError(err)
	assert.NotContains(string(raw), "api_key =")

	followup := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/health", nil)
	require.Equal(http.StatusOK, followup.Code, followup.Body.String())
	assert.Equal(upstream.URL, decodeMsgvaultHealth(t, followup).URL)
}

func TestMsgvaultConfigureEnvVarAbsentPersistsMisconfigured(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	t.Setenv("MSGVAULT_API_KEY_TEST", "")
	srv, cfgPath := setupPersistentMsgvaultRouteServer(t, nil)

	rr := doMsgvaultJSON(t, srv, http.MethodPost, "/api/v1/msgvault/configure", map[string]any{
		"url":         "https://msgvault.example.com",
		"api_key_env": "MSGVAULT_API_KEY_TEST",
	})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	body := decodeMsgvaultHealth(t, rr)
	assert.True(body.Configured)
	assert.False(body.OK)
	assert.Equal("misconfigured", body.Status)
	assert.Contains(body.StatusDetail, "MSGVAULT_API_KEY_TEST")
	assert.Equal("https://msgvault.example.com", body.URL)
	assert.Equal("MSGVAULT_API_KEY_TEST", body.APIKeyEnv)

	reloaded, err := config.Load(cfgPath)
	require.NoError(err)
	require.NotNil(reloaded.Msgvault)
	assert.Equal("https://msgvault.example.com", reloaded.Msgvault.URL)
	assert.Equal("MSGVAULT_API_KEY_TEST", reloaded.Msgvault.APIKeyEnv)
}

func TestMsgvaultConfigureRejectsInvalidUpdateWithoutReplacingPriorConfig(t *testing.T) {
	assert := Assert.New(t)
	requireAssert := require.New(t)
	t.Setenv("MSGVAULT_API_KEY_TEST", "secret-key")
	var searches atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health", "/api/v1/stats":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/search":
			searches.Add(1)
			assert.Equal("project", r.URL.Query().Get("q"))
			assert.Equal("Bearer secret-key", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{
				"query":"project","total":1,"page":1,"page_size":20,
				"messages":[{"id":101,"conversation_id":1001,"subject":"Project sync","from":"alice@example.com","to":["bob@example.com"],"sent_at":"2026-05-15T10:00:00Z","snippet":"...","labels":["work"],"has_attachments":false,"size_bytes":2048}]
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	srv, cfgPath := setupPersistentMsgvaultRouteServer(t, nil)

	configure := doMsgvaultJSON(t, srv, http.MethodPost, "/api/v1/msgvault/configure", map[string]any{
		"url":         upstream.URL,
		"api_key_env": "MSGVAULT_API_KEY_TEST",
	})
	requireAssert.Equal(http.StatusOK, configure.Code, configure.Body.String())

	for _, tc := range []struct {
		name   string
		body   map[string]any
		reason string
	}{
		{
			name:   "invalid URL",
			body:   map[string]any{"url": "ftp://msgvault.example.com", "api_key_env": "MSGVAULT_API_KEY_TEST"},
			reason: "invalidURL",
		},
		{
			name:   "plaintext http to non-loopback host",
			body:   map[string]any{"url": "http://msgvault.example.com", "api_key_env": "MSGVAULT_API_KEY_TEST"},
			reason: "invalidURL",
		},
		{
			name:   "plaintext http to private network host",
			body:   map[string]any{"url": "http://192.168.1.5:8080", "api_key_env": "MSGVAULT_API_KEY_TEST"},
			reason: "invalidURL",
		},
		{
			name:   "invalid env var",
			body:   map[string]any{"url": "https://msgvault.example.com", "api_key_env": "bad env"},
			reason: "invalidEnvVarName",
		},
		{
			name: "inline API key",
			body: map[string]any{
				"url":         "https://msgvault.example.com",
				"api_key_env": "MSGVAULT_API_KEY_TEST",
				"api_key":     "leak",
			},
			reason: "apiKeyUnsupported",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := Assert.New(t)
			require := require.New(t)
			rr := doMsgvaultJSON(t, srv, http.MethodPost, "/api/v1/msgvault/configure", tc.body)
			require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
			assert.Equal(tc.reason, decodeMsgvaultProblem(t, rr).Details["reason"])

			health := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/health", nil)
			require.Equal(http.StatusOK, health.Code, health.Body.String())
			body := decodeMsgvaultHealth(t, health)
			assert.True(body.Configured)
			assert.True(body.OK)
			assert.Equal("ok", body.Status)
			assert.Equal(upstream.URL, body.URL)
			assert.Equal("MSGVAULT_API_KEY_TEST", body.APIKeyEnv)

			search := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/search?q=project", nil)
			require.Equal(http.StatusOK, search.Code, search.Body.String())

			reloaded, err := config.Load(cfgPath)
			require.NoError(err)
			require.NotNil(reloaded.Msgvault)
			assert.Equal(upstream.URL, reloaded.Msgvault.URL)
			assert.Equal("MSGVAULT_API_KEY_TEST", reloaded.Msgvault.APIKeyEnv)
		})
	}
	assert.Equal(int32(5), searches.Load())
}

func TestMsgvaultConfigureRejectsAPIKey(t *testing.T) {
	for _, tc := range []struct {
		name   string
		apiKey any
	}{
		{name: "non-empty string", apiKey: "leak"},
		{name: "empty string", apiKey: ""},
		{name: "null", apiKey: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := Assert.New(t)
			require := require.New(t)
			srv, cfgPath := setupPersistentMsgvaultRouteServer(t, nil)
			body := map[string]any{
				"url":         "https://msgvault.example.com",
				"api_key_env": "MSGVAULT_API_KEY_TEST",
				"api_key":     tc.apiKey,
			}

			rr := doMsgvaultJSON(t, srv, http.MethodPost, "/api/v1/msgvault/configure", body)

			require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
			problem := decodeMsgvaultProblem(t, rr)
			assert.Equal(CodeBadRequest, problem.Code)
			assert.Equal("apiKeyUnsupported", problem.Details["reason"])
			reloaded, err := config.Load(cfgPath)
			require.NoError(err)
			assert.Nil(reloaded.Msgvault)
		})
	}
}

func TestMsgvaultConfigureRejectsMalformedURL(t *testing.T) {
	srv, cfgPath := setupPersistentMsgvaultRouteServer(t, nil)
	for _, tc := range []struct {
		name string
		url  string
	}{
		{"not a url", "::not a url::"},
		{"wrong scheme", "ftp://msgvault.example.com"},
		{"empty host", "https:///path"},
		{"userinfo", "https://user:pass@msgvault.example.com"},
		{"query string", "https://msgvault.example.com?token=secret"},
		{"fragment", "https://msgvault.example.com#token"},
		{"empty string", ""},
		{"whitespace only", "   "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := Assert.New(t)
			require := require.New(t)

			rr := doMsgvaultJSON(t, srv, http.MethodPost, "/api/v1/msgvault/configure", map[string]any{
				"url":         tc.url,
				"api_key_env": "MSGVAULT_API_KEY_TEST",
			})

			require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
			problem := decodeMsgvaultProblem(t, rr)
			assert.Equal(CodeBadRequest, problem.Code)
			assert.Equal("invalidURL", problem.Details["reason"])
		})
	}
	reloaded, err := config.Load(cfgPath)
	require.NoError(t, err)
	Assert.Nil(t, reloaded.Msgvault)
}

func TestMsgvaultConfigureRejectsBadEnvVarName(t *testing.T) {
	srv, _ := setupPersistentMsgvaultRouteServer(t, nil)
	for _, tc := range []struct {
		name  string
		input string
	}{
		{"lowercase", "msgvault_key"},
		{"mixed case", "MsgvaultKey"},
		{"space", "Foo Bar"},
		{"leading digit", "1MSGVAULT"},
		{"dash", "MSGVAULT-KEY"},
		{"empty", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := Assert.New(t)
			require := require.New(t)

			rr := doMsgvaultJSON(t, srv, http.MethodPost, "/api/v1/msgvault/configure", map[string]any{
				"url":         "https://msgvault.example.com",
				"api_key_env": tc.input,
			})

			require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
			problem := decodeMsgvaultProblem(t, rr)
			assert.Equal(CodeBadRequest, problem.Code)
			assert.Equal("invalidEnvVarName", problem.Details["reason"])
		})
	}
}

func TestMsgvaultConfigureCacheBust(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	t.Setenv("MSGVAULT_API_KEY_TEST", "secret-key")
	first := msgvaultOKUpstream(t)
	defer first.Close()
	second := msgvaultOKUpstream(t)
	defer second.Close()
	srv, _ := setupPersistentMsgvaultRouteServer(t, &config.Config{
		Msgvault: &config.Msgvault{URL: first.URL, APIKeyEnv: "MSGVAULT_API_KEY_TEST"},
	})

	before := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/health", nil)
	require.Equal(http.StatusOK, before.Code, before.Body.String())
	assert.Equal(first.URL, decodeMsgvaultHealth(t, before).URL)

	configure := doMsgvaultJSON(t, srv, http.MethodPost, "/api/v1/msgvault/configure", map[string]any{
		"url":         second.URL,
		"api_key_env": "MSGVAULT_API_KEY_TEST",
	})
	require.Equal(http.StatusOK, configure.Code, configure.Body.String())

	after := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/health", nil)
	require.Equal(http.StatusOK, after.Code, after.Body.String())
	assert.Equal(second.URL, decodeMsgvaultHealth(t, after).URL)
}

func TestMsgvaultConfigureRefreshesRuntimeTokenStripping(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	t.Setenv("MSGVAULT_API_KEY_TEST", "secret-key")
	upstream := msgvaultOKUpstream(t)
	defer upstream.Close()
	srv, _ := setupPersistentMsgvaultRouteServer(t, nil)
	owner := &msgvaultRuntimeOwner{}
	srv.runtime = localruntime.NewManager(localruntime.Options{
		Targets: []localruntime.LaunchTarget{{
			Key:       "helper",
			Label:     "Helper",
			Kind:      localruntime.LaunchTargetAgent,
			Source:    "test",
			Command:   []string{"/bin/echo"},
			Available: true,
		}},
		PtyOwnerRuntime: owner,
		StripEnvVars:    []string{"OLD_TOKEN"},
	})
	t.Cleanup(srv.runtime.Shutdown)

	rr := doMsgvaultJSON(t, srv, http.MethodPost, "/api/v1/msgvault/configure", map[string]any{
		"url":         upstream.URL,
		"api_key_env": "MSGVAULT_API_KEY_TEST",
	})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	_, err := srv.runtime.Launch(context.Background(), "ws-1", t.TempDir(), "helper")
	require.NoError(err)
	assert.Contains(owner.startedStripEnvVars, "MSGVAULT_API_KEY_TEST")
	// The manager preserves previously-stripped names (a rotated-away
	// token may still sit in the process environment), so the boot-time
	// entry survives the msgvault-driven update.
	assert.Contains(owner.startedStripEnvVars, "OLD_TOKEN")
}

func TestMsgvaultConfigureKeepsStartupBoundTokenStrippingAfterReload(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	t.Setenv("MSGVAULT_API_KEY_TEST", "secret-key")
	upstream := msgvaultOKUpstream(t)
	defer upstream.Close()
	srv, _ := setupPersistentMsgvaultRouteServer(t, &config.Config{
		Repos: []config.Repo{{
			Owner:    "acme",
			Name:     "widget",
			TokenEnv: "MIDDLEMAN_REPO_OLD_TOKEN",
		}},
	})
	srv.cfgMu.Lock()
	srv.cfg.Repos[0].TokenEnv = "MIDDLEMAN_REPO_NEW_TOKEN"
	srv.cfgMu.Unlock()
	owner := &msgvaultRuntimeOwner{}
	srv.runtime = localruntime.NewManager(localruntime.Options{
		Targets: []localruntime.LaunchTarget{{
			Key:       "helper",
			Label:     "Helper",
			Kind:      localruntime.LaunchTargetAgent,
			Source:    "test",
			Command:   []string{"/bin/echo"},
			Available: true,
		}},
		PtyOwnerRuntime: owner,
		StripEnvVars: []string{
			"MIDDLEMAN_REPO_OLD_TOKEN",
			"MIDDLEMAN_REPO_NEW_TOKEN",
		},
	})
	t.Cleanup(srv.runtime.Shutdown)

	rr := doMsgvaultJSON(t, srv, http.MethodPost, "/api/v1/msgvault/configure", map[string]any{
		"url":         upstream.URL,
		"api_key_env": "MSGVAULT_API_KEY_TEST",
	})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	_, err := srv.runtime.Launch(context.Background(), "ws-1", t.TempDir(), "helper")
	require.NoError(err)
	assert.Contains(owner.startedStripEnvVars, "MIDDLEMAN_REPO_OLD_TOKEN")
	assert.Contains(owner.startedStripEnvVars, "MIDDLEMAN_REPO_NEW_TOKEN")
	assert.Contains(owner.startedStripEnvVars, "MSGVAULT_API_KEY_TEST")
}

func TestMsgvaultConfigureConcurrentSavesKeepDiskAndMemoryAligned(t *testing.T) {
	require := require.New(t)
	upstream := msgvaultOKUpstream(t)
	defer upstream.Close()
	srv, cfgPath := setupPersistentMsgvaultRouteServer(t, nil)
	const numHappy = 8
	workerURL := func(i int) string { return upstream.URL + "/worker-" + strconv.Itoa(i) }
	workerEnv := func(i int) string { return "WORKER_ENV_" + strconv.Itoa(i) }
	statuses := make([]int, numHappy)
	var wg sync.WaitGroup
	for i := range numHappy {
		wg.Go(func() {
			rr := doMsgvaultJSON(t, srv, http.MethodPost, "/api/v1/msgvault/configure", map[string]any{
				"url":         workerURL(i),
				"api_key_env": workerEnv(i),
			})
			statuses[i] = rr.Code
		})
	}
	wg.Wait()

	for i, status := range statuses {
		Assert.Equalf(t, http.StatusOK, status, "worker %d", i)
	}
	reloaded, err := config.Load(cfgPath)
	require.NoError(err)
	require.NotNil(reloaded.Msgvault)
	health := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/health", nil)
	require.Equal(http.StatusOK, health.Code, health.Body.String())
	Assert.Equal(t, reloaded.Msgvault.URL, decodeMsgvaultHealth(t, health).URL)
}

func TestMsgvaultConfigureResponseEchoesOwnSavedConfig(t *testing.T) {
	require := require.New(t)
	upstream := msgvaultOKUpstream(t)
	defer upstream.Close()
	srv, _ := setupPersistentMsgvaultRouteServer(t, nil)
	const numHappy = 64
	workerURL := func(i int) string { return upstream.URL + "/response-" + strconv.Itoa(i) }
	workerEnv := func(i int) string { return "RESPONSE_ENV_" + strconv.Itoa(i) }
	responses := make([]msgvaultHealthWire, numHappy)
	statuses := make([]int, numHappy)
	var wg sync.WaitGroup
	for i := range numHappy {
		wg.Go(func() {
			rr := doMsgvaultJSON(t, srv, http.MethodPost, "/api/v1/msgvault/configure", map[string]any{
				"url":         workerURL(i),
				"api_key_env": workerEnv(i),
			})
			statuses[i] = rr.Code
			if rr.Code == http.StatusOK {
				responses[i] = decodeMsgvaultHealth(t, rr)
			}
		})
	}
	wg.Wait()

	for i, status := range statuses {
		require.Equalf(http.StatusOK, status, "worker %d", i)
		Assert.Equalf(t, workerURL(i), responses[i].URL, "worker %d", i)
		Assert.Equalf(t, workerEnv(i), responses[i].APIKeyEnv, "worker %d", i)
	}
}

func TestMsgvaultConfigureDoesNotHoldConfigLockDuringCapabilityProbe(t *testing.T) {
	require := require.New(t)
	t.Setenv("MSGVAULT_API_KEY_SLOW", "slow-secret")
	t.Setenv("MSGVAULT_API_KEY_FAST", "fast-secret")
	slowProbeStarted := make(chan struct{})
	releaseSlowProbe := make(chan struct{})
	var slowProbeOnce sync.Once
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			slowProbeOnce.Do(func() { close(slowProbeStarted) })
			<-releaseSlowProbe
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/stats":
			_, _ = w.Write([]byte(`{"total_messages":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer slow.Close()
	fast := msgvaultOKUpstream(t)
	defer fast.Close()
	cfg := &config.Config{SyncInterval: "5m", Host: "127.0.0.1", Port: 8091}
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(cfg.Save(cfgPath))
	loaded, err := config.Load(cfgPath)
	require.NoError(err)
	srv := setupMsgvaultRouteServer(t, loaded)
	srv.cfgPath = cfgPath
	slowDone := make(chan *httptest.ResponseRecorder, 1)
	fastDone := make(chan *httptest.ResponseRecorder, 1)
	configureBody := func(url, apiKeyEnv string) []byte {
		body, err := json.Marshal(map[string]any{
			"url":         url,
			"api_key_env": apiKeyEnv,
		})
		require.NoError(err)
		return body
	}
	doConfigure := func(body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/msgvault/configure", bytes.NewReader(body))
		setAcceptedHostForServerTest(req, srv)
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(middlemanCSRFHeaderName, "1")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		return rr
	}
	slowBody := configureBody(slow.URL, "MSGVAULT_API_KEY_SLOW")
	fastBody := configureBody(fast.URL, "MSGVAULT_API_KEY_FAST")

	go func() {
		slowDone <- doConfigure(slowBody)
	}()
	<-slowProbeStarted
	go func() {
		fastDone <- doConfigure(fastBody)
	}()

	select {
	case rr := <-fastDone:
		require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	case <-time.After(250 * time.Millisecond):
		close(releaseSlowProbe)
		<-slowDone
		<-fastDone
		require.Fail("fast configure blocked behind the slow capability probe")
	}
	close(releaseSlowProbe)
	require.Equal(http.StatusOK, (<-slowDone).Code)
}

func TestMsgvaultConfigureDoesNotHoldConfigLockBehindConcurrentHealthProbe(t *testing.T) {
	require := require.New(t)
	t.Setenv("MSGVAULT_API_KEY_HEALTH_SLOW", "slow-secret")
	t.Setenv("MSGVAULT_API_KEY_HEALTH_FAST", "fast-secret")
	slowProbeStarted := make(chan struct{})
	releaseSlowProbe := make(chan struct{})
	var slowProbeOnce sync.Once
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			slowProbeOnce.Do(func() { close(slowProbeStarted) })
			<-releaseSlowProbe
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/stats":
			_, _ = w.Write([]byte(`{"total_messages":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer slow.Close()
	fast := msgvaultOKUpstream(t)
	defer fast.Close()
	cfg := &config.Config{
		SyncInterval: "5m",
		Host:         "127.0.0.1",
		Port:         8091,
		Msgvault: &config.Msgvault{
			URL:       slow.URL,
			APIKeyEnv: "MSGVAULT_API_KEY_HEALTH_SLOW",
		},
	}
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(cfg.Save(cfgPath))
	loaded, err := config.Load(cfgPath)
	require.NoError(err)
	srv := setupMsgvaultRouteServer(t, loaded)
	srv.cfgPath = cfgPath
	configureBody, err := json.Marshal(map[string]any{
		"url":         fast.URL,
		"api_key_env": "MSGVAULT_API_KEY_HEALTH_FAST",
	})
	require.NoError(err)
	healthDone := make(chan *httptest.ResponseRecorder, 1)
	configureDone := make(chan *httptest.ResponseRecorder, 1)

	go func() {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/msgvault/health", nil)
		setAcceptedHostForServerTest(req, srv)
		req.RemoteAddr = "127.0.0.1:12345"
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		healthDone <- rr
	}()
	<-slowProbeStarted
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/msgvault/configure", bytes.NewReader(configureBody))
		setAcceptedHostForServerTest(req, srv)
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(middlemanCSRFHeaderName, "1")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		configureDone <- rr
	}()

	select {
	case rr := <-configureDone:
		require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	case <-time.After(250 * time.Millisecond):
		close(releaseSlowProbe)
		<-healthDone
		<-configureDone
		require.Fail("configure blocked behind the slow health capability probe")
	}
	close(releaseSlowProbe)
	require.Equal(http.StatusOK, (<-healthDone).Code)
}

func TestMsgvaultConfigureRejectsBadJSONAndUnknownFields(t *testing.T) {
	srv, cfgPath := setupPersistentMsgvaultRouteServer(t, nil)
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "bad json", body: "{not json"},
		{name: "unknown field", body: `{"url":"https://msgvault.example.com","apikey":"MSGVAULT_API_KEY_TEST"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := Assert.New(t)
			require := require.New(t)

			rr := doMsgvaultRaw(t, srv, http.MethodPost, "/api/v1/msgvault/configure",
				"127.0.0.1:12345", "application/json", tc.body)

			require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
			problem := decodeMsgvaultProblem(t, rr)
			assert.Equal(CodeBadRequest, problem.Code)
			assert.Equal("badRequest", problem.Details["reason"])
		})
	}
	reloaded, err := config.Load(cfgPath)
	require.NoError(t, err)
	Assert.Nil(t, reloaded.Msgvault)
}

func TestMsgvaultConfigureRejectsBodyTooLarge(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, cfgPath := setupPersistentMsgvaultRouteServer(t, nil)
	pad := strings.Repeat("a", 5<<20)
	body, err := json.Marshal(map[string]any{
		"url":         "https://" + pad,
		"api_key_env": "MSGVAULT_API_KEY_TEST",
	})
	require.NoError(err)

	rr := doMsgvaultRaw(t, srv, http.MethodPost, "/api/v1/msgvault/configure",
		"127.0.0.1:12345", "application/json", string(body))

	require.Equal(http.StatusRequestEntityTooLarge, rr.Code, rr.Body.String())
	problem := decodeMsgvaultProblem(t, rr)
	assert.Equal(CodePayloadTooLarge, problem.Code)
	reloaded, err := config.Load(cfgPath)
	require.NoError(err)
	assert.Nil(reloaded.Msgvault)
}

func TestMsgvaultConfigureRequiresJSONContentType(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, cfgPath := setupPersistentMsgvaultRouteServer(t, nil)

	rr := doMsgvaultRaw(t, srv, http.MethodPost, "/api/v1/msgvault/configure",
		"127.0.0.1:12345", "text/plain",
		`{"url":"https://msgvault.example.com","api_key_env":"MSGVAULT_API_KEY_TEST"}`)

	require.Equal(http.StatusUnsupportedMediaType, rr.Code, rr.Body.String())
	assert.Contains(rr.Body.String(), "Content-Type must be application/json")
	reloaded, err := config.Load(cfgPath)
	require.NoError(err)
	assert.Nil(reloaded.Msgvault)
}

func TestMsgvaultConfigureRequiresMiddlemanCSRFHeader(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, cfgPath := setupPersistentMsgvaultRouteServer(t, nil)

	rr := doMsgvaultRawWithoutCSRF(t, srv, http.MethodPost, "/api/v1/msgvault/configure",
		"127.0.0.1:12345", "application/json",
		`{"url":"https://msgvault.example.com","api_key_env":"MSGVAULT_API_KEY_TEST"}`)

	require.Equal(http.StatusForbidden, rr.Code, rr.Body.String())
	problem := decodeMsgvaultProblem(t, rr)
	assert.Equal(CodeForbidden, problem.Code)
	assert.Equal("missingCsrfHeader", problem.Details["reason"])
	reloaded, err := config.Load(cfgPath)
	require.NoError(err)
	assert.Nil(reloaded.Msgvault)
}

func TestMsgvaultConfigureRejectsNonLoopback(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, _ := setupPersistentMsgvaultRouteServer(t, nil)

	rr := doMsgvaultRaw(t, srv, http.MethodPost, "/api/v1/msgvault/configure",
		"203.0.113.7:12345", "application/json",
		`{"url":"https://msgvault.example.com","api_key_env":"MSGVAULT_API_KEY_TEST"}`)

	require.Equal(http.StatusForbidden, rr.Code, rr.Body.String())
	problem := decodeMsgvaultProblem(t, rr)
	assert.Equal(CodeForbidden, problem.Code)
	assert.Equal("loopbackOnly", problem.Details["reason"])
}

func TestMsgvaultConfigureRequestBodyIsJSON(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	doc := NewOpenAPI()
	item := doc.Paths["/msgvault/configure"]
	require.NotNil(item)
	require.NotNil(item.Post)
	require.NotNil(item.Post.RequestBody)
	content := item.Post.RequestBody.Content
	require.Contains(content, "application/json")
	assert.NotContains(content, "application/octet-stream")
	require.NotNil(content["application/json"].Schema)
	schema := content["application/json"].Schema
	assert.Contains(schema.Properties, "url")
	assert.Contains(schema.Properties, "api_key_env")
	assert.Equal(false, schema.AdditionalProperties)
}

func TestMsgvaultOpenAPIDocumentsMutationCSRFHeaders(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	doc := NewOpenAPI()
	spec, err := json.Marshal(doc)
	require.NoError(err)
	var decoded map[string]any
	require.NoError(json.Unmarshal(spec, &decoded))
	paths, ok := decoded["paths"].(map[string]any)
	require.True(ok)

	configure := openAPIPostOperation(t, paths, "/msgvault/configure")
	assert.True(openAPIParamRequired(t, configure, "header", middlemanCSRFHeaderName))

	savedSearches := openAPIPutOperation(t, paths, "/messages/saved-searches")
	assert.True(openAPIParamRequired(t, savedSearches, "header", middlemanCSRFHeaderName))
}

func TestMsgvaultOpenAPIImageRoutesAndRequiredQueries(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv := setupMsgvaultRouteServer(t, &config.Config{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil)
	setAcceptedHostForServerTest(req, srv)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var doc map[string]any
	require.NoError(json.NewDecoder(rr.Body).Decode(&doc))
	paths, ok := doc["paths"].(map[string]any)
	require.True(ok)

	aggregates := openAPIGETOperation(t, paths, "/msgvault/aggregates")
	assert.True(openAPIParamRequired(t, aggregates, "query", "view_type"))

	inline := openAPIGETOperation(t, paths, "/msgvault/messages/{id}/inline")
	assert.True(openAPIParamRequired(t, inline, "query", "cid"))
	assert.Equal(
		[]string{"image/gif", "image/jpeg", "image/png", "image/webp"},
		openAPIResponseContentTypes(t, inline, "200"),
	)

	remote := openAPIGETOperation(t, paths, "/msgvault/messages/{id}/remote-image/{token}/{idx}")
	assert.Equal(
		[]string{"image/gif", "image/jpeg", "image/png", "image/webp"},
		openAPIResponseContentTypes(t, remote, "200"),
	)
}

func openAPIGETOperation(t *testing.T, paths map[string]any, path string) map[string]any {
	t.Helper()
	item, ok := paths[path].(map[string]any)
	require.Truef(t, ok, "missing OpenAPI path %s", path)
	op, ok := item["get"].(map[string]any)
	require.Truef(t, ok, "missing OpenAPI GET operation for %s", path)
	return op
}

func openAPIPostOperation(t *testing.T, paths map[string]any, path string) map[string]any {
	t.Helper()
	item, ok := paths[path].(map[string]any)
	require.Truef(t, ok, "missing OpenAPI path %s", path)
	op, ok := item["post"].(map[string]any)
	require.Truef(t, ok, "missing OpenAPI POST operation for %s", path)
	return op
}

func openAPIPutOperation(t *testing.T, paths map[string]any, path string) map[string]any {
	t.Helper()
	item, ok := paths[path].(map[string]any)
	require.Truef(t, ok, "missing OpenAPI path %s", path)
	op, ok := item["put"].(map[string]any)
	require.Truef(t, ok, "missing OpenAPI PUT operation for %s", path)
	return op
}

func openAPIParamRequired(t *testing.T, op map[string]any, in string, name string) bool {
	t.Helper()
	params, ok := op["parameters"].([]any)
	require.True(t, ok)
	for _, rawParam := range params {
		param, ok := rawParam.(map[string]any)
		require.True(t, ok)
		if param["in"] == in && param["name"] == name {
			required, _ := param["required"].(bool)
			return required
		}
	}
	require.Failf(t, "missing OpenAPI parameter", "%s parameter %q was not found", in, name)
	return false
}

func openAPIResponseContentTypes(t *testing.T, op map[string]any, status string) []string {
	t.Helper()
	responses, ok := op["responses"].(map[string]any)
	require.True(t, ok)
	response, ok := responses[status].(map[string]any)
	require.Truef(t, ok, "missing OpenAPI response %s", status)
	content, ok := response["content"].(map[string]any)
	require.Truef(t, ok, "missing OpenAPI response content for %s", status)
	types := make([]string, 0, len(content))
	for mediaType := range content {
		types = append(types, mediaType)
	}
	sort.Strings(types)
	return types
}
