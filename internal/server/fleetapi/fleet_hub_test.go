package fleetapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/fleet"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestBuildFleetSnapshotMergesMemberAndDegrades(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/snapshot/raw" {
			http.Error(w, "no", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"protocolVersion":3,"nodeID":"` + testMemberNodeID + `","baseURL":"https://untrusted.example","host":{"hostname":"mbp","platform":"macos"},"projects":[{"scopedKey":"repo:/x","name":"x","rootPath":"/x"}]}`))
	}))
	defer peer.Close()

	srv := New(Deps{DB: dbtest.Open(t)})
	configureTestMembers(t, srv, testTLSClient(t, peer),
		config.FleetMember{NodeID: testMemberNodeID, Name: "mbp", BaseURL: peer.URL},
		config.FleetMember{NodeID: "cccccccccccccccccccccccccccccccc", Name: "epyc", BaseURL: "https://127.0.0.1:1"},
	)

	snap, err := srv.buildFleetSnapshot(context.Background(), true)
	require.NoError(err)
	var reachable, down int
	for _, h := range snap.Hosts {
		assert.Equal(h.ConfigKey, h.NodeID)
		if h.NodeID == testHubNodeID {
			assert.Equal(fleet.RoleHub, h.FederationRole)
			assert.Equal("https://hub.example", h.BaseURL)
		}
		if h.NodeID == testMemberNodeID {
			assert.Equal(fleet.RoleSpoke, h.FederationRole)
			assert.Equal(peer.URL, h.BaseURL)
		}
		if h.Reachable {
			reachable++
		} else {
			down++
		}
	}
	assert.GreaterOrEqual(reachable, 2, "want self+mbp reachable, hosts=%+v", snap.Hosts)
	assert.Equal(1, down, "want 1 unreachable (epyc)")
}

func TestBuildFleetSnapshotSkipsMembersWhenFederationDisabled(t *testing.T) {
	peerRequests := 0
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peerRequests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"protocolVersion":3,"nodeID":"` + testMemberNodeID + `","host":{"hostname":"mbp","platform":"macos"}}`))
	}))
	defer peer.Close()

	srv := New(Deps{DB: dbtest.Open(t), NodeID: testHubNodeID})
	srv.ApplyConfig(ConfigSnapshot{Fleet: config.Fleet{
		Role:    config.FleetRoleHub,
		Members: []config.FleetMember{{NodeID: testMemberNodeID, Name: "mbp", BaseURL: peer.URL}},
	}})

	snap, err := srv.buildFleetSnapshot(context.Background(), true)
	require.NoError(t, err)
	require.Len(t, snap.Hosts, 1, "disabled federation must return local host only")
	assert.Equal(t, 0, peerRequests, "disabled federation must not fetch members")
}

func TestBuildFleetSnapshotLocalOnly(t *testing.T) {
	srv := &Handler{db: dbtest.Open(t), config: ConfigSnapshot{}}
	snap, err := srv.buildFleetSnapshot(context.Background(), false)
	require.NoError(t, err)
	require.Len(t, snap.Hosts, 1, "local-only build must yield exactly one self host")
	assert.True(t, snap.Hosts[0].Reachable, "self host must be reachable")
}

func TestBuildFleetSnapshotDefaultsToFleetNamespace(t *testing.T) {
	// The snapshot derives IDs under the default fleet namespace and the
	// daemon's stable node ID, so output is byte-stable.
	require := require.New(t)
	assert := assert.New(t)
	srv := New(Deps{DB: dbtest.Open(t), NodeID: testHubNodeID})

	snap, err := srv.buildFleetSnapshot(context.Background(), false)
	require.NoError(err)
	require.Len(snap.Hosts, 1)
	assert.Equal(testHubNodeID, snap.Hosts[0].ConfigKey)
	assert.Equal(fleet.DefaultIdentity().HostID(testHubNodeID), snap.Hosts[0].ID)
}

func TestHubProjectionKeepsDirectMemberWorkspaceRouting(t *testing.T) {
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"protocolVersion":3,"nodeID":"` +
			testMemberNodeID + `","host":{"hostname":"mbp","platform":"macos"}}`))
	}))
	defer peer.Close()

	server := New(Deps{DB: dbtest.Open(t)})
	configureTestMembers(t, server, testTLSClient(t, peer), config.FleetMember{
		NodeID: testMemberNodeID, Name: "mbp", BaseURL: peer.URL,
	})
	snapshot, err := server.buildFleetSnapshot(t.Context(), true)
	require.NoError(t, err)
	for _, host := range snapshot.Hosts {
		if host.ConfigKey == testMemberNodeID {
			assert.True(t, host.OperationAvailability[fleet.OpWorkspaceWrite].Available)
			return
		}
	}
	require.FailNow(t, "member host missing from hub projection")
}

func TestSpokeProjectionConsumesHubAggregateAndRefreshesSelf(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	hubID := fleet.NodeID(testHubNodeID)
	nodeID := fleet.NodeID(testMemberNodeID)
	siblingID := fleet.NodeID("cccccccccccccccccccccccccccccccc")
	aggregate := fleet.NeutralSnapshot{
		ProtocolVersion: federation.ProtocolVersion,
		Hosts: []fleet.NeutralHost{
			{
				NodeID: hubID, FederationRole: fleet.RoleHub,
				Name: "hub", BaseURL: "https://hub.example", Reachable: true,
			},
			{
				NodeID: nodeID, FederationRole: fleet.RoleSpoke,
				Name: "stale-spoke", BaseURL: "https://spoke.example", Reachable: true,
			},
			{
				NodeID: siblingID, FederationRole: fleet.RoleSpoke,
				Name: "sibling", BaseURL: "https://sibling.example", Reachable: true,
			},
		},
		Workspaces: []fleet.RawWorkspace{
			{HostKey: string(hubID), ID: "ws-hub", Status: "ready"},
			{HostKey: string(nodeID), ID: "ws-stale-self", Status: "ready"},
			{HostKey: string(siblingID), ID: "ws-sibling", Status: "ready"},
		},
	}
	token := "spoke-calls-hub-token-000000000000000"
	hub := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		assert.Equal("/api/v1/snapshot/aggregate", request.URL.Path)
		assert.Equal("Bearer "+token, request.Header.Get("Authorization"))
		writer.Header().Set("Content-Type", "application/json")
		assert.NoError(json.NewEncoder(writer).Encode(aggregate))
	}))
	defer hub.Close()

	credentials, err := federationauth.Open(filepath.Join(
		t.TempDir(), "federation-credentials.json",
	))
	require.NoError(err)
	require.NoError(credentials.StoreOutbound(
		testHubNodeID, token, federationauth.SpokeToHubScopes(),
	))
	server := New(Deps{
		DB: dbtest.Open(t), NodeID: testMemberNodeID,
		FederationActive: true,
		Credentials:      credentials, FederationHTTPClient: testTLSClient(t, hub),
		Config: ConfigSnapshot{Fleet: config.Fleet{
			Enabled: true, Role: config.FleetRoleSpoke,
			BaseURL: "https://spoke.example",
			Hub: &config.FleetHub{
				NodeID: testHubNodeID, Name: "hub", BaseURL: hub.URL,
			},
		}},
		WorkspaceSnapshot: func(context.Context) (workspaceapi.FleetSnapshot, error) {
			return workspaceapi.FleetSnapshot{Workspaces: []fleet.RawWorkspace{{
				ID: "ws-fresh-self", Status: "ready",
			}}}, nil
		},
	})

	snapshot, err := server.buildFleetSnapshot(t.Context(), true)
	require.NoError(err)
	assert.False(snapshot.AggregateIncomplete)
	workspaceIDs := make(map[string]bool)
	for _, workspace := range snapshot.Workspaces {
		workspaceIDs[workspace.ID] = true
	}
	assert.True(workspaceIDs["ws-fresh-self"])
	assert.False(workspaceIDs["ws-hub"])
	assert.False(workspaceIDs["ws-sibling"])
	assert.False(workspaceIDs["ws-stale-self"])
	assert.Len(snapshot.Hosts, 3, "the Forge selector still needs the full host directory")
	for _, host := range snapshot.Hosts {
		assert.Equal(host.ConfigKey, host.NodeID)
		if host.ConfigKey == testMemberNodeID {
			assert.Equal(fleet.RoleSpoke, host.FederationRole)
			assert.Equal("https://spoke.example", host.BaseURL)
		}
		if host.ConfigKey == testHubNodeID {
			assert.Equal(fleet.RoleHub, host.FederationRole)
			assert.Equal("https://hub.example", host.BaseURL)
			availability := host.OperationAvailability[fleet.OpWorkspaceWrite]
			assert.False(availability.Available)
			require.NotNil(availability.UnavailableReason)
			assert.Equal(fleet.ReasonSummaryOnly, *availability.UnavailableReason)
			return
		}
	}
	require.FailNow("hub host missing from spoke projection")
}

func TestSpokeAggregateAllowsHubMemberFanoutToReachItsOwnDeadline(t *testing.T) {
	require := require.New(t)
	aggregate := fleet.NeutralSnapshot{
		ProtocolVersion: federation.ProtocolVersion,
		Hosts: []fleet.NeutralHost{{
			NodeID: fleet.NodeID(testHubNodeID), Reachable: true,
		}},
	}
	hub := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter, _ *http.Request,
	) {
		time.Sleep(40 * time.Millisecond)
		assert.NoError(t, json.NewEncoder(writer).Encode(aggregate))
	}))
	defer hub.Close()
	credentials, err := federationauth.Open(filepath.Join(t.TempDir(), "credentials.json"))
	require.NoError(err)
	require.NoError(credentials.StoreOutbound(
		testHubNodeID, "spoke-calls-hub-token",
		federationauth.SpokeToHubScopes(),
	))
	server := New(Deps{
		DB: dbtest.Open(t), NodeID: testMemberNodeID, FederationActive: true,
		Credentials: credentials, FederationHTTPClient: testTLSClient(t, hub),
		Config: ConfigSnapshot{Fleet: config.Fleet{
			Enabled: true, Role: config.FleetRoleSpoke, PeerTimeout: "30ms",
			Hub: &config.FleetHub{
				NodeID: testHubNodeID, BaseURL: hub.URL,
			},
		}},
	})

	snapshot, err := server.buildFleetSnapshot(t.Context(), true)
	require.NoError(err)
	require.True(slices.ContainsFunc(snapshot.Hosts, func(host fleet.HostSummary) bool {
		return host.ConfigKey == testHubNodeID && host.Reachable
	}))
	assert.False(t, snapshot.AggregateIncomplete)
}

func TestSpokeSnapshotMarksHubAggregateFailureIncomplete(t *testing.T) {
	require := require.New(t)
	credentials, err := federationauth.Open(filepath.Join(t.TempDir(), "credentials.json"))
	require.NoError(err)
	require.NoError(credentials.StoreOutbound(
		testHubNodeID, "spoke-calls-hub-token",
		federationauth.SpokeToHubScopes(),
	))
	server := New(Deps{
		DB: dbtest.Open(t), NodeID: testMemberNodeID, FederationActive: true,
		Credentials: credentials,
		Config: ConfigSnapshot{Fleet: config.Fleet{
			Enabled: true, Role: config.FleetRoleSpoke, PeerTimeout: "10ms",
			Hub: &config.FleetHub{
				NodeID: testHubNodeID, BaseURL: "https://127.0.0.1:1",
			},
		}},
	})

	snapshot, err := server.buildFleetSnapshot(t.Context(), true)

	require.NoError(err)
	assert.True(t, snapshot.AggregateIncomplete)
}

func TestInactiveSpokeSnapshotMarksAggregateIncomplete(t *testing.T) {
	server := New(Deps{
		DB: dbtest.Open(t), NodeID: testMemberNodeID,
		FederationUnavailableReason: "hub activation required",
		Config: ConfigSnapshot{Fleet: config.Fleet{
			Enabled: true, Role: config.FleetRoleSpoke,
			Hub: &config.FleetHub{
				NodeID: testHubNodeID, BaseURL: "https://hub.example",
			},
		}},
	})

	snapshot, err := server.buildFleetSnapshot(t.Context(), true)

	require.NoError(t, err)
	assert.True(t, snapshot.AggregateIncomplete)
}
