package landedwork_test

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/gitsafe"
	"go.kenn.io/forge/landedwork"
)

func TestPrepareIncludesSideAncestry(t *testing.T) {
	for _, squash := range []bool{false, true} {
		t.Run(map[bool]string{false: "merge", true: "squash"}[squash], func(t *testing.T) {
			f := buildFixture(t, squash)
			_, p := f.prepare(t)
			q := p.Query()
			want := []string{f.head}
			if !squash {
				want = append(want, f.source...)
			}
			assert := assert.New(t)
			assert.True(q.Complete)
			assert.Equal(f.bounds(), q.Bounds)
			assert.ElementsMatch(want, q.Commits)
			assert.Empty(q.Gaps)
		})
	}
}

func TestAnalyzeMissingBoundaryAfterPreparation(t *testing.T) {
	f := buildFixture(t, true)
	ctx, p := f.prepare(t)
	// Remove only a loose object from this test-owned repository after preparing.
	require.NoError(t, os.Remove(filepath.Join(f.repo.GitDir, "objects", f.base[:2], f.base[2:])))
	r, err := landedwork.Analyze(ctx, p, fixtureEvidence(f, p, "squash"), fixtureLimits())
	require := require.New(t)
	assert := assert.New(t)
	require.NoError(err)
	assert.Empty(r.Landings)
	assert.False(r.Coverage.Complete)
	assert.Equal(f.base, r.Coverage.CertifiedHead)
	assert.Equal(f.bounds(), r.Coverage.Bounds)
	require.NotEmpty(r.Coverage.Gaps)
	assert.Equal("objects_unavailable", r.Coverage.Gaps[0].Reason)
}

func TestObjectSourceIgnoresWorkingFilesAndReplacementRefs(t *testing.T) {
	f := buildFixture(t, false)
	f.repo.WriteFile("work.txt", "uncommitted\n")
	f.repo.Run("replace", f.head, f.base)
	f.repo.Run("remote", "add", "upstream", "https://unavailable.example/repository.git")
	refs, status := f.repo.Run("show-ref"), f.repo.Run("status", "--porcelain")
	ctx, p := f.prepare(t)
	r, err := landedwork.Analyze(ctx, p, fixtureEvidence(f, p, "merge"), fixtureLimits())
	require.NoError(t, err)
	assert := assert.New(t)
	assert.ElementsMatch([]string{f.source[0], f.source[1], f.head}, p.Query().Commits)
	assert.True(r.Coverage.Complete)
	assert.Equal(f.bounds(), r.Coverage.Bounds)
	assert.Equal(f.head, r.Coverage.CertifiedHead)
	assert.Empty(r.Coverage.Gaps)
	assert.Empty(r.Unattributed)
	assert.Len(r.Landings, 1)
	assert.Equal(refs, f.repo.Run("show-ref"))
	assert.Equal(status, f.repo.Run("status", "--porcelain"))
}

func TestPrepareShallowSourceDoesNotFetch(t *testing.T) {
	f := buildFixture(t, false)
	clone := filepath.Join(t.TempDir(), "shallow")
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	runner := gitsafe.Runner()
	source := (&url.URL{Scheme: "file", Path: filepath.ToSlash(f.repo.Root)}).String()
	_, _, err := runner.Run(ctx, "", nil, "clone", "--depth=1", "--no-local", source, clone)
	require := require.New(t)
	require.NoError(err)
	_, _, err = runner.Run(ctx, clone, nil, "remote", "set-url", "origin", "https://unavailable.example/repository.git")
	require.NoError(err)
	p, err := landedwork.Prepare(ctx, clone, f.bounds(), fixtureLimits())
	require.NoError(err)
	assert := assert.New(t)
	assert.False(p.Query().Complete)
	assert.Equal(f.bounds(), p.Query().Bounds)
	require.NotEmpty(p.Query().Gaps)
	// Base is missing in this depth-one clone; the gap must not be repaired by fetch.
	_, _, err = runner.Run(ctx, clone, nil, "cat-file", "-e", f.base)
	require.Error(err)
	r, err := landedwork.Analyze(ctx, p, fixtureEvidence(f, p, "merge"), fixtureLimits())
	require.NoError(err)
	assert.False(r.Coverage.Complete)
	assert.Equal(f.base, r.Coverage.CertifiedHead)
	assert.Equal(p.Query().Gaps, r.Coverage.Gaps)
	assert.Empty(r.Landings)
}

func TestPrepareGraphGaps(t *testing.T) {
	for _, name := range []string{"missing", "divergent", "side-base", "budget", "records", "bytes"} {
		t.Run(name, func(t *testing.T) {
			f := buildFixture(t, false)
			bounds, limits := f.bounds(), fixtureLimits()
			reason := "base_not_first_parent"
			switch name {
			case "missing":
				bounds.Head = strings.Repeat("a", 40)
				reason = "objects_unavailable"
			case "divergent":
				f.repo.Checkout("-b", "other", f.base)
				bounds.Base = f.repo.CommitFile("other.txt", "other\n", "other")
			case "side-base":
				bounds.Base = f.source[1]
			case "budget":
				limits.Nodes = 1
				reason = "input_budget_exhausted"
			case "records":
				limits.Records = 1
				reason = "input_budget_exhausted"
			case "bytes":
				limits.InputBytes = 1
				reason = "input_budget_exhausted"
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			p, err := landedwork.Prepare(ctx, f.repo.Root, bounds, limits)
			require := require.New(t)
			assert := assert.New(t)
			require.NoError(err)
			assert.False(p.Query().Complete)
			require.NotEmpty(p.Query().Gaps)
			assert.Equal(reason, p.Query().Gaps[0].Reason)
		})
	}
}

func TestPrepareCanceled(t *testing.T) {
	f := buildFixture(t, false)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	cancel()
	_, err := landedwork.Prepare(ctx, f.repo.Root, f.bounds(), fixtureLimits())
	require.ErrorIs(t, err, context.Canceled)
}
