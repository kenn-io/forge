package server

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/streamapi"
	"go.kenn.io/forge/internal/server/syncevents"
)

func (s *Server) registerFederationEventAPI(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "stream-federation-provider-events",
		Method:      http.MethodGet,
		Path:        "/federation/events",
		Summary:     "Stream hub-owned provider events",
		Tags:        []string{"Fleet"},
		Responses: map[string]*huma.Response{
			"200": {
				Description: "Filtered provider event stream",
				Content: map[string]*huma.MediaType{
					"text/event-stream": {},
				},
			},
		},
	}, s.streamFederationEvents)
}

func (s *Server) streamFederationEvents(
	requestContext context.Context, input *syncevents.FederationEventsInput,
) (*huma.StreamResponse, error) {
	if input.Protocol != providerplane.ProtocolVersionHeaderValue() {
		return nil, httpapi.Conflict(
			httpapi.CodeConflict,
			"federation protocol version does not match",
			map[string]any{"reason": "protocolMismatch"},
		)
	}
	if len(input.LastEventID) > syncevents.MaxFederationCursorLength ||
		len(input.Since) > syncevents.MaxFederationCursorLength {
		return nil, httpapi.BadRequest(
			httpapi.CodeValidationError,
			"federation event cursor exceeds its size limit",
			nil,
		)
	}
	if input.ContentLength != "" && input.ContentLength != "0" {
		return nil, httpapi.BadRequest(
			httpapi.CodeValidationError,
			"federation event requests must not contain a body",
			nil,
		)
	}
	if s.providerRouteSpoke {
		return nil, httpapi.ServiceUnavailable(
			"provider events are available only from the federation hub",
		)
	}
	streamContext := requestContext
	cleanup := func() {}
	if principal, ok := federationauth.PrincipalFromContext(requestContext); ok {
		streamContext, cleanup = s.syncevents.TrackFederationEventStream(
			requestContext, principal.NodeID,
		)
		if _, authorized := s.federationPrincipalEnrollmentState(principal); !authorized {
			cleanup()
			return nil, httpapi.Forbidden(
				"federation enrollment is no longer active",
				map[string]any{"reason": "federationEnrollmentInactive"},
			)
		}
	}
	return &huma.StreamResponse{
		Body: func(ctx huma.Context) {
			defer cleanup()
			ctx.SetHeader("Content-Type", "text/event-stream")
			ctx.SetHeader("Cache-Control", "no-cache")
			ctx.SetHeader("Connection", "keep-alive")

			r, w := humago.Unwrap(ctx)
			rc := http.NewResponseController(w)
			_ = rc.SetWriteDeadline(time.Time{})
			cursor, hasCursor := streamapi.ParseLastEventID(r)
			// The spoke performs an authoritative refresh at the replay barrier,
			// so injecting cached status afterward could overwrite that refresh.
			ch, done := s.hub.Subscribe(streamContext, false)
			streamapi.ServeSSESubscribedFromHubTransformed(
				streamContext, w, rc, s.hub, cursor, hasCursor, ch, done,
				func(uint64) syncevents.Event {
					return syncevents.Event{Type: "reconnect.stale", Data: struct{}{}}
				},
				func(record syncevents.RecordedEvent) (syncevents.RecordedEvent, bool) {
					return record, providerplane.IsHubProviderEvent(record.Event.Type)
				},
				syncevents.WriteFederationReplayComplete,
				nil,
			)
		},
	}, nil
}
