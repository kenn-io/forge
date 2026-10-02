package generated_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/doordash-oss/oapi-codegen-dd/v3/pkg/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient"
	"go.kenn.io/forge/internal/apiclient/generated"
)

func TestMalformedAPIErrorDoesNotExposePartialProblem(t *testing.T) {
	require := require.New(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusBadGateway)
		_, err := w.Write([]byte(`{"title":"unavailable","status":`))
		assert.NoError(t, err)
	}))
	defer server.Close()

	client, err := apiclient.NewWithHTTPClient(server.URL, server.Client())
	require.NoError(err)
	response, err := client.HTTP.StartArchivesWithResponse(t.Context(), &generated.StartArchivesRequestOptions{
		Body: new(generated.ArchiveMutationBody{All: true}),
	})
	require.ErrorContains(err, "decode API error response")
	require.NotNil(response)
	assert.Nil(t, response.Error)
}

func TestPlainTextAPIErrorKeepsRawBody(t *testing.T) {
	require := require.New(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unsupported telemetry event", http.StatusBadRequest)
	}))
	defer server.Close()

	client, err := apiclient.NewWithHTTPClient(server.URL, server.Client())
	require.NoError(err)
	response, err := client.HTTP.CaptureTelemetryEventWithResponse(t.Context(), &generated.CaptureTelemetryEventRequestOptions{
		Body: &generated.CaptureTelemetryEventBody{Event: "app_loaded"},
	})
	apiErr, ok := errors.AsType[*runtime.ClientAPIError](err)
	require.True(ok, "error %v", err)
	require.NotNil(response)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode())
	assert.Nil(t, response.Error)
	assert.Equal(t, "unsupported telemetry event\n", string(response.Body))
}
