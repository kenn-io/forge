package fleetapi

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/server/httpapi"
)

const (
	browserLoginTicketParam = "login_ticket"
	browserAuthTokenParam   = "auth_token"
	maxBrowserLoginTicket   = 128
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

func (h *Handler) registerBrowserLoginRoute(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "create-fleet-browser-login",
		Method:      http.MethodPost,
		Path:        "/fleet/hosts/{node_id}/browser-login",
		Summary:     "Create a one-time link that signs this browser into another Forge",
		Tags:        []string{"Fleet"},
	}, h.createBrowserLogin)
}

// createBrowserLogin asks a directly credentialed fleet peer for a one-time
// login ticket and returns the link that consumes it. The peer call uses this
// daemon's outbound federation bearer; the browser never sees that bearer.
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
	target, err := h.resolveBrowserLoginTarget(strings.TrimSpace(input.NodeID))
	if err != nil {
		return nil, err
	}
	ticket, err := h.requestBrowserLoginTicket(ctx, target)
	if err != nil {
		return nil, httpapi.NewProblem(
			http.StatusBadGateway, httpapi.CodeUpstreamError,
			"fleet peer browser login failed: "+err.Error(),
			map[string]any{"hostKey": target.member.NodeID},
		)
	}
	query := destination.RawQuery
	if query != "" {
		query += "&"
	}
	query += browserLoginTicketParam + "=" + url.QueryEscape(ticket.Ticket)
	link := strings.TrimRight(target.member.BaseURL, "/") +
		destination.EscapedPath() + "?" + query
	return &browserLoginOutput{Body: browserLoginOutputBody{
		URL: link, ExpiresAt: ticket.ExpiresAt.UTC(),
	}}, nil
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

// resolveBrowserLoginTarget returns a fleet peer this daemon can call with
// its own outbound federation bearer. A hub reaches its active spokes; a
// spoke reaches only its hub. Other fleet members have no direct credential.
func (h *Handler) resolveBrowserLoginTarget(nodeID string) (fleetHostTarget, error) {
	switch {
	case nodeID == "":
		return fleetHostTarget{}, fleetHostNotFoundProblem(nodeID)
	case nodeID == fleetSelfHostAlias || nodeID == h.fleetSelfKey(""):
		return fleetHostTarget{}, browserLoginConflict(
			nodeID, "selfHost", "this browser is already signed in to this Forge",
		)
	case strings.HasPrefix(nodeID, "devbox:"):
		return fleetHostTarget{}, browserLoginConflict(
			nodeID, "devboxHost", "devboxes do not serve a browser UI",
		)
	}
	fleetConfig := h.configSnapshot().Fleet
	if !fleetConfig.Enabled {
		return fleetHostTarget{}, fleetHostNotFoundProblem(nodeID)
	}
	noCredential := browserLoginConflict(
		nodeID, "noDirectFederationCredential",
		"this Forge has no active federation credential for that fleet host",
	)
	if fleetConfig.RoleOrDefault() == config.FleetRoleHub {
		for _, member := range fleetConfig.Members {
			if member.NodeID != nodeID {
				continue
			}
			target, ok := h.resolveEnrolledSpoke(member)
			if !ok {
				return fleetHostTarget{}, noCredential
			}
			return target, nil
		}
		return fleetHostTarget{}, fleetHostNotFoundProblem(nodeID)
	}
	hub := fleetConfig.Hub
	if hub != nil && hub.NodeID == nodeID {
		if !h.federationActive {
			return fleetHostTarget{}, noCredential
		}
		target, ok := h.resolveEnrolledMember(config.FleetMember{
			NodeID: hub.NodeID, Name: hub.Name,
			BaseURL: hub.BaseURL, State: federation.EnrollmentActive,
		})
		if !ok {
			return fleetHostTarget{}, noCredential
		}
		return target, nil
	}
	// A spoke learns about sibling spokes only through the hub aggregate and
	// never holds their credentials, so any other node ID is a sibling it
	// cannot sign into directly.
	if federation.ValidNodeID(nodeID) {
		return fleetHostTarget{}, noCredential
	}
	return fleetHostTarget{}, fleetHostNotFoundProblem(nodeID)
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
	if err := h.fetchFederationJSON(
		ctx, target, target.clients.rest,
		h.configSnapshot().Fleet.PeerTimeoutOrDefault(), request, &ticket,
	); err != nil {
		return browserLoginTicket{}, err
	}
	if !validBrowserLoginTicket(ticket.Ticket) {
		return browserLoginTicket{}, fmt.Errorf("peer returned an invalid login ticket")
	}
	if ticket.ExpiresAt.IsZero() {
		return browserLoginTicket{}, fmt.Errorf("peer returned a login ticket without an expiry")
	}
	return ticket, nil
}

func validBrowserLoginTicket(ticket string) bool {
	if len(ticket) < 43 || len(ticket) > maxBrowserLoginTicket {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(ticket)
	return err == nil
}
