package semantic

import (
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

func pull(id int64, repoID int64, number int, state, title, body string) db.SemanticSource {
	return db.SemanticSource{
		Kind: db.SemanticSourcePull, ID: id, RepoID: repoID, ItemType: "pr",
		ItemNumber: number, ItemState: state, Title: title, Body: body,
	}
}

func issue(id int64, repoID int64, number int, state, title, body string) db.SemanticSource {
	return db.SemanticSource{
		Kind: db.SemanticSourceIssue, ID: id, RepoID: repoID, ItemType: "issue",
		ItemNumber: number, ItemState: state, Title: title, Body: body,
	}
}

func pullComment(id int64, repoID int64, number int, body string) db.SemanticSource {
	return db.SemanticSource{
		Kind: db.SemanticSourcePullComment, ID: id, RepoID: repoID, ItemType: "pr",
		ItemNumber: number, ItemState: "open", Body: body,
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

func (f *fixture) indexer(t *testing.T, model string) *Indexer {
	t.Helper()
	encoder, err := NewEncoder(f.endpoint.Embedder(model))
	require.NoError(t, err)
	index, err := Open(t.Context(), f.path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = index.Close() })
	return NewIndexer(index, encoder, f.sources, time.Hour)
}

func itemsOf(hits []Hit) []string {
	out := make([]string, len(hits))
	for i, hit := range hits {
		out[i] = itemKey(hit.RepoID, hit.ItemType, hit.Number)
	}
	return out
}

func TestSearchRanksItemsByTheirClosestDocument(t *testing.T) {
	assert := assert.New(t)
	f := newFixture(t)
	f.sources.set(
		pull(1, 10, 101, "open", "Speed up cache warmup", "The cache is cold after deploys."),
		pull(2, 10, 102, "open", "Fix login redirect", "Users bounce back to the login page."),
		issue(3, 20, 7, "open", "Docs typo", "The docs page misspells a word."),
		pullComment(50, 10, 102, "This also looks flaky in CI, flaky flaky."),
	)
	x := f.indexer(t, "test-model")
	require.NoError(t, x.Refresh(t.Context()))

	status := x.Status()
	assert.True(status.Ready)
	assert.Equal(int64(4), status.Embedded)
	assert.Zero(status.Backlog)
	assert.Empty(status.LastError)

	hits, err := x.Search(t.Context(), Query{Text: "flaky tests", Limit: 10})
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	assert.Equal("10:pr:102", itemsOf(hits)[0], "a comment ranks its pull request")
	assert.Equal("pr_comment", hits[0].Match.Kind)
	assert.Equal(int64(50), hits[0].Match.SourceID)
	assert.Contains(hits[0].Match.Excerpt, "flaky in CI")
	assert.Len(hits, 3, "one hit per item even when several documents match")

	hits, err = x.Search(t.Context(), Query{Text: "cache", Limit: 10, ItemType: "issue"})
	require.NoError(t, err)
	assert.Equal([]string{"20:issue:7"}, itemsOf(hits), "item type filter applies before ranking")

	hits, err = x.Search(t.Context(), Query{Text: "docs", Limit: 10, RepoIDs: []int64{10}})
	require.NoError(t, err)
	assert.ElementsMatch([]string{"10:pr:101", "10:pr:102"}, itemsOf(hits), "repository filter applies before ranking")

	hits, err = x.Search(t.Context(), Query{Text: "cache", Limit: 10, RepoIDs: []int64{}})
	require.NoError(t, err)
	assert.Empty(hits, "an empty repository list matches nothing")
}

func TestRefreshReembedsOnlyChangedDocumentsAndDropsDeletedOnes(t *testing.T) {
	assert := assert.New(t)
	f := newFixture(t)
	f.sources.set(
		pull(1, 10, 101, "open", "Speed up cache warmup", "The cache is cold."),
		pull(2, 10, 102, "open", "Fix login redirect", "Login loops."),
	)
	x := f.indexer(t, "test-model")
	require.NoError(t, x.Refresh(t.Context()))
	f.endpoint.ResetInputs()

	// PR 102 is rewritten, PR 101 only changes state, and a new docs issue
	// appears.
	f.sources.set(
		pull(1, 10, 101, "merged", "Speed up cache warmup", "The cache is cold."),
		pull(2, 10, 102, "open", "Rewrite docs index", "The docs index is stale."),
		issue(3, 20, 7, "open", "Docs build", "docs docs"),
	)
	require.NoError(t, x.Refresh(t.Context()))
	encoded := f.endpoint.Inputs()
	assert.Len(encoded, 2, "unchanged text is not sent to the endpoint again")
	assert.NotContains(strings.Join(encoded, "\n"), "cache")

	hits, err := x.Search(t.Context(), Query{Text: "login", Limit: 10, States: []string{"open"}})
	require.NoError(t, err)
	assert.NotContains(itemsOf(hits), "10:pr:101", "state filter uses the refreshed state")
	for _, hit := range hits {
		assert.NotContains(hit.Match.Excerpt, "Login loops", "old text never surfaces after an edit")
	}

	f.sources.set(pull(1, 10, 101, "merged", "Speed up cache warmup", "The cache is cold."))
	require.NoError(t, x.Refresh(t.Context()))
	hits, err = x.Search(t.Context(), Query{Text: "docs", Limit: 10})
	require.NoError(t, err)
	assert.Equal([]string{"10:pr:101"}, itemsOf(hits), "deleted sources leave the index")
	assert.Equal(int64(1), x.Status().Embedded)
}

func TestRefreshSkipsDocumentsTheEndpointRejects(t *testing.T) {
	f := newFixture(t)
	f.endpoint.RejectInputsContaining("enormous")
	f.sources.set(
		pull(1, 10, 101, "open", "Cache fix", "cache"),
		pull(2, 10, 102, "open", "Huge paste", "enormous log output"),
	)
	x := f.indexer(t, "test-model")
	require.NoError(t, x.Refresh(t.Context()))
	status := x.Status()
	assert.Equal(t, int64(1), status.Embedded)
	assert.Equal(t, int64(1), status.Skipped)
	assert.True(t, status.Ready)
}

func TestModelChangeNeverAnswersFromAnotherModelsVectors(t *testing.T) {
	assert := assert.New(t)
	f := newFixture(t)
	f.sources.set(pull(1, 10, 101, "open", "Cache fix", "cache"))
	first := f.indexer(t, "model-a")
	require.NoError(t, first.Refresh(t.Context()))
	require.NoError(t, first.index.Close())

	// The new model's endpoint refuses every request, so its backfill fails.
	f.endpoint.RefuseModel("model-b")
	second := f.indexer(t, "model-b")
	_, err := second.Search(t.Context(), Query{Text: "cache", Limit: 5})
	require.ErrorIs(t, err, ErrNotReady, "model-a's generation is still active before the first pass")
	require.Error(t, second.Refresh(t.Context()))
	status := second.Status()
	assert.False(status.Ready)
	assert.Equal(int64(1), status.Backlog)
	assert.NotEmpty(status.LastError)

	f.endpoint.RefuseModel("")
	hits, err := second.Search(t.Context(), Query{Text: "cache", Limit: 5})
	require.NoError(t, err)
	assert.Empty(hits, "model-a vectors are not compared with a model-b query")

	require.NoError(t, second.Refresh(t.Context()))
	assert.True(second.Status().Ready)
	hits, err = second.Search(t.Context(), Query{Text: "cache", Limit: 5})
	require.NoError(t, err)
	assert.Equal([]string{"10:pr:101"}, itemsOf(hits))

	generations, err := second.index.store.Generations(t.Context())
	require.NoError(t, err)
	var live int
	for _, generation := range generations {
		if generation.State != "retired" {
			live++
		}
	}
	assert.Equal(1, live, "the previous model's generation is retired")
}

func TestOpenRebuildsSidecarWithUnknownSchemaVersion(t *testing.T) {
	f := newFixture(t)
	f.sources.set(pull(1, 10, 101, "open", "Cache fix", "cache"))
	x := f.indexer(t, "test-model")
	require.NoError(t, x.Refresh(t.Context()))
	_, err := x.index.db.ExecContext(t.Context(), `UPDATE semantic_meta SET value = 'old' WHERE key = 'schema_version'`)
	require.NoError(t, err)
	require.NoError(t, x.index.Close())

	reopened := f.indexer(t, "test-model")
	_, err = reopened.Search(t.Context(), Query{Text: "cache", Limit: 5})
	require.ErrorIs(t, err, ErrNotReady, "a rebuilt sidecar starts empty")
}

func TestWakesNeverStartPassesLessThanAMinuteApart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		x := &Indexer{wake: make(chan struct{}, 1)}
		start := time.Now()
		var passes []time.Duration
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go schedulePasses(ctx, time.Hour, x.wake, func() {
			passes = append(passes, time.Since(start))
		})

		synctest.Wait()
		assert.Equal([]time.Duration{0}, passes, "the first pass starts immediately")

		for range 5 {
			time.Sleep(10 * time.Second)
			x.Wake()
			synctest.Wait()
		}
		assert.Equal([]time.Duration{0}, passes, "wakes inside the gap wait it out")

		time.Sleep(10 * time.Second)
		synctest.Wait()
		assert.Equal([]time.Duration{0, time.Minute}, passes,
			"one pass answers every wake that arrived during the gap")

		time.Sleep(10 * time.Minute)
		synctest.Wait()
		assert.Len(passes, 2, "coalesced wakes leave no pass behind")

		x.Wake()
		synctest.Wait()
		assert.Equal([]time.Duration{0, time.Minute, 11 * time.Minute}, passes,
			"a wake after the gap starts a pass immediately")
	})
}
