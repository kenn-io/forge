package landedwork_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/landedwork"
)

func TestAutomaticSingleParent(t *testing.T) {
	for _, mode := range []string{"squash", "replay", "rebase", "fast forward", "ambiguous", "crosses base", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			f := buildFixture(t, true)
			proofs, spine := []string{"squash"}, []string{f.head}
			reason := ""
			switch mode {
			case "replay", "rebase":
				f.repo.Checkout("-b", "replay", f.base)
				f.base = f.repo.CommitFile("other", "context\n", "advance")
				first := f.repo.CommitFile("work.txt", "new\nkeep\n", "replay first")
				f.head = first
				spine = []string{first}
				proofs = []string{"rebase", "squash"}
				if mode == "replay" {
					f.source = f.source[:1]
				} else {
					f.head = f.repo.CommitFile("work.txt", "new\nkeep\none\ntwo\n", "replay second")
					spine = append(spine, f.head)
					proofs = []string{"rebase"}
				}
			case "fast forward":
				f.head = f.source[1]
				spine, proofs = f.source, []string{"fast_forward", "rebase"}
			case "ambiguous", "crosses base":
				f.repo.Checkout("-b", "exact", f.base)
				f.repo.Run("commit", "--allow-empty", "-m", "empty prefix")
				prefix := f.repo.Head()
				f.head = f.repo.CommitFile("work.txt", "changed\n", "content")
				f.source = []string{prefix, f.head}
				reason = "origin_ambiguous"
				if mode == "crosses base" {
					f.base = prefix
					reason = "range_crosses_base"
				}
			case "disabled":
				reason = "method_unproven"
			}
			ctx, p := f.prepare(t)
			e := fixtureEvidence(f, p, "")
			e.Candidates[0].MethodEvidence = ""
			e.Capabilities = landedwork.Capabilities{SingleParentCorrespondence: mode != "disabled"}
			r, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
			require.NoError(t, err)
			assert := assert.New(t)
			assert.Empty(r.DirectPushes)
			if reason != "" {
				assert.Empty(r.Landings)
				assert.False(r.Coverage.Complete)
				assert.Equal(f.base, r.Coverage.CertifiedHead)
				assert.Equal([]landedwork.Gap{{CandidateID: "7", ObjectID: f.head, Reason: reason,
					Span: landedwork.Span{Before: f.base, Through: f.head}}}, r.Coverage.Gaps)
				return
			}
			require.Len(t, r.Landings, 1)
			assert.Equal(landedwork.Landing{CandidateID: "7", Proofs: proofs, Before: f.base,
				Terminal: f.head, Source: f.source, Spine: spine, Introduced: spine}, r.Landings[0])
			assert.True(r.Coverage.Complete)
			assert.Equal(f.head, r.Coverage.CertifiedHead)
			assert.Empty(r.Coverage.Gaps)
		})
	}
}

func TestAutomaticEarlierHistory(t *testing.T) {
	for _, mode := range []string{"disproved", "missing", "missing boundary", "bounded mismatch", "budget", "shallow"} {
		t.Run(mode, func(t *testing.T) {
			require := require.New(t)
			f := buildFixture(t, true)
			f.repo.Checkout("topic")
			f.source = append(f.source, f.repo.CommitFile("source-extra", "extra\n", "third"))
			f.repo.Checkout("-b", "target", f.base)
			earlier := f.repo.CommitFile("other", "earlier\n", "earlier")
			f.base = f.repo.CommitFile("other", "base\n", "advance")
			f.repo.Run("merge", "--squash", "topic")
			f.repo.Run("commit", "-m", "squash")
			terminal := f.repo.Head()
			f.head = terminal
			if mode == "budget" {
				f.head = f.repo.CommitFile("later", "later\n", "after candidate")
			}
			f.repo.Run("commit-graph", "write", "--reachable")
			ctx, p := f.prepare(t)
			e := fixtureEvidence(f, p, "")
			e.Candidates[0].Terminal = terminal
			e.Candidates[0].MethodEvidence = ""
			e.Capabilities = landedwork.Capabilities{SingleParentCorrespondence: true}
			limits := fixtureLimits()
			reason, object := "", ""
			switch mode {
			case "missing":
				require.NoError(os.Remove(filepath.Join(f.repo.GitDir, "objects", earlier[:2], earlier[2:])))
				_, _, err := f.repo.Runner.Run(ctx, f.repo.Root, nil, "cat-file", "-e", earlier)
				require.Error(err)
				// The terminal pair already disproves the range. Older unrelated
				// history is not needed to accept the proven squash.
			case "missing boundary":
				require.NoError(os.Remove(filepath.Join(f.repo.GitDir, "objects", f.base[:2], f.base[2:])))
				reason, object = "objects_unavailable", f.base
			case "bounded mismatch":
				// Enough to prove the squash and reject the terminal range pair,
				// but not to walk the rest of the three-commit alternative.
				limits.Nodes = 7
			case "budget":
				// Source and squash checks fit; the alternative is still untested
				// when its next boundary read exhausts the allowance.
				limits.Nodes = 6
				reason, object = "input_budget_exhausted", terminal
			case "shallow":
				require.NoError(os.WriteFile(filepath.Join(f.repo.GitDir, "shallow"), []byte(earlier+"\n"), 0600))
				reason = "shallow_boundary"
			}
			r, err := landedwork.Analyze(ctx, p, e, limits)
			require.NoError(err)
			assert := assert.New(t)
			if reason == "" {
				require.Len(r.Landings, 1)
				assert.Equal([]string{"squash"}, r.Landings[0].Proofs)
				assert.Equal([]string{terminal}, r.Landings[0].Spine)
				assert.Equal(f.base, r.Landings[0].Before)
				assert.True(r.Coverage.Complete)
				assert.Equal(f.head, r.Coverage.CertifiedHead)
				return
			}
			require.Len(r.Coverage.Gaps, 1)
			assert.Equal(reason, r.Coverage.Gaps[0].Reason)
			assert.Equal(object, r.Coverage.Gaps[0].ObjectID)
			assert.Equal(landedwork.Span{Before: f.base, Through: f.head}, r.Coverage.Gaps[0].Span)
			assert.Empty(r.Landings)
			assert.Empty(r.DirectPushes)
			assert.False(r.Coverage.Complete)
			assert.Equal(f.base, r.Coverage.CertifiedHead)
		})
	}
}

func TestAutomaticAggregateCancellation(t *testing.T) {
	for _, empty := range []bool{false, true} {
		f := buildFixture(t, true)
		f.repo.Checkout("topic")
		f.source = append(f.source, f.repo.CommitFile("extra", "temporary\n", "insert"))
		f.repo.Run("rm", "extra")
		f.repo.Run("commit", "-m", "cancel")
		f.source = append(f.source, f.repo.Head())
		if empty {
			f.source = append(f.source, f.repo.CommitFile("work.txt", "old\nkeep\n", "undo all"))
		}
		f.repo.Checkout("-b", "target", f.base)
		f.repo.Run("merge", "--squash", "topic")
		f.repo.Run("commit", "--allow-empty", "-m", "aggregate")
		f.head = f.repo.Head()
		ctx, p := f.prepare(t)
		e := fixtureEvidence(f, p, "")
		e.Candidates[0].MethodEvidence = ""
		e.Capabilities = landedwork.Capabilities{SingleParentCorrespondence: true}
		r, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
		require.NoError(t, err)
		assert := assert.New(t)
		assert.Empty(r.DirectPushes)
		if empty {
			assert.Empty(r.Landings)
			assert.False(r.Coverage.Complete)
			assert.Equal(f.base, r.Coverage.CertifiedHead)
			require.Len(t, r.Coverage.Gaps, 1)
			assert.Equal("edits_unavailable", r.Coverage.Gaps[0].Reason)
		} else {
			require.Len(t, r.Landings, 1)
			assert.Equal([]string{"squash"}, r.Landings[0].Proofs)
			assert.Equal([]string{f.head}, r.Landings[0].Spine)
			assert.True(r.Coverage.Complete)
			assert.Equal(f.head, r.Coverage.CertifiedHead)
		}
	}
}
