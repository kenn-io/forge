package pulltest

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v93/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/internal/testutil/servertest"
	"go.kenn.io/forge/internal/testutil/tokenauthtest"
)

// visibleProbeServer answers batched freshness probes. Each pull request
// reports the update time in updatedAt, keyed by number.
type visibleProbeServer struct {
	mu        sync.Mutex
	updatedAt map[int]time.Time
	probes    atomic.Int64
}

func (s *visibleProbeServer) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		assert.NoError(t, json.UnmarshalRead(r.Body, &req))
		w.Header().Set("Content-Type", "application/json")
		if !strings.Contains(req.Query, "fragment Freshness") {
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{}}}}`))
			return
		}
		s.probes.Add(1)
		s.mu.Lock()
		defer s.mu.Unlock()
		data := map[string]any{}
		for name, value := range req.Variables {
			if !strings.HasPrefix(name, "p") {
				continue
			}
			number := int(value.(float64))
			data["pr"+strings.TrimPrefix(name, "p")] = map[string]any{"pullRequest": map[string]any{
				"updatedAt":        s.updatedAt[number].UTC().Format(time.RFC3339),
				"state":            "OPEN",
				"headRefOid":       "",
				"mergeStateStatus": "CLEAN",
			}}
		}
		out, err := json.Marshal(map[string]any{"data": data})
		assert.NoError(t, err)
		_, _ = w.Write(out)
	})
}

type visibleRefreshFixture struct {
	client   *generated.Client
	database *db.DB
	probe    *visibleProbeServer
	fetched  sync.Map // number -> budgeted context
	inFlight atomic.Int64
	peak     atomic.Int64
	// release, when set, holds each pull request read until the test lets
	// it go.
	release chan struct{}
}

func newVisibleRefreshFixture(
	t *testing.T, release chan struct{}, configure func(*ghclient.Syncer),
) *visibleRefreshFixture {
	t.Helper()
	f := &visibleRefreshFixture{probe: &visibleProbeServer{updatedAt: map[int]time.Time{}}, release: release}
	probeServer := httptest.NewServer(f.probe.handler(t))
	t.Cleanup(probeServer.Close)
	mock := &serverfake.MockGH{
		GetPullRequestFn: func(ctx context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			running := f.inFlight.Add(1)
			defer f.inFlight.Add(-1)
			for peak := f.peak.Load(); running > peak && !f.peak.CompareAndSwap(peak, running); peak = f.peak.Load() {
			}
			if f.release != nil {
				select {
				case <-f.release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			f.fetched.Store(number, ghclient.IsSyncBudgetContext(ctx))
			id := int64(number) * 1000
			title := "Refreshed #" + strconv.Itoa(number)
			now := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.PullRequest{
				ID: &id, Number: &number, State: new("open"), Title: &title,
				HTMLURL:   new("https://github.com/acme/widget/pull/" + strconv.Itoa(number)),
				UpdatedAt: &now, CreatedAt: &now,
				Head: &gh.PullRequestBranch{SHA: new("head-sha"), Ref: new("feature")},
				Base: &gh.PullRequestBranch{Ref: new("main")},
			}, nil
		},
	}
	srv, database := servertest.SetupTestServerWithMockAndSyncer(t, mock, func(syncer *ghclient.Syncer) {
		syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
			"github.com": ghclient.NewGraphQLFetcher(tokenauthtest.Source("token"), "github.com", nil, nil,
				ghclient.WithGraphQLEndpointForTesting(probeServer.URL)),
		})
		if configure != nil {
			configure(syncer)
		}
	})
	f.database = database
	for _, number := range []int{1, 2, 3, 4} {
		serverfake.SeedPR(t, database, "acme", "widget", number)
		row, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", number)
		require.NoError(t, err)
		require.NotNil(t, row)
		f.probe.updatedAt[number] = row.LastActivityAt
	}
	f.client = servertest.SetupTestClient(t, srv).HTTP
	return f
}

func (f *visibleRefreshFixture) refresh(t *testing.T) {
	t.Helper()
	ref := func(number int) generated.VisiblePullRef {
		return generated.VisiblePullRef{Provider: "github", Owner: "acme", Name: "widget", Number: int64(number)}
	}
	focus := ref(1)
	resp, err := f.client.RefreshVisiblePullsWithResponse(t.Context(), &generated.RefreshVisiblePullsRequestOptions{
		Body: &generated.RefreshVisiblePullsBody{
			Focus: &focus, Visible: []generated.VisiblePullRef{ref(1), ref(2), ref(3), ref(4)},
		},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
}

func TestRefreshVisiblePullsSyncsOnlyStaleRowsOneAtATime(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	release := make(chan struct{})
	f := newVisibleRefreshFixture(t, release, nil)
	// GitHub moved #2, #3, and #4 after they were stored. #1 is the opened
	// pull request, which syncs through its own visit.
	f.probe.mu.Lock()
	for _, number := range []int{2, 3, 4} {
		f.probe.updatedAt[number] = f.probe.updatedAt[number].Add(time.Hour)
	}
	f.probe.mu.Unlock()

	f.refresh(t)

	// Each read is held until released. While one is held, no other read
	// may start.
	for range 3 {
		assert.Eventually(func() bool { return f.inFlight.Load() == 1 },
			10*time.Second, 10*time.Millisecond)
		assert.Never(func() bool { return f.inFlight.Load() > 1 },
			100*time.Millisecond, 10*time.Millisecond, "stale rows are read one at a time")
		release <- struct{}{}
	}
	for _, number := range []int{2, 3, 4} {
		assert.Eventually(func() bool {
			row, err := f.database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", number)
			return err == nil && row != nil && row.Title == "Refreshed #"+strconv.Itoa(number)
		}, 10*time.Second, 10*time.Millisecond, "stale row #%d is read in full and persisted", number)
		budgeted, _ := f.fetched.Load(number)
		assert.Equal(true, budgeted, "background refreshes spend the sync budget")
	}
	assert.Equal(int64(1), f.peak.Load())
	assert.Never(func() bool {
		_, fetched := f.fetched.Load(1)
		return fetched
	}, 200*time.Millisecond, 10*time.Millisecond, "the opened pull request is not read by the refresh")
	assert.Equal(int64(1), f.probe.probes.Load(), "the rows share one probe")
}

func TestRefreshVisiblePullsDoesNothingInAirplaneMode(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	f := newVisibleRefreshFixture(t, nil, func(syncer *ghclient.Syncer) { syncer.SetAirplaneMode(true) })
	f.probe.mu.Lock()
	f.probe.updatedAt[2] = f.probe.updatedAt[2].Add(time.Hour)
	f.probe.mu.Unlock()

	f.refresh(t)

	assert.Never(t, func() bool {
		_, fetched := f.fetched.Load(2)
		return fetched || f.probe.probes.Load() > 0
	}, 200*time.Millisecond, 10*time.Millisecond)
}
