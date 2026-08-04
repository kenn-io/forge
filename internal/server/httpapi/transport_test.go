package httpapi

import (
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateTransportAccept(t *testing.T) {
	op := &huma.Operation{}
	AddTransportRoutes(op,
		TransportRoute{
			Method: http.MethodGet, Path: "/api/events",
			Transport: TransportHTTPStream, Accept: "application/x-ndjson",
		},
		TransportRoute{
			Method: http.MethodGet, Path: "/api/output",
			Transport: TransportHTTPStream, Accept: "application/x-ndjson",
			Query: map[string]string{"stream": "1"},
		},
	)

	tests := []struct {
		name   string
		target string
		accept string
		want   bool
	}{
		{name: "declared stream", target: "/api/events", accept: "application/x-ndjson", want: true},
		{name: "declared stream with parameters", target: "/api/events", accept: "application/json, application/x-ndjson; charset=utf-8", want: true},
		{name: "missing accept", target: "/api/events", want: false},
		{name: "rejected accept", target: "/api/events", accept: "application/x-ndjson; q=0", want: false},
		{name: "query stream", target: "/api/output?stream=1", accept: "application/x-ndjson", want: true},
		{name: "query stream missing accept", target: "/api/output?stream=1", want: false},
		{name: "ordinary query variant", target: "/api/output", want: true},
		{name: "unrelated route", target: "/api/jobs", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, tt.target, nil)
			require.NoError(t, err)
			if tt.accept != "" {
				req.Header.Set("Accept", tt.accept)
			}

			accepted, err := ValidateTransportAccept(op, req)
			require.NoError(t, err)
			assert.Equal(t, tt.want, accepted)
		})
	}
}
