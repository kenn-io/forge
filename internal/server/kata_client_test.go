package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/kata"
)

func TestNewKataAPIClientUsesResolvedTargetAuth(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	var authorization string
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		assert.Equal(t, "/api/v1/instance", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"instance_id":"test","version":"test"}`))
	}))
	t.Cleanup(daemon.Close)

	api, err := newKataAPIClient(t.Context(), kata.Daemon{
		ID:    "work",
		URL:   daemon.URL,
		Token: "secret-token",
	})
	require.NoError(err)

	response, err := api.InstanceWithResponse(t.Context())
	require.NoError(err)
	require.NotNil(response.JSON200)
	assert.Equal(t, "Bearer secret-token", authorization)
}
