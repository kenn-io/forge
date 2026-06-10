package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/config"
)

func setupMsgvaultRouteServerWithRemoteImageDeps(
	t *testing.T,
	upstream http.Handler,
	deps msgvaultRemoteImageDeps,
) (*Server, func()) {
	t.Helper()
	upstreamSrv := httptest.NewServer(upstream)
	t.Setenv("MV_TEST_KEY", "test-token")
	srv := New(openTestDB(t), nil, nil, "/", &config.Config{
		Msgvault: &config.Msgvault{URL: upstreamSrv.URL, APIKeyEnv: "MV_TEST_KEY"},
	}, ServerOptions{msgvaultRemoteImageDeps: &deps})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	return srv, upstreamSrv.Close
}

func startMsgvaultImageUpstream(t *testing.T, handler http.HandlerFunc) (*httptest.Server, msgvaultRemoteImageDialFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	return srv, dial
}

func msgvaultTestProxyDeps(fixedAddr netip.Addr, dial msgvaultRemoteImageDialFunc) msgvaultRemoteImageDeps {
	return msgvaultRemoteImageDeps{
		lookup: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{fixedAddr}, nil
		},
		dial: dial,
	}
}

func msgvaultFixtureUpstream(t *testing.T, id int64, bodyHTML string) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/api/v1/stats", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"total_messages":1}`))
	})
	mux.HandleFunc(fmt.Sprintf("/api/v1/messages/%d", id), func(w http.ResponseWriter, _ *http.Request) {
		out := map[string]any{
			"id":              id,
			"conversation_id": 1,
			"subject":         "synthetic",
			"from":            "alice@example.com",
			"to":              []string{"bob@example.com"},
			"cc":              []string{},
			"bcc":             []string{},
			"sent_at":         "2026-05-25T00:00:00Z",
			"snippet":         "x",
			"labels":          []string{"Inbox"},
			"has_attachments": false,
			"size_bytes":      100,
			"body":            "plain",
			"body_html":       bodyHTML,
			"attachments":     []any{},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(out); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	return mux
}

func msgvaultFlakyUpstream(t *testing.T, id int64, bodyHTML string, successfulCalls int) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/api/v1/stats", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"total_messages":1}`))
	})
	var calls atomic.Int32
	mux.HandleFunc(fmt.Sprintf("/api/v1/messages/%d", id), func(w http.ResponseWriter, _ *http.Request) {
		if int(calls.Add(1)) > successfulCalls {
			http.Error(w, "upstream boom", http.StatusInternalServerError)
			return
		}
		out := map[string]any{
			"id":              id,
			"conversation_id": 1,
			"subject":         "synthetic",
			"from":            "alice@example.com",
			"to":              []string{"bob@example.com"},
			"cc":              []string{},
			"bcc":             []string{},
			"sent_at":         "2026-05-25T00:00:00Z",
			"snippet":         "x",
			"labels":          []string{"Inbox"},
			"has_attachments": false,
			"size_bytes":      100,
			"body":            "plain",
			"body_html":       bodyHTML,
			"attachments":     []any{},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(out); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	return mux
}

func msgvaultNilRefetchUpstream(t *testing.T, id int64, bodyHTML string) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/api/v1/stats", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"total_messages":1}`))
	})
	var calls atomic.Int32
	mux.HandleFunc(fmt.Sprintf("/api/v1/messages/%d", id), func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) > 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`null`))
			return
		}
		out := map[string]any{
			"id":              id,
			"conversation_id": 1,
			"subject":         "synthetic",
			"from":            "alice@example.com",
			"to":              []string{"bob@example.com"},
			"cc":              []string{},
			"bcc":             []string{},
			"sent_at":         "2026-05-25T00:00:00Z",
			"snippet":         "x",
			"labels":          []string{"Inbox"},
			"has_attachments": false,
			"size_bytes":      100,
			"body":            "plain",
			"body_html":       bodyHTML,
			"attachments":     []any{},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(out); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	return mux
}

func msgvaultSanitizeAndGetToken(t *testing.T, srv *Server, id int64) string {
	t.Helper()
	rr := doMsgvaultJSON(t, srv, http.MethodGet, fmt.Sprintf("/api/v1/msgvault/messages/%d", id), nil)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var body map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	token, _ := body["remote_image_token"].(string)
	require.NotEmpty(t, token, "remote_image_token missing from response: %v", body)
	return token
}

func startMsgvaultRemoteImageHTTPServer(t *testing.T, srv *Server) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

func msgvaultLiveRemoteImageToken(t *testing.T, baseURL string, id int64) string {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("%s/api/v1/msgvault/messages/%d", baseURL, id))
	require.NoError(t, err)
	defer resp.Body.Close()
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, http.StatusOK, resp.StatusCode, "response body: %v", body)
	token, _ := body["remote_image_token"].(string)
	require.NotEmpty(t, token, "remote_image_token missing from response: %v", body)
	return token
}

func decodeMsgvaultProblemResponse(t *testing.T, resp *http.Response) ProblemError {
	t.Helper()
	var body ProblemError
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	return body
}

func TestMsgvaultRemoteImageLiveHTTPHappyPath(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	imageSrv, dial := startMsgvaultImageUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 'P', 'N', 'G'})
	})
	defer imageSrv.Close()
	deps := msgvaultTestProxyDeps(netip.MustParseAddr("1.2.3.4"), dial)
	srv, cleanup := setupMsgvaultRouteServerWithRemoteImageDeps(
		t,
		msgvaultFixtureUpstream(t, 1001, `<img src="http://example.com/image.png">`),
		deps,
	)
	defer cleanup()
	ts := startMsgvaultRemoteImageHTTPServer(t, srv)

	token := msgvaultLiveRemoteImageToken(t, ts.URL, 1001)
	resp, err := http.Get(fmt.Sprintf("%s/api/v1/msgvault/messages/1001/remote-image/%s/0", ts.URL, token))
	require.NoError(err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(err)

	assert.Equal(http.StatusOK, resp.StatusCode, string(body))
	assert.Equal("image/png", resp.Header.Get("Content-Type"))
	assert.Equal("nosniff", resp.Header.Get("X-Content-Type-Options"))
	assert.Equal("private, max-age=300", resp.Header.Get("Cache-Control"))
	assert.Equal("4", resp.Header.Get("Content-Length"))
	assert.Equal([]byte{0x89, 'P', 'N', 'G'}, body)
}

func TestMsgvaultRemoteImageLiveHTTPRefetchErrors(t *testing.T) {
	for _, tc := range []struct {
		name       string
		upstream   http.Handler
		wantStatus int
		wantCode   ProblemCode
		wantReason string
	}{
		{
			name:       "token mismatch maps to not found",
			upstream:   msgvaultFixtureUpstream(t, 1, `<img src="http://example.com/x">`),
			wantStatus: http.StatusNotFound,
			wantCode:   CodeNotFound,
			wantReason: "imageNotFound",
		},
		{
			name:       "upstream refetch failure maps to bad gateway",
			upstream:   msgvaultFlakyUpstream(t, 1, `<img src="http://example.com/x">`, 1),
			wantStatus: http.StatusBadGateway,
			wantCode:   CodeUpstreamError,
			wantReason: "imageFetchFailed",
		},
		{
			name:       "nil refetch maps to bad gateway",
			upstream:   msgvaultNilRefetchUpstream(t, 1, `<img src="http://example.com/x">`),
			wantStatus: http.StatusBadGateway,
			wantCode:   CodeUpstreamError,
			wantReason: "imageFetchFailed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := Assert.New(t)
			require := require.New(t)
			imageSrv, dial := startMsgvaultImageUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write([]byte("ok"))
			})
			defer imageSrv.Close()
			deps := msgvaultTestProxyDeps(netip.MustParseAddr("1.2.3.4"), dial)
			srv, cleanup := setupMsgvaultRouteServerWithRemoteImageDeps(t, tc.upstream, deps)
			defer cleanup()
			ts := startMsgvaultRemoteImageHTTPServer(t, srv)
			_ = msgvaultLiveRemoteImageToken(t, ts.URL, 1)

			resp, err := http.Get(ts.URL + "/api/v1/msgvault/messages/1/remote-image/00000000000000000000000000000000/0")
			require.NoError(err)
			defer resp.Body.Close()

			problem := decodeMsgvaultProblemResponse(t, resp)
			assert.Equal(tc.wantStatus, resp.StatusCode)
			assert.Equal(tc.wantCode, problem.Code)
			assert.Equal(tc.wantReason, problem.Details["reason"])
		})
	}
}

func TestMsgvaultRemoteImageHappyPath(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	imageSrv, dial := startMsgvaultImageUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 'P', 'N', 'G'})
	})
	defer imageSrv.Close()
	deps := msgvaultTestProxyDeps(netip.MustParseAddr("1.2.3.4"), dial)
	srv, cleanup := setupMsgvaultRouteServerWithRemoteImageDeps(
		t,
		msgvaultFixtureUpstream(t, 1001, `<img src="http://example.com/image.png">`),
		deps,
	)
	defer cleanup()
	token := msgvaultSanitizeAndGetToken(t, srv, 1001)

	rr := doMsgvaultJSON(t, srv, http.MethodGet,
		fmt.Sprintf("/api/v1/msgvault/messages/1001/remote-image/%s/0", token), nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal("image/png", rr.Header().Get("Content-Type"))
	assert.Equal("nosniff", rr.Header().Get("X-Content-Type-Options"))
	assert.Equal("private, max-age=300", rr.Header().Get("Cache-Control"))
	assert.Equal("4", rr.Header().Get("Content-Length"))
	body, err := io.ReadAll(rr.Body)
	require.NoError(err)
	assert.Equal([]byte{0x89, 'P', 'N', 'G'}, body)
}

func TestMsgvaultValidateRemoteImageURL(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want error
	}{
		{"http://x/", nil},
		{"https://x/", nil},
		{"https://x:443/", nil},
		{"ftp://x/y", errMsgvaultRemoteImageBadScheme},
		{"file:///etc/passwd", errMsgvaultRemoteImageBadScheme},
		{"https://user:pass@x/", errMsgvaultRemoteImageUserinfo},
		{"https://x:8080/", errMsgvaultRemoteImageBadPort},
		{"http://x:22/", errMsgvaultRemoteImageBadPort},
		{"https://[fe80::1%25eth0]/", errMsgvaultRemoteImageZoneID},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			u, err := url.Parse(tc.raw)
			require.NoError(t, err)
			Assert.ErrorIs(t, validateMsgvaultRemoteImageURL(u), tc.want)
		})
	}
}

func TestMsgvaultRemoteImagePrivateOrReserved(t *testing.T) {
	for _, tc := range []struct {
		ip   string
		want bool
	}{
		{"10.0.0.1", true},
		{"100.64.5.5", true},
		{"127.0.0.1", true},
		{"169.254.0.1", true},
		{"172.16.0.1", true},
		{"192.168.1.1", true},
		{"192.0.2.5", true},
		{"198.18.5.5", true},
		{"198.51.100.5", true},
		{"203.0.113.5", true},
		{"224.0.0.5", true},
		{"0.0.0.0", true},
		{"::1", true},
		{"fe80::1", true},
		{"fc00::1", true},
		{"ff00::1", true},
		{"::ffff:10.0.0.1", true},
		{"64:ff9b::1", true},
		{"8.8.8.8", false},
		{"1.2.3.4", false},
		{"2001:4860:4860::8888", false},
	} {
		t.Run(tc.ip, func(t *testing.T) {
			got := isMsgvaultRemoteImagePrivateOrReserved(netip.MustParseAddr(tc.ip))
			Assert.Equal(t, tc.want, got)
		})
	}
}

func TestMsgvaultRemoteImageDialerRejectsMixedPrivate(t *testing.T) {
	deps := msgvaultRemoteImageDeps{
		lookup: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{
				netip.MustParseAddr("1.2.3.4"),
				netip.MustParseAddr("10.0.0.1"),
			}, nil
		},
		dial: func(context.Context, string, string) (net.Conn, error) {
			require.FailNow(t, "dial should not be called when lookup includes private address")
			return nil, nil
		},
	}
	_, err := deps.dialContext(context.Background(), "tcp", "x.com:443")
	Assert.ErrorIs(t, err, errMsgvaultRemoteImagePrivateIP)
}

func TestMsgvaultRemoteImageDialerFallsBackAcrossPublicAddresses(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	deps := msgvaultRemoteImageDeps{
		lookup: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{
				netip.MustParseAddr("1.2.3.4"),
				netip.MustParseAddr("8.8.8.8"),
			}, nil
		},
	}
	var attempts []string
	deps.dial = func(_ context.Context, _, addr string) (net.Conn, error) {
		attempts = append(attempts, addr)
		if strings.HasPrefix(addr, "1.2.3.4:") {
			return nil, errors.New("unreachable")
		}
		client, server := net.Pipe()
		t.Cleanup(func() { _ = server.Close() })
		return client, nil
	}

	conn, err := deps.dialContext(context.Background(), "tcp", "example.com:443")

	require.NoError(err)
	t.Cleanup(func() { _ = conn.Close() })
	assert.Equal([]string{"1.2.3.4:443", "8.8.8.8:443"}, attempts)
}

func TestMsgvaultRemoteImageRouteFallsBackAcrossPublicAddresses(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	imageSrv, imageDial := startMsgvaultImageUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 'P', 'N', 'G'})
	})
	defer imageSrv.Close()
	deps := msgvaultRemoteImageDeps{
		lookup: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{
				netip.MustParseAddr("1.2.3.4"),
				netip.MustParseAddr("8.8.8.8"),
			}, nil
		},
	}
	var attempts []string
	deps.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		attempts = append(attempts, addr)
		if strings.HasPrefix(addr, "1.2.3.4:") {
			return nil, errors.New("unreachable")
		}
		return imageDial(ctx, network, addr)
	}
	srv, cleanup := setupMsgvaultRouteServerWithRemoteImageDeps(
		t,
		msgvaultFixtureUpstream(t, 1001, `<img src="http://example.com/image.png">`),
		deps,
	)
	defer cleanup()
	token := msgvaultSanitizeAndGetToken(t, srv, 1001)

	rr := doMsgvaultJSON(t, srv, http.MethodGet,
		fmt.Sprintf("/api/v1/msgvault/messages/1001/remote-image/%s/0", token), nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal("image/png", rr.Header().Get("Content-Type"))
	assert.Equal([]string{"1.2.3.4:80", "8.8.8.8:80"}, attempts)
	body, err := io.ReadAll(rr.Body)
	require.NoError(err)
	assert.Equal([]byte{0x89, 'P', 'N', 'G'}, body)
}

func TestMsgvaultRemoteImageRejectsWrongContentType(t *testing.T) {
	imageSrv, dial := startMsgvaultImageUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html/>"))
	})
	defer imageSrv.Close()
	deps := msgvaultTestProxyDeps(netip.MustParseAddr("1.2.3.4"), dial)
	srv, cleanup := setupMsgvaultRouteServerWithRemoteImageDeps(
		t,
		msgvaultFixtureUpstream(t, 1, `<img src="http://example.com/x">`),
		deps,
	)
	defer cleanup()
	token := msgvaultSanitizeAndGetToken(t, srv, 1)

	rr := doMsgvaultJSON(t, srv, http.MethodGet,
		fmt.Sprintf("/api/v1/msgvault/messages/1/remote-image/%s/0", token), nil)

	require.Equal(t, http.StatusUnsupportedMediaType, rr.Code, rr.Body.String())
	problem := decodeMsgvaultProblem(t, rr)
	Assert.Equal(t, CodeBadRequest, problem.Code)
	Assert.Equal(t, "unsupportedImageType", problem.Details["reason"])
}

func TestMsgvaultRemoteImageRejectsContentEncoding(t *testing.T) {
	imageSrv, dial := startMsgvaultImageUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write([]byte{0x1f, 0x8b})
	})
	defer imageSrv.Close()
	deps := msgvaultTestProxyDeps(netip.MustParseAddr("1.2.3.4"), dial)
	srv, cleanup := setupMsgvaultRouteServerWithRemoteImageDeps(
		t,
		msgvaultFixtureUpstream(t, 1, `<img src="http://example.com/x">`),
		deps,
	)
	defer cleanup()
	token := msgvaultSanitizeAndGetToken(t, srv, 1)

	rr := doMsgvaultJSON(t, srv, http.MethodGet,
		fmt.Sprintf("/api/v1/msgvault/messages/1/remote-image/%s/0", token), nil)

	require.Equal(t, http.StatusBadGateway, rr.Code, rr.Body.String())
}

func TestMsgvaultRemoteImageRejectsOversize(t *testing.T) {
	imageSrv, dial := startMsgvaultImageUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(make([]byte, msgvaultRemoteImageMaxBytes+1))
	})
	defer imageSrv.Close()
	deps := msgvaultTestProxyDeps(netip.MustParseAddr("1.2.3.4"), dial)
	srv, cleanup := setupMsgvaultRouteServerWithRemoteImageDeps(
		t,
		msgvaultFixtureUpstream(t, 1, `<img src="http://example.com/x">`),
		deps,
	)
	defer cleanup()
	token := msgvaultSanitizeAndGetToken(t, srv, 1)

	rr := doMsgvaultJSON(t, srv, http.MethodGet,
		fmt.Sprintf("/api/v1/msgvault/messages/1/remote-image/%s/0", token), nil)

	require.Equal(t, http.StatusBadGateway, rr.Code, rr.Body.String())
}

func TestMsgvaultRemoteImageHeaderHygiene(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	var captured http.Header
	imageSrv, dial := startMsgvaultImageUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Clone()
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("ok"))
	})
	defer imageSrv.Close()
	deps := msgvaultTestProxyDeps(netip.MustParseAddr("1.2.3.4"), dial)
	srv, cleanup := setupMsgvaultRouteServerWithRemoteImageDeps(
		t,
		msgvaultFixtureUpstream(t, 1, `<img src="http://example.com/x">`),
		deps,
	)
	defer cleanup()
	token := msgvaultSanitizeAndGetToken(t, srv, 1)

	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/v1/msgvault/messages/1/remote-image/%s/0", token), nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Cookie", "session=abc")
	req.Header.Set("Referer", "http://middleman")
	req.Header.Set("Origin", "http://middleman")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	for _, header := range []string{"Authorization", "Cookie", "Referer", "Origin", "Accept-Encoding", "Accept-Language"} {
		assert.Empty(captured.Get(header), "%s should be stripped", header)
	}
	assert.Equal("middleman-msgvault/1", captured.Get("User-Agent"))
}

func TestMsgvaultRemoteImageRedirectStripsReferer(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	var captured http.Header
	imageSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			w.Header().Set("Location", "/end")
			w.WriteHeader(http.StatusFound)
		case "/end":
			captured = r.Header.Clone()
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("ok"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer imageSrv.Close()
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, network, imageSrv.Listener.Addr().String())
	}
	deps := msgvaultTestProxyDeps(netip.MustParseAddr("1.2.3.4"), dial)
	srv, cleanup := setupMsgvaultRouteServerWithRemoteImageDeps(
		t,
		msgvaultFixtureUpstream(t, 1, `<img src="http://example.com/start">`),
		deps,
	)
	defer cleanup()
	token := msgvaultSanitizeAndGetToken(t, srv, 1)

	rr := doMsgvaultJSON(t, srv, http.MethodGet,
		fmt.Sprintf("/api/v1/msgvault/messages/1/remote-image/%s/0", token), nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	require.NotNil(captured)
	assert.Empty(captured.Get("Referer"))
}

func TestMsgvaultRemoteImageRedirectChainCap(t *testing.T) {
	chain := func(terminalAt int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var n int
			if _, err := fmt.Sscanf(r.URL.Path, "/hop%d", &n); err != nil {
				http.NotFound(w, r)
				return
			}
			if n >= terminalAt {
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write([]byte("ok"))
				return
			}
			w.Header().Set("Location", fmt.Sprintf("/hop%d", n+1))
			w.WriteHeader(http.StatusFound)
		}))
	}
	depsFor := func(srv *httptest.Server) msgvaultRemoteImageDeps {
		dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, network, srv.Listener.Addr().String())
		}
		return msgvaultTestProxyDeps(netip.MustParseAddr("1.2.3.4"), dial)
	}

	t.Run("exactly max succeeds", func(t *testing.T) {
		imageSrv := chain(msgvaultRemoteImageMaxRedirects + 1)
		defer imageSrv.Close()
		srv, cleanup := setupMsgvaultRouteServerWithRemoteImageDeps(
			t,
			msgvaultFixtureUpstream(t, 1, `<img src="http://example.com/hop1">`),
			depsFor(imageSrv),
		)
		defer cleanup()
		token := msgvaultSanitizeAndGetToken(t, srv, 1)

		rr := doMsgvaultJSON(t, srv, http.MethodGet,
			fmt.Sprintf("/api/v1/msgvault/messages/1/remote-image/%s/0", token), nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	})

	t.Run("max plus one rejected", func(t *testing.T) {
		imageSrv := chain(msgvaultRemoteImageMaxRedirects + 2)
		defer imageSrv.Close()
		srv, cleanup := setupMsgvaultRouteServerWithRemoteImageDeps(
			t,
			msgvaultFixtureUpstream(t, 1, `<img src="http://example.com/hop1">`),
			depsFor(imageSrv),
		)
		defer cleanup()
		token := msgvaultSanitizeAndGetToken(t, srv, 1)

		rr := doMsgvaultJSON(t, srv, http.MethodGet,
			fmt.Sprintf("/api/v1/msgvault/messages/1/remote-image/%s/0", token), nil)
		require.Equal(t, http.StatusBadGateway, rr.Code, rr.Body.String())
	})
}

func TestMsgvaultRemoteImageRefetchUpstreamErrorMapsTo502(t *testing.T) {
	imageSrv, dial := startMsgvaultImageUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("ok"))
	})
	defer imageSrv.Close()
	deps := msgvaultTestProxyDeps(netip.MustParseAddr("1.2.3.4"), dial)
	srv, cleanup := setupMsgvaultRouteServerWithRemoteImageDeps(
		t,
		msgvaultFlakyUpstream(t, 1, `<img src="http://example.com/x">`, 1),
		deps,
	)
	defer cleanup()
	_ = msgvaultSanitizeAndGetToken(t, srv, 1)

	rr := doMsgvaultJSON(t, srv, http.MethodGet,
		"/api/v1/msgvault/messages/1/remote-image/00000000000000000000000000000000/0", nil)

	require.Equal(t, http.StatusBadGateway, rr.Code, rr.Body.String())
}

func TestMsgvaultRemoteImageCacheMissTokenMismatch(t *testing.T) {
	imageSrv, dial := startMsgvaultImageUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("ok"))
	})
	defer imageSrv.Close()
	deps := msgvaultTestProxyDeps(netip.MustParseAddr("1.2.3.4"), dial)
	srv, cleanup := setupMsgvaultRouteServerWithRemoteImageDeps(
		t,
		msgvaultFixtureUpstream(t, 1, `<img src="http://example.com/x">`),
		deps,
	)
	defer cleanup()
	_ = msgvaultSanitizeAndGetToken(t, srv, 1)

	rr := doMsgvaultJSON(t, srv, http.MethodGet,
		"/api/v1/msgvault/messages/1/remote-image/00000000000000000000000000000000/0", nil)

	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
}

func TestMsgvaultRemoteImageErrorEnvelopeShape(t *testing.T) {
	srv, cleanup := setupMsgvaultRouteServerWithRemoteImageDeps(
		t,
		msgvaultFixtureUpstream(t, 1, ""),
		defaultMsgvaultRemoteImageDeps(),
	)
	defer cleanup()

	rr := doMsgvaultJSON(t, srv, http.MethodGet,
		"/api/v1/msgvault/messages/1/remote-image/00000000000000000000000000000000/0", nil)

	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
	problem := decodeMsgvaultProblem(t, rr)
	Assert.Equal(t, CodeNotFound, problem.Code)
	Assert.Equal(t, "imageNotFound", problem.Details["reason"])
}

func TestMsgvaultRemoteImageContextCancel(t *testing.T) {
	dialed := make(chan struct{})
	slowDial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		close(dialed)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	deps := msgvaultRemoteImageDeps{
		lookup: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("1.2.3.4")}, nil
		},
		dial: slowDial,
	}
	srv, cleanup := setupMsgvaultRouteServerWithRemoteImageDeps(
		t,
		msgvaultFixtureUpstream(t, 1, `<img src="http://e.com/x">`),
		deps,
	)
	defer cleanup()
	token := msgvaultSanitizeAndGetToken(t, srv, 1)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/v1/msgvault/messages/1/remote-image/%s/0", token), nil).WithContext(ctx)
	req.RemoteAddr = "127.0.0.1:12345"
	done := make(chan struct{})
	go func() {
		defer close(done)
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
	}()
	<-dialed
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "handler did not return after context cancel")
	}
}
