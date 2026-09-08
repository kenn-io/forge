package landedwork_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/gitsafe"
	"go.kenn.io/forge/landedwork"
	"go.kenn.io/forge/platform"
	gittest "go.kenn.io/kit/git/test"
)

func TestMain(m *testing.M) { os.Exit(gitsafe.RunIsolatedMain(m)) }

type fixture struct {
	repo       *gittest.Repo
	base, head string
	source     []string
}

// Scripted edits deliberately have a hand-counted net diff of +3/-1.
func buildFixture(t *testing.T, squash bool) fixture {
	t.Helper()
	r := gittest.NewRepo(t, gittest.Options{InitArgs: []string{"init", "-b", "main"}, ConfigureUser: true})
	r.Runner = gitsafe.Runner()
	base := r.CommitFile("work.txt", "old\nkeep\n", "base")
	r.Checkout("-b", "topic")
	a := r.CommitFile("work.txt", "new\nkeep\n", "replace")
	b := r.CommitFile("work.txt", "new\nkeep\none\ntwo\n", "append")
	r.Checkout("main")
	if squash {
		r.Run("merge", "--squash", "topic")
		r.Run("commit", "-m", "squash")
	} else {
		r.Run("merge", "--no-ff", "topic", "-m", "merge")
	}
	return fixture{r, base, r.Head(), []string{a, b}}
}

func fixtureLimits() landedwork.Limits {
	return landedwork.Limits{Records: 1000, Nodes: 1000, InputBytes: 1 << 20, OutputBytes: 1 << 20}
}

func (f fixture) bounds() landedwork.Bounds {
	return landedwork.Bounds{Repository: landedwork.Repository{Provider: platform.KindGitHub, Host: "github.com", ID: "101"}, Base: f.base, Head: f.head}
}

func (f fixture) prepare(t *testing.T) (context.Context, *landedwork.Interval) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)
	p, err := landedwork.Prepare(ctx, f.repo.Root, f.bounds(), fixtureLimits())
	require.NoError(t, err)
	return ctx, p
}

func buildSideFixture(t *testing.T) fixture {
	t.Helper()
	r := gittest.NewRepo(t, gittest.Options{InitArgs: []string{"init", "-b", "main"}, ConfigureUser: true})
	r.Runner = gitsafe.Runner()
	r.CommitFile("work.txt", "old\nkeep\n", "base")
	r.Checkout("-b", "topic")
	a := r.CommitFile("work.txt", "new\nkeep\n", "replace")
	r.Checkout("main")
	base := r.CommitFile("other.txt", "already present\n", "main advances")
	r.Checkout("topic")
	r.Run("merge", "--no-ff", "main", "-m", "merge main")
	internalMerge := r.Head()
	b := r.CommitFile("work.txt", "new\nkeep\none\ntwo\n", "append")
	r.Checkout("main")
	r.Run("merge", "--no-ff", "topic", "-m", "merge topic")
	return fixture{r, base, r.Head(), []string{a, base, internalMerge, b}}
}
