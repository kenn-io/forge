package e2etest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/server/httpapi"
)

type fleetBrowserLogin struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// browserRequest sends one request the way a browser would: no redirect
// following, and only the credentials the caller attaches.
func (f *federatedForgesFixture) browserRequest(
	t *testing.T, method, target, body string, decorate func(*http.Request),
) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequestWithContext(t.Context(), method, target, reader)
	require.NoError(t, err)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if decorate != nil {
		decorate(request)
	}
	client := *f.HTTPClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	response, err := client.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response, payload
}

func (f *federatedForgesFixture) requestBrowserLogin(
	t *testing.T, source *federatedDaemonFixture, nodeID, body string,
	credential func(*http.Request),
) (*http.Response, []byte) {
	t.Helper()
	return f.browserRequest(t, http.MethodPost,
		source.HTTP.URL+"/api/v1/fleet/hosts/"+url.PathEscape(nodeID)+"/browser-login",
		body, credential)
}

func localBearer(daemon *federatedDaemonFixture) func(*http.Request) {
	return func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+daemon.LocalToken)
	}
}

func withCookie(cookie *http.Cookie) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(cookie) }
}

// followBrowserLogin opens a login link and returns the redirect target and
// the session cookie the destination set.
func (f *federatedForgesFixture) followBrowserLogin(
	t *testing.T, link string,
) (string, *http.Cookie) {
	t.Helper()
	response, body := f.browserRequest(t, http.MethodGet, link, "", nil)
	require.Equal(t, http.StatusSeeOther, response.StatusCode, string(body))
	for _, cookie := range response.Cookies() {
		if cookie.Name == "forge_session" {
			return response.Header.Get("Location"), cookie
		}
	}
	require.Fail(t, "login link did not set a session cookie")
	return "", nil
}

func decodeBrowserLogin(t *testing.T, response *http.Response, body []byte) fleetBrowserLogin {
	t.Helper()
	require.Equal(t, http.StatusOK, response.StatusCode, string(body))
	var login fleetBrowserLogin
	require.NoError(t, json.Unmarshal(body, &login))
	return login
}

// TestFleetBrowserLoginE2E proves the machine switcher's cross-node sign-in:
// a hub mints a link on its spoke with its own federation credential, the
// link establishes a spoke session, and that session can hop back to the hub.
func TestFleetBrowserLoginE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newFederatedForgesFixture(t)
	hub, nodeA := fixture.Hub, fixture.NodeA

	response, body := fixture.requestBrowserLogin(t, hub, nodeA.NodeID,
		`{"path":"/pulls/github/acme/widget/1?tab=files&auth_token=leak#review"}`,
		localBearer(hub))
	login := decodeBrowserLogin(t, response, body)
	link, err := url.Parse(login.URL)
	require.NoError(err)
	assert.Equal(nodeA.HTTP.URL, link.Scheme+"://"+link.Host)
	assert.Equal("/pulls/github/acme/widget/1", link.Path)
	assert.Empty(link.Fragment)
	assert.Equal("files", link.Query().Get("tab"))
	assert.False(link.Query().Has("auth_token"), "the source token must not leave in a link")
	assert.NotEmpty(link.Query().Get("login_ticket"))
	assert.True(login.ExpiresAt.After(time.Now()))
	assert.False(login.ExpiresAt.After(time.Now().Add(time.Minute)))

	location, spokeSession := fixture.followBrowserLogin(t, login.URL)
	assert.Equal("/pulls/github/acme/widget/1?tab=files", location)
	assert.True(spokeSession.Secure, "a session set over HTTPS is Secure")
	snapshot, _ := fixture.browserRequest(t, http.MethodGet,
		nodeA.HTTP.URL+"/api/v1/snapshot", "", withCookie(spokeSession))
	assert.Equal(http.StatusOK, snapshot.StatusCode, "the link signs the browser into the spoke")
	unauthenticated, _ := fixture.browserRequest(t, http.MethodGet,
		nodeA.HTTP.URL+"/api/v1/snapshot", "", nil)
	assert.Equal(http.StatusUnauthorized, unauthenticated.StatusCode)

	reused, _ := fixture.browserRequest(t, http.MethodGet, login.URL, "", nil)
	assert.Equal(http.StatusForbidden, reused.StatusCode, "a login link works once")

	response, body = fixture.requestBrowserLogin(t, nodeA, hub.NodeID, "",
		withCookie(spokeSession))
	back := decodeBrowserLogin(t, response, body)
	assert.True(strings.HasPrefix(back.URL, hub.HTTP.URL+"/?login_ticket="), back.URL)
	_, hubSession := fixture.followBrowserLogin(t, back.URL)
	hubSnapshot, _ := fixture.browserRequest(t, http.MethodGet,
		hub.HTTP.URL+"/api/v1/snapshot", "", withCookie(hubSession))
	assert.Equal(http.StatusOK, hubSnapshot.StatusCode,
		"a peer-issued session can hop onward to the next Forge")
}

func TestFleetBrowserLoginRejectsUnreachableTargets(t *testing.T) {
	fixture := newFederatedForgesFixture(t)
	hub, nodeA, nodeB := fixture.Hub, fixture.NodeA, fixture.NodeB
	for _, test := range []struct {
		name   string
		source *federatedDaemonFixture
		nodeID string
		status int
		code   string
		reason string
	}{
		{name: "self alias", source: hub, nodeID: "self", status: http.StatusConflict, code: string(httpapi.CodeConflict), reason: "selfHost"},
		{name: "own node ID", source: nodeA, nodeID: nodeA.NodeID, status: http.StatusConflict, code: string(httpapi.CodeConflict), reason: "selfHost"},
		{name: "devbox", source: hub, nodeID: "devbox:compute-a", status: http.StatusConflict, code: string(httpapi.CodeConflict), reason: "devboxHost"},
		{name: "sibling spoke", source: nodeA, nodeID: nodeB.NodeID, status: http.StatusConflict, code: string(httpapi.CodeConflict), reason: "noDirectFederationCredential"},
		{name: "unknown member", source: hub, nodeID: "40404040404040404040404040404040", status: http.StatusNotFound, code: string(httpapi.CodeNotFound)},
		{name: "malformed host", source: nodeA, nodeID: "not-a-node", status: http.StatusNotFound, code: string(httpapi.CodeNotFound)},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, body := fixture.requestBrowserLogin(
				t, test.source, test.nodeID, `{"path":"/"}`, localBearer(test.source),
			)
			require.Equal(t, test.status, response.StatusCode, string(body))
			problem := decodeFleetProblem(t, string(body))
			assert.Equal(t, test.code, problem.Code)
			if test.reason != "" {
				assert.Equal(t, test.reason, problem.Details["reason"])
			}
		})
	}
}

func TestFleetBrowserLoginValidatesDestinationPath(t *testing.T) {
	fixture := newFederatedForgesFixture(t)
	for _, path := range []string{
		"//evil.example/pulls",
		`/\evil.example`,
		"https://evil.example/pulls",
		"pulls",
		"/pulls\nx",
	} {
		t.Run(path, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			payload, err := json.Marshal(map[string]string{"path": path})
			require.NoError(err)
			response, body := fixture.requestBrowserLogin(
				t, fixture.Hub, fixture.NodeA.NodeID, string(payload), localBearer(fixture.Hub),
			)
			require.Equal(http.StatusBadRequest, response.StatusCode, string(body))
			problem := decodeFleetProblem(t, string(body))
			assert.Equal(string(httpapi.CodeValidationError), problem.Code)
			assert.Equal("path", problem.Details["field"])
		})
	}
}

func TestFleetBrowserLoginIsNotPeerCallable(t *testing.T) {
	fixture := newFederatedForgesFixture(t)
	response, body := fixture.requestBrowserLogin(
		t, fixture.Hub, fixture.NodeB.NodeID, `{"path":"/"}`,
		func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+fixture.nodeAToken) },
	)
	require.Equal(t, http.StatusForbidden, response.StatusCode, string(body))
	assert.Equal(t, "federationRouteNotAllowed", decodeFleetProblem(t, string(body)).Details["reason"])
}

func TestFleetBrowserLoginReportsPeerFailure(t *testing.T) {
	assert := assert.New(t)
	fixture := newFederatedForgesFixture(t)
	fixture.Hub.Switch.offline.Store(true)
	response, body := fixture.requestBrowserLogin(
		t, fixture.NodeA, fixture.Hub.NodeID, `{"path":"/"}`, localBearer(fixture.NodeA),
	)
	require.Equal(t, http.StatusBadGateway, response.StatusCode, string(body))
	problem := decodeFleetProblem(t, string(body))
	assert.Equal(string(httpapi.CodeUpstreamError), problem.Code)
	assert.Equal(fixture.Hub.NodeID, problem.Details["hostKey"])
	assert.Contains(problem.Detail, "the federation hub is unavailable",
		"the peer's own cause reaches the operator")
}
