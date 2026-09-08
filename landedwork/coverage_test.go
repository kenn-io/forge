package landedwork_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/landedwork"
)

func TestAnalyzeCoverage(t *testing.T) {
	cases := []struct {
		name, reason string
		change       func(*landedwork.Evidence)
		proven       bool
	}{
		{"partial source", "source_incomplete", func(e *landedwork.Evidence) { e.Candidates[0].SourceComplete = false }, false},
		{"rebase", "method_unsupported", func(e *landedwork.Evidence) { e.Candidates[0].Method = "rebase" }, false},
		{"fast forward", "method_unsupported", func(e *landedwork.Evidence) { e.Candidates[0].Method = "fast_forward" }, false},
		{"missing method evidence", "method_unproven", func(e *landedwork.Evidence) { e.Candidates[0].MethodEvidence = "" }, false},
		{"missing terminal evidence", "terminal_unproven", func(e *landedwork.Evidence) { e.Candidates[0].TerminalEvidence = "" }, false},
		{"missing object", "objects_unavailable", func(e *landedwork.Evidence) {
			e.Candidates[0].Source = append(slices.Clone(e.Candidates[0].Source), strings.Repeat("a", 40))
		}, false},
		{"duplicate source", "source_invalid", func(e *landedwork.Evidence) {
			e.Candidates[0].Source = append(slices.Clone(e.Candidates[0].Source), e.Candidates[0].Source[0])
		}, false},
		{"wrong head", "source_head_mismatch", func(e *landedwork.Evidence) { e.Candidates[0].SourceHead = e.Query.Bounds.Base }, false},
		{"incomplete correspondence", "source_correspondence_unproven", func(e *landedwork.Evidence) { e.Candidates[0].Source = e.Candidates[0].Source[1:] }, false},
		{"unsupported capability", "method_unsupported", func(e *landedwork.Evidence) { e.Capabilities.Merge = false }, false},
		{"unknown inventory", "inventory_unknown", func(e *landedwork.Evidence) { e.Inventory = landedwork.Inventory{} }, true},
		{"partial sweep", "association_sweep_incomplete", func(e *landedwork.Evidence) {
			e.Inventory.Complete = false
			e.Inventory.Reason = "association_sweep_incomplete"
			e.Inventory.NextCommit = e.Query.Commits[0]
			e.Inventory.NextPage = "next-page"
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := buildFixture(t, false)
			ctx, p := f.prepare(t)
			e := fixtureEvidence(f, p, "merge")
			tc.change(&e)
			r, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
			require := require.New(t)
			assert := assert.New(t)
			require.NoError(err)
			require.Len(r.Coverage.Gaps, 1)
			assert.Equal(tc.reason, r.Coverage.Gaps[0].Reason)
			assert.Equal(f.bounds(), r.Coverage.Bounds)
			assert.Equal(e.Inventory, r.Coverage.Inventory)
			assert.False(r.Coverage.Complete)
			assert.Equal(f.base, r.Coverage.CertifiedHead)
			if tc.proven {
				assert.Len(r.Landings, 1)
				assert.Empty(r.Unattributed)
			} else {
				assert.Empty(r.Landings)
				assert.Equal([]string{f.head}, r.Unattributed)
				assert.Equal("7", r.Coverage.Gaps[0].CandidateID)
			}
		})
	}
}

func TestAnalyzeCoverageNoCandidateAndConflict(t *testing.T) {
	for _, name := range []string{"unattributed", "conflict", "rebase with merge"} {
		t.Run(name, func(t *testing.T) {
			f := buildSideFixture(t)
			ctx, p := f.prepare(t)
			e := fixtureEvidence(f, p, "merge")
			switch name {
			case "unattributed":
				e.Candidates = nil
			case "conflict":
				c := e.Candidates[0]
				c.ID = "8"
				e.Candidates = append(e.Candidates, c)
			case "rebase with merge":
				e.Candidates[0].Method = "rebase"
			}
			r, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
			require.NoError(t, err)
			assert := assert.New(t)
			assert.Empty(r.Landings)
			assert.Equal(f.bounds(), r.Coverage.Bounds)
			if name == "unattributed" {
				assert.Empty(r.Unattributed)
				assert.True(r.Coverage.Complete)
				assert.Equal(f.head, r.Coverage.CertifiedHead)
				assert.Len(r.DirectPushes, 1)
			} else {
				assert.Equal([]string{f.head}, r.Unattributed)
				assert.False(r.Coverage.Complete)
				assert.Equal(f.base, r.Coverage.CertifiedHead)
				assert.Empty(r.DirectPushes)
			}
			switch name {
			case "unattributed":
				assert.Empty(r.Coverage.Gaps)
			case "conflict":
				assert.Equal([]landedwork.Gap{{CandidateID: "7", ObjectID: f.head, Reason: "candidate_conflict", Span: landedwork.Span{Before: f.base, Through: f.head}}, {CandidateID: "8", ObjectID: f.head, Reason: "candidate_conflict", Span: landedwork.Span{Before: f.base, Through: f.head}}}, r.Coverage.Gaps)
			case "rebase with merge":
				assert.Equal([]landedwork.Gap{{CandidateID: "7", ObjectID: f.head, Reason: "method_unsupported", Span: landedwork.Span{Before: f.base, Through: f.head}}}, r.Coverage.Gaps)
			}
		})
	}
}

func TestAnalyzeCoverageInputBudget(t *testing.T) {
	for _, name := range []string{"records", "bytes", "nodes"} {
		t.Run(name, func(t *testing.T) {
			f := buildFixture(t, false)
			ctx, p := f.prepare(t)
			limits := fixtureLimits()
			switch name {
			case "records":
				limits.Records = 1
			case "bytes":
				limits.InputBytes = 1
			case "nodes":
				limits.Nodes = 1
			}
			r, err := landedwork.Analyze(ctx, p, fixtureEvidence(f, p, "merge"), limits)
			if err != nil {
				require.ErrorIs(t, err, landedwork.ErrOutputBudget)
				return
			}
			assert := assert.New(t)
			assert.False(r.Coverage.Complete)
			assert.Equal(f.base, r.Coverage.CertifiedHead)
			assert.Equal([]string{f.head}, r.Unattributed)
			assert.Equal(f.bounds(), r.Coverage.Bounds)
			require.NotEmpty(t, r.Coverage.Gaps)
			assert.Equal("input_budget_exhausted", r.Coverage.Gaps[0].Reason)
		})
	}
}
