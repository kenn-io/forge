package browserloginapi

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/server/httpapi"
)

type BrowserLoginTicketBody struct {
	Ticket    string    `json:"ticket" doc:"Opaque single-use secret; append it to a page URL as login_ticket."`
	ExpiresAt time.Time `json:"expires_at" doc:"UTC instant after which the ticket is rejected."`
}

type browserLoginTicketOutput = httpapi.BodyOutput[BrowserLoginTicketBody]

// registerBrowserLoginAPI exposes the peer-only ticket route. An active fleet
// peer calls it with its federation bearer on behalf of a browser user who
// is switching machines; the ticket then establishes a browser session here.
func (s *Handlers) RegisterBrowserLoginAPI(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "issue-federation-browser-login-ticket",
		Method:      http.MethodPost,
		Path:        "/federation/browser-login-tickets",
		Summary:     "Issue a one-time browser login ticket for a fleet peer",
		Tags:        []string{"Fleet"},
	}, s.issueBrowserLoginTicket)
}

func (s *Handlers) issueBrowserLoginTicket(
	ctx context.Context, _ *struct{},
) (*browserLoginTicketOutput, error) {
	principal, ok := federationauth.PrincipalFromContext(ctx)
	if !ok {
		return nil, httpapi.Forbidden(
			"browser login tickets are issued only to fleet peers",
			map[string]any{"reason": "federationPrincipalRequired"},
		)
	}
	ticket, grant, err := (*s.BrowserLoginTickets).Issue(principal.NodeID)
	if err != nil {
		return nil, httpapi.Internal("issue browser login ticket: " + err.Error())
	}
	return &browserLoginTicketOutput{Body: BrowserLoginTicketBody{
		Ticket: ticket, ExpiresAt: grant.ExpiresAt,
	}}, nil
}
