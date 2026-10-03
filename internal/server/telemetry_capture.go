package server

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"reflect"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/routepolicy"
	kittelemetry "go.kenn.io/kit/telemetry"
)

// The size cap matches kit's handler so oversized bodies still get problem JSON; the timeout is Huma's default.
const (
	telemetryEventMaxBodyBytes    = 64 << 10
	telemetryEventBodyReadTimeout = 5 * time.Second
)

// TelemetryEventInputBody documents the request body kit's capture handler reads.
type TelemetryEventInputBody struct {
	Event      string         `json:"event"`
	Properties map[string]any `json:"properties,omitempty"`
}

// TelemetryEventResponse documents the 202 body kit's capture handler writes.
type TelemetryEventResponse struct {
	Status string `json:"status"`
}

func (s *Server) registerTelemetryCapture(api huma.API) {
	schemas := api.OpenAPI().Components.Schemas
	problem := &huma.Schema{Ref: "#/components/schemas/ProblemError"}
	plainText := &huma.Schema{Type: "string"}
	op := &huma.Operation{
		OperationID: "capture-telemetry-event",
		Method:      http.MethodPost,
		Path:        "/telemetry/events",
		Summary:     "Capture telemetry event",
		Tags:        []string{"System"},
		RequestBody: &huma.RequestBody{
			Required: true,
			Content: map[string]*huma.MediaType{
				"application/json": {Schema: schemas.Schema(reflect.TypeFor[TelemetryEventInputBody](), true, "TelemetryEventInputBody")},
			},
		},
		Responses: map[string]*huma.Response{
			"202": {
				Description: "Accepted",
				Content: map[string]*huma.MediaType{
					"application/json": {Schema: schemas.Schema(reflect.TypeFor[TelemetryEventResponse](), true, "TelemetryEventResponse")},
				},
			},
			"400": {
				Description: "Unsupported event, malformed body, or unreadable body",
				Content: map[string]*huma.MediaType{
					"text/plain":               {Schema: plainText},
					"application/problem+json": {Schema: problem},
				},
			},
			"500": {
				Description: "Capture failed",
				Content: map[string]*huma.MediaType{
					"text/plain": {Schema: plainText},
				},
			},
			"default": {
				Description: "Error",
				Content: map[string]*huma.MediaType{
					"application/problem+json": {Schema: problem},
				},
			},
		},
	}
	api.OpenAPI().AddOperation(op)
	api.Adapter().Handle(op, api.Middlewares().Handler(func(ctx huma.Context) {
		r, raw := humago.Unwrap(ctx)
		// Write through the Huma context so the compression middleware sees the response.
		w := &humaResponseWriter{ctx: ctx, header: raw.Header()}
		if !telemetryContentTypeAllowed(w, r) || !readTelemetryBody(ctx, w, r) {
			return
		}
		handler := s.telemetryCapture
		if handler == nil {
			handler = kittelemetry.NewPostHogCaptureHandler(nil)
		}
		handler.ServeHTTP(w, r)
	}))
}

// humaResponseWriter routes an http.Handler's response through a Huma context.
type humaResponseWriter struct {
	ctx         huma.Context
	header      http.Header
	wroteHeader bool
}

func (w *humaResponseWriter) Header() http.Header { return w.header }

func (w *humaResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.ctx.SetStatus(status)
}

func (w *humaResponseWriter) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.ctx.BodyWriter().Write(p)
}

func telemetryContentTypeAllowed(w http.ResponseWriter, r *http.Request) bool {
	// Kit accepts only application/json; rejecting here keeps the 415 as problem JSON.
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err == nil && mediaType == "application/json" {
		return true
	}
	routepolicy.WriteProblemResponse(w, httpapi.NewProblem(
		http.StatusUnsupportedMediaType, httpapi.CodeBadRequest, "Content-Type must be application/json", nil,
	))
	return false
}

// readTelemetryBody reads the whole bounded body before kit decodes one JSON value from it.
func readTelemetryBody(ctx huma.Context, w http.ResponseWriter, r *http.Request) bool {
	tooLarge := httpapi.NewProblem(
		http.StatusRequestEntityTooLarge, httpapi.CodePayloadTooLarge,
		"telemetry request body is too large", map[string]any{"maxBytes": telemetryEventMaxBodyBytes},
	)
	if r.ContentLength > telemetryEventMaxBodyBytes {
		routepolicy.WriteProblemResponse(w, tooLarge)
		return false
	}
	_ = ctx.SetReadDeadline(time.Now().Add(telemetryEventBodyReadTimeout))
	body, err := io.ReadAll(io.LimitReader(r.Body, telemetryEventMaxBodyBytes+1))
	_ = ctx.SetReadDeadline(time.Time{})
	if err != nil {
		if netErr, ok := errors.AsType[net.Error](err); ok && netErr.Timeout() {
			routepolicy.WriteProblemResponse(w, httpapi.NewProblem(
				http.StatusRequestTimeout, httpapi.CodeBadRequest, "telemetry request body read timed out", nil,
			))
			return false
		}
		routepolicy.WriteProblemResponse(w, httpapi.NewProblem(
			http.StatusBadRequest, httpapi.CodeBadRequest, "could not read telemetry request body", nil,
		))
		return false
	}
	if int64(len(body)) > telemetryEventMaxBodyBytes {
		routepolicy.WriteProblemResponse(w, tooLarge)
		return false
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	return true
}
