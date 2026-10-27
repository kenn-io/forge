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
		for _, mode := range []string{"target context", "raced push", "context and raced push", "different tree"} {
			t.Run(method+"/"+mode, func(t *testing.T) {
				f := buildFixture(t, true)
				r := f.repo
				// The source revises a file the target already landed, so a
				// merge from before that landing conflicts.
				r.Checkout("-b", "target", f.base)
				landed := r.CommitFile("feature.txt", "first\n", "land feature")
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
				f.base, f.head = landed, r.Head()
				// GitHub lists commits from the change's recorded base, which can
				// be older than the commit the branch was rebased onto.
				f.source = map[string][]string{
					"target context":         {landed, revise, extend},
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
				result, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
				require.NoError(t, err)
				assert := assert.New(t)
				assert.Empty(result.DirectPushes)
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
