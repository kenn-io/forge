package landedwork_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/gitsafe"
	"go.kenn.io/forge/landedwork"
	gittest "go.kenn.io/kit/git/test"
)

// newOverlapRepo seeds two files. Each case branches a source from the seed
// and lands it on a target whose squash parent already carries some of the
// same edits, as when other work landed first.
func newOverlapRepo(t *testing.T) (*gittest.Repo, string) {
	t.Helper()
	r := gittest.NewRepo(t, gittest.Options{InitArgs: []string{"init", "-b", "main"}, ConfigureUser: true})
	r.Runner = gitsafe.Runner()
	r.CommitFile("a.txt", "a1\na2\na3\n", "seed a")
	return r, r.CommitFile("b.txt", "b1\nb2\nb3\n", "seed b")
}

func squashOnto(t *testing.T, f *fixture, target string) {
	t.Helper()
	f.repo.Checkout("-B", "target", target)
	f.base = target
	f.repo.Run("merge", "--squash", "topic")
	f.repo.Run("commit", "-m", "squash topic")
	f.head = f.repo.Head()
}

func automaticEvidence(f fixture, p *landedwork.Interval, method string) landedwork.Evidence {
	e := fixtureEvidence(f, p, method)
	if method == "" {
		e.Candidates[0].MethodEvidence = ""
		e.Capabilities = landedwork.Capabilities{SingleParentCorrespondence: true}
	}
	return e
}

// Another change on the target already made the first source edit to a.txt,
// alongside unrelated work. The squash adds only the rest.
func independentOverlap(t *testing.T) fixture {
	r, start := newOverlapRepo(t)
	f := fixture{repo: r}
	r.Checkout("-b", "topic", start)
	f.source = []string{
		r.CommitFile("a.txt", "a1\nA2\na3\n", "edit a"),
		r.CommitFile("b.txt", "b1\nB2\nb3\n", "edit b"),
		r.CommitFile("b.txt", "b1\nB2\nB3\n", "edit b again"),
	}
	r.Checkout("-b", "other", start)
	r.CommitFile("a.txt", "a1\nA2\na3\n", "same edit to a")
	r.CommitFile("c.txt", "c\n", "unrelated work")
	r.Checkout("main")
	r.Run("merge", "--squash", "other")
	r.Run("commit", "-m", "squash other")
	squashOnto(t, &f, r.Head())
	return f
}

func TestSquashWithEditsAlreadyOnTarget(t *testing.T) {
	shapes := map[string]func(t *testing.T) fixture{
		"independent overlap": independentOverlap,
		// A one-commit source owns only the squash commit under every method.
		"single commit": func(t *testing.T) fixture {
			r, start := newOverlapRepo(t)
			f := fixture{repo: r}
			r.Checkout("-b", "topic", start)
			r.WriteFile("a.txt", "a1\nA2\na3\n")
			r.WriteFile("b.txt", "b1\nB2\nb3\n")
			r.Run("add", "a.txt", "b.txt")
			r.Run("commit", "-m", "edit a and b")
			f.source = []string{r.Head()}
			r.Checkout("main")
			r.CommitFile("a.txt", "a1\nA2\na3\n", "same edit to a")
			squashOnto(t, &f, r.CommitFile("c.txt", "c\n", "unrelated work"))
			return f
		},
		// The target carries the later source edit as its own commit. A
		// rebase replays source commits in order, so it cannot produce an
		// earlier source edit after a later one.
		"later edit landed first": func(t *testing.T) fixture {
			r, start := newOverlapRepo(t)
			f := fixture{repo: r}
			r.Checkout("-b", "topic", start)
			f.source = []string{
				r.CommitFile("a.txt", "a1\nA2\na3\n", "edit a"),
				r.CommitFile("b.txt", "b1\nB2\nb3\n", "edit b"),
			}
			r.Checkout("main")
			squashOnto(t, &f, r.CommitFile("b.txt", "b1\nB2\nb3\n", "same edit to b"))
			return f
		},
	}
	for name, build := range shapes {
		for _, method := range []string{"squash", ""} {
			t.Run(name+"/"+map[string]string{"squash": "squash", "": "automatic"}[method], func(t *testing.T) {
				f := build(t)
				require.Len(t, strings.Fields(f.repo.Run("rev-list", "--parents", "-n", "1", f.head)), 2)
				ctx, p := f.prepare(t)
				r, err := landedwork.Analyze(ctx, p, automaticEvidence(f, p, method), fixtureLimits())
				require.NoError(t, err)
				assert := assert.New(t)
				assert.Empty(r.Coverage.Gaps)
				require.Len(t, r.Landings, 1)
				assert.Equal(landedwork.Landing{
					CandidateID: "7", Proofs: []string{"squash"}, Before: f.base, Terminal: f.head,
					Spine: []string{f.head}, Source: f.source, Introduced: []string{f.head},
				}, r.Landings[0])
				assert.True(r.Coverage.Complete)
			})
		}
	}
}

// When the squash parent and the squash itself each replay a source commit in
// source order, the target is indistinguishable from a rebase that owns both.
// Neither method may claim only the squash commit. Automatic resolution finds
// that rebase, which starts before the interval.
func TestSquashAfterRebaseShapedTargetRemainsUnresolved(t *testing.T) {
	for _, method := range []string{"squash", ""} {
		t.Run(map[string]string{"squash": "squash", "": "automatic"}[method], func(t *testing.T) {
			r, start := newOverlapRepo(t)
			f := fixture{repo: r}
			r.Checkout("-b", "topic", start)
			f.source = []string{
				r.CommitFile("a.txt", "a1\nA2\na3\n", "edit a"),
				r.CommitFile("b.txt", "b1\nB2\nb3\n", "edit b"),
			}
			r.Checkout("main")
			r.CommitFile("c.txt", "c\n", "target advances")
			r.Run("cherry-pick", f.source[0])
			squashOnto(t, &f, r.Head())
			ctx, p := f.prepare(t)
			res, err := landedwork.Analyze(ctx, p, automaticEvidence(f, p, method), fixtureLimits())
			require.NoError(t, err)
			assert.Empty(t, res.Landings)
			assert.False(t, res.Coverage.Complete)
			require.Len(t, res.Coverage.Gaps, 1)
			want := map[string]string{"squash": "source_correspondence_unproven", "": "range_crosses_base"}[method]
			assert.Equal(t, want, res.Coverage.Gaps[0].Reason)
		})
	}
}

// A squash witness must reproduce the landed tree exactly; extra target edits
// are not explained by the source.
func TestSquashWithEditsAlreadyOnTargetRejectsExtraEdits(t *testing.T) {
	r, start := newOverlapRepo(t)
	f := fixture{repo: r}
	r.Checkout("-b", "topic", start)
	f.source = []string{
		r.CommitFile("a.txt", "a1\nA2\na3\n", "edit a"),
		r.CommitFile("b.txt", "b1\nB2\nb3\n", "edit b"),
	}
	r.Checkout("main")
	f.base = r.CommitFile("a.txt", "a1\nA2\na3\n", "same edit to a")
	r.WriteFile("b.txt", "b1\nB2\nb3\nextra\n")
	r.Run("add", "b.txt")
	r.Run("commit", "-m", "squash with an extra edit")
	f.head = r.Head()
	ctx, p := f.prepare(t)
	res, err := landedwork.Analyze(ctx, p, automaticEvidence(f, p, ""), fixtureLimits())
	require.NoError(t, err)
	assert.Empty(t, res.Landings)
	require.Len(t, res.Coverage.Gaps, 1)
	assert.Equal(t, "source_correspondence_unproven", res.Coverage.Gaps[0].Reason)
}
