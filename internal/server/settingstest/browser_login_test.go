package settingstest

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/server/httpapi"
)

func decodeProblem(t *testing.T, response *http.Response) httpapi.ProblemError {
	t.Helper()
	var problem httpapi.ProblemError
	require.NoError(t, json.NewDecoder(response.Body).Decode(&problem))
	return problem
}

func TestBrowserLoginTicketRejectsLocalCredentials(t *testing.T) {
	require := require.New(t)
	ts, _, _ := newFederationAuthTestServer(t, federationauth.ScopeBrowserLogin)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		ts.URL+"/api/v1/federation/browser-login-tickets", nil)
	require.NoError(err)
	request.Header.Set("Authorization", "Bearer local-secret")
	response, err := ts.Client().Do(request)
	require.NoError(err)
	defer response.Body.Close()
	require.Equal(http.StatusForbidden, response.StatusCode)
	assert.Equal(t, "federationPrincipalRequired", decodeProblem(t, response).Details["reason"])
}
