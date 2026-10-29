package github

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	platformgithub "go.kenn.io/forge/platform/github"
)

// ciServer serves two pages of check runs and one combined status. Each
// response's ETag is its generation, so bumping the generation changes CI.
type ciServer struct {
	generation   atomic.Int64
	notModified  atomic.Int64
	conditionals atomic.Int64
}

func (s *ciServer) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gen := s.generation.Load()
		etag := `W/"gen-` + strconv.FormatInt(gen, 10) + `-` + r.URL.RawQuery + `"`
		w.Header().Set("X-RateLimit-Remaining", strconv.FormatInt(4000-s.notModified.Load(), 10))
		w.Header().Set("X-RateLimit-Limit", "5000")
		if match := r.Header.Get("If-None-Match"); match != "" {
			s.conditionals.Add(1)
			if match == etag {
				s.notModified.Add(1)
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/repos/acme/widget/commits/abc123/check-runs":
			page := r.URL.Query().Get("page")
			if page == "" || page == "1" {
				w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?page=2&per_page=100>; rel="next"`, r.Host, r.URL.Path))
				_, _ = fmt.Fprintf(w, `{"total_count":2,"check_runs":[{"id":1,"name":"build-%d","status":"completed","conclusion":"success"}]}`, gen)
				return
			}
			_, _ = fmt.Fprintf(w, `{"total_count":2,"check_runs":[{"id":2,"name":"test-%d","status":"in_progress"}]}`, gen)
		case "/api/v3/repos/acme/widget/commits/abc123/status":
			_, _ = fmt.Fprintf(w, `{"state":"pending","statuses":[{"context":"lint-%d","state":"pending"}]}`, gen)
		default:
			assert.Failf(t, "unexpected path", "%s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func TestUnchangedCIReadsAreFreeAndReturnTheCachedAnswer(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)

	ci := &ciServer{}
	server := httptest.NewServer(ci.handler(t))
	t.Cleanup(server.Close)

	budget := NewSyncBudget(100)
	client, err := NewClient(
		testTokenSource("token"), "github.com", nil, budget,
		WithBaseURLForTesting(server.URL),
	)
	require.NoError(err)
	ctx := WithSyncBudget(t.Context())

	read := func() ([]string, string) {
		runs, err := client.ListCheckRunsForRef(ctx, "acme", "widget", "abc123")
		require.NoError(err)
		status, err := client.GetCombinedStatus(ctx, "acme", "widget", "abc123")
		require.NoError(err)
		names := make([]string, 0, len(runs))
		for _, run := range runs {
			names = append(names, run.GetName())
		}
		return names, status.Statuses[0].GetContext()
	}

	firstRuns, firstStatus := read()
	assert.Equal([]string{"build-0", "test-0"}, firstRuns)
	assert.Equal("lint-0", firstStatus)
	spentAfterFirst := budget.Spent()
	require.Equal(3, spentAfterFirst)

	// Unchanged CI: every read revalidates, GitHub answers 304, and the
	// caller still gets both pages and the status.
	againRuns, againStatus := read()
	assert.Equal(firstRuns, againRuns)
	assert.Equal(firstStatus, againStatus)
	assert.Equal(int64(3), ci.notModified.Load())
	assert.Equal(spentAfterFirst, budget.Spent(), "304s must not spend the sync budget")

	// Changed CI: the stale validator misses and the new answer is read.
	ci.generation.Add(1)
	changedRuns, changedStatus := read()
	assert.Equal([]string{"build-1", "test-1"}, changedRuns)
	assert.Equal("lint-1", changedStatus)
	assert.Equal(int64(3), ci.notModified.Load())
	assert.Equal(spentAfterFirst+3, budget.Spent())
}

func TestUnconditionalCIReadSkipsRevalidation(t *testing.T) {
	t.Parallel()

	require := require.New(t)

	ci := &ciServer{}
	server := httptest.NewServer(ci.handler(t))
	t.Cleanup(server.Close)

	client, err := NewClient(
		testTokenSource("token"), "github.com", nil, nil,
		WithBaseURLForTesting(server.URL),
	)
	require.NoError(err)

	_, err = client.GetCombinedStatus(t.Context(), "acme", "widget", "abc123")
	require.NoError(err)
	_, err = client.GetCombinedStatus(
		platformgithub.WithUnconditionalRead(t.Context()), "acme", "widget", "abc123",
	)
	require.NoError(err)
	assert.Equal(t, int64(0), ci.conditionals.Load())
}
