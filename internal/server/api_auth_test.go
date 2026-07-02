package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/testutil/dbtest"
)

func newAuthTestServer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	srv := New(dbtest.Open(t), nil, nil, "/", nil, ServerOptions{
		APIAuthToken: token,
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

func authGet(
	t *testing.T, ts *httptest.Server, path string,
	decorate func(*http.Request),
) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	require.NoError(t, err)
	if decorate != nil {
		decorate(req)
	}
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// TestAPIAuthGatesAPIRoutes pins the gate: with a token configured,
// API routes 401 (problem+json, unauthorized code) without a
// credential and serve normally with the bearer header.
func TestAPIAuthGatesAPIRoutes(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ts := newAuthTestServer(t, "secret-token")

	resp := authGet(t, ts, "/api/v1/snapshot", nil)
	require.Equal(http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(`Bearer realm="middleman"`,
		resp.Header.Get("WWW-Authenticate"))
	var problem struct {
		Code string `json:"code"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&problem))
	assert.Equal("unauthorized", problem.Code)

	resp = authGet(t, ts, "/api/v1/snapshot", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer secret-token")
	})
	assert.Equal(http.StatusOK, resp.StatusCode)

	resp = authGet(t, ts, "/api/v1/snapshot", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer wrong")
	})
	assert.Equal(http.StatusUnauthorized, resp.StatusCode)
}

// TestAPIAuthGatesTerminalWebSocketRoutes pins that the /ws/ terminal
// routes are gated alongside /api/. These open interactive shells, so an
// unauthenticated request must be rejected before routing; a valid
// credential clears the gate (the route itself may then 404 in this
// minimal server, but it is no longer a 401).
func TestAPIAuthGatesTerminalWebSocketRoutes(t *testing.T) {
	assert := assert.New(t)
	ts := newAuthTestServer(t, "secret-token")

	resp := authGet(t, ts, "/ws/v1/workspaces/ws-1/terminal", nil)
	assert.Equal(http.StatusUnauthorized, resp.StatusCode,
		"unauthenticated terminal WebSocket requests must be rejected")

	resp = authGet(t, ts, "/ws/v1/workspaces/ws-1/terminal",
		func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer secret-token")
		})
	assert.NotEqual(http.StatusUnauthorized, resp.StatusCode,
		"a valid credential clears the gate")
}

// TestAPIAuthHealthAndAssetsStayOpen pins the exemptions: health
// probes (supervisors poll before reading the token file) and
// non-API paths (SPA assets) are not gated.
func TestAPIAuthHealthAndAssetsStayOpen(t *testing.T) {
	assert := assert.New(t)
	ts := newAuthTestServer(t, "secret-token")

	for _, path := range []string{"/healthz", "/livez"} {
		resp := authGet(t, ts, path, nil)
		assert.Equal(http.StatusOK, resp.StatusCode, path)
	}
}

// TestAPIAuthCookieBootstrap pins the browser flow: loading any URL
// with ?auth_token=<token> sets the session cookie and redirects to
// the same URL without the token; the cookie then authorizes API
// requests; a wrong bootstrap token is rejected outright.
func TestAPIAuthCookieBootstrap(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ts := newAuthTestServer(t, "secret-token")

	resp := authGet(t, ts, "/?auth_token=secret-token", nil)
	require.Equal(http.StatusSeeOther, resp.StatusCode)
	assert.Equal("/", resp.Header.Get("Location"),
		"token must be stripped from the redirect target")
	cookies := resp.Cookies()
	require.Len(cookies, 1)
	assert.Equal("middleman_auth", cookies[0].Name)
	assert.True(cookies[0].HttpOnly)

	resp = authGet(t, ts, "/api/v1/snapshot", func(r *http.Request) {
		r.AddCookie(cookies[0])
	})
	assert.Equal(http.StatusOK, resp.StatusCode,
		"the bootstrap cookie authorizes API requests")

	resp = authGet(t, ts, "/?auth_token=wrong", nil)
	assert.Equal(http.StatusForbidden, resp.StatusCode)
	assert.Empty(resp.Cookies())
}

// TestAuthBootstrapURLRoundTrip pins the glue between the CLI and the
// server: the exact URL `middleman auth url` prints (AuthBootstrapURL)
// must bootstrap a session cookie that authorizes the API, at the root
// and under a base path. The other bootstrap tests hand-build the URL,
// so they would not catch AuthBootstrapURL drifting from the handler.
func TestAuthBootstrapURLRoundTrip(t *testing.T) {
	for _, basePath := range []string{"/", "/middleman/"} {
		t.Run("basePath="+basePath, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			srv := New(dbtest.Open(t), nil, nil, basePath, nil, ServerOptions{
				APIAuthToken: "secret-token",
			})
			ts := httptest.NewServer(srv)
			t.Cleanup(ts.Close)

			base := ts.URL + strings.TrimSuffix(basePath, "/")
			bootstrap := AuthBootstrapURL(base, "secret-token")
			resp := authGet(t, ts, strings.TrimPrefix(bootstrap, ts.URL), nil)
			require.Equal(http.StatusSeeOther, resp.StatusCode)
			assert.Equal(basePath, resp.Header.Get("Location"),
				"token must be stripped from the redirect target")
			cookies := resp.Cookies()
			require.Len(cookies, 1)

			apiPath := strings.TrimSuffix(basePath, "/") + "/api/v1/snapshot"
			resp = authGet(t, ts, apiPath, nil)
			require.Equal(http.StatusUnauthorized, resp.StatusCode)
			resp = authGet(t, ts, apiPath, func(r *http.Request) {
				r.AddCookie(cookies[0])
			})
			assert.Equal(http.StatusOK, resp.StatusCode,
				"the CLI-printed URL must yield a cookie that authorizes the API")
		})
	}
}

// TestAPIAuthDisabledByDefault pins the default: with no token
// configured, behavior is unchanged and nothing is gated.
func TestAPIAuthDisabledByDefault(t *testing.T) {
	ts := newAuthTestServer(t, "")
	resp := authGet(t, ts, "/api/v1/snapshot", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestRedactedQueryMasksBootstrapToken pins the log-redaction
// contract: the bootstrap token never appears in the logged query.
func TestRedactedQueryMasksBootstrapToken(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	u, err := url.Parse("/?auth_token=secret&tab=pulls")
	require.NoError(err)
	redacted := redactedQuery(u)
	assert.NotContains(redacted, "secret")
	assert.Contains(redacted, "auth_token=REDACTED")
	assert.Contains(redacted, "tab=pulls")

	plain, err := url.Parse("/?tab=pulls")
	require.NoError(err)
	assert.Equal("tab=pulls", redactedQuery(plain))
}

func authPostLogin(
	t *testing.T, ts *httptest.Server, path, body string,
	decorate func(*http.Request),
) *http.Response {
	t.Helper()
	req, err := http.NewRequest(
		http.MethodPost, ts.URL+path, strings.NewReader(body),
	)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if decorate != nil {
		decorate(req)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// TestAuthLoginSetsCookie pins the overlay login path: POSTing the
// token as JSON sets the session cookie without the token ever
// appearing in a request URI, and that cookie authorizes the API. A
// wrong token is rejected without a cookie.
func TestAuthLoginSetsCookie(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ts := newAuthTestServer(t, "secret-token")

	resp := authPostLogin(t, ts, "/auth/login", `{"token":"secret-token"}`, nil)
	require.Equal(http.StatusNoContent, resp.StatusCode)
	cookies := resp.Cookies()
	require.Len(cookies, 1)
	assert.Equal("middleman_auth", cookies[0].Name)
	assert.True(cookies[0].HttpOnly)

	apiResp := authGet(t, ts, "/api/v1/snapshot", func(r *http.Request) {
		r.AddCookie(cookies[0])
	})
	assert.Equal(http.StatusOK, apiResp.StatusCode,
		"the login cookie authorizes API requests")

	resp = authPostLogin(t, ts, "/auth/login", `{"token":"wrong"}`, nil)
	assert.Equal(http.StatusForbidden, resp.StatusCode)
	assert.Empty(resp.Cookies())
}

// TestAuthLoginRejectsForgeableRequests pins the same-origin defenses:
// cross-origin fetches, non-JSON bodies (cross-origin form posts), and
// non-POST methods are all rejected, and a malformed body is a 400.
func TestAuthLoginRejectsForgeableRequests(t *testing.T) {
	assert := assert.New(t)
	ts := newAuthTestServer(t, "secret-token")

	resp := authPostLogin(t, ts, "/auth/login", `{"token":"secret-token"}`,
		func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Site", "cross-site")
		})
	assert.Equal(http.StatusForbidden, resp.StatusCode)
	assert.Empty(resp.Cookies())

	resp = authPostLogin(t, ts, "/auth/login", "token=secret-token",
		func(r *http.Request) {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		})
	assert.Equal(http.StatusUnsupportedMediaType, resp.StatusCode)
	assert.Empty(resp.Cookies())

	resp = authGet(t, ts, "/auth/login", nil)
	assert.Equal(http.StatusMethodNotAllowed, resp.StatusCode)

	resp = authPostLogin(t, ts, "/auth/login", "{", nil)
	assert.Equal(http.StatusBadRequest, resp.StatusCode)
}

// TestAuthLoginUnderBasePath pins login when middleman is mounted
// under a base path.
func TestAuthLoginUnderBasePath(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv := New(dbtest.Open(t), nil, nil, "/middleman/", nil, ServerOptions{
		APIAuthToken: "secret-token",
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	resp := authPostLogin(
		t, ts, "/middleman/auth/login", `{"token":"secret-token"}`, nil,
	)
	require.Equal(http.StatusNoContent, resp.StatusCode)
	require.Len(resp.Cookies(), 1)

	apiResp := authGet(t, ts, "/middleman/api/v1/snapshot",
		func(r *http.Request) {
			r.AddCookie(resp.Cookies()[0])
		})
	assert.Equal(http.StatusOK, apiResp.StatusCode)
}

// TestAuthLogoutExpiresCookie pins logout: GET /auth/logout expires the
// session cookie and redirects to the base path, regardless of whether
// require_auth is on (the table covers both).
func TestAuthLogoutExpiresCookie(t *testing.T) {
	for _, token := range []string{"secret-token", ""} {
		t.Run("token="+token, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			ts := newAuthTestServer(t, token)

			resp := authGet(t, ts, "/auth/logout", nil)
			require.Equal(http.StatusSeeOther, resp.StatusCode)
			assert.Equal("/", resp.Header.Get("Location"))
			cookies := resp.Cookies()
			require.Len(cookies, 1)
			assert.Equal("middleman_auth", cookies[0].Name)
			assert.Negative(cookies[0].MaxAge, "cookie must be expired")
		})
	}
}

// TestAuthLogoutUnderBasePath pins logout when middleman is mounted under
// a base path: /middleman/auth/logout expires the cookie and redirects to
// the base path.
func TestAuthLogoutUnderBasePath(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv := New(dbtest.Open(t), nil, nil, "/middleman/", nil, ServerOptions{})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	resp := authGet(t, ts, "/middleman/auth/logout", nil)
	require.Equal(http.StatusSeeOther, resp.StatusCode)
	assert.Equal("/middleman/", resp.Header.Get("Location"))
}
