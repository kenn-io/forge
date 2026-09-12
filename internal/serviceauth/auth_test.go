package serviceauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/tokenauth"
)

const testOwnerID int64 = 4242

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type githubFixture struct {
	mu           sync.Mutex
	codes        map[string]int64
	initialTTL   int
	refreshCalls int
	refreshMode  string
	refreshGate  chan struct{}
	currentUser  int64
}

func (f *githubFixture) roundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch {
	case req.URL.String() == "https://github.com/login/oauth/access_token":
		requireHeader(req, "Accept", "application/json")
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		values, err := url.ParseQuery(string(body))
		if err != nil {
			return nil, err
		}
		if values.Get("grant_type") == "refresh_token" {
			f.refreshCalls++
			gate := f.refreshGate
			mode := f.refreshMode
			f.mu.Unlock()
			if gate != nil {
				<-gate
			}
			f.mu.Lock()
			switch mode {
			case "invalid-success":
				return jsonResponse(http.StatusOK, `{"error":"bad_refresh_token"}`), nil
			case "invalid":
				return jsonResponse(http.StatusBadRequest, `{"error":"bad_refresh_token","error_description":"secret refresh token"}`), nil
			case "transient":
				return jsonResponse(http.StatusServiceUnavailable, `temporary outage with secret refresh token`), nil
			default:
				return jsonResponse(http.StatusOK, `{"access_token":"access-2","refresh_token":"refresh-2","expires_in":3600,"refresh_token_expires_in":7200,"token_type":"bearer"}`), nil
			}
		}
		ownerID, ok := f.codes[values.Get("code")]
		if !ok {
			return jsonResponse(http.StatusBadRequest, `{"error":"bad_verification_code"}`), nil
		}
		if values.Get("code_verifier") == "" || values.Get("redirect_uri") != "https://forge.example/team/auth/github/callback" {
			return jsonResponse(http.StatusBadRequest, `{"error":"invalid_request"}`), nil
		}
		ttl := f.initialTTL
		if ttl == 0 {
			ttl = 3600
		}
		f.currentUser = ownerID
		return jsonResponse(http.StatusOK, `{"access_token":"access-1","refresh_token":"refresh-1","expires_in":`+strconv.Itoa(ttl)+`,"refresh_token_expires_in":7200,"token_type":"bearer"}`), nil
	case req.URL.String() == "https://api.github.com/user":
		requireHeader(req, "Authorization", "Bearer access-1")
		return jsonResponse(http.StatusOK, `{"login":"owner","id":`+strconv.FormatInt(f.currentUser, 10)+`}`), nil
	default:
		return nil, errors.New("unexpected GitHub request: " + req.URL.String())
	}
}

func requireHeader(req *http.Request, name, value string) {
	if req.Header.Get(name) != value {
		panic(name + " header mismatch")
	}
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func newTestManager(t *testing.T, fixture *githubFixture, dataDir string) (*Manager, Options) {
	t.Helper()
	secretPath := filepath.Join(dataDir, "client-secret")
	require.NoError(t, os.WriteFile(secretPath, []byte("client-secret\n"), 0o600))
	opts := Options{
		ClientID:         "client-id",
		ClientSecretFile: secretPath,
		BaseURL:          "https://forge.example/team/",
		DataDir:          dataDir,
		OwnerID:          testOwnerID,
		HTTPClient:       &http.Client{Transport: roundTripFunc(fixture.roundTrip)},
	}
	manager, err := New(opts)
	require.NoError(t, err)
	return manager, opts
}

func startLogin(t *testing.T, manager *Manager) (*http.Cookie, string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	manager.StartLogin(recorder, httptest.NewRequest(http.MethodGet, "https://forge.example/team/auth/github/login", nil))
	require.Equal(t, http.StatusFound, recorder.Code)
	location, err := url.Parse(recorder.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "https://github.com/login/oauth/authorize", location.Scheme+"://"+location.Host+location.Path)
	require.Equal(t, "client-id", location.Query().Get("client_id"))
	require.Equal(t, "https://forge.example/team/auth/github/callback", location.Query().Get("redirect_uri"))
	require.Equal(t, "S256", location.Query().Get("code_challenge_method"))
	require.NotEmpty(t, location.Query().Get("code_challenge"))
	cookies := recorder.Result().Cookies()
	require.Len(t, cookies, 1)
	require.True(t, cookies[0].Secure)
	require.True(t, cookies[0].HttpOnly)
	require.Equal(t, http.SameSiteLaxMode, cookies[0].SameSite)
	require.Equal(t, "/team/auth/github", cookies[0].Path)
	return cookies[0], location.Query().Get("state")
}

func completeLogin(t *testing.T, manager *Manager, code string) *http.Cookie {
	t.Helper()
	stateCookie, state := startLogin(t, manager)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://forge.example/team/auth/github/callback?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(state), nil)
	req.AddCookie(stateCookie)
	manager.CompleteLogin(recorder, req)
	require.Equal(t, http.StatusSeeOther, recorder.Code, recorder.Body.String())
	require.Equal(t, "https://forge.example/team", recorder.Header().Get("Location"))
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == sessionCookieName {
			require.True(t, cookie.Secure)
			require.True(t, cookie.HttpOnly)
			require.Equal(t, http.SameSiteLaxMode, cookie.SameSite)
			require.Equal(t, "/team", cookie.Path)
			return cookie
		}
	}
	require.FailNow(t, "callback did not set a session cookie")
	return nil
}

func TestLoginOwnerBindingAndPersistence(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := &githubFixture{codes: map[string]int64{"owner-code": testOwnerID}}
	manager, opts := newTestManager(t, fixture, t.TempDir())
	session := completeLogin(t, manager, "owner-code")

	assert.Equal(Status{Connected: true, Login: "owner", UserID: testOwnerID}, manager.Status())
	req := httptest.NewRequest(http.MethodGet, "https://forge.example/team/api", nil)
	req.AddCookie(session)
	assert.True(manager.Authenticated(req))
	token, err := manager.Token(t.Context())
	require.NoError(err)
	assert.Equal("access-1", token)

	restarted, err := New(opts)
	require.NoError(err)
	assert.Equal(manager.Status(), restarted.Status())
	assert.True(restarted.Authenticated(req))

	info, err := os.Stat(filepath.Join(opts.DataDir, tokenFileName))
	require.NoError(err)
	assert.Equal(os.FileMode(0o600), info.Mode().Perm())
}

func TestLoginRejectsInvalidStateAndAnotherOwnerWithoutReplacingAuthorization(t *testing.T) {
	assert := assert.New(t)
	fixture := &githubFixture{codes: map[string]int64{
		"owner-code": testOwnerID,
		"other-code": 9999,
	}}
	manager, _ := newTestManager(t, fixture, t.TempDir())
	session := completeLogin(t, manager, "owner-code")

	stateCookie, _ := startLogin(t, manager)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://forge.example/team/auth/github/callback?code=owner-code&state=wrong", nil)
	req.AddCookie(stateCookie)
	manager.CompleteLogin(recorder, req)
	assert.Equal(http.StatusBadRequest, recorder.Code)
	assert.Equal(Status{Connected: true, Login: "owner", UserID: testOwnerID}, manager.Status())

	stateCookie, state := startLogin(t, manager)
	recorder = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "https://forge.example/team/auth/github/callback?code=other-code&state="+url.QueryEscape(state), nil)
	req.AddCookie(stateCookie)
	manager.CompleteLogin(recorder, req)
	assert.Equal(http.StatusForbidden, recorder.Code)
	assert.Equal(Status{Connected: true, Login: "owner", UserID: testOwnerID}, manager.Status())
	authReq := httptest.NewRequest(http.MethodGet, "https://forge.example/team/api", nil)
	authReq.AddCookie(session)
	assert.True(manager.Authenticated(authReq))
}

func TestRefreshRotatesPersistedCredentialsOnceAcrossManagers(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	gate := make(chan struct{})
	fixture := &githubFixture{
		codes:       map[string]int64{"owner-code": testOwnerID},
		initialTTL:  1,
		refreshGate: gate,
	}
	manager, opts := newTestManager(t, fixture, t.TempDir())
	completeLogin(t, manager, "owner-code")
	dataDir, err := filepath.Abs(opts.DataDir)
	require.NoError(err)
	storeLocks.Delete(filepath.Join(dataDir, lockFileName))
	second, err := New(opts)
	require.NoError(err)

	tokens := make(chan string, 2)
	errs := make(chan error, 2)
	for _, current := range []*Manager{manager, second} {
		go func() {
			token, tokenErr := current.Token(t.Context())
			tokens <- token
			errs <- tokenErr
		}()
	}
	require.Eventually(func() bool {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		return fixture.refreshCalls == 1
	}, time.Second, time.Millisecond)
	close(gate)
	for range 2 {
		require.NoError(<-errs)
		assert.Equal("access-2", <-tokens)
	}
	fixture.mu.Lock()
	assert.Equal(1, fixture.refreshCalls)
	fixture.mu.Unlock()

	restarted, err := New(opts)
	require.NoError(err)
	token, err := restarted.Token(t.Context())
	require.NoError(err)
	assert.Equal("access-2", token)
}

func TestRefreshFailureClassification(t *testing.T) {
	for _, test := range []struct {
		name       string
		mode       string
		wantSigned bool
	}{
		{name: "transient failure preserves authorization", mode: "transient"},
		{name: "invalid refresh signs out", mode: "invalid", wantSigned: true},
		{name: "invalid refresh in successful HTTP response signs out", mode: "invalid-success", wantSigned: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			fixture := &githubFixture{
				codes:       map[string]int64{"owner-code": testOwnerID},
				initialTTL:  1,
				refreshMode: test.mode,
			}
			manager, opts := newTestManager(t, fixture, t.TempDir())
			session := completeLogin(t, manager, "owner-code")
			_, err := manager.Token(t.Context())
			if test.wantSigned {
				require.ErrorIs(err, ErrSignedOut)
				assert.False(manager.Status().Connected)
				req := httptest.NewRequest(http.MethodGet, "https://forge.example/team/api", nil)
				req.AddCookie(session)
				assert.False(manager.Authenticated(req))
			} else {
				require.Error(err)
				require.NotErrorIs(err, ErrSignedOut)
				assert.True(manager.Status().Connected)
				restarted, restartErr := New(opts)
				require.NoError(restartErr)
				assert.True(restarted.Status().Connected)
			}
		})
	}
}

func TestInvalidateForcesRefreshOnlyForCurrentToken(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := &githubFixture{codes: map[string]int64{"owner-code": testOwnerID}}
	manager, _ := newTestManager(t, fixture, t.TempDir())
	completeLogin(t, manager, "owner-code")

	manager.Invalidate("some-other-token")
	token, err := manager.Token(t.Context())
	require.NoError(err)
	assert.Equal("access-1", token)
	manager.Invalidate("access-1")
	token, err = manager.Token(t.Context())
	require.NoError(err)
	assert.Equal("access-2", token)
}

func TestLogoutDisconnectsAndClearsCanonicalCookie(t *testing.T) {
	assert := assert.New(t)
	fixture := &githubFixture{codes: map[string]int64{"owner-code": testOwnerID}}
	manager, _ := newTestManager(t, fixture, t.TempDir())
	session := completeLogin(t, manager, "owner-code")
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "https://forge.example/team/auth/github/logout", nil)
	req.AddCookie(session)
	manager.Logout(recorder, req)

	assert.Equal(http.StatusSeeOther, recorder.Code)
	assert.Equal("https://forge.example/team", recorder.Header().Get("Location"))
	require.Len(t, recorder.Result().Cookies(), 1)
	assert.Equal(-1, recorder.Result().Cookies()[0].MaxAge)
	assert.Equal("/team", recorder.Result().Cookies()[0].Path)
	assert.False(manager.Status().Connected)
	_, err := manager.Token(t.Context())
	assert.ErrorIs(err, ErrSignedOut)
}

func TestNewValidatesLocallyWithoutGitHubRequest(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	dataDir := t.TempDir()
	secretPath := filepath.Join(dataDir, "secret")
	require.NoError(os.WriteFile(secretPath, []byte("secret"), 0o600))
	requests := atomic.Int32{}
	manager, err := New(Options{
		ClientID:         "client",
		ClientSecretFile: secretPath,
		BaseURL:          "https://forge.example/prefix",
		DataDir:          dataDir,
		OwnerID:          1,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			requests.Add(1)
			return nil, errors.New("network must not be used")
		})},
	})
	require.NoError(err)
	assert.NotNil(manager)
	assert.Zero(requests.Load())

	for _, opts := range []Options{
		{},
		{ClientID: "client", ClientSecretFile: secretPath, BaseURL: "http://forge.example", DataDir: dataDir, OwnerID: 1},
		{ClientID: "client", ClientSecretFile: secretPath, BaseURL: "https://forge.example?q=1", DataDir: dataDir, OwnerID: 1},
		{ClientID: "client", ClientSecretFile: secretPath, BaseURL: "https://forge.example", DataDir: dataDir},
	} {
		_, err := New(opts)
		assert.Error(err)
	}
}

func TestDescriptorSelectsOnlyGitHubAppUser(t *testing.T) {
	fixture := &githubFixture{codes: map[string]int64{"owner-code": testOwnerID}}
	manager, _ := newTestManager(t, fixture, t.TempDir())
	assert.Equal(t, tokenauth.Descriptor{
		Key: tokenauth.Key{Platform: "github", Host: "github.com"},
		Candidates: []tokenauth.Candidate{{
			Kind: tokenauth.SourceKindGitHubAppUser,
			Host: "github.com",
		}},
	}, manager.Descriptor())
}

func TestCallbackRejectsBadGitHubResponsesWithoutLeakingBodies(t *testing.T) {
	validToken := `{"access_token":"secret-access","refresh_token":"secret-refresh","expires_in":3600,"refresh_token_expires_in":7200,"token_type":"bearer"}`
	for _, test := range []struct {
		name        string
		tokenStatus int
		tokenBody   string
		userStatus  int
		userBody    string
	}{
		{name: "token rejection", tokenStatus: http.StatusBadRequest, tokenBody: `secret token rejection`},
		{name: "malformed token", tokenStatus: http.StatusOK, tokenBody: `{"access_token":`},
		{name: "oversized token", tokenStatus: http.StatusOK, tokenBody: strings.Repeat("secret", maxResponseBytes)},
		{name: "user rejection", tokenStatus: http.StatusOK, tokenBody: validToken, userStatus: http.StatusUnauthorized, userBody: `secret user rejection`},
		{name: "malformed user", tokenStatus: http.StatusOK, tokenBody: validToken, userStatus: http.StatusOK, userBody: `{"login":`},
		{name: "oversized user", tokenStatus: http.StatusOK, tokenBody: validToken, userStatus: http.StatusOK, userBody: strings.Repeat("secret", maxResponseBytes)},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			dataDir := t.TempDir()
			secretPath := filepath.Join(dataDir, "secret")
			require.NoError(os.WriteFile(secretPath, []byte("secret"), 0o600))
			manager, err := New(Options{
				ClientID:         "client-id",
				ClientSecretFile: secretPath,
				BaseURL:          "https://forge.example/team",
				DataDir:          dataDir,
				OwnerID:          testOwnerID,
				HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					if req.URL.String() == "https://github.com/login/oauth/access_token" {
						return jsonResponse(test.tokenStatus, test.tokenBody), nil
					}
					return jsonResponse(test.userStatus, test.userBody), nil
				})},
			})
			require.NoError(err)
			stateCookie, state := startLogin(t, manager)
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "https://forge.example/team/auth/github/callback?code=code&state="+url.QueryEscape(state), nil)
			req.AddCookie(stateCookie)
			manager.CompleteLogin(recorder, req)
			assert.Equal(http.StatusBadGateway, recorder.Code)
			assert.NotContains(recorder.Body.String(), "secret")
			assert.False(manager.Status().Connected)
		})
	}
}

func TestStatusTreatsExpiredAccessWithLiveRefreshAsConnected(t *testing.T) {
	fixture := &githubFixture{codes: map[string]int64{"owner-code": testOwnerID}, initialTTL: 1}
	manager, _ := newTestManager(t, fixture, t.TempDir())
	completeLogin(t, manager, "owner-code")
	assert.Equal(t, Status{Connected: true, Login: "owner", UserID: testOwnerID}, manager.Status())
}

func TestTokenHonorsCanceledContextWhileWaitingForRefresh(t *testing.T) {
	fixture := &githubFixture{codes: map[string]int64{"owner-code": testOwnerID}, initialTTL: 1, refreshGate: make(chan struct{})}
	manager, _ := newTestManager(t, fixture, t.TempDir())
	completeLogin(t, manager, "owner-code")
	done := make(chan error, 1)
	go func() {
		_, err := manager.Token(t.Context())
		done <- err
	}()
	require.Eventually(t, func() bool {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		return fixture.refreshCalls == 1
	}, time.Second, time.Millisecond)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := manager.Token(ctx)
	require.ErrorIs(t, err, context.Canceled)
	close(fixture.refreshGate)
	require.NoError(t, <-done)
}
