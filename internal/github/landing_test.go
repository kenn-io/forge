package github

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/platform"
)

type foregroundLandingProvider struct{ calls atomic.Int64 }

func (*foregroundLandingProvider) Platform() platform.Kind { return platform.KindGitLab }
func (*foregroundLandingProvider) Host() string            { return "gitlab.test" }
func (*foregroundLandingProvider) Capabilities() platform.Capabilities {
	return platform.Capabilities{}
}
func (*foregroundLandingProvider) LandingCapabilities() platform.LandingCapabilities {
	return platform.LandingCapabilities{}
}
func (p *foregroundLandingProvider) CollectLandingEvidence(context.Context, platform.RepoRef, platform.Budget) (platform.LandingSnapshot, error) {
	p.calls.Add(1)
	return platform.LandingSnapshot{Schema: platform.LandingSnapshotSchema}, nil
}

func TestLandingCancellationReleasesForegroundAdmissionWithoutProviderIO(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	provider := new(foregroundLandingProvider)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	syncer := NewSyncerWithRegistry(registry, dbtest.Open(t), nil, nil, time.Hour, nil, nil)
	t.Cleanup(syncer.Stop)
	key := RateBucketKey("gitlab", "gitlab.test", "host")
	archiveCtx, releaseArchive, allowed := syncer.tryBeginArchiveProviderRequest(t.Context(), key)
	require.True(allowed)
	t.Cleanup(releaseArchive)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	ref := platform.RepoRef{Platform: platform.KindGitLab, Host: "gitlab.test", Owner: "team-a", Name: "project-a"}
	go func() {
		_, err := syncer.CollectLandingEvidence(ctx, ref, platform.Budget{})
		done <- err
	}()
	select {
	case <-archiveCtx.Done():
	case <-ctx.Done():
		require.FailNow("foreground request did not reach admission")
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(err, context.Canceled)
	case <-time.After(10 * time.Second):
		require.FailNow("canceled caller still waits for archive")
	}
	assert.Zero(provider.calls.Load())
	releaseArchive()
	_, releaseNext, allowed := syncer.tryBeginArchiveProviderRequest(t.Context(), key)
	require.True(allowed, "canceled foreground caller must release its slot")
	releaseNext()
	_, err = syncer.CollectLandingEvidence(t.Context(), ref, platform.Budget{})
	require.NoError(err)
	assert.Equal(int64(1), provider.calls.Load())
}
