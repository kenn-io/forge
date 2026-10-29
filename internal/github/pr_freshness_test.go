package github

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v93/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
)

// freshnessGraphQLServer answers freshness probes for every aliased pull
// request with the current comment edit time, so tests change the
// fingerprint by moving it. Other queries get an empty answer.
type freshnessGraphQLServer struct {
	mu           sync.Mutex
	commentEdit  string
	mergeState   string
	probes       atomic.Int64
	probedPRs    atomic.Int64
	probeQueries []string
}

func (s *freshnessGraphQLServer) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		assert.NoError(t, json.UnmarshalRead(r.Body, &req))
		w.Header().Set("Content-Type", "application/json")
		if !strings.Contains(req.Query, "fragment Freshness") {
			_, _ = w.Write([]byte(`{"data":{}}`))
			return
		}
		s.probes.Add(1)
		s.mu.Lock()
		edit, mergeState := s.commentEdit, s.mergeState
		s.probeQueries = append(s.probeQueries, req.Query)
		s.mu.Unlock()
		data := map[string]any{}
		for name := range req.Variables {
			if !strings.HasPrefix(name, "p") {
				continue
			}
			s.probedPRs.Add(1)
			data["pr"+strings.TrimPrefix(name, "p")] = map[string]any{"pullRequest": map[string]any{
				"updatedAt":        "2024-06-01T12:00:00Z",
				"state":            "OPEN",
				"mergeStateStatus": mergeState,
				"comments": map[string]any{
					"totalCount": 1,
					"nodes":      []any{map[string]any{"updatedAt": edit, "isMinimized": false}},
				},
			}}
		}
		out, err := json.Marshal(map[string]any{"data": data})
		assert.NoError(t, err)
		_, _ = w.Write(out)
	})
}

func TestWatchedPassSkipsPullRequestsWhoseProbeMatchesTheLastFullRead(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	gql := &freshnessGraphQLServer{commentEdit: "2024-06-01T12:00:00Z", mergeState: "CLEAN"}
	server := httptest.NewServer(gql.handler(t))
	t.Cleanup(server.Close)

	var fullReads atomic.Int64
	client := &mockClient{getPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
		fullReads.Add(1)
		return buildOpenPR(number, time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)), nil
	}}
	syncer := NewSyncer(map[string]Client{"github.com": client}, d, nil,
		[]RepoRef{{Owner: "acme", Name: "app", PlatformHost: "github.com"}},
		time.Hour, nil, nil)
	syncer.SetFetchers(map[string]*GraphQLFetcher{
		"github.com": NewGraphQLFetcher(testTokenSource("token"), "github.com", nil, nil,
			WithGraphQLEndpointForTesting(server.URL)),
	})
	now := time.Date(2024, 6, 1, 13, 0, 0, 0, time.UTC)
	syncer.SetClock(func() time.Time { return now })
	syncer.SetWatchInterval(time.Nanosecond)
	syncer.SetWatchedMRs([]WatchedMR{{Owner: "acme", Name: "app", Number: 7}})

	stored := func() *db.MergeRequest {
		mr, err := d.GetMergeRequest(ctx, "github", "github.com", "acme", "app", 7)
		require.NoError(err)
		require.NotNil(mr)
		return mr
	}

	// No baseline yet: the probe runs, then the full read.
	syncer.syncWatchedMRs(ctx)
	assert.Equal(int64(1), fullReads.Load())
	assert.Equal(int64(1), gql.probes.Load())
	stale := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := d.WriteDB().ExecContext(ctx,
		"UPDATE forge_merge_requests SET detail_fetched_at = ? WHERE id = ?", stale, stored().ID)
	require.NoError(err)

	// Unchanged: the probe vouches for the stored detail, so no full read,
	// and the detail still counts as freshly read.
	now = now.Add(time.Minute)
	syncer.syncWatchedMRs(ctx)
	assert.Equal(int64(1), fullReads.Load())
	assert.Equal(int64(2), gql.probes.Load())
	require.NotNil(stored().DetailFetchedAt)
	assert.True(stored().DetailFetchedAt.After(stale), "a matching probe must refresh the detail marker")

	// A comment edit changes the fingerprint and forces a full read.
	gql.mu.Lock()
	gql.commentEdit = "2024-06-01T12:30:00Z"
	gql.mu.Unlock()
	now = now.Add(time.Minute)
	syncer.syncWatchedMRs(ctx)
	assert.Equal(int64(2), fullReads.Load())

	// Mergeability GitHub is still computing never vouches.
	gql.mu.Lock()
	gql.mergeState = "UNKNOWN"
	gql.mu.Unlock()
	now = now.Add(time.Minute)
	syncer.syncWatchedMRs(ctx)
	assert.Equal(int64(3), fullReads.Load())

	// A matching probe cannot stand in for a full read forever.
	gql.mu.Lock()
	gql.mergeState = "CLEAN"
	gql.mu.Unlock()
	now = now.Add(time.Minute)
	syncer.syncWatchedMRs(ctx) // records a new baseline
	assert.Equal(int64(4), fullReads.Load())
	now = now.Add(prFreshnessFullReadInterval)
	syncer.syncWatchedMRs(ctx)
	assert.Equal(int64(5), fullReads.Load())
}

func TestFreshnessProbeBatchesTwentyFivePullRequestsPerRequest(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)

	gql := &freshnessGraphQLServer{commentEdit: "2024-06-01T12:00:00Z", mergeState: "CLEAN"}
	server := httptest.NewServer(gql.handler(t))
	t.Cleanup(server.Close)
	fetcher := NewGraphQLFetcher(testTokenSource("token"), "github.com", nil, nil,
		WithGraphQLEndpointForTesting(server.URL))

	refs := make([]PullRequestFreshnessRef, 0, 60)
	for i := range 60 {
		refs = append(refs, PullRequestFreshnessRef{Owner: "acme", Name: "app", Number: i + 1})
	}
	fingerprints, err := fetcher.FetchPullRequestFreshness(t.Context(), refs)
	require.NoError(err)
	assert.Len(fingerprints, 60)
	assert.Equal(int64(3), gql.probes.Load(), "60 pull requests fit in 3 requests of 25")
	assert.Equal(int64(60), gql.probedPRs.Load())
	for _, query := range gql.probeQueries {
		assert.NotContains(query, "acme", "owner and name travel as variables")
	}
}

func TestFreshnessProbeIsolatesAMissingPullRequest(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"data":{
			"pr0":{"pullRequest":{"state":"OPEN","mergeStateStatus":"BLOCKED"}},
			"pr1":{"pullRequest":null}},
			"errors":[{"type":"NOT_FOUND","path":["pr1","pullRequest"],"message":"Could not resolve"}]}`)
	}))
	t.Cleanup(server.Close)
	fetcher := NewGraphQLFetcher(testTokenSource("token"), "github.com", nil, nil,
		WithGraphQLEndpointForTesting(server.URL))

	found := PullRequestFreshnessRef{Owner: "acme", Name: "app", Number: 1}
	missing := PullRequestFreshnessRef{Owner: "acme", Name: "app", Number: 2}
	fingerprints, err := fetcher.FetchPullRequestFreshness(
		t.Context(), []PullRequestFreshnessRef{found, missing},
	)
	require.NoError(err)
	assert.Contains(fingerprints, found)
	assert.NotContains(fingerprints, missing)
}
