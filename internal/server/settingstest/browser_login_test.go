package settingstest

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/federationauth"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	servertest "go.kenn.io/forge/internal/testutil/servertest"
)

func TestBrowserLoginTicketRejectsLocalCredentials(t *testing.T) {
	require := require.New(t)
	ts, _, _ := servertest.NewFederationAuthTestServer(t, federationauth.ScopeBrowserLogin)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		ts.URL+"/api/v1/federation/browser-login-tickets", nil)
	require.NoError(err)
	request.Header.Set("Authorization", "Bearer local-secret")
	response, err := ts.Client().Do(request)
	require.NoError(err)
	defer response.Body.Close()
	require.Equal(http.StatusForbidden, response.StatusCode)
	assert.Equal(t, "federationPrincipalRequired", serverfake.DecodeProblem(t, response).Details["reason"])
}
