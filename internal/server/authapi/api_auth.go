package authapi

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/routepolicy"
)

const AuthCookieName = "forge_auth"

// authBootstrapParam is the query parameter that converts a token
// into a session cookie; it is stripped from the URL by redirect so
// the token does not linger in the location bar or history beyond
// the first load.
const authBootstrapParam = "auth_token"

// browserSessionCookieName carries a session established by a fleet peer's
// one-time login ticket. It grants the same access as the local browser
// cookie only while the issuing peer's enrollment remains active.
const BrowserSessionCookieName = "forge_session"

// loginTicketParam is the query parameter a fleet peer's login link uses to
// deliver a single-use ticket; it is stripped by redirect once consumed.
const LoginTicketParam = "login_ticket"

func TokenEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func HasValidBearer(r *http.Request, expected string) bool {
	if expected == "" {
		return false
	}
	token, ok := RequestBearer(r)
	return ok && TokenEqual(strings.TrimSpace(token), expected)
}

func RequestBearer(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

// handleAuthBootstrap converts a valid ?auth_token= query into the
// session cookie and redirects to the same URL without the parameter.
// Returns true when it wrote a response (redirect or rejection).
func (s *Handlers) HandleAuthBootstrap(
	w http.ResponseWriter, r *http.Request,
) bool {
	token := r.URL.Query().Get(authBootstrapParam)
	if token == "" {
		return false
	}
	if !TokenEqual(token, s.DaemonRequests.Token) {
		http.Error(w, "invalid auth token", http.StatusForbidden)
		return true
	}
	http.SetCookie(w, &http.Cookie{
		Name:     AuthCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	redirect := *r.URL
	query := redirect.Query()
	query.Del(authBootstrapParam)
	redirect.RawQuery = query.Encode()
	target := redirect.String()
	if target == "" {
		target = "/"
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
	return true
}

// requestArrivedOverHTTPS reports whether the browser reached this daemon
// over TLS, either directly or through a trusted reverse proxy that records
// the original scheme.
func (s *Handlers) RequestArrivedOverHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if hostOpts := s.HostOpts.Load(); hostOpts == nil || !hostOpts.TrustReverseProxy {
		return false
	}
	if values := r.Header.Values("X-Forwarded-Proto"); len(values) > 0 {
		return len(values) == 1 && strings.EqualFold(strings.TrimSpace(values[0]), "https")
	}
	values := r.Header.Values("Forwarded")
	if len(values) != 1 || strings.Contains(values[0], ",") {
		return false
	}
	for part := range strings.SplitSeq(values[0], ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && strings.EqualFold(strings.TrimSpace(key), "proto") {
			return strings.EqualFold(strings.Trim(strings.TrimSpace(value), `"`), "https")
		}
	}
	return false
}

func BrowserWebSocketOriginAllowed(r *http.Request) bool {
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") {
		return true
	}
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) != 1 {
		return false
	}
	origin, err := url.Parse(strings.TrimSpace(origins[0]))
	if err != nil || origin.Scheme != "https" ||
		origin.User != nil || origin.Host == "" || origin.Path != "" ||
		origin.RawQuery != "" || origin.Fragment != "" {
		return false
	}
	originHost, err := config.ParseHostKey(origin.Host)
	if err != nil {
		return false
	}
	requestHost, err := config.ParseHostKey(r.Host)
	if err != nil {
		return false
	}
	if originHost.Port == "" {
		originHost.Port = "443"
	}
	if requestHost.Port == "" {
		requestHost.Port = "443"
	}
	return originHost.Equal(requestHost)
}

func PendingProviderRouteAllowed(method, path string) bool {
	if method != http.MethodPost {
		return false
	}
	switch path {
	case "/api/v1/federation/provider/repository-descriptor",
		"/api/v1/federation/provider/workspace-launch-spec",
		"/api/v1/federation/provider-state/review-drafts/import",
		"/api/v1/federation/provider-state/workflow-states/import":
		return true
	default:
		return false
	}
}

func (s *Handlers) IsPreEnrollmentRequest(r *http.Request) bool {
	return r.Method == http.MethodPost &&
		s.CanonicalAPIPath(r) == "/api/v1/federation/enrollments"
}

func WriteFederationAuthProblem(
	w http.ResponseWriter, detail string, details map[string]any,
) {
	routepolicy.WriteProblemResponse(w, httpapi.NewProblem(
		http.StatusForbidden, httpapi.CodeForbidden, detail, details,
	))
}

func (s *Handlers) CanonicalAPIPath(r *http.Request) string {
	path := r.URL.EscapedPath()
	if (*s.BasePath) == "/" {
		return path
	}
	prefix := strings.TrimSuffix((*s.BasePath), "/")
	return strings.TrimPrefix(path, prefix)
}

// isGatedAPIRequest reports whether the path is a route subject to
// auth: the REST API under /api/ and the terminal WebSocket routes
// under /ws/, which open interactive shells and must not be reachable
// without a credential. Health probes are exempt so supervisors can
// poll liveness before reading the token file. Browsers carry the
// session cookie on the WebSocket upgrade, so the same cookie/bearer
// check applies uniformly.
func (s *Handlers) IsGatedAPIRequest(r *http.Request) bool {
	path := r.URL.Path
	if (*s.BasePath) != "/" {
		prefix := strings.TrimSuffix((*s.BasePath), "/")
		path = strings.TrimPrefix(path, prefix)
	}
	return strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/ws/")
}

// redactedQuery renders a URL's query for logging with credential
// parameters masked, so bootstrap tokens and login tickets never land in
// debug logs.
func RedactedQuery(u *url.URL) string {
	query := u.Query()
	redacted := false
	for _, param := range []string{authBootstrapParam, LoginTicketParam} {
		if _, ok := query[param]; ok {
			query.Set(param, "REDACTED")
			redacted = true
		}
	}
	if redacted {
		return query.Encode()
	}
	// Pairs Go refuses to parse (for example with ';') are kept verbatim by
	// RawQuery, so mask the whole query rather than risk logging a secret.
	if strings.Contains(u.RawQuery, authBootstrapParam) ||
		strings.Contains(u.RawQuery, LoginTicketParam) {
		return "REDACTED"
	}
	return u.RawQuery
}
