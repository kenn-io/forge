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
		for _, mode := range []string{"complete", "omitted", "different tree", "missing source", "conflict", "partly present"} {
			t.Run(method+"/"+mode, func(t *testing.T) {
				f := buildFixture(t, true)
				f.repo.Checkout("-b", "target", f.base)
				update := f.repo.CommitFile("target", "update\n", "target advances")
				f.repo.Checkout("topic")
				f.repo.Run("merge", "--no-ff", "target", "-m", "update branch")
				f.source = append(f.source, f.repo.Head())
				f.repo.Checkout("target")
				f.base = f.repo.CommitFile("later", "later\n", "target advances again")
				if mode == "partly present" {
					// The first source edit is present under a different commit ID.
					// Squashing now applies only the second edit in the same file.
					f.repo.Run("cherry-pick", f.source[0])
					f.base = f.repo.Head()
					require.NotEqual(t, f.source[0], f.base)
				}
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
					assert.Equal(f.base, r.Coverage.CertifiedHead)
					require.Len(t, r.Coverage.Gaps, 1)
					assert.Equal(map[string]string{"omitted": "source_incomplete", "different tree": "source_correspondence_unproven", "missing source": "objects_unavailable", "conflict": "merge_conflict", "partly present": "source_correspondence_unproven"}[mode], r.Coverage.Gaps[0].Reason)
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

func TestSquashWithMultipleMergeBasesRemainsUnresolved(t *testing.T) {
	for _, method := range []string{"squash", "automatic"} {
		t.Run(method, func(t *testing.T) {
			f := buildFixture(t, true)
			f.repo.Checkout("-b", "target", f.base)
			f.repo.CommitFile("target", "update\n", "target advances")
			f.repo.Checkout("topic")
			f.repo.Run("merge", "--no-ff", "target", "-m", "update branch")
			f.source = append(f.source, f.repo.Head())
			f.source = append(f.source, f.repo.CommitFile("extra", "extra\n", "source advances"))
			f.repo.Checkout("target")
			// Both branches merge the other's earlier tip independently. Neither
			// of those tips is the unique boundary for the remaining source edits.
			f.repo.Run("merge", "--no-ff", f.source[1], "-m", "integrate earlier source")
			f.base = f.repo.Head()
			f.repo.Run("merge", "--squash", "topic")
			f.repo.Run("commit", "-m", "squash remaining source")
			f.head = f.repo.Head()
			ctx, p := f.prepare(t)
			e := fixtureEvidence(f, p, "squash")
			if method == "automatic" {
				e.Candidates[0].Method = ""
				e.Candidates[0].MethodEvidence = ""
				e.Capabilities = landedwork.Capabilities{SingleParentCorrespondence: true}
			}
			r, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
			require.NoError(t, err)
			assert := assert.New(t)
			assert.Empty(r.Landings)
			assert.Empty(r.DirectPushes)
			assert.False(r.Coverage.Complete)
			assert.Equal(f.base, r.Coverage.CertifiedHead)
			require.Len(t, r.Coverage.Gaps, 1)
			assert.Equal("merge_base_unproven", r.Coverage.Gaps[0].Reason)
		})
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

// squashCandidate describes a squash landing declared by the provider. Its
// terminal is not one of its source commits: the landing created it.
func squashCandidate(f fixture, id, terminal string, source ...string) landedwork.Candidate {
	return landedwork.Candidate{
		Repository: f.bounds().Repository, ID: id, Terminal: terminal,
		SourceHead: source[len(source)-1], Source: source, SourceComplete: true,
		TerminalEvidence: "fixture-terminal",
	}
}

func TestSquashOfAlreadyPresentEdits(t *testing.T) {
	type landing struct {
		id, terminal string
		proofs       []string
	}
	for _, method := range []string{"squash", "automatic"} {
		for _, mode := range []string{"cancelled", "landed by another", "pushed directly", "stacked", "partly pushed directly", "replayed parent", "replayed merge", "empty boundary"} {
			if method == "automatic" && mode == "partly pushed directly" {
				// The automatic rewritten-range alternative explains this shape.
				continue
			}
			t.Run(method+"/"+mode, func(t *testing.T) {
				f := buildFixture(t, true)
				r := f.repo
				r.Checkout("-b", "target", f.base)
				var candidates []landedwork.Candidate
				var want []landing
				var direct []string
				reason := ""
				switch mode {
				case "cancelled":
					// The source edits cancel out, so the squash has no edits.
					r.Checkout("-b", "other", f.base)
					other := r.CommitFile("other.txt", "other\n", "other change")
					r.Checkout("-b", "cancel", f.base)
					first := r.CommitFile("work.txt", "temporary\nkeep\n", "try")
					last := r.CommitFile("work.txt", "old\nkeep\n", "undo")
					r.Checkout("target")
					r.Run("merge", "--squash", "other")
					r.Run("commit", "-m", "squash other")
					previous := r.Head()
					r.Run("merge", "--squash", "cancel")
					r.Run("commit", "--allow-empty", "-m", "squash cancelled")
					candidates = []landedwork.Candidate{
						squashCandidate(f, "8", previous, other),
						squashCandidate(f, "7", r.Head(), first, last),
					}
					want = []landing{{"8", previous, nil}, {"7", r.Head(), []string{"squash"}}}
				case "landed by another", "pushed directly":
					// Another landing already contains the source edit by the
					// time the branch is updated and squashed.
					r.Checkout("-b", "fix", f.base)
					fix := r.CommitFile("work.txt", "new\nkeep\n", "fix")
					r.Checkout("target")
					if mode == "pushed directly" {
						direct = append(direct, r.CommitFile("work.txt", "new\nkeep\n", "same fix pushed"))
					} else {
						r.Checkout("-b", "other", f.base)
						same := r.CommitFile("work.txt", "new\nkeep\n", "same fix")
						extra := r.CommitFile("other.txt", "other\n", "other change")
						r.Checkout("target")
						r.Run("merge", "--squash", "other")
						r.Run("commit", "-m", "squash other")
						candidates = append(candidates, squashCandidate(f, "8", r.Head(), same, extra))
						want = append(want, landing{"8", r.Head(), nil})
					}
					r.Checkout("fix")
					r.Run("merge", "--no-ff", "target", "-m", "update branch")
					update := r.Head()
					r.Checkout("target")
					r.Run("merge", "--squash", "fix")
					r.Run("commit", "--allow-empty", "-m", "squash fix")
					candidates = append(candidates, squashCandidate(f, "7", r.Head(), fix, update))
					want = append(want, landing{"7", r.Head(), []string{"squash"}})
					// In "pushed directly", the pushed commit replays the first
					// source commit, but the branch update merged it into the
					// source, so this change's landing did not create it.
				case "replayed parent":
					// The parent replays the first source commit and the empty
					// terminal could replay the second, so the pair could be a
					// rebase of the source rather than a squash. A rebase may
					// drop the second commit's edits only if it was empty.
					r.Checkout("-b", "undo", f.base)
					edit := r.CommitFile("work.txt", "new\nkeep\n", "edit")
					undo := r.CommitFile("work.txt", "old\nkeep\n", "undo")
					r.Checkout("target")
					direct = append(direct, r.CommitFile("work.txt", "new\nkeep\n", "edit pushed"))
					r.Run("merge", "--squash", "undo")
					r.Run("commit", "--allow-empty", "-m", "squash undo")
					candidates = append(candidates, squashCandidate(f, "7", r.Head(), edit, undo))
					reason = "empty_tree_edits"
				case "replayed merge":
					// The second source commit merges a target update and adds
					// its own edit. The target holds a replay of the first commit
					// and a first-parent pick of the merge, so the empty terminal
					// could end a rewritten range over both.
					update := r.CommitFile("other.txt", "update\n", "target update")
					r.Checkout("-b", "work", f.base)
					edit := r.CommitFile("work.txt", "new\nkeep\n", "edit")
					r.Run("merge", "--no-ff", "--no-commit", update)
					merge := r.CommitFile("extra.txt", "extra\n", "update branch with extra")
					r.Checkout("target")
					r.Run("cherry-pick", edit)
					r.Run("cherry-pick", "-m", "1", merge)
					r.Run("merge", "--squash", "work")
					r.Run("commit", "--allow-empty", "-m", "squash work")
					candidates = append(candidates, squashCandidate(f, "7", r.Head(), edit, merge))
					reason = "source_correspondence_unproven"
				case "empty boundary":
					// The source starts at an empty target commit, which would
					// replay any source commit. The source was written on top of
					// it, so it is not a commit this change created by landing.
					r.Run("commit", "--allow-empty", "-m", "empty push")
					boundary := r.Head()
					direct = append(direct, boundary)
					r.Checkout("-b", "undo", boundary)
					edit := r.CommitFile("work.txt", "new\nkeep\n", "edit")
					undo := r.CommitFile("work.txt", "old\nkeep\n", "undo")
					r.Checkout("target")
					r.Run("merge", "--squash", "undo")
					r.Run("commit", "--allow-empty", "-m", "squash undo")
					candidates = append(candidates, squashCandidate(f, "7", r.Head(), edit, undo))
					want = append(want, landing{"7", r.Head(), []string{"squash"}})
				case "stacked", "partly pushed directly":
					// The second change was stacked on the first, so its source
					// repeats the first change's commit.
					r.Checkout("-b", "first", f.base)
					first := r.CommitFile("work.txt", "new\nkeep\n", "first")
					r.Checkout("-b", "second", first)
					second := r.CommitFile("other.txt", "other\n", "second")
					r.Checkout("target")
					if mode == "stacked" {
						r.Run("merge", "--squash", "first")
						r.Run("commit", "-m", "squash first")
						candidates = append(candidates, squashCandidate(f, "8", r.Head(), first))
						want = append(want, landing{"8", r.Head(), nil})
					} else {
						direct = append(direct, r.CommitFile("work.txt", "new\nkeep\n", "first pushed"))
						reason = "source_correspondence_unproven"
					}
					r.Run("merge", "--squash", "second")
					r.Run("commit", "-m", "squash second")
					candidates = append(candidates, squashCandidate(f, "7", r.Head(), first, second))
					want = append(want, landing{"7", r.Head(), []string{"squash"}})
				}
				f.head = r.Head()
				ctx, p := f.prepare(t)
				e := fixtureEvidence(f, p, "")
				e.Candidates = candidates
				for i := range e.Candidates {
					if method == "squash" {
						e.Candidates[i].Method, e.Candidates[i].MethodEvidence = "squash", "fixture-method"
					}
				}
				if method == "automatic" {
					e.Capabilities = landedwork.Capabilities{SingleParentCorrespondence: true}
				}
				res, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
				require.NoError(t, err)
				assert := assert.New(t)
				if reason != "" {
					assert.Empty(res.Landings)
					assert.False(res.Coverage.Complete)
					require.Len(t, res.Coverage.Gaps, 1)
					assert.Equal(reason, res.Coverage.Gaps[0].Reason)
					assert.Empty(res.DirectPushes)
					return
				}
				assert.Empty(res.Coverage.Gaps)
				require.Len(t, res.Landings, len(want))
				for i, l := range want {
					assert.Equal(l.id, res.Landings[i].CandidateID)
					assert.Equal([]string{l.terminal}, res.Landings[i].Spine)
					if l.proofs != nil {
						assert.Equal(l.proofs, res.Landings[i].Proofs)
					}
				}
				assert.Len(res.DirectPushes, len(direct))
				assert.True(res.Coverage.Complete)
				assert.Equal(f.head, res.Coverage.CertifiedHead)
			})
		}
	}
}
