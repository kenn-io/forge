package server

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/routepolicy"
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

// handleLoginTicketBootstrap converts a valid ?login_ticket= on a page load
// into a browser session cookie and redirects to the same URL without the
// parameter. The ticket is consumed even when the request is then rejected.
// Returns true when it wrote a response (redirect or rejection).
func (s *Server) handleLoginTicketBootstrap(
	w http.ResponseWriter, r *http.Request,
) bool {
	query := r.URL.Query()
	if !query.Has(authapi.LoginTicketParam) || r.Method != http.MethodGet ||
		s.authapi.IsGatedAPIRequest(r) {
		return false
	}
	grant, ok := s.browserLoginTickets.Consume(query.Get(authapi.LoginTicketParam))
	if !ok || !s.browserLoginPeerActive(grant.NodeID) {
		http.Error(w, "invalid or expired login ticket", http.StatusForbidden)
		return true
	}
	session, sessionGrant, err := s.browserSessions.Issue(grant.NodeID)
	if err != nil {
		http.Error(w, "browser session unavailable", http.StatusInternalServerError)
		return true
	}
	http.SetCookie(w, &http.Cookie{
		Name:     authapi.BrowserSessionCookieName,
		Value:    session,
		Path:     "/",
		Expires:  sessionGrant.ExpiresAt,
		HttpOnly: true,
		Secure:   s.authapi.RequestArrivedOverHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
	query.Del(authapi.LoginTicketParam)
	// Collapse leading slashes so the relative Location can never become a
	// scheme-relative redirect to another origin.
	target := "/" + strings.TrimLeft(r.URL.EscapedPath(), "/")
	if encoded := query.Encode(); encoded != "" {
		target += "?" + encoded
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
	return true
}

// browserLoginPeerActive reports whether the fleet peer that issued a login
// ticket or session still holds an active enrollment. Revocation, disabled
// federation, and an expired activation lease all end its browser sessions.
func (s *Server) browserLoginPeerActive(nodeID string) bool {
	state, ok := s.federationPrincipalEnrollmentState(
		federationauth.Principal{NodeID: nodeID},
	)
	return ok && state == federation.EnrollmentActive
}

func (s *Server) hasValidBrowserSession(r *http.Request) bool {
	cookie, err := r.Cookie(authapi.BrowserSessionCookieName)
	if err != nil {
		return false
	}
	grant, ok := s.browserSessions.Lookup(cookie.Value)
	return ok && s.browserLoginPeerActive(grant.NodeID)
}

// authorizeAPIRequest reports whether the request carries a valid
// credential for a gated API route, writing the 401 when it does not.
func (s *Server) authorizeAPIRequest(
	w http.ResponseWriter, r *http.Request,
) bool {
	if s.options.ExecutionWorker {
		if authapi.HasValidBearer(r, s.daemonRequests.Token) {
			return true
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="kenn-forge-worker"`)
		routepolicy.WriteProblemResponse(w, httpapi.NewProblem(http.StatusUnauthorized, httpapi.CodeUnauthorized, "missing or invalid worker bearer", nil))
		return false
	}
	if s.authapi.IsPreEnrollmentRequest(r) {
		return true
	}
	if authapi.HasValidBearer(r, s.daemonRequests.Token) {
		return true
	}
	if cookie, err := r.Cookie(authapi.AuthCookieName); err == nil {
		if authapi.TokenEqual(cookie.Value, s.daemonRequests.Token) {
			return true
		}
	}
	// Federation requests sent through Tailscale Serve also carry its user
	// identity header. Authenticate the narrower bearer first so handlers retain
	// the spoke principal and federation scope checks still apply.
	if token, ok := authapi.RequestBearer(r); ok && s.federationAuth != nil {
		if principal, authenticated := s.federationAuth.Authenticate(token); authenticated {
			return s.authorizeFederationRequest(w, r, principal)
		}
	}
	// Tailscale Serve identity and peer-issued browser sessions are network
	// browser credentials, so both reject cross-origin WebSocket upgrades.
	if s.daemonRequests.AcceptsTailscaleServeUser(r) || s.hasValidBrowserSession(r) {
		if !authapi.BrowserWebSocketOriginAllowed(r) {
			routepolicy.WriteProblemResponse(w, httpapi.NewProblem(
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
	routepolicy.WriteProblemResponse(w, httpapi.NewProblem(
		http.StatusUnauthorized,
		httpapi.CodeUnauthorized,
		"missing or invalid API auth token",
		nil,
	))
	return false
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
			local.ActivationLeaseVersion == federation.ActivationLeaseVersion &&
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
	if enrollment.State != federation.EnrollmentActive ||
		enrollment.ActivationLeaseVersion != federation.ActivationLeaseVersion ||
		!enrollment.ActivationValidUntil.After(s.now().UTC()) {
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

func (s *Server) authorizeFederationRequest(
	w http.ResponseWriter, r *http.Request, principal federationauth.Principal,
) bool {
	if claimed := r.Header.Get(federationauth.NodeIDHeader); claimed != "" &&
		claimed != principal.NodeID {
		authapi.WriteFederationAuthProblem(
			w,
			"federation credential subject does not match the supplied node ID",
			map[string]any{"reason": "federationSubjectMismatch"},
		)
		return false
	}
	canonicalPath := s.authapi.CanonicalAPIPath(r)
	enrollmentState, enrolled := s.federationPrincipalEnrollmentState(principal)
	if !enrolled && s.allowsActivationLeaseHandshake(r, canonicalPath, principal) {
		enrollmentState = federation.EnrollmentActive
		enrolled = true
	}
	if !enrolled && s.allowsSpokeEnrollmentRevocation(r, canonicalPath, principal) {
		enrollmentState = federation.EnrollmentRevoked
		enrolled = true
	}
	if !enrolled {
		authapi.WriteFederationAuthProblem(
			w,
			"federation credential is not attached to an authorized enrollment",
			map[string]any{"reason": "federationEnrollmentInactive"},
		)
		return false
	}
	required, listed := s.federationAuth.RequiredScope(r.Method, canonicalPath)
	providerRule, providerOwned := providerRouteRuleForRequest(r.Method, canonicalPath)
	providerOwned = providerOwned && providerRule.Owner != routepolicy.NodeLocal
	if !listed && providerOwned {
		required = providerRule.PeerScope
		listed = true
	}
	if enrollmentState == federation.EnrollmentPending && providerOwned &&
		!authapi.PendingProviderRouteAllowed(r.Method, canonicalPath) {
		authapi.WriteFederationAuthProblem(
			w,
			"pending federation credentials cannot access this provider route",
			map[string]any{"reason": "federationEnrollmentPending"},
		)
		return false
	}
	if !listed {
		authapi.WriteFederationAuthProblem(
			w,
			"federation credentials cannot access this route",
			map[string]any{"reason": "federationRouteNotAllowed"},
		)
		return false
	}
	if !principal.Has(required) {
		authapi.WriteFederationAuthProblem(
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
		routepolicy.WriteProblemResponse(w, httpapi.NewProblem(
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

func (s *Server) allowsActivationLeaseHandshake(
	r *http.Request, canonicalPath string, principal federationauth.Principal,
) bool {
	s.cfgMu.Lock()
	fleetEnabled := s.cfg != nil && s.cfg.Fleet.Enabled
	s.cfgMu.Unlock()
	if !fleetEnabled || s.options.FederationEnrollments == nil ||
		s.bootCfgSnapshot.FleetRole != config.FleetRoleHub {
		return false
	}
	enrollment, ok := s.options.FederationEnrollments.EnrollmentForSpoke(principal.NodeID)
	if !ok || enrollment.State != federation.EnrollmentActive {
		return false
	}
	if r.Method == http.MethodGet && canonicalPath == "/api/v1/federation/identity" {
		return true
	}
	return r.Method == http.MethodPost && canonicalPath ==
		"/api/v1/federation/enrollments/"+url.PathEscape(enrollment.ID)+"/activate"
}

func (s *Server) allowsSpokeEnrollmentRevocation(
	r *http.Request, canonicalPath string, principal federationauth.Principal,
) bool {
	if r.Method != http.MethodDelete || s.options.FederationEnrollments == nil {
		return false
	}
	local, ok := s.options.FederationEnrollments.Local()
	if !ok || (local.State != federation.EnrollmentActive &&
		local.State != federation.EnrollmentRevoked) ||
		local.HubID != principal.NodeID {
		return false
	}
	s.cfgMu.Lock()
	fleetEnabled := s.cfg != nil && s.cfg.Fleet.Enabled
	s.cfgMu.Unlock()
	return fleetEnabled && canonicalPath ==
		"/api/v1/fleet/enrollments/"+url.PathEscape(local.EnrollmentID)
}
