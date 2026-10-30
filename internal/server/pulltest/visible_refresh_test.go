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
}

func newVisibleRefreshFixture(t *testing.T, configure func(*ghclient.Syncer)) *visibleRefreshFixture {
	t.Helper()
	f := &visibleRefreshFixture{probe: &visibleProbeServer{updatedAt: map[int]time.Time{}}}
	probeServer := httptest.NewServer(f.probe.handler(t))
	t.Cleanup(probeServer.Close)
	mock := &serverfake.MockGH{
		GetPullRequestFn: func(ctx context.Context, _, _ string, number int) (*gh.PullRequest, error) {
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
	for _, number := range []int{1, 2} {
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
		Body: &generated.RefreshVisiblePullsBody{Focus: &focus, Visible: []generated.VisiblePullRef{ref(1), ref(2)}},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
}

func TestRefreshVisiblePullsSyncsOnlyStaleRowsOnTheSyncBudget(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	f := newVisibleRefreshFixture(t, nil)
	// GitHub moved #2 after it was stored; #1 is unchanged.
	f.probe.mu.Lock()
	f.probe.updatedAt[2] = f.probe.updatedAt[2].Add(time.Hour)
	f.probe.mu.Unlock()

	f.refresh(t)

	assert.Eventually(func() bool {
		row, err := f.database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 2)
		return err == nil && row != nil && row.Title == "Refreshed #2"
	}, 10*time.Second, 10*time.Millisecond, "the stale row is read in full and persisted")
	budgeted, _ := f.fetched.Load(2)
	assert.Equal(true, budgeted, "background refreshes spend the sync budget")
	assert.Never(func() bool {
		_, fetched := f.fetched.Load(1)
		return fetched
	}, 200*time.Millisecond, 10*time.Millisecond, "an unchanged row is not read")
	assert.Equal(int64(1), f.probe.probes.Load(), "both rows share one probe")
}

func TestRefreshVisiblePullsDoesNothingInAirplaneMode(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	f := newVisibleRefreshFixture(t, func(syncer *ghclient.Syncer) { syncer.SetAirplaneMode(true) })
	f.probe.mu.Lock()
	f.probe.updatedAt[2] = f.probe.updatedAt[2].Add(time.Hour)
	f.probe.mu.Unlock()

	f.refresh(t)

	assert.Never(t, func() bool {
		_, fetched := f.fetched.Load(2)
		return fetched || f.probe.probes.Load() > 0
	}, 200*time.Millisecond, 10*time.Millisecond)
}
