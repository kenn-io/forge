package landedwork_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/internal/testutil/gitsafe"
	"go.kenn.io/forge/landedwork"
	gittest "go.kenn.io/kit/git/test"
)

// stackedFixture squashes a two-change stack onto main. The child branch
// starts on the parent branch, merges the parent's later commit, and then edits
// the parent's file again. The child's list omits the parent's commits, and a
// plain three-way merge from main conflicts on that file, so only the child's
// reported base, the parent's head, proves the child's squash.
type stackedFixture struct {
	fixture
	sourceBase string
}

func buildStackedFixture(t *testing.T, extra bool) stackedFixture {
	t.Helper()
	r := gittest.NewRepo(t, gittest.Options{Dir: gitfixture.ScratchDir(t), InitArgs: []string{"init", "-b", "main"}, ConfigureUser: true})
	r.Runner = gitsafe.Runner()
	r.CommitFile("shared.txt", "start\n", "base")
	r.Checkout("-b", "parent")
	r.CommitFile("shared.txt", "parent one\n", "parent edit")
	r.Checkout("-b", "child")
	child := []string{r.CommitFile("child.txt", "child\n", "child edit")}
	r.Checkout("parent")
	parentHead := r.CommitFile("shared.txt", "parent two\n", "parent edit again")
	r.Checkout("child")
	r.Run("merge", "--no-ff", "parent", "-m", "merge parent")
	child = append(child, r.Head(), r.CommitFile("shared.txt", "child over parent\n", "child edits shared file"))
	r.Checkout("main")
	r.Run("merge", "--squash", "parent")
	r.Run("commit", "-m", "squash parent")
	before := r.Head()
	// The provider squashes the child's files onto the parent's squash.
	r.Run("checkout", child[len(child)-1], "--", "shared.txt", "child.txt")
	if extra {
		require.NoError(t, os.WriteFile(filepath.Join(r.Root, "extra.txt"), []byte("not in the change\n"), 0o600))
		r.Run("add", "extra.txt")
	}
	r.Run("commit", "-m", "squash child")
	return stackedFixture{fixture{r, before, r.Head(), child}, parentHead}
}

// pushAfterMergeFixture squashes a branch that merged main, then pushes one more
// commit to the branch, so the reported head is past the squashed commit.
func buildPushAfterMergeFixture(t *testing.T, extra bool) fixture {
	t.Helper()
	r := gittest.NewRepo(t, gittest.Options{Dir: gitfixture.ScratchDir(t), InitArgs: []string{"init", "-b", "main"}, ConfigureUser: true})
	r.Runner = gitsafe.Runner()
	r.CommitFile("work.txt", "start\n", "base")
	r.Checkout("-b", "topic")
	source := []string{r.CommitFile("work.txt", "topic\n", "topic edit")}
	r.Checkout("main")
	r.CommitFile("main.txt", "main\n", "main advances")
	r.Checkout("topic")
	r.Run("merge", "--no-ff", "main", "-m", "merge main")
	source = append(source, r.Head(), r.CommitFile("topic.txt", "topic\n", "topic adds file"))
	r.Checkout("main")
	before := r.CommitFile("later.txt", "later\n", "main advances again")
	r.Run("merge", "--squash", "topic")
	if extra {
		require.NoError(t, os.WriteFile(filepath.Join(r.Root, "extra.txt"), []byte("not in the change\n"), 0o600))
		r.Run("add", "extra.txt")
	}
	r.Run("commit", "-m", "squash topic")
	head := r.Head()
	r.Checkout("topic")
	source = append(source, r.CommitFile("late.txt", "late\n", "push after the merge"))
	return fixture{r, before, head, source}
}

func contentEvidence(f fixture, p *landedwork.Interval, method, sourceBase string) landedwork.Evidence {
	e := automaticEvidence(f, p, method)
	e.Candidates[0].SourceBase = sourceBase
	return e
}

func contentLanding(f fixture) landedwork.Landing {
	return landedwork.Landing{
		CandidateID: "7", Proofs: []string{"squash"}, Before: f.base, Terminal: f.head,
		Spine: []string{f.head}, Source: f.source, Introduced: []string{f.head},
	}
}

func TestStackedSquashProvedByContent(t *testing.T) {
	for _, method := range []string{"squash", ""} {
		for _, tc := range []struct {
			name         string
			base, reason string
			extra        bool
		}{
			{name: "reported base", base: "parent"},
			// The plain merge from main conflicts on the shared file.
			{name: "no reported base", reason: "source_incomplete"},
			{name: "reported base unavailable", base: strings.Repeat("e", 40), reason: "source_incomplete"},
			{name: "extra file change", base: "parent", reason: "source_incomplete", extra: true},
		} {
			t.Run(map[string]string{"squash": "squash", "": "automatic"}[method]+"/"+tc.name, func(t *testing.T) {
				f := buildStackedFixture(t, tc.extra)
				base := tc.base
				if base == "parent" {
					base = f.sourceBase
				}
				ctx, p := f.prepare(t)
				r, err := landedwork.Analyze(ctx, p, contentEvidence(f.fixture, p, method, base), fixtureLimits())
				require.NoError(t, err)
				assert := assert.New(t)
				assert.Empty(r.DirectPushes)
				if tc.reason != "" {
					assert.Empty(r.Landings)
					require.Len(t, r.Coverage.Gaps, 1)
					assert.Equal(tc.reason, r.Coverage.Gaps[0].Reason)
					return
				}
				assert.Equal([]landedwork.Landing{contentLanding(f.fixture)}, r.Landings)
				assert.True(r.Coverage.Complete)
			})
		}
	}
}

func TestPushAfterMergeSquashProvedByContent(t *testing.T) {
	for _, method := range []string{"squash", ""} {
		for _, tc := range []struct {
			name   string
			base   string
			extra  bool
			reason string
		}{
			{name: "no reported base"},
			{name: "reported base unavailable", base: strings.Repeat("e", 40)},
			{name: "extra file change", extra: true, reason: "source_correspondence_unproven"},
		} {
			t.Run(map[string]string{"squash": "squash", "": "automatic"}[method]+"/"+tc.name, func(t *testing.T) {
				f := buildPushAfterMergeFixture(t, tc.extra)
				ctx, p := f.prepare(t)
				r, err := landedwork.Analyze(ctx, p, contentEvidence(f, p, method, tc.base), fixtureLimits())
				require.NoError(t, err)
				assert := assert.New(t)
				assert.Empty(r.DirectPushes)
				if tc.reason != "" {
					assert.Empty(r.Landings)
					require.Len(t, r.Coverage.Gaps, 1)
					assert.Equal(tc.reason, r.Coverage.Gaps[0].Reason)
					return
				}
				assert.Equal([]landedwork.Landing{contentLanding(f)}, r.Landings)
				assert.True(r.Coverage.Complete)
			})
		}
	}
}

// A branch of two commits that merged main is rebased onto main as two
// commits, and the list omits the first commit. The second rebased commit holds
// the files of the whole branch, but it is the last commit of a rebase whose
// first commit replays the omitted one, so it is not credited as a squash.
func TestRebaseOfOmittedCommitIsNotContentSquash(t *testing.T) {
	r := gittest.NewRepo(t, gittest.Options{Dir: gitfixture.ScratchDir(t), InitArgs: []string{"init", "-b", "main"}, ConfigureUser: true})
	r.Runner = gitsafe.Runner()
	r.CommitFile("work.txt", "start\n", "base")
	r.Checkout("-b", "topic")
	first := r.CommitFile("a.txt", "a\n", "first")
	second := r.CommitFile("b.txt", "b\n", "second")
	r.Checkout("main")
	base := r.CommitFile("main.txt", "main\n", "main advances")
	r.Checkout("topic")
	r.Run("merge", "--no-ff", "main", "-m", "merge main")
	merge := r.Head()
	r.Checkout("main")
	r.Run("cherry-pick", first)
	r.Run("cherry-pick", second)
	f := fixture{r, base, r.Head(), []string{second, merge}}
	for _, method := range []string{"squash", ""} {
		t.Run(map[string]string{"squash": "squash", "": "automatic"}[method], func(t *testing.T) {
			ctx, p := f.prepare(t)
			r, err := landedwork.Analyze(ctx, p, contentEvidence(f, p, method, ""), fixtureLimits())
			require.NoError(t, err)
			assert.Empty(t, r.Landings)
			assert.Empty(t, r.DirectPushes)
			assert.False(t, r.Coverage.Complete)
		})
	}
}
