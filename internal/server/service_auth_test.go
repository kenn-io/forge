package server

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/serviceauth"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestServiceOAuthEnablesBrowserAndLogoutRevokesAccess(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	require.NoError(os.WriteFile(secret, []byte("test-secret"), 0o600))
	ownerID := int64(123)
	auth, err := serviceauth.New(serviceauth.Options{
		ClientID: "test-client", ClientSecretFile: secret, OwnerID: ownerID,
		BaseURL: "https://forge.example.com/team", DataDir: dir,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			body := `{"access_token":"delegated","refresh_token":"refresh","expires_in":3600,"refresh_token_expires_in":7200,"token_type":"bearer"}`
			if r.URL.Host == "api.github.com" {
				body = fmt.Sprintf(`{"login":"owner","id":%d}`, ownerID)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})},
	})
	require.NoError(err)
	cfg := &config.Config{Host: "127.0.0.1", Port: 8091, BasePath: "/team/", AllowedHosts: []string{"forge.example.com"}}
	options := ServerOptions{ServiceAuth: auth, DaemonAccess: DaemonAccessOptions{Token: "daemon-secret", RequireAPIAuth: true}, HostCheck: resolveHostCheckOptions(cfg, HostCheckOptions{}, false)}
	startup := NewStartupHandler(nil, cfg, options, staticListener{addr: staticListenerAddr("127.0.0.1:8091")})
	running := New(dbtest.Open(t), nil, nil, "/team/", nil, options)
	login := func() *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		startup.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "https://forge.example.com/team/auth/github/login", nil))
		require.Equal(http.StatusFound, rr.Code)
		location, err := url.Parse(rr.Header().Get("Location"))
		require.NoError(err)
		req := httptest.NewRequest(http.MethodGet, "https://forge.example.com/team/auth/github/callback?code=test&state="+url.QueryEscape(location.Query().Get("state")), nil)
		for _, cookie := range rr.Result().Cookies() {
			req.AddCookie(cookie)
		}
		rr = httptest.NewRecorder()
		startup.ServeHTTP(rr, req)
		return rr
	}
	ownerID = 456
	denied := login()
	require.Equal(http.StatusForbidden, denied.Code)
	assert.False(auth.Status().Connected)
	ownerID = 123
	allowed := login()
	require.Equal(http.StatusSeeOther, allowed.Code)
	request := func(method, path, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://forge.example.com/team"+path, nil)
		for _, cookie := range allowed.Result().Cookies() {
			req.AddCookie(cookie)
		}
		req.Header.Set("Origin", origin)
		rr := httptest.NewRecorder()
		running.ServeHTTP(rr, req)
		return rr
	}
	assert.Equal(http.StatusOK, request(http.MethodGet, "/api/v1/snapshot", "").Code)
	assert.Equal(http.StatusOK, request(http.MethodGet, "/auth/github/status", "").Code)
	assert.Equal(http.StatusForbidden, request(http.MethodPost, "/auth/github/logout", "https://other.example.com").Code)
	assert.True(auth.Status().Connected)
	assert.Equal(http.StatusSeeOther, request(http.MethodPost, "/auth/github/logout", "https://forge.example.com").Code)
	assert.Equal(http.StatusUnauthorized, request(http.MethodGet, "/api/v1/snapshot", "").Code)
	local := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8091/team/api/v1/snapshot", nil)
	local.RemoteAddr = "127.0.0.1:1234"
	local.Header.Set("Authorization", "Bearer daemon-secret")
	rr := httptest.NewRecorder()
	running.ServeHTTP(rr, local)
	assert.Equal(http.StatusOK, rr.Code)
	_, err = auth.Token(t.Context())
	require.ErrorIs(err, serviceauth.ErrSignedOut)
	assert.Equal(http.StatusSeeOther, login().Code)
}

func TestServiceConfigChangeRequiresRestartWithoutApplyingCredentials(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	require.NoError(os.WriteFile(path, []byte(`host = "127.0.0.1"
port = 8091
data_dir = `+fmt.Sprintf("%q", dir)+`
[service]
enabled = true
github_user_id = 123
github_client_id = "client"
github_client_secret_file = "secret"
base_url = "https://forge.example.com"
`), 0o600))
	cfg, err := config.Load(path)
	require.NoError(err)
	srv := NewWithConfig(dbtest.Open(t), nil, nil, nil, cfg, "", ServerOptions{})
	srv.cfgPath = path
	require.NoError(os.WriteFile(path, []byte(`host = "127.0.0.1"
port = 8091
data_dir = `+fmt.Sprintf("%q", dir)), 0o600))
	result := srv.applyConfigChange(t.Context())
	assert.False(result.Valid)
	assert.True(result.RestartRequired)
	assert.True(srv.cfg.Service.Enabled)
}

func TestServiceLoginGatesStartupAndRunningTransports(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	require.NoError(t, os.WriteFile(secret, []byte("test-secret"), 0o600))
	auth, err := serviceauth.New(serviceauth.Options{
		ClientID: "test-client", ClientSecretFile: secret, OwnerID: 123,
		BaseURL: "https://forge.example.com", DataDir: dir,
	})
	require.NoError(t, err)
	cfg := &config.Config{Host: "127.0.0.1", Port: 8091, BasePath: "/", AllowedHosts: []string{"forge.example.com"}}
	options := ServerOptions{ServiceAuth: auth, DaemonAccess: DaemonAccessOptions{
		Token: "daemon-secret", RequireAPIAuth: true,
		TailscaleServeEnabled: true, TailscaleServeUsers: []string{"user@example.com"},
	}}
	frontend := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("private-app")}}
	startup := NewStartupHandler(frontend, cfg, options, staticListener{addr: staticListenerAddr("127.0.0.1:8091")})
	options.HostCheck = resolveHostCheckOptions(cfg, HostCheckOptions{}, false)
	running := New(dbtest.Open(t), nil, frontend, "/", nil, options)
	for name, handler := range map[string]http.Handler{"startup": startup, "running": running} {
		t.Run(name, func(t *testing.T) {
			assert := assert.New(t)
			for _, path := range []string{"/api/v1/settings", "/ws/terminal/test"} {
				for _, credential := range []string{"none", "bearer", "cookie", "tailscale"} {
					req := httptest.NewRequest(http.MethodGet, "https://forge.example.com"+path, nil)
					req.RemoteAddr = "127.0.0.1:1234"
					switch credential {
					case "bearer":
						req.Header.Set("Authorization", "Bearer daemon-secret")
					case "cookie":
						req.AddCookie(&http.Cookie{Name: authCookieName, Value: "daemon-secret"})
					case "tailscale":
						req.Header.Set("Tailscale-User-Login", "user@example.com")
					}
					rr := httptest.NewRecorder()
					handler.ServeHTTP(rr, req)
					assert.Equal(http.StatusUnauthorized, rr.Code, "%s %s", path, credential)
				}
			}
			for _, path := range []string{"/", "/?auth_token=daemon-secret"} {
				rr := httptest.NewRecorder()
				handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "https://forge.example.com"+path, nil))
				assert.Contains(rr.Body.String(), "Sign in with GitHub")
				assert.NotContains(rr.Body.String(), "private-app")
				assert.Empty(rr.Result().Cookies())
			}
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "https://forge.example.com/healthz", nil))
			assert.Equal(http.StatusOK, rr.Code)
		})
	}
}
