package landedwork_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/landedwork"
)

func TestSquashAfterUpdatingBranch(t *testing.T) {
	for _, method := range []string{"squash", "automatic"} {
		for _, mode := range []string{"complete", "omitted", "different tree", "missing source", "conflict"} {
			t.Run(method+"/"+mode, func(t *testing.T) {
				f := buildFixture(t, true)
				f.repo.Checkout("-b", "target", f.base)
				update := f.repo.CommitFile("target", "update\n", "target advances")
				f.repo.Checkout("topic")
				f.repo.Run("merge", "--no-ff", "target", "-m", "update branch")
				f.source = append(f.source, f.repo.Head())
				f.repo.Checkout("target")
				f.base = f.repo.CommitFile("later", "later\n", "target advances again")
				if mode == "conflict" {
					f.base = f.repo.CommitFile("work.txt", "target change\n", "target edits same lines")
					require.NoError(t, os.WriteFile(filepath.Join(f.repo.Root, "work.txt"), []byte("resolved\n"), 0o600))
					f.repo.Run("add", "work.txt")
				} else {
					f.repo.Run("merge", "--squash", "topic")
				}
				if mode == "different tree" {
					require.NoError(t, os.WriteFile(filepath.Join(f.repo.Root, "work.txt"), []byte("different\n"), 0o600))
					f.repo.Run("add", "work.txt")
				}
				f.repo.Run("commit", "-m", "squash updated branch")
				f.head = f.repo.Head()
				ctx, p := f.prepare(t)
				e := fixtureEvidence(f, p, "squash")
				if method == "automatic" {
					e.Candidates[0].Method = ""
					e.Candidates[0].MethodEvidence = ""
					e.Capabilities = landedwork.Capabilities{SingleParentCorrespondence: true}
				}
				if mode == "omitted" {
					e.Candidates[0].Source = e.Candidates[0].Source[1:]
				}
				if mode == "missing source" {
					require.NoError(t, os.Remove(filepath.Join(f.repo.GitDir, "objects", f.source[0][:2], f.source[0][2:])))
				}
				r, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
				require.NoError(t, err)
				assert := assert.New(t)
				assert.Empty(r.DirectPushes)
				if mode != "complete" {
					assert.Empty(r.Landings)
					assert.False(r.Coverage.Complete)
					require.Len(t, r.Coverage.Gaps, 1)
					assert.Equal(map[string]string{"omitted": "source_incomplete", "different tree": "source_correspondence_unproven", "missing source": "objects_unavailable", "conflict": "merge_conflict"}[mode], r.Coverage.Gaps[0].Reason)
					return
				}
				require.Len(t, r.Landings, 1)
				assert.Equal(landedwork.Landing{CandidateID: "7", Proofs: []string{"squash"}, Before: f.base, Terminal: f.head, Spine: []string{f.head}, Source: f.source, Introduced: []string{f.head}}, r.Landings[0])
				assert.True(r.Coverage.Complete)
				// The update from the target is context, not a second landing or PR source.
				assert.NotContains(r.Landings[0].Source, update)
			})
		}
	}
}

func TestRepeatedTextWithShiftedTarget(t *testing.T) {
	for _, method := range []string{"squash", "automatic"} {
		for _, insertion := range []bool{false, true} {
			t.Run(method+map[bool]string{false: "/removal", true: "/insertion"}[insertion], func(t *testing.T) {
				f := buildFixture(t, true)
				f.repo.Checkout("-b", "common", f.base)
				common := f.repo.CommitFile("text", "header\npadding\nold\nkeep\nold\nkeep\nend\n", "repeated text")
				f.repo.Checkout("-b", "source", common)
				content := "header\npadding\nnew\nkeep\nold\nkeep\nend\n"
				if insertion {
					content = "header\npadding\nold\nnew\nkeep\nold\nkeep\nend\n"
				}
				f.source = []string{f.repo.CommitFile("text", content, "source")}
				f.repo.Checkout("-b", "target", common)
				f.base = f.repo.CommitFile("text", "prefix\nheader\npadding\nold\nkeep\nold\nkeep\nend\n", "shift base")
				f.head = f.repo.CommitFile("text", "prefix\n"+content, "land")
				ctx, p := f.prepare(t)
				e := fixtureEvidence(f, p, "squash")
				if method == "automatic" {
					e.Candidates[0].Method = ""
					e.Candidates[0].MethodEvidence = ""
					e.Capabilities = landedwork.Capabilities{SingleParentCorrespondence: true}
				}
				r, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
				require.NoError(t, err)
				require.Len(t, r.Landings, 1)
				assert.Equal(t, []string{f.head}, r.Landings[0].Spine)
				assert.True(t, r.Coverage.Complete)
			})
		}
	}
}

func TestRepeatedTextReplayedFromDifferentAncestry(t *testing.T) {
	f := buildFixture(t, true)
	f.repo.Checkout("-b", "source-base", f.base)
	f.repo.CommitFile("text", "header\npadding\nold\nkeep\nold\nkeep\nend\n", "source base")
	f.source = []string{f.repo.CommitFile("text", "header\npadding\nold\nnew\nkeep\nold\nkeep\nend\n", "source insertion")}
	f.repo.Checkout("-b", "target", f.base)
	f.base = f.repo.CommitFile("text", "prefix\nheader\npadding\nold\nkeep\nold\nkeep\nend\n", "independent target base")
	f.head = f.repo.CommitFile("text", "prefix\nheader\npadding\nold\nnew\nkeep\nold\nkeep\nend\n", "replay insertion")
	ctx, p := f.prepare(t)
	e := fixtureEvidence(f, p, "")
	e.Candidates[0].MethodEvidence = ""
	e.Capabilities = landedwork.Capabilities{SingleParentCorrespondence: true}
	r, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
	require.NoError(t, err)
	require.Len(t, r.Landings, 1)
	assert.Equal(t, []string{"rebase", "squash"}, r.Landings[0].Proofs)
	assert.Equal(t, []string{f.head}, r.Landings[0].Spine)
	assert.True(t, r.Coverage.Complete)
}
