package federationtest

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/federationauth"
)

// ProviderSpoke configures a spoke's outbound provider connection to a test hub.
func ProviderSpoke(t *testing.T, hubID, hubURL, secret string) (*config.Config, *federationauth.Store) {
	t.Helper()
	credentials, err := federationauth.Open(filepath.Join(t.TempDir(), "credentials.json"))
	require.NoError(t, err)
	require.NoError(t, credentials.StoreOutbound(hubID, secret, federationauth.SpokeToHubScopes()))
	return &config.Config{Fleet: config.Fleet{
		Enabled: true, Role: config.FleetRoleSpoke,
		Hub: &config.FleetHub{NodeID: hubID, BaseURL: hubURL},
	}}, credentials
}
