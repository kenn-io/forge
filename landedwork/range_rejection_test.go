package landedwork_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/landedwork"
)

func TestRebaseMismatchOutweighsAmbiguousPair(t *testing.T) {
	for _, ambiguousFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "first", false: "last"}[ambiguousFirst], func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			f := buildFixture(t, true)
			f.repo.Checkout("-b", "common", f.base)
			f.base = f.repo.CommitFile("repeat", "same\nsame\n", "repeated lines")
			for _, branch := range []string{"source", "target"} {
				f.repo.Checkout("-b", branch, f.base)
				var commits []string
				for _, ambiguous := range []bool{ambiguousFirst, !ambiguousFirst} {
					if ambiguous {
						commits = append(commits, f.repo.CommitFile("repeat", "same\n", branch+" removes one occurrence"))
					} else {
						commits = append(commits, f.repo.CommitFile("other", branch+"\n", branch+" differs"))
					}
				}
				if branch == "source" {
					f.source = commits
				} else {
					f.head = commits[1]
				}
			}
			ctx, p := f.prepare(t)
			e := fixtureEvidence(f, p, "rebase")
			e.Capabilities.Rebase = true
			r, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
			require.NoError(err)
			require.Len(r.Coverage.Gaps, 1)
			assert.Equal("source_correspondence_unproven", r.Coverage.Gaps[0].Reason)
			assert.Empty(r.Landings)
		})
	}
}

func TestRebaseOneSidedEmptyIsMismatch(t *testing.T) {
	for _, emptySource := range []bool{false, true} {
		t.Run(map[bool]string{true: "source", false: "target"}[emptySource], func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			f := buildFixture(t, true)
			f.repo.Checkout("-b", "empty", f.base)
			f.repo.Run("commit", "--allow-empty", "-m", "empty")
			f.head = f.repo.Head()
			f.source = f.source[:1]
			if emptySource {
				f.head, f.source[0] = f.source[0], f.head
			}
			ctx, p := f.prepare(t)
			e := fixtureEvidence(f, p, "rebase")
			e.Capabilities.Rebase = true
			r, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
			require.NoError(err)
			require.Len(r.Coverage.Gaps, 1)
			assert.Equal("source_correspondence_unproven", r.Coverage.Gaps[0].Reason)
			assert.Empty(r.Landings)
		})
	}
}

func TestAutomaticSquashRejectsRangeBeforeHunks(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := buildFixture(t, true)
	f.repo.Checkout("-b", "common", f.base)
	f.base = f.repo.CommitFile("repeat", "same\nsame\n", "repeated lines")
	f.repo.Checkout("-b", "source")
	f.source = []string{f.repo.CommitFile("repeat", "same\n", "remove occurrence")}
	require.NoError(os.WriteFile(filepath.Join(f.repo.Root, "repeat"), []byte("same\nsame\n"), 0600))
	f.repo.Run("add", "repeat")
	f.source = append(f.source, f.repo.CommitFile("extra", "extra\n", "restore and add"))
	f.repo.Checkout("-b", "target", f.base)
	f.base = f.repo.CommitFile("other", "other\n", "advance")
	f.repo.Run("merge", "--squash", "source")
	f.repo.Run("commit", "-m", "squash")
	f.head = f.repo.Head()
	ctx, p := f.prepare(t)
	e := fixtureEvidence(f, p, "")
	e.Capabilities.SingleParentCorrespondence = true
	r, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
	require.NoError(err)
	require.Len(r.Landings, 1)
	assert.Equal([]string{"squash"}, r.Landings[0].Proofs)
	assert.True(r.Coverage.Complete)
}
