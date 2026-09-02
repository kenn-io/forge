package server

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/providerplane"
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
	token, ok := requestBearer(r)
	return ok && tokenEqual(strings.TrimSpace(token), expected)
}

func requestBearer(r *http.Request) (string, bool) {
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
	if s.isPreEnrollmentRequest(r) {
		return true
	}
	if hasValidBearer(r, s.daemonRequests.token) {
		return true
	}
	if cookie, err := r.Cookie(authCookieName); err == nil {
		if tokenEqual(cookie.Value, s.daemonRequests.token) {
			return true
		}
	}
	// Federation requests sent through Tailscale Serve also carry its user
	// identity header. Authenticate the narrower bearer first so handlers retain
	// the spoke principal and federation scope checks still apply.
	if token, ok := requestBearer(r); ok && s.federationAuth != nil {
		if principal, authenticated := s.federationAuth.Authenticate(token); authenticated {
			return s.authorizeFederationRequest(w, r, principal)
		}
	}
	if s.daemonRequests.acceptsTailscaleServeUser(r) {
		if !tailscaleWebSocketOriginAllowed(r) {
			writeProblemResponse(w, httpapi.NewProblem(
				http.StatusForbidden,
				httpapi.CodeForbidden,
				"cross-origin WebSocket access is not allowed",
				nil,
			))
			return false
		}
		return true
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

func tailscaleWebSocketOriginAllowed(r *http.Request) bool {
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

func (s *Server) federationPrincipalEnrollmentState(
	principal federationauth.Principal,
) (federation.EnrollmentState, bool) {
	if s.options.FederationEnrollments == nil {
		return federation.EnrollmentActive, true
	}
	s.cfgMu.Lock()
	if s.cfg == nil || !s.cfg.Fleet.Enabled {
		s.cfgMu.Unlock()
		return "", false
	}
	members := append([]config.FleetMember{}, s.cfg.Fleet.Members...)
	s.cfgMu.Unlock()
	role := s.bootCfgSnapshot.FleetRole
	var hub *config.FleetHub
	if s.bootCfgSnapshot.Hub != nil {
		hub = &config.FleetHub{
			NodeID:  s.bootCfgSnapshot.Hub.NodeID,
			BaseURL: s.bootCfgSnapshot.Hub.BaseURL,
		}
	}
	local, hasLocal := s.options.FederationEnrollments.Local()
	if hasLocal && local.State == federation.EnrollmentPending &&
		local.HubID == principal.NodeID {
		return federation.EnrollmentPending,
			local.PreparationStarted || local.ExpiresAt.After(s.now().UTC())
	}

	if role == config.FleetRoleSpoke {
		if !hasLocal || hub == nil ||
			hub.NodeID != principal.NodeID ||
			local.HubID != principal.NodeID {
			return "", false
		}
		if local.State == federation.EnrollmentPending {
			return federation.EnrollmentPending,
				local.PreparationStarted || local.ExpiresAt.After(s.now().UTC())
		}
		return federation.EnrollmentActive, s.options.FederationSpokeActive &&
			local.State == federation.EnrollmentActive &&
			local.ActivationValidUntil.After(s.now().UTC()) &&
			hub.NodeID == principal.NodeID &&
			local.HubID == principal.NodeID
	}

	enrollment, ok := s.options.FederationEnrollments.EnrollmentForSpoke(principal.NodeID)
	if !ok || enrollment.State == federation.EnrollmentRevoked {
		return "", false
	}
	if enrollment.State == federation.EnrollmentPending {
		return federation.EnrollmentPending,
			enrollment.PreparationStarted || enrollment.ExpiresAt.After(s.now().UTC())
	}
	if enrollment.State != federation.EnrollmentActive {
		return "", false
	}
	for _, member := range members {
		if member.NodeID == principal.NodeID &&
			member.BaseURL == enrollment.SpokeBaseURL &&
			member.State == federation.EnrollmentActive {
			return federation.EnrollmentActive, true
		}
	}
	return "", false
}

func pendingProviderRouteAllowed(method, path string) bool {
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

func (s *Server) isPreEnrollmentRequest(r *http.Request) bool {
	return r.Method == http.MethodPost &&
		s.canonicalAPIPath(r) == "/api/v1/federation/enrollments"
}

func (s *Server) authorizeFederationRequest(
	w http.ResponseWriter, r *http.Request, principal federationauth.Principal,
) bool {
	if claimed := r.Header.Get(federationauth.NodeIDHeader); claimed != "" &&
		claimed != principal.NodeID {
		writeFederationAuthProblem(
			w,
			"federation credential subject does not match the supplied node ID",
			map[string]any{"reason": "federationSubjectMismatch"},
		)
		return false
	}
	canonicalPath := s.canonicalAPIPath(r)
	enrollmentState, enrolled := s.federationPrincipalEnrollmentState(principal)
	if !enrolled && s.allowsRevokedSpokeRevocation(r, canonicalPath, principal) {
		enrollmentState = federation.EnrollmentRevoked
		enrolled = true
	}
	if !enrolled {
		writeFederationAuthProblem(
			w,
			"federation credential is not attached to an authorized enrollment",
			map[string]any{"reason": "federationEnrollmentInactive"},
		)
		return false
	}
	required, listed := s.federationAuth.RequiredScope(r.Method, canonicalPath)
	providerRule, providerOwned := providerRouteRuleForRequest(r.Method, canonicalPath)
	providerOwned = providerOwned && providerRule.Owner != NodeLocal
	if !listed && providerOwned {
		required = providerRule.PeerScope
		listed = true
	}
	if enrollmentState == federation.EnrollmentPending && providerOwned &&
		!pendingProviderRouteAllowed(r.Method, canonicalPath) {
		writeFederationAuthProblem(
			w,
			"pending federation credentials cannot access this provider route",
			map[string]any{"reason": "federationEnrollmentPending"},
		)
		return false
	}
	if !listed {
		writeFederationAuthProblem(
			w,
			"federation credentials cannot access this route",
			map[string]any{"reason": "federationRouteNotAllowed"},
		)
		return false
	}
	if !principal.Has(required) {
		writeFederationAuthProblem(
			w,
			"federation credential does not grant the required scope",
			map[string]any{
				"reason": "federationScopeDenied", "required_scope": required,
			},
		)
		return false
	}
	if providerOwned && r.Header.Get(providerplane.ProtocolVersionHeader) !=
		strconv.Itoa(federation.ProtocolVersion) {
		writeProblemResponse(w, httpapi.NewProblem(
			http.StatusConflict,
			httpapi.CodeConflict,
			"federation protocol version does not match",
			map[string]any{
				"reason":   "protocolMismatch",
				"expected": federation.ProtocolVersion,
			},
		))
		return false
	}
	*r = *r.WithContext(federationauth.WithPrincipal(r.Context(), principal))
	return true
}

func (s *Server) allowsRevokedSpokeRevocation(
	r *http.Request, canonicalPath string, principal federationauth.Principal,
) bool {
	if r.Method != http.MethodDelete || s.options.FederationEnrollments == nil {
		return false
	}
	local, ok := s.options.FederationEnrollments.Local()
	if !ok || local.State != federation.EnrollmentRevoked ||
		local.HubID != principal.NodeID {
		return false
	}
	s.cfgMu.Lock()
	fleetEnabled := s.cfg != nil && s.cfg.Fleet.Enabled
	s.cfgMu.Unlock()
	return fleetEnabled && canonicalPath ==
		"/api/v1/fleet/enrollments/"+url.PathEscape(local.EnrollmentID)
}

func writeFederationAuthProblem(
	w http.ResponseWriter, detail string, details map[string]any,
) {
	writeProblemResponse(w, httpapi.NewProblem(
		http.StatusForbidden, httpapi.CodeForbidden, detail, details,
	))
}

func (s *Server) canonicalAPIPath(r *http.Request) string {
	path := r.URL.EscapedPath()
	if s.basePath == "/" {
		return path
	}
	prefix := strings.TrimSuffix(s.basePath, "/")
	return strings.TrimPrefix(path, prefix)
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
