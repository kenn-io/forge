package fleetapi

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/server/httpapi"
)

const (
	browserLoginTicketParam = "login_ticket"
	browserAuthTokenParam   = "auth_token"
	maxBrowserLoginTicket   = 128
	// siblingBrowserLoginFailedReason marks a hub 502 that reports the
	// sibling's own failure, so a spoke can tell it apart from a 502 that a
	// proxy in front of a stopped hub returns.
	siblingBrowserLoginFailedReason = "siblingBrowserLoginFailed"
)

type browserLoginRequestBody struct {
	Path string `json:"path,omitempty" maxLength:"8192" doc:"Same-origin page to open on the destination, such as /pulls?state=open. Defaults to /."`
}

type browserLoginInput struct {
	NodeID string `path:"node_id" doc:"Stable node ID of the destination fleet host."`
	Body   *browserLoginRequestBody
}

type browserLoginOutputBody struct {
	URL       string    `json:"url" doc:"One-time login link on the destination Forge."`
	ExpiresAt time.Time `json:"expires_at" doc:"UTC instant after which the link no longer signs in."`
}

type browserLoginOutput = httpapi.BodyOutput[browserLoginOutputBody]

type browserLoginTicket struct {
	Ticket    string    `json:"ticket"`
	ExpiresAt time.Time `json:"expires_at"`
}

type siblingBrowserLoginTicketInput struct {
	NodeID string `path:"node_id" doc:"Stable node ID of the sibling spoke to sign in to."`
}

type siblingBrowserLoginTicketBody struct {
	Ticket    string    `json:"ticket" doc:"Opaque single-use secret issued by the sibling spoke; append it to a page URL there as login_ticket."`
	ExpiresAt time.Time `json:"expires_at" doc:"UTC instant after which the ticket is rejected."`
	BaseURL   string    `json:"base_url" doc:"Enrolled origin of the sibling spoke, the only Forge that accepts the ticket."`
}

type siblingBrowserLoginTicketOutput = httpapi.BodyOutput[siblingBrowserLoginTicketBody]

// browserLoginRoute names the peer this daemon calls for a login ticket. A
// spoke has no credential for a sibling spoke, so it calls its hub, and the
// hub obtains the sibling's ticket with its own credential.
type browserLoginRoute struct {
	target fleetHostTarget
	// sibling is the destination node ID when target is the hub relaying the
	// request; it is empty when target itself issues the ticket.
	sibling string
}

func (h *Handler) registerBrowserLoginRoute(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "create-fleet-browser-login",
		Method:      http.MethodPost,
		Path:        "/fleet/hosts/{node_id}/browser-login",
		Summary:     "Create a one-time link that signs this browser into another Forge",
		Tags:        []string{"Fleet"},
	}, h.createBrowserLogin)
	// The fleet host prefix makes the federation authenticator require the
	// browser.login scope of /federation/browser-login-tickets, the route the
	// hub calls on the sibling.
	huma.Register(api, huma.Operation{
		OperationID: "issue-fleet-host-browser-login-ticket",
		Method:      http.MethodPost,
		Path:        "/fleet/hosts/{node_id}/federation/browser-login-tickets",
		Summary:     "Obtain a sibling spoke's one-time browser login ticket for a spoke",
		Tags:        []string{"Fleet"},
	}, h.issueSiblingBrowserLoginTicket)
}

// createBrowserLogin asks the destination for a one-time login ticket,
// directly or through this spoke's hub, and returns the link that consumes
// it. Peer calls use this daemon's outbound federation bearer; the browser
// never sees that bearer.
func (h *Handler) createBrowserLogin(
	ctx context.Context, input *browserLoginInput,
) (*browserLoginOutput, error) {
	rawPath := ""
	if input.Body != nil {
		rawPath = input.Body.Path
	}
	destination, err := browserLoginDestination(rawPath)
	if err != nil {
		return nil, err
	}
	nodeID := strings.TrimSpace(input.NodeID)
	route, err := h.resolveBrowserLoginTarget(nodeID)
	if err != nil {
		return nil, err
	}
	var ticket browserLoginTicket
	baseURL := route.target.member.BaseURL
	if route.sibling == "" {
		ticket, err = h.requestBrowserLoginTicket(ctx, route.target)
	} else {
		ticket, baseURL, err = h.requestSiblingBrowserLoginTicket(ctx, route)
	}
	if err != nil {
		if problem, ok := errors.AsType[huma.StatusError](err); ok {
			return nil, problem
		}
		return nil, browserLoginUpstreamProblem(nodeID, err)
	}
	query := destination.RawQuery
	if query != "" {
		query += "&"
	}
	query += browserLoginTicketParam + "=" + url.QueryEscape(ticket.Ticket)
	link := strings.TrimRight(baseURL, "/") +
		destination.EscapedPath() + "?" + query
	return &browserLoginOutput{Body: browserLoginOutputBody{
		URL: link, ExpiresAt: ticket.ExpiresAt.UTC(),
	}}, nil
}

func browserLoginUpstreamProblem(nodeID string, err error) error {
	return httpapi.NewProblem(
		http.StatusBadGateway, httpapi.CodeUpstreamError,
		"fleet peer browser login failed: "+err.Error(),
		map[string]any{"hostKey": nodeID},
	)
}

// issueSiblingBrowserLoginTicket lets an active spoke sign its user in to
// another active spoke. The federation authenticator admits only a peer whose
// enrollment is active and whose grant carries browser.login, which on a hub
// means an active enrolled spoke. The hub then calls the sibling with its own
// credential, so the sibling's session stays bound to the hub's enrollment.
func (h *Handler) issueSiblingBrowserLoginTicket(
	ctx context.Context, input *siblingBrowserLoginTicketInput,
) (*siblingBrowserLoginTicketOutput, error) {
	principal, peer := federationauth.PrincipalFromContext(ctx)
	fleetConfig := h.configSnapshot().Fleet
	if !peer || !fleetConfig.Enabled || fleetConfig.RoleOrDefault() != config.FleetRoleHub {
		return nil, httpapi.Forbidden(
			"only a fleet hub issues sibling browser login tickets, and only to its spokes",
			map[string]any{"reason": "federationSpokeRequired"},
		)
	}
	nodeID := strings.TrimSpace(input.NodeID)
	if nodeID == principal.NodeID {
		return nil, browserLoginConflict(
			nodeID, "selfHost", "this browser is already signed in to this Forge",
		)
	}
	route, err := h.resolveBrowserLoginTarget(nodeID)
	if err != nil {
		return nil, err
	}
	ticket, err := h.requestBrowserLoginTicket(ctx, route.target)
	if err != nil {
		// The spoke adds its own login-failure prefix, so the hub sends only
		// the sibling's cause.
		return nil, httpapi.NewProblem(
			http.StatusBadGateway, httpapi.CodeUpstreamError, err.Error(),
			map[string]any{"hostKey": nodeID, "reason": siblingBrowserLoginFailedReason},
		)
	}
	return &siblingBrowserLoginTicketOutput{Body: siblingBrowserLoginTicketBody{
		Ticket: ticket.Ticket, ExpiresAt: ticket.ExpiresAt.UTC(),
		BaseURL: route.target.member.BaseURL,
	}}, nil
}

// requestSiblingBrowserLoginTicket asks the hub for a sibling spoke's ticket
// and returns it with the sibling's enrolled origin. A hub that cannot issue
// one, including an older hub without the route or a hub that is down,
// leaves the browser to open the sibling's plain address, so those outcomes
// keep the 409 the frontend falls back on. A hub that answers with the
// sibling's own failure still reports it. The hub waits up to one peer
// timeout for the sibling, so the spoke allows two for the hub; otherwise a
// stalled sibling would surface as a stalled hub and open the plain link.
func (h *Handler) requestSiblingBrowserLoginTicket(
	ctx context.Context, route browserLoginRoute,
) (browserLoginTicket, string, error) {
	request, err := generated.NewIssueFleetHostBrowserLoginTicketRequest(
		ctx, strings.TrimRight(route.target.member.BaseURL, "/")+"/api/v1",
		&generated.IssueFleetHostBrowserLoginTicketRequestOptions{
			PathParams: &generated.IssueFleetHostBrowserLoginTicketPath{NodeID: route.sibling},
		},
	)
	if err != nil {
		return browserLoginTicket{}, "", err
	}
	var body siblingBrowserLoginTicketBody
	fleet := h.configSnapshot().Fleet
	if err := h.fetchFederationJSON(
		ctx, route.target, route.target.clients.rest,
		2*fleet.PeerTimeoutOrDefault(), request, &body,
	); err != nil {
		if hubCannotRelayBrowserLogin(err) {
			return browserLoginTicket{}, "", noDirectBrowserLoginCredential(route.sibling)
		}
		return browserLoginTicket{}, "", err
	}
	ticket := browserLoginTicket{Ticket: body.Ticket, ExpiresAt: body.ExpiresAt}
	if err := validateBrowserLoginTicket(ticket); err != nil {
		return browserLoginTicket{}, "", err
	}
	baseURL, err := federation.CanonicalOrigin(body.BaseURL)
	if err != nil || baseURL != body.BaseURL {
		return browserLoginTicket{}, "", fmt.Errorf("hub returned an invalid sibling origin")
	}
	return ticket, baseURL, nil
}

// hubCannotRelayBrowserLogin reports a hub outcome that means "no ticket for
// that node": an unknown or inactive sibling, a hub that predates the route
// or has fleet connections turned off, or a hub that is unreachable, stalls,
// or is unavailable, including a proxy's 502 in front of a stopped hub. Hubs
// from before fleet host routes were peer-callable reject the path in
// authentication rather than routing. Only a 502 the hub marks as the
// sibling's own failure is reported to the person.
func hubCannotRelayBrowserLogin(err error) bool {
	if _, ok := errors.AsType[*url.Error](err); ok {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if timeout, ok := errors.AsType[net.Error](err); ok && timeout.Timeout() {
		return true
	}
	refusal, ok := errors.AsType[*federationPeerStatusError](err)
	if !ok {
		return false
	}
	switch refusal.StatusCode {
	case http.StatusNotFound, http.StatusConflict,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	case http.StatusBadGateway:
		return refusal.Reason != siblingBrowserLoginFailedReason
	case http.StatusForbidden:
		return refusal.Reason == "federationRouteNotAllowed" ||
			refusal.Reason == "federationEnrollmentInactive"
	}
	return false
}

// browserLoginDestination validates a same-origin relative reference. The
// fragment is dropped, and credential parameters are removed so a copied
// location can neither leak this daemon's token nor shadow the new ticket.
func browserLoginDestination(raw string) (*url.URL, error) {
	if raw == "" {
		raw = "/"
	}
	for _, char := range raw {
		if char < 0x20 || char == 0x7f {
			return nil, httpapi.Validation("path", "path must not contain control characters")
		}
	}
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") ||
		strings.HasPrefix(raw, `/\`) {
		return nil, httpapi.Validation(
			"path", "path must be a same-origin reference starting with a single /",
		)
	}
	destination, err := url.Parse(raw)
	if err != nil || destination.Scheme != "" || destination.Host != "" ||
		destination.User != nil || destination.Opaque != "" {
		return nil, httpapi.Validation(
			"path", "path must be a same-origin reference starting with a single /",
		)
	}
	destination.Fragment = ""
	destination.RawFragment = ""
	values := destination.Query()
	if values.Has(browserLoginTicketParam) || values.Has(browserAuthTokenParam) {
		values.Del(browserLoginTicketParam)
		values.Del(browserAuthTokenParam)
		destination.RawQuery = values.Encode()
	}
	return destination, nil
}

// resolveBrowserLoginTarget returns the fleet peer this daemon calls with
// its own outbound federation bearer. A hub reaches its active spokes; a
// spoke reaches its hub directly and sibling spokes through that hub.
func (h *Handler) resolveBrowserLoginTarget(nodeID string) (browserLoginRoute, error) {
	switch {
	case nodeID == "":
		return browserLoginRoute{}, fleetHostNotFoundProblem(nodeID)
	case nodeID == fleetSelfHostAlias || nodeID == h.fleetSelfKey(""):
		return browserLoginRoute{}, browserLoginConflict(
			nodeID, "selfHost", "this browser is already signed in to this Forge",
		)
	case strings.HasPrefix(nodeID, "devbox:"):
		return browserLoginRoute{}, browserLoginConflict(
			nodeID, "devboxHost", "devboxes do not serve a browser UI",
		)
	}
	fleetConfig := h.configSnapshot().Fleet
	if !fleetConfig.Enabled {
		return browserLoginRoute{}, fleetHostNotFoundProblem(nodeID)
	}
	if fleetConfig.RoleOrDefault() == config.FleetRoleHub {
		for _, member := range fleetConfig.Members {
			if member.NodeID != nodeID {
				continue
			}
			target, ok := h.resolveEnrolledSpoke(member)
			if !ok {
				return browserLoginRoute{}, noDirectBrowserLoginCredential(nodeID)
			}
			return browserLoginRoute{target: target}, nil
		}
		return browserLoginRoute{}, fleetHostNotFoundProblem(nodeID)
	}
	// A spoke learns about sibling spokes only through the hub aggregate, so
	// any other valid node ID is a sibling the hub signs into on its behalf.
	if (fleetConfig.Hub == nil || fleetConfig.Hub.NodeID != nodeID) &&
		!federation.ValidNodeID(nodeID) {
		return browserLoginRoute{}, fleetHostNotFoundProblem(nodeID)
	}
	hub, ok := h.resolveSpokeHub()
	if !ok {
		return browserLoginRoute{}, noDirectBrowserLoginCredential(nodeID)
	}
	if hub.member.NodeID == nodeID {
		return browserLoginRoute{target: hub}, nil
	}
	return browserLoginRoute{target: hub, sibling: nodeID}, nil
}

func noDirectBrowserLoginCredential(nodeID string) error {
	return browserLoginConflict(
		nodeID, "noDirectFederationCredential",
		"this Forge has no active federation credential for that fleet host",
	)
}

func browserLoginConflict(nodeID, reason, detail string) error {
	return httpapi.Conflict(httpapi.CodeConflict, detail, map[string]any{
		"reason": reason, "hostKey": nodeID,
	})
}

func (h *Handler) requestBrowserLoginTicket(
	ctx context.Context, target fleetHostTarget,
) (browserLoginTicket, error) {
	request, err := generated.NewIssueFederationBrowserLoginTicketRequest(
		ctx, strings.TrimRight(target.member.BaseURL, "/")+"/api/v1",
	)
	if err != nil {
		return browserLoginTicket{}, err
	}
	var ticket browserLoginTicket
	fleet := h.configSnapshot().Fleet
	if err := h.fetchFederationJSON(
		ctx, target, target.clients.rest,
		fleet.PeerTimeoutOrDefault(), request, &ticket,
	); err != nil {
		return browserLoginTicket{}, err
	}
	if err := validateBrowserLoginTicket(ticket); err != nil {
		return browserLoginTicket{}, err
	}
	return ticket, nil
}

func validateBrowserLoginTicket(ticket browserLoginTicket) error {
	if !validBrowserLoginTicket(ticket.Ticket) {
		return errors.New("peer returned an invalid login ticket")
	}
	if ticket.ExpiresAt.IsZero() {
		return errors.New("peer returned a login ticket without an expiry")
	}
	return nil
}

func validBrowserLoginTicket(ticket string) bool {
	if len(ticket) < 43 || len(ticket) > maxBrowserLoginTicket {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(ticket)
	return err == nil
}
