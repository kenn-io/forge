package server

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"

	"go.kenn.io/forge/internal/server/httpapi"
)

// API auth gates /api and /ws routes behind the daemon's bearer token
// when the server is configured to require it (ServerOptions.DaemonAccess,
// minted under data_dir at serve start). Two credentials are
// accepted: an `Authorization: Bearer <token>` header (CLI, native
// thin clients, SSE over plain HTTP clients) and the session cookie a
// browser obtains once by loading any page with `?auth_token=<token>`
// — the tokenized URL recorded next to the runtime metadata. Health
// probes (/healthz, /livez) stay open so supervisors can poll before
// they have read the token file.

const authCookieName = "forge_auth"

// authBootstrapParam is the query parameter that converts a token
// into a session cookie; it is stripped from the URL by redirect so
// the token does not linger in the location bar or history beyond
// the first load.
const authBootstrapParam = "auth_token"

func tokenEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func hasValidBearer(r *http.Request, expected string) bool {
	if expected == "" {
		return false
	}
	header := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(header, "Bearer ")
	return ok && tokenEqual(strings.TrimSpace(token), expected)
}

// handleAuthBootstrap converts a valid ?auth_token= query into the
// session cookie and redirects to the same URL without the parameter.
// Returns true when it wrote a response (redirect or rejection).
func (s *Server) handleAuthBootstrap(
	w http.ResponseWriter, r *http.Request,
) bool {
	token := r.URL.Query().Get(authBootstrapParam)
	if token == "" {
		return false
	}
	if !tokenEqual(token, s.daemonRequests.token) {
		http.Error(w, "invalid auth token", http.StatusForbidden)
		return true
	}
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
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

// authorizeAPIRequest reports whether the request carries a valid
// credential for a gated API route, writing the 401 when it does not.
func (s *Server) authorizeAPIRequest(
	w http.ResponseWriter, r *http.Request,
) bool {
	if hasValidBearer(r, s.daemonRequests.token) {
		return true
	}
	if cookie, err := r.Cookie(authCookieName); err == nil {
		if tokenEqual(cookie.Value, s.daemonRequests.token) {
			return true
		}
	}
	w.Header().Set("WWW-Authenticate", `Bearer realm="kenn-forge"`)
	writeProblemResponse(w, httpapi.NewProblem(
		http.StatusUnauthorized,
		httpapi.CodeUnauthorized,
		"missing or invalid API auth token",
		nil,
	))
	return false
}

// isGatedAPIRequest reports whether the path is a route subject to
// auth: the REST API under /api/ and the terminal WebSocket routes
// under /ws/, which open interactive shells and must not be reachable
// without a credential. Health probes are exempt so supervisors can
// poll liveness before reading the token file. Browsers carry the
// session cookie on the WebSocket upgrade, so the same cookie/bearer
// check applies uniformly.
func (s *Server) isGatedAPIRequest(r *http.Request) bool {
	path := r.URL.Path
	if s.basePath != "/" {
		prefix := strings.TrimSuffix(s.basePath, "/")
		path = strings.TrimPrefix(path, prefix)
	}
	return strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/ws/")
}

// redactedQuery renders a URL's query for logging with credential
// parameters masked, so the auth bootstrap token never lands in
// debug logs.
func redactedQuery(u *url.URL) string {
	query := u.Query()
	if _, ok := query[authBootstrapParam]; !ok {
		return u.RawQuery
	}
	query.Set(authBootstrapParam, "REDACTED")
	return query.Encode()
}
