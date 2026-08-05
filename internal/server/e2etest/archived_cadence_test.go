package e2etest

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/servertest"
)

// A bucket holding only archived repositories drops out of the pass before
// dispatch eligibility is computed, so its cadence gate must be advanced by
// the attempted metadata refresh itself: consecutive scheduled passes inside
// the gate fetch archived metadata exactly once. Passes are driven through
// RunOnce — the scheduled entry point — because the explicit API sync
// trigger deliberately bypasses cadence gates.
func TestScheduledSyncFetchesArchivedMetadataOncePerCadenceE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	var repoFetches atomic.Int32
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/repos/corp/frozen":
			repoFetches.Add(1)
			_, _ = w.Write([]byte(`{"id":1,"node_id":"R_frozen","name":"frozen","full_name":"corp/frozen","owner":{"login":"corp"},"archived":true}`))
		case "/api/v3/rate_limit":
			_, _ = w.Write([]byte(`{"resources":{"core":{"limit":5000,"remaining":4000,"reset":4102444800}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(providerServer.Close)

	providerClient, err := ghclient.NewClient(
		staticTokenSource("archive-token"), "github.com", nil, nil,
		ghclient.WithBaseURLForTesting(providerServer.URL),
	)
	require.NoError(err)
	registry, err := ghclient.NewProviderRegistry(map[string]ghclient.Client{
		"github.com": providerClient,
	})
	require.NoError(err)
	database := dbtest.Open(t)

	tracker := ghclient.NewRateTracker(database, "github.com", "host", "rest")
	tracker.UpdateFromRate(ghclient.Rate{
		Limit:     5000,
		Remaining: 4000,
		Reset:     time.Now().UTC().Add(time.Hour),
	})
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil,
		[]ghclient.RepoRef{{
			Platform: "github", PlatformHost: "github.com",
			Owner: "corp", Name: "frozen", RepoPath: "corp/frozen",
			Archived: true,
		}},
		time.Hour,
		map[string]*ghclient.RateTracker{"github.com": tracker},
		nil,
	)
	t.Cleanup(syncer.Stop)

	srv := servertest.New(t, database, syncer, nil, "/", nil, server.ServerOptions{
		HostCheckAllowLoopbackAnyPort: true,
	})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	syncer.RunOnce(t.Context())
	require.Equal(int32(1), repoFetches.Load(),
		"first scheduled pass refreshes the archived repo's metadata")

	syncer.RunOnce(t.Context())
	assert.Equal(int32(1), repoFetches.Load(),
		"second pass inside the cadence gate must not refetch archived metadata")

	tracked := syncer.TrackedRepos()
	require.Len(tracked, 1)
	assert.True(tracked[0].Archived)
}
