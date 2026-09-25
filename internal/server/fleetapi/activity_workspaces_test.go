package fleetapi

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/fleet"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestActivityWorkspacesDoNotWaitForSlowMembers(t *testing.T) {
	release := make(chan struct{})
	var memberReads atomic.Int32
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		memberReads.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"protocolVersion":` + strconv.Itoa(federation.ProtocolVersion) +
			`,"nodeID":"` + testMemberNodeID + `","host":{"hostname":"mbp","platform":"macos"},` +
			`"workspaces":[{"id":"member-workspace","status":"ready","createdAt":"2026-01-02T03:04:05Z"}]}`))
	}))
	defer peer.Close()
	releaseOnce := sync.OnceFunc(func() { close(release) })
	defer releaseOnce()

	srv := New(Deps{DB: dbtest.Open(t)})
	configureTestMembers(t, srv, testTLSClient(t, peer), config.FleetMember{
		NodeID: testMemberNodeID, Name: "mbp", BaseURL: peer.URL,
	})
	cfg := srv.configSnapshot()
	cfg.Fleet.PeerTimeout = "1m"
	srv.ApplyConfig(cfg)

	// Concurrent reads while the member hangs return without it, well before
	// the peer timeout, and share a single member fan-out.
	start := time.Now()
	var wait sync.WaitGroup
	reads := make([][]fleet.WorkspaceSummary, 3)
	errs := make([]error, len(reads))
	for index := range reads {
		wait.Go(func() {
			reads[index], errs[index] = srv.ActivityWorkspaces(t.Context())
		})
	}
	wait.Wait()
	for index := range reads {
		require.NoError(t, errs[index])
		assert.False(t, hasWorkspace(reads[index], "member-workspace"))
	}
	assert.Less(t, time.Since(start), 10*time.Second)
	assert.Equal(t, int32(1), memberReads.Load())

	releaseOnce()
	require.Eventually(t, func() bool {
		workspaces, err := srv.ActivityWorkspaces(t.Context())
		return err == nil && hasWorkspace(workspaces, "member-workspace")
	}, 10*time.Second, 10*time.Millisecond)
}

func hasWorkspace(workspaces []fleet.WorkspaceSummary, id string) bool {
	for _, workspace := range workspaces {
		if workspace.ID == id {
			return true
		}
	}
	return false
}
