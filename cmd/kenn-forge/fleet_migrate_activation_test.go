package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/servertest"
)

func TestFleetMigrateProtocolRestoresAuthenticatedActivation(t *testing.T) {
	for _, state := range []federation.EnrollmentState{federation.EnrollmentPending, federation.EnrollmentActive} {
		t.Run(string(state), func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			hub := httptest.NewUnstartedServer(nil)
			t.Cleanup(hub.Close)
			hubURL := "https://" + hub.Listener.Addr().String()
			hubDir, spokeDir := t.TempDir(), t.TempDir()
			hubConfig := &config.Config{DataDir: hubDir, BasePath: "/", Host: "127.0.0.1", Port: 8091,
				Fleet: config.Fleet{Enabled: true, Role: config.FleetRoleHub, BaseURL: hubURL}}
			hubConfig.Tmux.Command = []string{"kenn-forge-no-such-tmux"}
			spokeConfig := &config.Config{DataDir: spokeDir, BasePath: "/", Host: "127.0.0.1", Port: 8091,
				Fleet: config.Fleet{Enabled: true, Role: config.FleetRoleSpoke, BaseURL: "https://spoke.example",
					Hub: &config.FleetHub{NodeID: startupHubID, BaseURL: hubURL}}}
			hubConfig.API.RequireAuth = true
			spokeConfig.API.RequireAuth = true
			member := config.FleetMember{NodeID: startupNodeID, Name: "Spoke", BaseURL: spokeConfig.Fleet.BaseURL, State: federation.EnrollmentActive}
			if state == federation.EnrollmentActive {
				hubConfig.Fleet.Members = []config.FleetMember{member}
			}
			hubPath, spokePath := filepath.Join(hubDir, "config.toml"), filepath.Join(spokeDir, "config.toml")
			require.NoError(hubConfig.Save(hubPath))
			require.NoError(spokeConfig.Save(spokePath))
			hubConfig, err := config.Load(hubPath)
			require.NoError(err)
			spokeConfig, err = config.Load(spokePath)
			require.NoError(err)
			hubDB, spokeDB := dbtest.OpenAt(t, hubConfig.DBPath()), dbtest.OpenAt(t, spokeConfig.DBPath())
			binding := db.SpokePreparationBinding{EnrollmentID: startupEnrollmentID, HubNodeID: startupHubID, LocalNodeID: startupNodeID, ProtocolVersion: 3}
			_, err = spokeDB.BeginSpokePreparation(t.Context(), binding)
			require.NoError(err)
			generation, err := spokeDB.FreezeSpokePreparationAckGeneration(t.Context())
			require.NoError(err)
			emptyReceipts := sha256.Sum256([]byte("[]"))
			sealRequest := db.SpokePreparationSealRequest{EnrollmentID: startupEnrollmentID, NodeID: startupNodeID,
				HubNodeID: startupHubID, ProtocolVersion: 3, MigrationVersion: db.WorkspaceLaunchSpecMigrationVersion,
				ReceiptsDigest: hex.EncodeToString(emptyReceipts[:]), DrainedAckGeneration: generation}
			sealRequest.PreparationDigest, err = db.SpokePreparationSealDigest(sealRequest)
			require.NoError(err)
			seal, err := hubDB.IssueSpokePreparationSeal(t.Context(), sealRequest)
			require.NoError(err)
			require.NoError(spokeDB.StoreLocalSpokePreparationSeal(t.Context(), seal.PreparationDigest, seal.Seal))
			now := time.Now().UTC()
			enrollment := federation.Enrollment{ID: startupEnrollmentID, NodeID: startupNodeID,
				SpokeName: "Spoke", SpokePlatform: "linux", SpokeBaseURL: spokeConfig.Fleet.BaseURL,
				HubID: startupHubID, HubURL: hubURL, ProtocolVersion: 3, State: state,
				ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now, PreparationStarted: true}
			local := federation.LocalEnrollment{EnrollmentID: enrollment.ID, NodeID: enrollment.NodeID,
				SpokeName: enrollment.SpokeName, SpokePlatform: enrollment.SpokePlatform, SpokeBaseURL: enrollment.SpokeBaseURL,
				HubID: enrollment.HubID, HubURL: hubURL, ProtocolVersion: 3, State: state,
				ExpiresAt: enrollment.ExpiresAt, PreparationStarted: true, PreparationRequired: state == federation.EnrollmentPending,
				Preparation: &federation.LocalPreparationSeal{EnrollmentID: enrollment.ID, NodeID: enrollment.NodeID,
					HubID: enrollment.HubID, ProtocolVersion: 3, PreparationDigest: seal.PreparationDigest, Seal: seal.Seal}}
			for dir, record := range map[string]any{
				hubDir:   map[string]any{"version": 1, "tokens": []any{}, "enrollments": []federation.Enrollment{enrollment}},
				spokeDir: map[string]any{"version": 1, "tokens": []any{}, "enrollments": []any{}, "local": local},
			} {
				encoded, err := json.Marshal(record)
				require.NoError(err)
				require.NoError(os.WriteFile(federation.DefaultStorePath(dir), encoded, 0o600))
			}
			hubCredentials, err := federationauth.Open(federationauth.DefaultStorePath(hubDir))
			require.NoError(err)
			spokeCredentials, err := federationauth.Open(federationauth.DefaultStorePath(spokeDir))
			require.NoError(err)
			require.NoError(hubCredentials.StoreInbound(startupNodeID, "spoke-to-hub", federationauth.PendingSpokeToHubScopes()))
			require.NoError(hubCredentials.StoreOutbound(startupNodeID, "hub-to-spoke", federationauth.PendingHubToSpokeScopes()))
			require.NoError(spokeCredentials.StoreInbound(startupHubID, "hub-to-spoke", federationauth.PendingHubToSpokeScopes()))
			require.NoError(spokeCredentials.StoreOutbound(startupHubID, "spoke-to-hub", federationauth.PendingSpokeToHubScopes()))
			require.NoError(hubDB.Close())
			require.NoError(spokeDB.Close())

			for _, path := range []string{hubPath, spokePath, hubPath, spokePath} {
				require.NoError(migrateFleetProtocol(t.Context(), path))
			}
			hubDB = dbtest.OpenPreparedAt(t, hubConfig.DBPath())
			spokeDB = dbtest.OpenPreparedAt(t, spokeConfig.DBPath())
			hubCredentials, err = federationauth.Open(federationauth.DefaultStorePath(hubDir))
			require.NoError(err)
			spokeCredentials, err = federationauth.Open(federationauth.DefaultStorePath(spokeDir))
			require.NoError(err)
			hubEnrollments, err := federation.Open(federation.DefaultStorePath(hubDir), federation.StoreOptions{})
			require.NoError(err)
			spokeEnrollments, err := federation.Open(federation.DefaultStorePath(spokeDir), federation.StoreOptions{})
			require.NoError(err)
			hub.Config.Handler = servertest.NewWithConfig(t, hubDB, nil, nil, nil, hubConfig, hubPath, server.ServerOptions{
				DaemonAccess:          server.DaemonAccessOptions{Token: "local-secret", RequireAPIAuth: true},
				FederationCredentials: hubCredentials, FederationEnrollments: hubEnrollments, FederationSpokeID: startupHubID,
				WorktreeDir: t.TempDir(), DisableWorkspaceBackgroundMonitors: true, HostCheckAllowLoopbackAnyPort: true,
			})
			hub.StartTLS()
			for range 2 {
				status := activateFederationSpokeAtStartup(t.Context(), spokeDB, spokeConfig, startupNodeID,
					spokeEnrollments, spokeCredentials, hub.Client())
				require.Equal(federationStartupActive, status.State, status.Reason)
			}
			migrated, ok := spokeEnrollments.Local()
			require.True(ok)
			assert.Equal(seal.Seal, migrated.Preparation.Seal)
			assert.True(migrated.ActivationValidUntil.After(time.Now()))
			persisted, err := config.Load(hubPath)
			require.NoError(err)
			assert.Equal([]config.FleetMember{member}, persisted.Fleet.Members)
			principal, ok := hubCredentials.Authenticate("spoke-to-hub")
			require.True(ok)
			assert.Equal(startupNodeID, principal.NodeID)
			outbound, ok := spokeCredentials.Outbound(startupHubID)
			require.True(ok)
			assert.Equal("spoke-to-hub", outbound.Token)
		})
	}
}
