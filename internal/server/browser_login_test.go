package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/browserloginapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
)

const (
	browserLoginHubID        = "0123456789abcdef0123456789abcdef"
	browserLoginSpokeID      = "fedcba9876543210fedcba9876543210"
	browserLoginEnrollmentID = "11111111111111111111111111111111"
)

type browserLoginClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *browserLoginClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *browserLoginClock) Set(now time.Time) {
	c.mu.Lock()
	c.now = now
	c.mu.Unlock()
}

type browserLoginFixture struct {
	ts          *httptest.Server
	srv         *Server
	clock       *browserLoginClock
	peerToken   string
	enrollments *federation.Store
	lease       time.Time
	// forwardedHost is sent on every request when the fixture trusts a proxy.
	forwardedHost string
}

// newBrowserLoginHub builds an enabled hub whose spoke holds an active,
// leased enrollment and the ordinary active spoke-to-hub grant.
func newBrowserLoginHub(t *testing.T, hostCheck authapi.HostCheckOptions) *browserLoginFixture {
	t.Helper()
	require := require.New(t)
	clock := &browserLoginClock{now: time.Now().UTC()}
	enrollments, err := federation.Open(
		filepath.Join(t.TempDir(), "enrollments.json"), federation.StoreOptions{},
	)
	require.NoError(err)
	oneTime, err := enrollments.CreateOneTimeToken(federation.Identity{
		NodeID: browserLoginHubID, BaseURL: "https://hub.example",
	}, clock.Now().Add(time.Minute))
	require.NoError(err)
	_, err = enrollments.Begin(t.Context(), oneTime.Token, federation.JoinRequest{
		EnrollmentID:    browserLoginEnrollmentID,
		NodeID:          browserLoginSpokeID,
		BaseURL:         "https://spoke.example",
		Platform:        "linux",
		ProtocolVersion: federation.ProtocolVersion,
		HubCredential:   "hub-credential",
	})
	require.NoError(err)
	lease := clock.Now().Add(time.Hour)
	require.NoError(enrollments.Activate(t.Context(), browserLoginEnrollmentID, lease))
	credentials, err := federationauth.Open(filepath.Join(t.TempDir(), "credentials.json"))
	require.NoError(err)
	token, err := credentials.MintInbound(
		browserLoginSpokeID, federationauth.SpokeToHubScopes(),
	)
	require.NoError(err)
	cfg := &config.Config{Fleet: config.Fleet{
		Enabled: true, Role: config.FleetRoleHub,
		Members: []config.FleetMember{{
			NodeID: browserLoginSpokeID, BaseURL: "https://spoke.example",
			State: federation.EnrollmentActive,
		}},
	}}
	srv := New(dbtest.Open(t), nil, nil, "/", cfg, ServerOptions{
		DaemonAccess: authapi.DaemonAccessOptions{
			Token: "local-secret", RequireAPIAuth: true,
		},
		FederationCredentials: credentials,
		FederationEnrollments: enrollments,
		FederationSpokeID:     browserLoginHubID,
		HostCheck:             hostCheck,
	})
	srv.now = clock.Now
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	return &browserLoginFixture{
		ts: ts, srv: srv, clock: clock, peerToken: token,
		enrollments: enrollments, lease: lease,
	}
}

func (f *browserLoginFixture) do(
	t *testing.T, method, path string, decorate func(*http.Request),
) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), method, f.ts.URL+path, nil)
	require.NoError(t, err)
	if f.forwardedHost != "" {
		request.Header.Set("X-Forwarded-Host", f.forwardedHost)
	}
	if decorate != nil {
		decorate(request)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	response, err := client.Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { response.Body.Close() })
	return response
}

func (f *browserLoginFixture) issueTicket(t *testing.T) browserloginapi.BrowserLoginTicketBody {
	t.Helper()
	response := f.do(t, http.MethodPost, "/api/v1/federation/browser-login-tickets",
		func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+f.peerToken) })
	require.Equal(t, http.StatusOK, response.StatusCode)
	var ticket browserloginapi.BrowserLoginTicketBody
	require.NoError(t, json.NewDecoder(response.Body).Decode(&ticket))
	return ticket
}

// signIn consumes a fresh ticket and returns the session cookie it set.
func (f *browserLoginFixture) signIn(t *testing.T) *http.Cookie {
	t.Helper()
	ticket := f.issueTicket(t)
	response := f.do(t, http.MethodGet, "/?login_ticket="+ticket.Ticket, nil)
	require.Equal(t, http.StatusSeeOther, response.StatusCode)
	cookie := browserSessionCookie(response)
	require.NotNil(t, cookie)
	return cookie
}

func (f *browserLoginFixture) snapshotStatus(t *testing.T, cookie *http.Cookie) int {
	t.Helper()
	return f.do(t, http.MethodGet, "/api/v1/snapshot", func(r *http.Request) {
		r.AddCookie(cookie)
	}).StatusCode
}

func browserSessionCookie(response *http.Response) *http.Cookie {
	for _, cookie := range response.Cookies() {
		if cookie.Name == authapi.BrowserSessionCookieName {
			return cookie
		}
	}
	return nil
}

func TestBrowserLoginTicketBootstrapsSession(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	hub := newBrowserLoginHub(t, authapi.HostCheckOptions{})

	ticket := hub.issueTicket(t)
	response := hub.do(t, http.MethodGet,
		"/pulls/github/acme/widget/7?state=open&login_ticket="+ticket.Ticket, nil)
	require.Equal(http.StatusSeeOther, response.StatusCode)
	assert.Equal("/pulls/github/acme/widget/7?state=open", response.Header.Get("Location"))
	cookie := browserSessionCookie(response)
	require.NotNil(cookie)
	assert.True(cookie.HttpOnly)
	assert.Equal(http.SameSiteLaxMode, cookie.SameSite)
	assert.Equal("/", cookie.Path)
	assert.False(cookie.Secure, "plain HTTP without a trusted proxy is not HTTPS")
	assert.NotEqual(ticket.Ticket, cookie.Value)

	assert.Equal(http.StatusOK, hub.snapshotStatus(t, cookie))
	assert.Equal(http.StatusUnauthorized, hub.snapshotStatus(t, &http.Cookie{
		Name: authapi.BrowserSessionCookieName, Value: ticket.Ticket,
	}), "the ticket itself is not a session")

	reused := hub.do(t, http.MethodGet, "/?login_ticket="+ticket.Ticket, nil)
	assert.Equal(http.StatusForbidden, reused.StatusCode)
	assert.Nil(browserSessionCookie(reused))

	bogus := hub.do(t, http.MethodGet, "/?login_ticket=not-a-ticket", nil)
	assert.Equal(http.StatusForbidden, bogus.StatusCode)
	assert.Nil(browserSessionCookie(bogus))

	expired := hub.issueTicket(t)
	hub.clock.Set(expired.ExpiresAt)
	late := hub.do(t, http.MethodGet, "/?login_ticket="+expired.Ticket, nil)
	assert.Equal(http.StatusForbidden, late.StatusCode)
	assert.Nil(browserSessionCookie(late))
}

func TestBrowserLoginRedirectStaysSameOrigin(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	hub := newBrowserLoginHub(t, authapi.HostCheckOptions{})
	ticket := hub.issueTicket(t)
	response := hub.do(t, http.MethodGet, "//evil.example/path?login_ticket="+ticket.Ticket, nil)
	require.Equal(http.StatusSeeOther, response.StatusCode)
	location, err := url.Parse(response.Header.Get("Location"))
	require.NoError(err)
	assert.Empty(location.Host)
	assert.Equal("/evil.example/path", location.Path)
}

func TestBrowserSessionCookieSecureFollowsTrustedScheme(t *testing.T) {
	for _, test := range []struct {
		name    string
		trusted bool
		proto   string
		secure  bool
	}{
		{name: "trusted https proxy", trusted: true, proto: "https", secure: true},
		{name: "trusted http proxy", trusted: true, proto: "http"},
		{name: "untrusted forwarded proto", proto: "https"},
	} {
		t.Run(test.name, func(t *testing.T) {
			hostCheck := authapi.HostCheckOptions{
				Bind:                 config.HostKey{Host: "127.0.0.1", Port: "8091"},
				AllowLoopbackAnyPort: true,
			}
			if test.trusted {
				hostCheck.TrustReverseProxy = true
				hostCheck.Allowed = []config.HostKey{{Host: "forge.example.test"}}
			}
			hub := newBrowserLoginHub(t, hostCheck)
			if test.trusted {
				hub.forwardedHost = "forge.example.test"
			}
			decorate := func(r *http.Request) {
				r.Header.Set("X-Forwarded-Proto", test.proto)
			}
			ticket := hub.issueTicket(t)
			response := hub.do(t, http.MethodGet, "/?login_ticket="+ticket.Ticket, decorate)
			require.Equal(t, http.StatusSeeOther, response.StatusCode)
			cookie := browserSessionCookie(response)
			require.NotNil(t, cookie)
			assert.Equal(t, test.secure, cookie.Secure)
		})
	}
}

func TestBrowserSessionEndsWithIssuingPeerEnrollment(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	hub := newBrowserLoginHub(t, authapi.HostCheckOptions{})
	cookie := hub.signIn(t)
	require.Equal(http.StatusOK, hub.snapshotStatus(t, cookie))

	now := hub.clock.Now()
	hub.clock.Set(hub.lease)
	assert.Equal(http.StatusUnauthorized, hub.snapshotStatus(t, cookie),
		"an expired activation lease ends the peer's browser sessions")
	hub.clock.Set(now)
	require.Equal(http.StatusOK, hub.snapshotStatus(t, cookie))

	hub.srv.cfgMu.Lock()
	hub.srv.cfg.Fleet.Enabled = false
	hub.srv.cfgMu.Unlock()
	assert.Equal(http.StatusUnauthorized, hub.snapshotStatus(t, cookie),
		"disabling federation ends peer-issued browser sessions")
	hub.srv.cfgMu.Lock()
	hub.srv.cfg.Fleet.Enabled = true
	hub.srv.cfgMu.Unlock()
	require.Equal(http.StatusOK, hub.snapshotStatus(t, cookie))

	pending := hub.issueTicket(t)
	require.NoError(hub.enrollments.Revoke(t.Context(), browserLoginEnrollmentID))
	assert.Equal(http.StatusUnauthorized, hub.snapshotStatus(t, cookie),
		"revocation ends the peer's browser sessions")
	late := hub.do(t, http.MethodGet, "/?login_ticket="+pending.Ticket, nil)
	assert.Equal(http.StatusForbidden, late.StatusCode,
		"a ticket issued before revocation cannot sign in afterward")
	assert.Nil(browserSessionCookie(late))
}

func TestBrowserSessionRejectsCrossOriginWebSocket(t *testing.T) {
	assert := assert.New(t)
	hub := newBrowserLoginHub(t, authapi.HostCheckOptions{})
	cookie := hub.signIn(t)
	upgrade := func(origin string) int {
		return hub.do(t, http.MethodGet, "/ws/v1/workspaces/ws-1/terminal", func(r *http.Request) {
			r.AddCookie(cookie)
			r.Header.Set("Origin", origin)
			r.Header.Set("Connection", "Upgrade")
			r.Header.Set("Upgrade", "websocket")
			r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
			r.Header.Set("Sec-WebSocket-Version", "13")
		}).StatusCode
	}
	hostURL, err := url.Parse(hub.ts.URL)
	require.NoError(t, err)
	assert.Equal(http.StatusForbidden, upgrade("https://attacker.example"))
	status := upgrade("https://" + hostURL.Host)
	assert.NotEqual(http.StatusUnauthorized, status)
	assert.NotEqual(http.StatusForbidden, status)
}
