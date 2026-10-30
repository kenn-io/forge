package semantic

import (
	"cmp"
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/embedfake"
)

type fakeSources struct {
	mu   sync.Mutex
	rows map[db.SemanticSourceKind][]db.SemanticSource
}

func (f *fakeSources) ListSemanticSources(
	_ context.Context, kind db.SemanticSourceKind, afterID int64, limit int,
) ([]db.SemanticSource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []db.SemanticSource
	for _, row := range f.rows[kind] {
		if row.ID > afterID && len(out) < limit {
			out = append(out, row)
		}
	}
	return out, nil
}

func (f *fakeSources) set(rows ...db.SemanticSource) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = make(map[db.SemanticSourceKind][]db.SemanticSource)
	for _, row := range rows {
		f.rows[row.Kind] = append(f.rows[row.Kind], row)
	}
	for kind := range f.rows {
		slices.SortFunc(f.rows[kind], func(a, b db.SemanticSource) int { return int(a.ID - b.ID) })
	}
}

func pull(id int64, repoID int64, number int, title, body string) db.SemanticSource {
	return db.SemanticSource{
		Kind: db.SemanticSourcePull, ID: id, RepoID: repoID, ItemType: "pr",
		ItemNumber: number, Title: title, Body: body,
	}
}

func issue(id int64, repoID int64, number int, title, body string) db.SemanticSource {
	return db.SemanticSource{
		Kind: db.SemanticSourceIssue, ID: id, RepoID: repoID, ItemType: "issue",
		ItemNumber: number, Title: title, Body: body,
	}
}

func pullComment(id int64, repoID int64, number int, body string) db.SemanticSource {
	return db.SemanticSource{
		Kind: db.SemanticSourcePullComment, ID: id, RepoID: repoID, ItemType: "pr",
		ItemNumber: number, Body: body,
	}
}

type fixture struct {
	endpoint *embedfake.Server
	sources  *fakeSources
	path     string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return &fixture{
		endpoint: embedfake.New(t, "cache", "login", "flaky", "docs"),
		sources:  &fakeSources{},
		path:     filepath.Join(t.TempDir(), SidecarFile),
	}
}

func (f *fixture) open(t *testing.T, model string) *Indexer {
	t.Helper()
	encoder, err := NewEncoder(f.endpoint.Embedder(model))
	require.NoError(t, err)
	x, err := Open(t.Context(), f.path, encoder, f.sources, time.Hour)
	require.NoError(t, err)
	t.Cleanup(func() { _ = x.Close() })
	return x
}

func item(repoID int64, number int) db.RepoItemNumber {
	return db.RepoItemNumber{RepoID: repoID, Number: number}
}

// rank ranks candidate items of itemType and returns them best first.
func rank(t *testing.T, x *Indexer, text, itemType string, offset, limit int, candidates ...db.RepoItemNumber) []db.RepoItemNumber {
	t.Helper()
	ranked, err := Rank(t.Context(), x, text, itemType, candidates,
		func(row db.RepoItemNumber) db.RepoItemNumber { return row }, offset, limit)
	require.NoError(t, err)
	assert.True(t, slices.IsSortedFunc(ranked, func(a, b Ranked[db.RepoItemNumber]) int {
		return cmp.Compare(b.Score, a.Score)
	}), "scores descend")
	rows := make([]db.RepoItemNumber, len(ranked))
	for i, match := range ranked {
		rows[i] = match.Row
	}
	return rows
}

func TestRankOrdersCandidatesByTheirClosestDocument(t *testing.T) {
	assert := assert.New(t)
	f := newFixture(t)
	f.sources.set(
		pull(1, 10, 101, "Speed up cache warmup", "The cache is cold after deploys."),
		pull(2, 10, 102, "Fix login redirect", "Users bounce back to the login page."),
		pull(3, 20, 103, "Docs typo", "The docs page misspells a word."),
		issue(4, 10, 101, "Flaky flaky flaky", "Same number as a pull request."),
		pullComment(50, 10, 102, "This also looks flaky in CI, flaky flaky."),
	)
	x := f.open(t, "test-model")
	require.NoError(t, x.Refresh(t.Context()))

	pulls := []db.RepoItemNumber{item(10, 101), item(10, 102), item(20, 103)}
	ranked := rank(t, x, "flaky tests", "pr", 0, 0, pulls...)
	assert.Len(ranked, 3, "one row per item even when several of its documents match")
	assert.Equal(item(10, 102), ranked[0], "a comment ranks its pull request")

	assert.ElementsMatch([]db.RepoItemNumber{item(10, 101), item(20, 103)},
		rank(t, x, "flaky tests", "pr", 0, 0, item(10, 101), item(20, 103)),
		"documents outside the candidates never take a candidate's place")
	assert.Equal(ranked[1:2], rank(t, x, "flaky tests", "pr", 1, 1, pulls...), "offset and limit page the ranking")

	score := func(itemType string) float32 {
		ranked, err := Rank(t.Context(), x, "flaky", itemType, []db.RepoItemNumber{item(10, 101)},
			func(row db.RepoItemNumber) db.RepoItemNumber { return row }, 0, 0)
		require.NoError(t, err)
		require.Len(t, ranked, 1)
		return ranked[0].Score
	}
	assert.Greater(score("issue"), score("pr"), "an issue's text never ranks the pull request with its number")
}

func TestRefreshReembedsOnlyChangedDocumentsAndDropsDeletedOnes(t *testing.T) {
	assert := assert.New(t)
	f := newFixture(t)
	f.sources.set(
		pull(1, 10, 101, "Speed up cache warmup", "The cache is cold."),
		pull(2, 10, 102, "Fix login redirect", "Login loops."),
	)
	x := f.open(t, "test-model")
	require.NoError(t, x.Refresh(t.Context()))
	f.endpoint.ResetInputs()

	// PR 102 is rewritten and a docs issue appears; PR 101 is unchanged.
	f.sources.set(
		pull(1, 10, 101, "Speed up cache warmup", "The cache is cold."),
		pull(2, 10, 102, "Rewrite docs index", "The docs index is stale."),
		issue(3, 20, 7, "Docs build", "docs docs"),
	)
	require.NoError(t, x.Refresh(t.Context()))
	encoded := f.endpoint.Inputs()
	assert.Len(encoded, 2, "unchanged text is not sent to the endpoint again")
	assert.NotContains(strings.Join(encoded, "\n"), "cache")
	assert.Equal(item(10, 102), rank(t, x, "docs", "pr", 0, 0, item(10, 101), item(10, 102))[0],
		"the rewritten description ranks with its new text")

	f.sources.set(pull(1, 10, 101, "Speed up cache warmup", "The cache is cold."))
	require.NoError(t, x.Refresh(t.Context()))
	assert.Equal([]db.RepoItemNumber{item(10, 101)}, rank(t, x, "docs", "pr", 0, 0, item(10, 101), item(10, 102)),
		"deleted sources leave the index")
}

func TestRefreshSkipsOnlyDocumentsTheEndpointRejects(t *testing.T) {
	f := newFixture(t)
	f.endpoint.RejectInputsContaining("enormous")
	f.sources.set(
		pull(1, 10, 101, "Cache fix", "cache"),
		pull(2, 10, 102, "Huge paste", "enormous log output"),
	)
	x := f.open(t, "test-model")
	require.NoError(t, x.Refresh(t.Context()))
	assert.Equal(t, []db.RepoItemNumber{item(10, 101)}, rank(t, x, "cache", "pr", 0, 0, item(10, 101), item(10, 102)))
}

func TestModelChangeRebuildsTheIndex(t *testing.T) {
	assert := assert.New(t)
	f := newFixture(t)
	f.sources.set(pull(1, 10, 101, "Cache fix", "cache"))
	first := f.open(t, "model-a")
	require.NoError(t, first.Refresh(t.Context()))
	require.NoError(t, first.Close())

	// The new model's endpoint refuses every request, so its backfill fails
	// rather than marking the document skipped.
	f.endpoint.RefuseModel("model-b")
	second := f.open(t, "model-b")
	require.Error(t, second.Refresh(t.Context()))
	f.endpoint.RefuseModel("")
	assert.Empty(rank(t, second, "cache", "pr", 0, 0, item(10, 101)), "model-a vectors are never compared with a model-b query")

	require.NoError(t, second.Refresh(t.Context()))
	assert.Equal("model-b", f.endpoint.LastModel())
	assert.Equal([]db.RepoItemNumber{item(10, 101)}, rank(t, second, "cache", "pr", 0, 0, item(10, 101)))
}

func TestWakesNeverStartPassesLessThanAMinuteApart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		x := &Indexer{wake: make(chan struct{}, 1)}
		start := time.Now()
		var mu sync.Mutex
		var passes []time.Duration
		started := func() []time.Duration {
			mu.Lock()
			defer mu.Unlock()
			return slices.Clone(passes)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go schedulePasses(ctx, time.Hour, x.wake, func() {
			mu.Lock()
			defer mu.Unlock()
			passes = append(passes, time.Since(start))
		})

		synctest.Wait()
		assert.Equal([]time.Duration{0}, started(), "the first pass starts immediately")

		for range 5 {
			time.Sleep(10 * time.Second)
			x.Wake()
			synctest.Wait()
		}
		assert.Equal([]time.Duration{0}, started(), "wakes inside the gap wait it out")

		time.Sleep(10 * time.Second)
		synctest.Wait()
		assert.Equal([]time.Duration{0, time.Minute}, started(),
			"one pass answers every wake that arrived during the gap")

		time.Sleep(10 * time.Minute)
		synctest.Wait()
		assert.Len(started(), 2, "coalesced wakes leave no pass behind")

		x.Wake()
		synctest.Wait()
		assert.Equal([]time.Duration{0, time.Minute, 11 * time.Minute}, started(),
			"a wake after the gap starts a pass immediately")
	})
}
