package settingsservertest

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/server/browserloginapi"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	servertest "go.kenn.io/forge/internal/testutil/servertest"
)

func TestBrowserLoginTicketRequiresActivePeerGrant(t *testing.T) {
	for _, test := range []struct {
		name   string
		scopes []federationauth.Scope
		status int
	}{
		{name: "active hub", scopes: federationauth.HubToSpokeScopes(), status: http.StatusOK},
		{name: "active spoke", scopes: federationauth.SpokeToHubScopes(), status: http.StatusOK},
		{name: "pending hub", scopes: federationauth.PendingHubToSpokeScopes(), status: http.StatusForbidden},
		{name: "pending spoke", scopes: federationauth.PendingSpokeToHubScopes(), status: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			ts, _, token := servertest.NewFederationAuthTestServer(t, test.scopes...)
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
				ts.URL+"/api/v1/federation/browser-login-tickets", nil)
			require.NoError(err)
			request.Header.Set("Authorization", "Bearer "+token)
			issuedAfter := time.Now().UTC().Truncate(time.Second)
			response, err := ts.Client().Do(request)
			require.NoError(err)
			defer response.Body.Close()
			require.Equal(test.status, response.StatusCode)
			if test.status != http.StatusOK {
				problem := serverfake.DecodeProblem(t, response)
				assert.Equal("federationScopeDenied", problem.Details["reason"])
				assert.Equal(string(federationauth.ScopeBrowserLogin), problem.Details["required_scope"])
				return
			}
			var ticket browserloginapi.BrowserLoginTicketBody
			require.NoError(json.NewDecoder(response.Body).Decode(&ticket))
			raw, err := base64.RawURLEncoding.DecodeString(ticket.Ticket)
			require.NoError(err)
			assert.GreaterOrEqual(len(raw), 32)
			assert.Equal(time.UTC, ticket.ExpiresAt.Location())
			assert.False(ticket.ExpiresAt.Before(issuedAfter.Add(59 * time.Second)))
			assert.False(ticket.ExpiresAt.After(time.Now().UTC().Add(time.Minute)))
		})
	}
}
