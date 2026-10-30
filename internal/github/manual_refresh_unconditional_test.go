package github

import (
	"context"
	"sync"
	"testing"
	"time"

	gh "github.com/google/go-github/v93/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
	platformgithub "go.kenn.io/forge/platform/github"
)

func TestManualRefreshReadsCIUnconditionally(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()

	var mu sync.Mutex
	var unconditional []bool
	client := &mockClient{
		getPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			return buildOpenPR(number, time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)), nil
		},
		listCheckRunsForRefFn: func(ctx context.Context, _, _, _ string) ([]*gh.CheckRun, error) {
			mu.Lock()
			defer mu.Unlock()
			unconditional = append(unconditional, platformgithub.UnconditionalRead(ctx))
			return nil, nil
		},
	}
	syncer := NewSyncer(map[string]Client{"github.com": client}, openTestDB(t), nil,
		[]RepoRef{{Owner: "acme", Name: "app", PlatformHost: "github.com"}}, time.Hour, nil, nil)

	// A manual refresh forces fresh CI past any cached validator; a
	// background poll may revalidate.
	require.NoError(syncer.SyncMROnProvider(ctx, platform.KindGitHub, "github.com", "acme", "app", 7))
	require.NoError(syncer.SyncMROnProviderIfChanged(
		WithSyncBudget(ctx), platform.KindGitHub, "github.com", "acme", "app", 7,
	))
	mu.Lock()
	defer mu.Unlock()
	require.Len(unconditional, 2)
	assert.True(t, unconditional[0], "manual refresh")
	assert.False(t, unconditional[1], "background poll")
}
