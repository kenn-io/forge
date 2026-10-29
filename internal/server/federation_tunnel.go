package server

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/coder/websocket"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/fleettunnel"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/routepolicy"
)

const federationTunnelPath = "/federation/tunnel"

// registerFederationTunnelAPI accepts the tunnel each active spoke keeps open
// to its hub. The hub sends its spoke requests through that tunnel, so a spoke
// the hub cannot dial stays reachable.
func (s *Server) registerFederationTunnelAPI(api huma.API) {
	api.Adapter().Handle(&huma.Operation{
		OperationID: "connect-federation-tunnel",
		Method:      http.MethodGet,
		Path:        federationTunnelPath,
		Hidden:      true,
	}, func(ctx huma.Context) {
		r, w := humago.Unwrap(ctx)
		s.serveFederationTunnel(w, r)
	})
}

func (s *Server) serveFederationTunnel(w http.ResponseWriter, r *http.Request) {
	if s.providerRouteSpoke {
		routepolicy.WriteProblemResponse(w, httpapi.NewProblem(
			http.StatusServiceUnavailable, httpapi.CodeServiceUnavailable,
			"fleet tunnels are accepted only by the federation hub", nil,
		))
		return
	}
	principal, ok := federationauth.PrincipalFromContext(r.Context())
	if !ok {
		authapi.WriteFederationAuthProblem(
			w, "fleet tunnels require a spoke federation credential",
			map[string]any{"reason": "federationCredentialRequired"},
		)
		return
	}
	if r.Header.Get(providerplane.ProtocolVersionHeader) !=
		providerplane.ProtocolVersionHeaderValue() {
		routepolicy.WriteProblemResponse(w, httpapi.NewProblem(
			http.StatusConflict, httpapi.CodeConflict,
			"federation protocol version does not match",
			map[string]any{"reason": "protocolMismatch"},
		))
		return
	}
	// Revocation cancels the spoke's federation streams, which closes the
	// tunnel together with its event stream.
	ctx, cleanup := s.syncevents.TrackFederationEventStream(r.Context(), principal.NodeID)
	defer cleanup()
	if _, authorized := s.federationPrincipalEnrollmentState(principal); !authorized {
		authapi.WriteFederationAuthProblem(
			w, "federation enrollment is no longer active",
			map[string]any{"reason": "federationEnrollmentInactive"},
		)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	err = s.tunnels.Serve(
		ctx, principal.NodeID, websocket.NetConn(ctx, conn, websocket.MessageBinary),
	)
	slog.Debug("fleet tunnel closed", "node_id", principal.NodeID, "err", err)
}

type hubTunnelRequestKey struct{}

func isHubTunnelRequest(r *http.Request) bool {
	tunneled, _ := r.Context().Value(hubTunnelRequestKey{}).(bool)
	return tunneled
}

// hubTunnelHandler serves the requests the hub sends through this spoke's
// tunnel. Only the hub's federation credential may use the tunnel, so ambient
// credentials and proxy metadata that a direct ingress could carry are dropped
// before the ordinary handler authorizes the request.
func (s *Server) hubTunnelHandler(hubNodeID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del("Cookie")
		r.Header.Del("Forwarded")
		for name := range r.Header {
			if strings.HasPrefix(name, "X-Forwarded-") {
				r.Header.Del(name)
			}
		}
		token, hasToken := authapi.RequestBearer(r)
		principal, authenticated := s.federationAuth.Authenticate(token)
		if !hasToken || !authenticated || principal.NodeID != hubNodeID {
			routepolicy.WriteProblemResponse(w, httpapi.NewProblem(
				http.StatusUnauthorized, httpapi.CodeUnauthorized,
				"fleet tunnel requests require the hub's federation credential", nil,
			))
			return
		}
		s.ServeHTTP(w, r.WithContext(
			context.WithValue(r.Context(), hubTunnelRequestKey{}, true),
		))
	})
}

// newHubTunnelClient builds the active spoke's tunnel to its hub. The tunnel
// authenticates with the spoke's hub credential, read again for every
// connection attempt.
func (s *Server) newHubTunnelClient(
	hub config.FleetHub, options ServerOptions,
) (*fleettunnel.Client, error) {
	return fleettunnel.NewClient(fleettunnel.ClientOptions{
		URL: strings.TrimRight(hub.BaseURL, "/") + "/api/v1" + federationTunnelPath,
		Header: func() (http.Header, error) {
			credential, ok := options.FederationCredentials.Outbound(hub.NodeID)
			if !ok || !slices.Contains(credential.Scopes, federationauth.ScopeEventsRead) {
				return nil, providerplane.ErrCredentialUnavailable
			}
			header := make(http.Header)
			header.Set("Authorization", "Bearer "+credential.Token)
			header.Set(federationauth.NodeIDHeader, options.FederationSpokeID)
			header.Set(providerplane.ProtocolVersionHeader, providerplane.ProtocolVersionHeaderValue())
			return header, nil
		},
		HTTPClient: options.FederationHTTPClient,
		Handler:    s.hubTunnelHandler(hub.NodeID),
	})
}
