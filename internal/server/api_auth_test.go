package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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
