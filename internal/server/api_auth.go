package server

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// API auth gates /api and /ws routes behind the daemon's bearer token
// when the server is constructed with one (ServerOptions.APIAuthToken,
// minted under data_dir at serve start). Two credentials are
// accepted: an `Authorization: Bearer <token>` header (CLI, native
// thin clients, SSE over plain HTTP clients) and the session cookie a
// browser obtains once by loading any page with `?auth_token=<token>`
// — the tokenized URL recorded next to the runtime metadata. Health
// probes (/healthz, /livez) stay open so supervisors can poll before
// they have read the token file.

const authCookieName = "middleman_auth"

// authBootstrapParam is the query parameter that converts a token
// into a session cookie; it is stripped from the URL by redirect so
// the token does not linger in the location bar or history beyond
// the first load.
const authBootstrapParam = "auth_token"

func tokenEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// stripBasePath removes the configured base path prefix so /auth/*
// route matching works both at the root and under a mount point.
func (s *Server) stripBasePath(path string) string {
	if s.basePath == "/" {
		return path
	}
	return strings.TrimPrefix(path, strings.TrimSuffix(s.basePath, "/"))
}

func newAuthSessionCookie(token string) *http.Cookie {
	return &http.Cookie{
		Name:     authCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
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
	if !tokenEqual(token, s.apiAuthToken) {
		http.Error(w, "invalid auth token", http.StatusForbidden)
		return true
	}
	http.SetCookie(w, newAuthSessionCookie(token))
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

// handleLogin exchanges a token submitted in a JSON POST body for the
// session cookie. The login overlay uses it instead of navigating to
// the ?auth_token= bootstrap URL so a pasted token never appears in a
// request URI, which reverse proxies commonly log. Same-origin
// enforcement matches the mutating API routes (checkCSRF). Returns
// true when it wrote a response.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) bool {
	if s.stripBasePath(r.URL.Path) != "/auth/login" {
		return false
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return true
	}
	if !checkCSRF(w, r, false) {
		return true
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		http.Error(w, "body must be JSON with a token field", http.StatusBadRequest)
		return true
	}
	if !tokenEqual(strings.TrimSpace(body.Token), s.apiAuthToken) {
		http.Error(w, "invalid auth token", http.StatusForbidden)
		return true
	}
	http.SetCookie(w, newAuthSessionCookie(s.apiAuthToken))
	w.WriteHeader(http.StatusNoContent)
	return true
}

// authorizeAPIRequest reports whether the request carries a valid
// credential for a gated API route, writing the 401 when it does not.
func (s *Server) authorizeAPIRequest(
	w http.ResponseWriter, r *http.Request,
) bool {
	header := r.Header.Get("Authorization")
	if token, ok := strings.CutPrefix(header, "Bearer "); ok {
		if tokenEqual(strings.TrimSpace(token), s.apiAuthToken) {
			return true
		}
	}
	if cookie, err := r.Cookie(authCookieName); err == nil {
		if tokenEqual(cookie.Value, s.apiAuthToken) {
			return true
		}
	}
	w.Header().Set("WWW-Authenticate", `Bearer realm="middleman"`)
	writeProblemResponse(w, newProblem(
		http.StatusUnauthorized,
		CodeUnauthorized,
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
	path := s.stripBasePath(r.URL.Path)
	return strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/ws/")
}

// AuthBootstrapURL renders the tokenized URL a browser loads once to
// obtain the session cookie.
func AuthBootstrapURL(baseURL, token string) string {
	return baseURL + "/?" + authBootstrapParam + "=" + url.QueryEscape(token)
}

// handleLogout expires the session cookie and redirects to the base
// path when the request targets /auth/logout. It is wired ahead of the
// auth-token block so it works whether or not require_auth is on; the
// cookie is HttpOnly, so only the server can clear it. Returns true when
// it wrote a response.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) bool {
	if s.stripBasePath(r.URL.Path) != "/auth/logout" {
		return false
	}
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, s.basePath, http.StatusSeeOther)
	return true
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
