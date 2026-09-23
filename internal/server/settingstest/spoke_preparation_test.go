package settingstest

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	forgeserver "go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/authapi"
)

const (
	preparationHubNodeID    = "0123456789abcdef0123456789abcdef"
	preparationLocalNodeID  = "fedcba9876543210fedcba9876543210"
	preparationEnrollmentID = "11111111111111111111111111111111"
)

func openFederationPreparationStores(
	t *testing.T, name string,
) (*federation.Store, *federationauth.Store) {
	t.Helper()
	dir := t.TempDir()
	enrollments, err := federation.Open(
		filepath.Join(dir, name+"-enrollments.json"), federation.StoreOptions{},
	)
	require.NoError(t, err)
	credentials, err := federationauth.Open(filepath.Join(dir, name+"-credentials.json"))
	require.NoError(t, err)
	return enrollments, credentials
}

func TestAbortPreparationFromNodeShapedServerRequiresRestart(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	enrollments, credentials := openFederationPreparationStores(t, "abort-spoke")
	require.NoError(enrollments.SaveLocal(t.Context(), federation.LocalEnrollment{
		EnrollmentID: preparationEnrollmentID, NodeID: preparationLocalNodeID,
		SpokeBaseURL:    "https://spoke.example",
		HubID:           preparationHubNodeID,
		HubURL:          "https://hub.example",
		ProtocolVersion: federation.ProtocolVersion, State: federation.EnrollmentPending,
		ExpiresAt: time.Now().Add(-time.Minute),
	}))
	srv, _, _ := setupTestServerWithConfigContentAndOptions(t, `
host = "127.0.0.1"
port = 8091

[api]
require_auth = true

[fleet]
enabled = true
role = "spoke"
base_url = "https://spoke.example"

[fleet.hub]
node_id = "0123456789abcdef0123456789abcdef"
base_url = "https://hub.example"
`, &mockGH{}, forgeserver.ServerOptions{
		DaemonAccess:          authapi.DaemonAccessOptions{Token: "local-secret", RequireAPIAuth: true},
		FederationCredentials: credentials, FederationEnrollments: enrollments,
		FederationSpokeID: preparationLocalNodeID, HostCheckAllowLoopbackAnyPort: true,
	})
	daemon := httptest.NewServer(srv)
	t.Cleanup(daemon.Close)
	request, err := http.NewRequestWithContext(
		t.Context(), http.MethodPost, daemon.URL+"/api/v1/fleet/prepare-spoke/abort",
		bytes.NewReader([]byte(`{}`)),
	)
	require.NoError(err)
	request.Header.Set("Authorization", "Bearer local-secret")
	request.Header.Set("Content-Type", "application/json")
	response, err := daemon.Client().Do(request)
	require.NoError(err)
	t.Cleanup(func() { require.NoError(response.Body.Close()) })
	var report struct {
		ProviderWritesOpen bool `json:"provider_writes_open"`
		RestartRequired    bool `json:"restart_required"`
	}
	require.NoError(json.NewDecoder(response.Body).Decode(&report))

	assert.Equal(http.StatusOK, response.StatusCode)
	assert.False(report.ProviderWritesOpen)
	assert.True(report.RestartRequired)
}

func TestForcedAbortPreservesHubRevocationPath(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	enrollments, credentials := openFederationPreparationStores(t, "forced-abort")
	require.NoError(enrollments.SaveLocal(t.Context(), federation.LocalEnrollment{
		EnrollmentID: preparationEnrollmentID, NodeID: preparationLocalNodeID,
		SpokePlatform: "linux", SpokeBaseURL: "https://spoke.example",
		HubID: preparationHubNodeID, HubURL: "https://hub.example",
		ProtocolVersion: federation.ProtocolVersion, State: federation.EnrollmentPending,
		ExpiresAt: time.Now().Add(time.Hour), PreparationRequired: true,
	}))
	require.NoError(credentials.StoreOutbound(
		preparationHubNodeID, "spoke-to-hub-token",
		federationauth.PendingSpokeToHubScopes(),
	))
	require.NoError(credentials.StoreInbound(
		preparationHubNodeID, "hub-to-spoke-token",
		federationauth.PendingHubToSpokeScopes(),
	))
	srv, _, _ := setupTestServerWithConfigContentAndOptions(t, `
host = "127.0.0.1"
port = 8091

[api]
require_auth = true

[fleet]
enabled = true
role = "spoke"
base_url = "https://spoke.example"

[fleet.hub]
node_id = "0123456789abcdef0123456789abcdef"
base_url = "https://hub.example"
`, &mockGH{}, forgeserver.ServerOptions{
		DaemonAccess:          authapi.DaemonAccessOptions{Token: "local-secret", RequireAPIAuth: true},
		FederationCredentials: credentials, FederationEnrollments: enrollments,
		FederationSpokeID: preparationLocalNodeID,
		FederationHTTPClient: &http.Client{Transport: roundTripFunc(func(
			*http.Request,
		) (*http.Response, error) {
			return nil, errors.New("hub offline")
		})},
		HostCheckAllowLoopbackAnyPort: true,
	})
	server := httptest.NewServer(srv)
	t.Cleanup(server.Close)
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	abort, err := http.NewRequestWithContext(
		t.Context(), http.MethodPost,
		server.URL+"/api/v1/fleet/prepare-spoke/abort",
		bytes.NewReader([]byte(`{"force":true}`)),
	)
	require.NoError(err)
	abort.Header.Set("Authorization", "Bearer local-secret")
	abort.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(abort)
	require.NoError(err)
	responseBody, err := io.ReadAll(response.Body)
	require.NoError(err)
	response.Body.Close()
	require.Equal(http.StatusOK, response.StatusCode, string(responseBody))
	local, ok := enrollments.Local()
	require.True(ok)
	assert.Equal(federation.EnrollmentRevoked, local.State)
	_, ok = credentials.Outbound(preparationHubNodeID)
	assert.False(ok)
	principal, ok := credentials.Authenticate("hub-to-spoke-token")
	require.True(ok)
	assert.Equal(
		map[federationauth.Scope]struct{}{federationauth.ScopeEnrollmentActivate: {}},
		principal.Scopes,
	)

	revoke, err := http.NewRequestWithContext(
		t.Context(), http.MethodDelete,
		server.URL+"/api/v1/fleet/enrollments/"+preparationEnrollmentID,
		http.NoBody,
	)
	require.NoError(err)
	revoke.Header.Set("Authorization", "Bearer hub-to-spoke-token")
	revoke.Header.Set(federationauth.NodeIDHeader, preparationHubNodeID)
	revoke.Header.Set("Content-Type", "application/json")
	response, err = server.Client().Do(revoke)
	require.NoError(err)
	response.Body.Close()
	assert.Equal(http.StatusNoContent, response.StatusCode)
}
