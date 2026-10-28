package landedwork_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/landedwork"
)

func TestSquashOfPartOfListedSource(t *testing.T) {
	for _, method := range []string{"squash", "automatic"} {
		for _, mode := range []string{"target context", "declared context", "raced push", "context and raced push", "different tree"} {
			t.Run(method+"/"+mode, func(t *testing.T) {
				f := buildFixture(t, true)
				r := f.repo
				// The source revises a file another change already landed, so
				// a merge from before that landing conflicts.
				r.Checkout("-b", "feature", f.base)
				feature := r.CommitFile("feature.txt", "first\n", "add feature")
				r.Checkout("-b", "target", f.base)
				r.Run("merge", "--squash", feature)
				r.Run("commit", "-m", "squash feature")
				landed := r.Head()
				r.Checkout("-b", "source", landed)
				revise := r.CommitFile("feature.txt", "second\n", "revise feature")
				extend := r.CommitFile("work.txt", "new\nkeep\n", "replace")
				late := r.CommitFile("work.txt", "new\nkeep\nlate\n", "pushed while merging")
				r.Checkout("target")
				r.Run("merge", "--squash", extend)
				if mode == "different tree" {
					require.NoError(t, os.WriteFile(filepath.Join(r.Root, "other.txt"), []byte("unrelated\n"), 0o600))
					r.Run("add", "other.txt")
				}
				r.Run("commit", "-m", "squash")
				f.head = r.Head()
				if mode != "declared context" {
					// Otherwise the interval also holds the other change's landing.
					f.base = landed
				}
				// GitHub lists commits from the change's recorded base, which can
				// be older than the commit the branch was rebased onto.
				f.source = map[string][]string{
					"target context":         {landed, revise, extend},
					"declared context":       {landed, revise, extend},
					"raced push":             {revise, extend, late},
					"context and raced push": {landed, revise, extend, late},
					"different tree":         {landed, revise, extend},
				}[mode]
				ctx, p := f.prepare(t)
				e := fixtureEvidence(f, p, "squash")
				if method == "automatic" {
					e.Candidates[0].Method = ""
					e.Candidates[0].MethodEvidence = ""
					e.Capabilities = landedwork.Capabilities{SingleParentCorrespondence: true}
				}
				if mode == "declared context" {
					other := squashCandidate(f, "8", landed, feature)
					other.Method, other.MethodEvidence = e.Candidates[0].Method, e.Candidates[0].MethodEvidence
					e.Candidates = append(e.Candidates, other)
				}
				result, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
				require.NoError(t, err)
				assert := assert.New(t)
				assert.Empty(result.DirectPushes)
				if mode == "declared context" {
					require.Len(t, result.Landings, 2)
					assert.Equal("8", result.Landings[0].CandidateID)
					result.Landings = result.Landings[1:]
				}
				if mode == "different tree" {
					assert.Empty(result.Landings)
					assert.False(result.Coverage.Complete)
					require.Len(t, result.Coverage.Gaps, 1)
					assert.Equal("source_correspondence_unproven", result.Coverage.Gaps[0].Reason)
					return
				}
				require.Len(t, result.Landings, 1)
				// Only the commits the squash landed are its source.
				assert.Equal(landedwork.Landing{
					CandidateID: "7", Proofs: []string{"squash"}, Before: landed, Terminal: f.head,
					Spine: []string{f.head}, Source: []string{revise, extend}, Introduced: []string{f.head},
				}, result.Landings[0])
				assert.True(result.Coverage.Complete)
			})
		}
	}
}

// A rebase can land every part of its source as its own commit. When a late
// push leaves one more listed commit, the part that landed still has to be
// checked as a rebase. Here each listed edit sequence also nets to the
// terminal's own edits, so the terminal alone looks like a squash of the part.
func TestRebaseOfPartOfListedSourceIsNotASquash(t *testing.T) {
	for _, mode := range []string{"cancelling", "reapplied"} {
		t.Run(mode, func(t *testing.T) {
			f := buildFixture(t, true)
			r := f.repo
			r.Checkout("-b", "source", f.base)
			var source []string
			if mode == "cancelling" {
				source = append(source,
					r.CommitFile("work.txt", "temporary\nkeep\n", "try"),
					r.CommitFile("work.txt", "old\nkeep\n", "undo"),
					r.CommitFile("other.txt", "other\n", "add other"))
			} else {
				source = append(source,
					r.CommitFile("work.txt", "new\nkeep\n", "edit"),
					r.CommitFile("work.txt", "old\nkeep\n", "revert"),
					r.CommitFile("work.txt", "new\nkeep\n", "reapply"))
			}
			late := r.CommitFile("late.txt", "late\n", "pushed while merging")
			r.Checkout("-b", "target", f.base)
			f.base = r.CommitFile("unrelated.txt", "unrelated\n", "target advances")
			for _, id := range source {
				r.Run("cherry-pick", id)
			}
			f.head = r.Head()
			f.source = append(source, late)
			ctx, p := f.prepare(t)
			e := fixtureEvidence(f, p, "")
			e.Candidates[0].MethodEvidence = ""
			e.Capabilities = landedwork.Capabilities{SingleParentCorrespondence: true}
			result, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
			require.NoError(t, err)
			assert := assert.New(t)
			assert.Empty(result.Landings, "the terminal alone must not become the change's squash")
			assert.Empty(result.DirectPushes, "the rebased commits must not become direct pushes")
			assert.False(result.Coverage.Complete)
			require.Len(t, result.Coverage.Gaps, 1)
			// Repeated edits leave the part's rebase unreadable, so the proof
			// keeps the full source's mismatch instead of trying shorter parts.
			assert.Equal(map[string]string{"cancelling": "origin_ambiguous", "reapplied": "source_correspondence_unproven"}[mode],
				result.Coverage.Gaps[0].Reason)
		})
	}
}

// A fast-forward lands the change's own commits unchanged, so the target holds
// them too. They are the change's landing, not context from another change,
// even when an earlier interval already holds the first of them.
func TestFastForwardOfPartOfListedSourceKeepsItsCommits(t *testing.T) {
	for _, mode := range []string{"inside interval", "crosses base"} {
		t.Run(mode, func(t *testing.T) {
			f := buildFixture(t, true)
			r := f.repo
			r.Checkout("-b", "source", f.base)
			first := r.CommitFile("work.txt", "new\nkeep\n", "replace")
			second := r.CommitFile("other.txt", "other\n", "add other")
			late := r.CommitFile("late.txt", "late\n", "pushed while merging")
			r.Checkout("-b", "target", f.base)
			r.Run("merge", "--ff-only", second)
			f.head = r.Head()
			if mode == "crosses base" {
				f.base = first
			}
			f.source = []string{first, second, late}
			ctx, p := f.prepare(t)
			e := fixtureEvidence(f, p, "")
			e.Candidates[0].MethodEvidence = ""
			e.Capabilities = landedwork.Capabilities{SingleParentCorrespondence: true, FastForward: true, Rebase: true}
			result, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
			require.NoError(t, err)
			assert := assert.New(t)
			assert.Empty(result.DirectPushes, "the change's first commit must not become a direct push")
			if mode == "crosses base" {
				assert.Empty(result.Landings, "the second commit alone must not become the change's landing")
				assert.False(result.Coverage.Complete)
				require.Len(t, result.Coverage.Gaps, 1)
				assert.Equal("range_crosses_base", result.Coverage.Gaps[0].Reason)
				return
			}
			require.Len(t, result.Landings, 1)
			assert.Equal([]string{first, second}, result.Landings[0].Spine)
			assert.Equal([]string{first, second}, result.Landings[0].Source)
			assert.True(result.Coverage.Complete)
		})
	}
}
