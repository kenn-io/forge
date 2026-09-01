package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

const (
	startupNodeID       = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	startupHubID        = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	startupEnrollmentID = "cccccccccccccccccccccccccccccccc"
	startupSeal         = "startup-seal"
	startupDigest       = "startup-digest"
)

func TestFederationSpokeStartupRequiresLocalSealWithoutHubTraffic(t *testing.T) {
	var requests atomic.Int32
	hub := httptest.NewTLSServer(http.HandlerFunc(func(
		w http.ResponseWriter, _ *http.Request,
	) {
		requests.Add(1)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	t.Cleanup(hub.Close)
	database := dbtest.Open(t)
	enrollments, credentials, cfg := spokeStartupFixture(
		t, database, hub.URL, false,
	)

	status := activateFederationSpokeAtStartup(
		t.Context(), database, cfg, startupNodeID,
		enrollments, credentials, hub.Client(),
	)
	assert.Equal(t, federationStartupActionRequired, status.State)
	assert.Contains(t, status.Reason, "seal")
	assert.Zero(t, requests.Load())
}

func TestFederationSpokeStartupRejectsChangedOriginWithoutHubTraffic(t *testing.T) {
	var requests atomic.Int32
	hub := httptest.NewTLSServer(http.HandlerFunc(func(
		w http.ResponseWriter, _ *http.Request,
	) {
		requests.Add(1)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	t.Cleanup(hub.Close)
	database := dbtest.Open(t)
	enrollments, credentials, cfg := spokeStartupFixture(
		t, database, hub.URL, true,
	)
	cfg.Fleet.BaseURL = "https://renamed-spoke.example"

	status := activateFederationSpokeAtStartup(
		t.Context(), database, cfg, startupNodeID,
		enrollments, credentials, hub.Client(),
	)

	assert.Equal(t, federationStartupActionRequired, status.State)
	assert.Contains(t, status.Reason, "sealed enrollment")
	assert.Zero(t, requests.Load())
}

func TestFederationHubStartupRejectsChangedEnrolledOrigin(t *testing.T) {
	require := require.New(t)
	store, err := federation.Open(
		filepath.Join(t.TempDir(), "enrollments.json"), federation.StoreOptions{},
	)
	require.NoError(err)
	identity := federation.Identity{
		NodeID: startupHubID, Name: "Hub",
		BaseURL: "https://hub.example",
	}
	token, err := store.CreateOneTimeToken(identity, time.Now().Add(time.Minute))
	require.NoError(err)
	_, err = store.Begin(t.Context(), token.Token, federation.JoinRequest{
		EnrollmentID: startupEnrollmentID, NodeID: startupNodeID,
		Name: "Spoke", Platform: "linux", BaseURL: "https://spoke.example",
		ProtocolVersion: federation.ProtocolVersion,
		HubCredential:   "hub-to-spoke",
	})
	require.NoError(err)
	cfg := &config.Config{Fleet: config.Fleet{
		Enabled: true, Role: config.FleetRoleHub,
		BaseURL: "https://new-hub.example",
	}}

	require.Error(validateFederationHubOrigin(cfg, store))
	require.NoError(store.Revoke(t.Context(), startupEnrollmentID))
	require.NoError(validateFederationHubOrigin(cfg, store))
}

func TestFederationSpokeStartupActivatesMatchingSealAndRetriesSafely(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var activations atomic.Int32
	var membershipWrites atomic.Int32
	var hubActive atomic.Bool
	hub := httptest.NewTLSServer(http.HandlerFunc(func(
		w http.ResponseWriter, r *http.Request,
	) {
		assert.Equal("Bearer spoke-to-hub", r.Header.Get("Authorization"))
		assert.Equal(startupNodeID, r.Header.Get(federationauth.NodeIDHeader))
		assert.Equal(providerplane.ProtocolVersionHeaderValue(),
			r.Header.Get(providerplane.ProtocolVersionHeader))
		switch r.URL.Path {
		case "/api/v1/federation/identity":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":          startupHubID,
				"protocol_version": federation.ProtocolVersion,
			})
		case "/api/v1/federation/enrollments/" + startupEnrollmentID + "/activate":
			activations.Add(1)
			var body map[string]any
			assert.NoError(json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(startupSeal, body["preparation_seal"])
			if hubActive.CompareAndSwap(false, true) {
				membershipWrites.Add(1)
				_, _ = w.Write([]byte("{"))
				return
			}
			_ = json.NewEncoder(w).Encode(federation.Enrollment{
				ID: startupEnrollmentID, NodeID: startupNodeID,
				HubID:           startupHubID,
				ProtocolVersion: federation.ProtocolVersion,
				State:           federation.EnrollmentActive,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(hub.Close)
	database := dbtest.Open(t)
	enrollments, credentials, cfg := spokeStartupFixture(
		t, database, hub.URL, true,
	)

	status := activateFederationSpokeAtStartup(
		t.Context(), database, cfg, startupNodeID,
		enrollments, credentials, hub.Client(),
	)
	require.Equal(federationStartupActive, status.State, status.Reason)
	assert.Equal(int32(2), activations.Load())
	assert.Equal(int32(1), membershipWrites.Load(),
		"a lost activation response must not duplicate membership")
	local, ok := enrollments.Local()
	require.True(ok)
	assert.Equal(federation.EnrollmentActive, local.State)
	assert.False(local.PreparationRequired)
}

func TestFederationSpokeStartupKeepsActiveEnrollmentDuringHubOutage(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var requests atomic.Int32
	hub := httptest.NewTLSServer(http.HandlerFunc(func(
		w http.ResponseWriter, _ *http.Request,
	) {
		requests.Add(1)
		http.Error(w, "hub unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(hub.Close)
	database := dbtest.Open(t)
	enrollments, credentials, cfg := spokeStartupFixture(
		t, database, hub.URL, true,
	)
	require.NoError(enrollments.MarkLocalActive(t.Context(), startupEnrollmentID))
	require.NoError(credentials.UpdateInboundScopes(
		startupHubID, federationauth.HubToSpokeScopes(),
	))
	require.NoError(credentials.UpdateOutboundScopes(
		startupHubID, federationauth.SpokeToHubScopes(),
	))

	status := activateFederationSpokeAtStartup(
		t.Context(), database, cfg, startupNodeID,
		enrollments, credentials, hub.Client(),
	)

	assert.Equal(federationStartupActive, status.State, status.Reason)
	assert.Zero(requests.Load(),
		"an active sealed enrollment must not depend on hub reachability at boot")
}

func TestFederationSpokeStartupKeepsActiveBindingDormantWhileDisabled(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var requests atomic.Int32
	hub := httptest.NewTLSServer(http.HandlerFunc(func(
		w http.ResponseWriter, _ *http.Request,
	) {
		requests.Add(1)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	t.Cleanup(hub.Close)
	database := dbtest.Open(t)
	enrollments, credentials, cfg := spokeStartupFixture(
		t, database, hub.URL, true,
	)
	require.NoError(enrollments.MarkLocalActive(t.Context(), startupEnrollmentID))
	require.NoError(credentials.UpdateInboundScopes(
		startupHubID, federationauth.HubToSpokeScopes(),
	))
	require.NoError(credentials.UpdateOutboundScopes(
		startupHubID, federationauth.SpokeToHubScopes(),
	))
	cfg.Fleet.Enabled = false

	status := activateFederationSpokeAtStartup(
		t.Context(), database, cfg, startupNodeID,
		enrollments, credentials, hub.Client(),
	)

	assert.Equal(federationStartupActive, status.State, status.Reason)
	assert.Zero(requests.Load(), "disabled federation must stay dormant")
}

func TestDisabledFederationSpokeStartupRepairsInterruptedCredentialPromotion(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*testing.T, *federationauth.Store)
	}{
		{name: "active enrollment persisted before either credential"},
		{
			name: "inbound credential persisted before outbound credential",
			prepare: func(t *testing.T, credentials *federationauth.Store) {
				require.NoError(t, credentials.UpdateInboundScopes(
					startupHubID, federationauth.HubToSpokeScopes(),
				))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			database := dbtest.Open(t)
			enrollments, credentials, cfg := spokeStartupFixture(
				t, database, "https://hub.example", true,
			)
			require.NoError(enrollments.MarkLocalActive(t.Context(), startupEnrollmentID))
			if test.prepare != nil {
				test.prepare(t, credentials)
			}
			cfg.Fleet.Enabled = false

			status := activateFederationSpokeAtStartup(
				t.Context(), database, cfg, startupNodeID,
				enrollments, credentials, http.DefaultClient,
			)

			require.Equal(federationStartupActive, status.State, status.Reason)
			principal, ok := credentials.Authenticate("hub-to-spoke")
			require.True(ok)
			expectedInbound := make(map[federationauth.Scope]struct{})
			for _, scope := range federationauth.HubToSpokeScopes() {
				expectedInbound[scope] = struct{}{}
			}
			assert.Equal(federationauth.Principal{
				NodeID: startupHubID,
				Scopes: expectedInbound,
			}, principal)
			outbound, ok := credentials.Outbound(startupHubID)
			require.True(ok)
			assert.Equal(federationauth.SpokeToHubScopes(), outbound.Scopes)
		})
	}
}

func TestFederationSpokeStartupSuppressesIncompatibleHub(t *testing.T) {
	hub := httptest.NewTLSServer(http.HandlerFunc(func(
		w http.ResponseWriter, r *http.Request,
	) {
		if r.URL.Path != "/api/v1/federation/identity" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"node_id":          startupHubID,
			"protocol_version": federation.ProtocolVersion + 1,
		})
	}))
	t.Cleanup(hub.Close)
	database := dbtest.Open(t)
	enrollments, credentials, cfg := spokeStartupFixture(
		t, database, hub.URL, true,
	)

	status := activateFederationSpokeAtStartup(
		t.Context(), database, cfg, startupNodeID,
		enrollments, credentials, hub.Client(),
	)
	assert.Equal(t, federationStartupIncompatible, status.State)
}

func TestFederationSpokeStartupLoadsOldProtocolAsIncompatibleWithoutTraffic(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var requests atomic.Int32
	hub := httptest.NewTLSServer(http.HandlerFunc(func(
		w http.ResponseWriter, _ *http.Request,
	) {
		requests.Add(1)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	t.Cleanup(hub.Close)
	path := filepath.Join(t.TempDir(), "enrollments.json")
	oldProtocol := federation.ProtocolVersion - 1
	contents, err := json.Marshal(map[string]any{
		"version": 1, "tokens": []any{}, "enrollments": []any{},
		"local": federation.LocalEnrollment{
			EnrollmentID: startupEnrollmentID, NodeID: startupNodeID,
			SpokePlatform: "linux", SpokeBaseURL: "https://spoke.example",
			HubID: startupHubID, HubURL: hub.URL,
			ProtocolVersion: oldProtocol, State: federation.EnrollmentPending,
			ExpiresAt: time.Now().Add(time.Minute), PreparationStarted: true,
			PreparationRequired: true,
			Preparation: &federation.LocalPreparationSeal{
				EnrollmentID: startupEnrollmentID, NodeID: startupNodeID,
				HubID: startupHubID, ProtocolVersion: oldProtocol,
				PreparationDigest: startupDigest, Seal: startupSeal,
			},
		},
	})
	require.NoError(err)
	require.NoError(os.WriteFile(path, contents, 0o600))
	enrollments, err := federation.Open(path, federation.StoreOptions{})
	require.NoError(err)
	cfg := &config.Config{Fleet: config.Fleet{
		Enabled: true, Role: config.FleetRoleSpoke,
		Hub: &config.FleetHub{
			NodeID: startupHubID, BaseURL: hub.URL,
		},
	}}

	status := activateFederationSpokeAtStartup(
		t.Context(), nil, cfg, startupNodeID, enrollments, nil,
		hub.Client(),
	)
	assert.Equal(federationStartupIncompatible, status.State)
	assert.Zero(requests.Load())
}

func spokeStartupFixture(
	t *testing.T,
	database *db.DB,
	hubURL string,
	withSeal bool,
) (*federation.Store, *federationauth.Store, *config.Config) {
	t.Helper()
	dir := t.TempDir()
	enrollments, err := federation.Open(
		filepath.Join(dir, "enrollments.json"), federation.StoreOptions{},
	)
	require.NoError(t, err)
	credentials, err := federationauth.Open(filepath.Join(dir, "credentials.json"))
	require.NoError(t, err)
	require.NoError(t, credentials.StoreOutbound(
		startupHubID, "spoke-to-hub",
		federationauth.PendingSpokeToHubScopes(),
	))
	require.NoError(t, credentials.StoreInbound(
		startupHubID, "hub-to-spoke",
		federationauth.PendingHubToSpokeScopes(),
	))
	require.NoError(t, enrollments.SaveLocal(t.Context(), federation.LocalEnrollment{
		EnrollmentID: startupEnrollmentID, NodeID: startupNodeID,
		SpokePlatform: "linux", SpokeBaseURL: "https://spoke.example",
		HubID: startupHubID, HubURL: hubURL,
		ProtocolVersion: federation.ProtocolVersion,
		State:           federation.EnrollmentPending, ExpiresAt: time.Now().Add(time.Minute),
		PreparationStarted: withSeal, PreparationRequired: true,
	}))
	if withSeal {
		require.NoError(t, enrollments.SaveLocalPreparationSeal(
			t.Context(), federation.LocalPreparationSeal{
				EnrollmentID: startupEnrollmentID, NodeID: startupNodeID,
				HubID:             startupHubID,
				ProtocolVersion:   federation.ProtocolVersion,
				PreparationDigest: startupDigest, Seal: startupSeal,
			},
		))
		_, err = database.BeginSpokePreparation(t.Context(), db.SpokePreparationBinding{
			EnrollmentID:    startupEnrollmentID,
			HubNodeID:       startupHubID,
			LocalNodeID:     startupNodeID,
			ProtocolVersion: federation.ProtocolVersion,
		})
		require.NoError(t, err)
		require.NoError(t, database.StoreLocalSpokePreparationSeal(
			t.Context(), startupDigest, startupSeal,
		))
	}
	return enrollments, credentials, &config.Config{Fleet: config.Fleet{
		Enabled: true, Role: config.FleetRoleSpoke,
		BaseURL: "https://spoke.example",
		Hub: &config.FleetHub{
			NodeID: startupHubID, BaseURL: hubURL,
		},
	}}
}
