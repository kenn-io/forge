package generated_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

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
