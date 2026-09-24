package landedwork_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/landedwork"
)

func TestFromRootIncludesFirstCommitAndResumes(t *testing.T) {
	for _, method := range []string{"merge", "squash"} {
		t.Run(method, func(t *testing.T) {
			f := buildFixture(t, method == "squash")
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			bounds := f.bounds()
			bounds.Base, bounds.Head, bounds.FromRoot = "", f.base, true
			p, err := landedwork.Prepare(ctx, f.repo.Root, bounds, fixtureLimits())
			require := require.New(t)
			assert := assert.New(t)
			require.NoError(err)
			assert.Equal([]string{f.base}, p.Query().Commits)
			e := landedwork.Evidence{Query: p.Query(), Inventory: landedwork.Inventory{Supported: true, Complete: true}}
			first, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
			require.NoError(err)
			assert.True(first.Coverage.Complete)
			assert.Equal(bounds, first.Coverage.Bounds)
			assert.Equal(f.base, first.Coverage.CertifiedHead)
			assert.Equal([]landedwork.DirectPush{{Before: "", Terminal: f.base, Introduced: []string{f.base}}}, first.DirectPushes)

			// Resume from the certified root rather than collecting it again.
			bounds.Base, bounds.Head, bounds.FromRoot = first.Coverage.CertifiedHead, f.head, false
			p, err = landedwork.Prepare(ctx, f.repo.Root, bounds, fixtureLimits())
			require.NoError(err)
			next, err := landedwork.Analyze(ctx, p, fixtureEvidence(f, p, method), fixtureLimits())
			require.NoError(err)
			assert.True(next.Coverage.Complete)
			assert.Empty(next.DirectPushes)
			require.Len(next.Landings, 1)
			assert.Equal(f.head, next.Landings[0].Terminal)

			// A single full-history interval must produce the same two origins.
			bounds.Base, bounds.FromRoot = "", true
			p, err = landedwork.Prepare(ctx, f.repo.Root, bounds, fixtureLimits())
			require.NoError(err)
			want := []string{f.base, f.head}
			if method == "merge" {
				want = append(want, f.source...)
			}
			assert.ElementsMatch(want, p.Query().Commits)
			all, err := landedwork.Analyze(ctx, p, fixtureEvidence(f, p, method), fixtureLimits())
			require.NoError(err)
			assert.True(all.Coverage.Complete)
			assert.Equal(f.head, all.Coverage.CertifiedHead)
			assert.Equal(first.DirectPushes, all.DirectPushes)
			assert.Equal(next.Landings, all.Landings)
			assert.Empty(all.Coverage.Gaps)
		})
	}
}

func TestFromRootNeedsCompleteHistory(t *testing.T) {
	for _, mode := range []string{"inventory", "shallow", "missing root", "nodes"} {
		t.Run(mode, func(t *testing.T) {
			f := buildFixture(t, false)
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			bounds := f.bounds()
			bounds.Base, bounds.FromRoot = "", true
			p, err := landedwork.Prepare(ctx, f.repo.Root, bounds, fixtureLimits())
			require := require.New(t)
			assert := assert.New(t)
			require.NoError(err)
			require.True(p.Query().Complete)
			e := fixtureEvidence(f, p, "merge")
			limits := fixtureLimits()
			reason := "inventory_unknown"
			switch mode {
			case "inventory":
				e.Inventory.Complete = false
			case "shallow":
				require.NoError(os.WriteFile(filepath.Join(f.repo.GitDir, "shallow"), []byte(f.head+"\n"), 0o600))
				reason = "shallow_boundary"
			case "missing root":
				f.repo.Run("commit-graph", "write", "--reachable")
				require.NoError(os.Remove(filepath.Join(f.repo.GitDir, "objects", f.base[:2], f.base[2:])))
				reason = "objects_unavailable"
			case "nodes":
				limits.Nodes = 1
				reason = "input_budget_exhausted"
			}
			r, err := landedwork.Analyze(ctx, p, e, limits)
			require.NoError(err)
			assert.False(r.Coverage.Complete)
			assert.Empty(r.Coverage.CertifiedHead)
			assert.Empty(r.DirectPushes)
			require.NotEmpty(r.Coverage.Gaps)
			assert.Equal(reason, r.Coverage.Gaps[0].Reason)
			if mode == "inventory" {
				return
			}
			// Preparation must also preserve the gap, not accept an artificial root.
			p, err = landedwork.Prepare(ctx, f.repo.Root, bounds, limits)
			require.NoError(err)
			assert.False(p.Query().Complete)
			require.NotEmpty(p.Query().Gaps)
			assert.Equal(reason, p.Query().Gaps[0].Reason)
		})
	}
}

func TestFromRootMustBeExplicit(t *testing.T) {
	f := buildFixture(t, false)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	for _, bounds := range []landedwork.Bounds{
		{Repository: f.bounds().Repository, Head: f.head},
		{Repository: f.bounds().Repository, Base: f.base, Head: f.head, FromRoot: true},
		{Repository: f.bounds().Repository, FromRoot: true},
	} {
		_, err := landedwork.Prepare(ctx, f.repo.Root, bounds, fixtureLimits())
		require.Error(t, err)
	}
}
