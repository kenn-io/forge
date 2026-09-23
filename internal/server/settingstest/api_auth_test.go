package settingstest

import (
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func newFederationAuthTestServer(
	t *testing.T, scopes ...federationauth.Scope,
) (*httptest.Server, *federationauth.Store, string) {
	t.Helper()
	store, err := federationauth.Open(
		filepath.Join(t.TempDir(), "federation-credentials.json"),
	)
	require.NoError(t, err)
	token, err := store.MintInbound(
		"fedcba9876543210fedcba9876543210", scopes,
	)
	require.NoError(t, err)
	srv := server.New(dbtest.Open(t), nil, nil, "/", nil, server.ServerOptions{
		DaemonAccess: server.DaemonAccessOptions{
			Token: "local-secret", RequireAPIAuth: true,
		},
		FederationCredentials: store,
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts, store, token
}
