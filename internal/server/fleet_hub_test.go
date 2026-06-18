package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/fleet"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

func TestBuildFleetSnapshotMergesPeerAndDegrades(t *testing.T) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/snapshot/raw" {
			http.Error(w, "no", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schemaVersion":2,"host":{"hostname":"mbp","platform":"macos"},"projects":[{"scopedKey":"repo:/x","name":"x","rootPath":"/x"}]}`))
	}))
	defer peer.Close()

	srv := &Server{db: dbtest.Open(t), cfg: &config.Config{}}
	srv.cfg.Fleet = config.Fleet{
		Enabled: true,
		Key:     "studio",
		Peers: []config.FleetPeer{
			{Key: "mbp", Name: "mbp", BaseURL: peer.URL},
			{Key: "epyc", Name: "epyc", BaseURL: "http://127.0.0.1:0"}, // unreachable
		},
	}

	snap, err := srv.buildFleetSnapshot(context.Background(), true)
	require.NoError(t, err)
	var reachable, down int
	for _, h := range snap.Hosts {
		if h.Reachable {
			reachable++
		} else {
			down++
		}
	}
	assert.GreaterOrEqual(t, reachable, 2, "want self+mbp reachable, hosts=%+v", snap.Hosts)
	assert.Equal(t, 1, down, "want 1 unreachable (epyc)")
}

func TestBuildFleetSnapshotSkipsPeersWhenFederationDisabled(t *testing.T) {
	peerRequests := 0
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peerRequests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schemaVersion":2,"host":{"hostname":"mbp","platform":"macos"}}`))
	}))
	defer peer.Close()

	srv := &Server{db: dbtest.Open(t), cfg: &config.Config{}}
	srv.cfg.Fleet = config.Fleet{
		Key: "studio",
		Peers: []config.FleetPeer{
			{Key: "mbp", Name: "mbp", BaseURL: peer.URL},
		},
	}

	snap, err := srv.buildFleetSnapshot(context.Background(), true)
	require.NoError(t, err)
	require.Len(t, snap.Hosts, 1, "disabled federation must return local host only")
	assert.Equal(t, 0, peerRequests, "disabled federation must not fetch HTTP peers")
}

func TestBuildFleetSnapshotLocalOnly(t *testing.T) {
	srv := &Server{db: dbtest.Open(t), cfg: &config.Config{}}
	snap, err := srv.buildFleetSnapshot(context.Background(), false)
	require.NoError(t, err)
	require.Len(t, snap.Hosts, 1, "local-only build must yield exactly one self host")
	assert.True(t, snap.Hosts[0].Reachable, "self host must be reachable")
}

func TestBuildFleetSnapshotDefaultsToFleetNamespace(t *testing.T) {
	// The snapshot derives IDs under the default fleet namespace and
	// the configured fleet key, so hub output is byte-stable.
	require := require.New(t)
	assert := assert.New(t)
	srv := &Server{
		db:  dbtest.Open(t),
		cfg: &config.Config{Fleet: config.Fleet{Key: "studio"}},
	}

	snap, err := srv.buildFleetSnapshot(context.Background(), false)
	require.NoError(err)
	require.Len(snap.Hosts, 1)
	assert.Equal("studio", snap.Hosts[0].ConfigKey)
	assert.Equal(fleet.DefaultIdentity().HostID("studio"), snap.Hosts[0].ID)
}

// TestHubRoutabilityPolicySuppressesUnroutableHosts pins the
// availability contract per host class: configured peers keep their
// real capabilities, and hosts the hub cannot route to lose every
// mutation it would 404 on.
func TestHubRoutabilityPolicySuppressesUnroutableHosts(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv := &Server{db: dbtest.Open(t), cfg: &config.Config{}}
	srv.cfg.Fleet = config.Fleet{
		Enabled: true,
		Key:     "studio",
		Peers: []config.FleetPeer{
			{Key: "mbp", BaseURL: "http://peer.invalid"},
		},
	}

	apply := func(hostKey string) map[string]fleet.HostOperationAvailability {
		caps := fleet.CommandCapabilities{
			WorktreeCreate: true, WorktreeImportPR: true,
			WorktreeDelete: true, SessionEnsure: true,
			SessionKill: true, RepositoryClone: true,
			ProjectAdd: true, ProjectRemove: true,
		}
		policy, ok := srv.fleetAvailabilityPolicy().(fleet.HostKeyedPolicy)
		require.True(ok, "hub policy must be host-keyed")
		return fleet.OperationAvailabilityFromState(
			nil, caps, true, policy.ForHost(hostKey),
		)
	}

	peer := apply("mbp")
	assert.True(peer[fleet.OpRepositoryClone].Available,
		"HTTP peer keeps clone")
	assert.True(peer[fleet.OpProjectAdd].Available,
		"HTTP peer keeps project add")

	unroutable := apply("ssh-only")
	for _, op := range fleet.DefaultMutationOps() {
		assert.False(unroutable[op].Available,
			"unroutable host must suppress %s", op)
	}

}
