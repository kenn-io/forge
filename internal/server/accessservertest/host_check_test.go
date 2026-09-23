package accessservertest

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/server/authapi"
)

func bindLoopback8091() config.HostKey {
	return config.HostKey{Host: "127.0.0.1", Port: "8091"}
}

func directDaemonRequest(t *testing.T, bearer string, headers http.Header) *http.Request {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/snapshot", nil)
	req.Host = "127.0.0.1:8091"
	req.RemoteAddr = "127.0.0.1:1234"
	if headers != nil {
		req.Header = headers.Clone()
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return req
}

// TestDirectDaemonBearerClassification protects the native-client trust
// boundary without constructing the full application. If the classifier is
// widened, cookies, listener aliases, non-loopback peers, or forwarded
// requests can bypass reverse-proxy Host validation.
func TestDirectDaemonBearerClassification(t *testing.T) {
	tests := []struct {
		name, token, host, remoteAddr, bearer string
		headers                               http.Header
		cookie, wantBypass                    bool
		bind                                  config.HostKey
	}{
		{name: "valid", token: "secret", host: "127.0.0.1:8091", remoteAddr: "127.0.0.1:1", bearer: "secret", wantBypass: true},
		{name: "missing bearer", token: "secret", host: "127.0.0.1:8091", remoteAddr: "127.0.0.1:1"},
		{name: "invalid bearer", token: "secret", host: "127.0.0.1:8091", remoteAddr: "127.0.0.1:1", bearer: "wrong"},
		{name: "cookie", token: "secret", host: "127.0.0.1:8091", remoteAddr: "127.0.0.1:1", cookie: true},
		{name: "listener alias", token: "secret", host: "localhost:8091", remoteAddr: "127.0.0.1:1", bearer: "secret"},
		{name: "non-loopback peer", token: "secret", host: "127.0.0.1:8091", remoteAddr: "192.0.2.1:1", bearer: "secret"},
		{name: "public host", token: "secret", host: "mm.example.com", remoteAddr: "127.0.0.1:1", bearer: "secret"},
		{name: "forwarded", token: "secret", host: "127.0.0.1:8091", remoteAddr: "127.0.0.1:1", bearer: "secret", headers: http.Header{"Forwarded": {"host=mm.example.com"}}},
		{name: "missing token", host: "127.0.0.1:8091", remoteAddr: "127.0.0.1:1", bearer: "secret"},
		{name: "IPv6", token: "secret", bind: config.HostKey{Host: "[::1]", Port: "8091"}, host: "[::1]:8091", remoteAddr: "[::1]:1", bearer: "secret", wantBypass: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bind := bindLoopback8091()
			if tt.bind.Host != "" {
				bind = tt.bind
			}
			req := directDaemonRequest(t, tt.bearer, tt.headers)
			req.Host, req.RemoteAddr = tt.host, tt.remoteAddr
			if tt.cookie {
				req.AddCookie(&http.Cookie{Name: authapi.AuthCookieName, Value: "secret"})
			}
			rr := httptest.NewRecorder()

			admission := (authapi.DaemonRequestPolicy{Token: tt.token}).Admit(
				rr, req, authapi.HostCheckOptions{Bind: bind}, true,
			)

			assert.False(t, admission.Handled)
			assert.Equal(t, tt.wantBypass, admission.BypassProxyHostCheck)
		})
	}
}

// TestParseForwardedHost exercises the unexported helpers
// directly, covering the zero-length-header and
// malformed-but-present cases that httptest.NewRequest cannot
// otherwise hand to the middleware (Go's http.Header normalises
// empty values away on set).
func TestParseForwardedHost(t *testing.T) {
	t.Run("Forwarded", func(t *testing.T) {
		cases := []struct {
			name   string
			input  string
			wantOK bool
			want   config.HostKey
		}{
			{
				name:   "host param",
				input:  "host=mm.example.com",
				wantOK: true,
				want:   config.HostKey{Host: "mm.example.com", Port: ""},
			},
			{
				name:   "for and host",
				input:  "for=10.0.0.1;host=mm.example.com",
				wantOK: true,
				want:   config.HostKey{Host: "mm.example.com", Port: ""},
			},
			{
				name:   "quoted host",
				input:  `host="mm.example.com"`,
				wantOK: true,
				want:   config.HostKey{Host: "mm.example.com", Port: ""},
			},
			{
				name:   "case-insensitive param",
				input:  "Host=mm.example.com",
				wantOK: true,
				want:   config.HostKey{Host: "mm.example.com", Port: ""},
			},
			{
				name:   "multiple entries rejected",
				input:  "host=mm.example.com, host=attacker.example",
				wantOK: false,
			},
			{
				name:   "first entry lacks host=",
				input:  "for=10.0.0.1, host=mm.example.com",
				wantOK: false,
			},
			{
				name:   "empty",
				input:  "",
				wantOK: false,
			},
			{
				name:   "garbage",
				input:  "wat",
				wantOK: false,
			},
			{
				name:   "empty quoted host",
				input:  `host=""`,
				wantOK: false,
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got, err := authapi.ParseForwardedHost(tc.input)
				if tc.wantOK {
					require.NoError(t, err)
					assert.Equal(t, tc.want, got)
				} else {
					assert.Error(t, err)
				}
			})
		}
	})

	t.Run("X-Forwarded-Host", func(t *testing.T) {
		cases := []struct {
			name   string
			input  string
			wantOK bool
			want   config.HostKey
		}{
			{
				name:   "single host",
				input:  "mm.example.com",
				wantOK: true,
				want:   config.HostKey{Host: "mm.example.com", Port: ""},
			},
			{
				name:   "host with port",
				input:  "mm.example.com:8443",
				wantOK: true,
				want:   config.HostKey{Host: "mm.example.com", Port: "8443"},
			},
			{
				name:   "multiple values rejected",
				input:  "mm.example.com, attacker.example",
				wantOK: false,
			},
			{
				name:   "leading whitespace trimmed",
				input:  "  mm.example.com",
				wantOK: true,
				want:   config.HostKey{Host: "mm.example.com", Port: ""},
			},
			{
				name:   "empty",
				input:  "",
				wantOK: false,
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got, err := authapi.ParseXForwardedHost(tc.input)
				if tc.wantOK {
					require.NoError(t, err)
					assert.Equal(t, tc.want, got)
				} else {
					assert.Error(t, err)
				}
			})
		}
	})
}
