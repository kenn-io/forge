package landedwork_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/landedwork"
)

func twoLandings(t *testing.T) (fixture, []landedwork.Candidate) {
	t.Helper()
	f := buildFixture(t, false)
	first := landedwork.Candidate{Repository: f.bounds().Repository, ID: "7", Terminal: f.head,
		SourceHead: f.source[1], Source: f.source, SourceComplete: true, Method: "merge",
		MethodEvidence: "fixture-method", TerminalEvidence: "fixture-terminal"}
	f.repo.Checkout("-b", "second")
	id := f.repo.CommitFile("second.txt", "second\n", "second change")
	f.repo.Checkout("main")
	f.repo.Run("merge", "--no-ff", "second", "-m", "second merge")
	f.head = f.repo.Head()
	second := first
	second.ID, second.Terminal, second.SourceHead, second.Source = "8", f.head, id, []string{id}
	return f, []landedwork.Candidate{first, second}
}

func TestAnalyzeMixedCoverage(t *testing.T) {
	for _, name := range []string{"complete", "unresolved", "unattributed"} {
		t.Run(name, func(t *testing.T) {
			f, candidates := twoLandings(t)
			ctx, p := f.prepare(t)
			e := fixtureEvidence(f, p, "merge")
			e.Candidates = candidates
			switch name {
			case "unresolved":
				e.Candidates[1].SourceComplete = false
			case "unattributed":
				e.Candidates = e.Candidates[:1]
			}
			r, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
			require := require.New(t)
			assert := assert.New(t)
			require.NoError(err)
			require.NotEmpty(r.Landings)
			assert.Equal("7", r.Landings[0].CandidateID)
			assert.Equal(f.bounds(), r.Coverage.Bounds)
			switch name {
			case "complete":
				assert.Len(r.Landings, 2)
				assert.True(r.Coverage.Complete)
				assert.Equal(f.head, r.Coverage.CertifiedHead)
				assert.Empty(r.Unattributed)
				assert.Empty(r.Coverage.Gaps)
			case "unresolved":
				assert.Len(r.Landings, 1)
				assert.False(r.Coverage.Complete)
				assert.Equal(f.base, r.Coverage.CertifiedHead)
				assert.Equal([]string{f.head}, r.Unattributed)
				assert.Equal([]landedwork.Gap{{CandidateID: "8", ObjectID: f.head, Reason: "source_incomplete", Span: landedwork.Span{Before: f.base, Through: f.head}}}, r.Coverage.Gaps)
			case "unattributed":
				assert.Len(r.Landings, 1)
				assert.True(r.Coverage.Complete)
				assert.Equal(f.head, r.Coverage.CertifiedHead)
				assert.Empty(r.Unattributed)
				assert.Equal([]landedwork.DirectPush{{Before: candidates[0].Terminal, Terminal: f.head, Introduced: []string{candidates[1].SourceHead, f.head}}}, r.DirectPushes)
				assert.Empty(r.Coverage.Gaps)
			}
		})
	}
}

func TestAnalyzeOrderIndependentUnderExhaustion(t *testing.T) {
	f, candidates := twoLandings(t)
	ctx, p := f.prepare(t)
	e := fixtureEvidence(f, p, "merge")
	e.Candidates = candidates
	limits := fixtureLimits()
	limits.Nodes = 12 // Enough for the first proof, not both; candidate order must not select the winner.
	r, err := landedwork.Analyze(ctx, p, e, limits)
	require.NoError(t, err)
	e.Candidates = slices.Clone(e.Candidates)
	slices.Reverse(e.Candidates)
	reversed, err := landedwork.Analyze(ctx, p, e, limits)
	require.NoError(t, err)
	assert.Equal(t, r, reversed)
}

func TestAnalyzeQueryBinding(t *testing.T) {
	for _, name := range []string{"repository", "pin", "commits", "coverage", "candidate identity"} {
		t.Run(name, func(t *testing.T) {
			f := buildFixture(t, false)
			ctx, p := f.prepare(t)
			e := fixtureEvidence(f, p, "merge")
			switch name {
			case "repository":
				e.Query.Bounds.Repository.ID = "202"
			case "pin":
				e.Query.Bounds.Head = f.base
			case "commits":
				e.Query.Commits = e.Query.Commits[:1]
			case "coverage":
				e.Query.Complete = false
			case "candidate identity":
				e.Candidates[0].Repository.ID = "202"
			}
			r, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
			require.Error(t, err)
			assert.Equal(t, landedwork.Result{}, r)
		})
	}
}

func TestAnalyzeIncompletePreparation(t *testing.T) {
	f := buildFixture(t, false)
	ctx, _ := f.prepare(t)
	limits := fixtureLimits()
	limits.Nodes = 1
	p, err := landedwork.Prepare(ctx, f.repo.Root, f.bounds(), limits)
	require := require.New(t)
	require.NoError(err)
	r, err := landedwork.Analyze(ctx, p, fixtureEvidence(f, p, "merge"), fixtureLimits())
	require.NoError(err)
	assert := assert.New(t)
	assert.False(r.Coverage.Complete)
	assert.Equal(f.base, r.Coverage.CertifiedHead)
	assert.Equal(p.Query().Gaps, r.Coverage.Gaps)
	assert.Equal(f.bounds(), r.Coverage.Bounds)
	assert.Empty(r.Landings)
}

func TestAnalyzeBudgetPreservesPreparedGaps(t *testing.T) {
	f := buildFixture(t, false)
	ctx, _ := f.prepare(t)
	bounds := f.bounds()
	bounds.Head = strings.Repeat("a", 40)
	p, err := landedwork.Prepare(ctx, f.repo.Root, bounds, fixtureLimits())
	require := require.New(t)
	require.NoError(err)
	expected := []landedwork.Gap{{ObjectID: bounds.Head, Reason: "objects_unavailable", Span: landedwork.Span{Before: bounds.Base, Through: bounds.Head}}}
	require.Equal(expected, p.Query().Gaps)
	limits := fixtureLimits()
	limits.InputBytes = 1
	r, err := landedwork.Analyze(ctx, p, fixtureEvidence(f, p, "merge"), limits)
	require.NoError(err)
	assert := assert.New(t)
	assert.Equal(append(expected, landedwork.Gap{Reason: "input_budget_exhausted", Span: landedwork.Span{Before: bounds.Base, Through: bounds.Head}}), r.Coverage.Gaps)
	assert.False(r.Coverage.Complete)
	assert.Equal(bounds, r.Coverage.Bounds)
	assert.Equal(bounds.Base, r.Coverage.CertifiedHead)
}
