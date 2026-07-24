package server

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"go.kenn.io/middleman/internal/server/httpapi"
	"go.kenn.io/middleman/internal/server/kataapi"
)

type streamKataTaskEventsInput struct {
	DaemonID string `header:"X-Middleman-Kata-Daemon" doc:"Kata daemon id; the effective default daemon when empty"`
}

func (s *Server) registerKataFrontendAPI(api huma.API) {
	huma.Get(api, "/kata/tasks/snapshot", s.kataTaskSnapshot,
		httpapi.DocumentOperation("get-kata-task-snapshot", "Get authoritative Kata task snapshot", "Kata"))
	huma.Get(api, "/kata/tasks/references", s.kataTaskReferences,
		httpapi.DocumentOperation("search-kata-task-references", "Search Kata task references", "Kata"))
	huma.Register(api, huma.Operation{
		OperationID: "stream-kata-task-events",
		Method:      http.MethodGet,
		Path:        "/kata/tasks/events",
		Summary:     "Stream Kata task invalidations",
		Tags:        []string{"Kata"},
		Responses: map[string]*huma.Response{
			"200": {
				Description: "Server-sent Kata task invalidation stream",
				Content: map[string]*huma.MediaType{"text/event-stream": {}},
			},
		},
	}, s.streamKataTaskEvents)
}

func (s *Server) streamKataTaskEvents(
	_ context.Context,
	input *streamKataTaskEventsInput,
) (*huma.StreamResponse, error) {
	daemon, problem := s.kataAPI.SelectDaemonForID(input.DaemonID)
	if problem != nil {
		return nil, huma.ErrorWithHeaders(problem, http.Header{
			"Vary": []string{kataapi.DaemonHeaderName},
		})
	}
	binding, err := s.kataEvents.Ensure(daemon)
	if err != nil {
		return nil, huma.ErrorWithHeaders(
			httpapi.ServiceUnavailable("Kata task events are unavailable while the server is shutting down"),
			http.Header{"Vary": []string{kataapi.DaemonHeaderName}},
		)
	}
	return &huma.StreamResponse{Body: func(ctx huma.Context) {
		ctx.SetHeader("Content-Type", "text/event-stream")
		ctx.SetHeader("Cache-Control", "no-cache")
		ctx.SetHeader("Connection", "keep-alive")
		ctx.AppendHeader("Vary", kataapi.DaemonHeaderName)

		r, w := humago.Unwrap(ctx)
		rc := http.NewResponseController(w)
		_ = rc.SetWriteDeadline(time.Time{})
		cursor, hasCursor := parseLastEventID(r)
		binding.serve(ctx.Context(), w, rc, cursor, hasCursor)
	}}, nil
}
