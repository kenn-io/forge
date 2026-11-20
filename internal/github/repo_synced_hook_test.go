package github

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	gh "github.com/google/go-github/v93/github"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/reposeed"
)

// TestRunOnceRunsRepoSyncedHookBeforeThePassFinishes pins that a repository's
// hook does not wait for the rest of the pass. The second repository cannot
// finish listing until the first repository's hook has run, so a hook held
// until the end of the pass deadlocks the bubble. Every repository hook must
// still finish before the end-of-pass hook, which reconciles what the drains
// saved afterward.
func TestRunOnceRunsRepoSyncedHookBeforeThePassFinishes(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		require := require.New(t)
		d := openTestDB(t)
		repos := []RepoRef{
			{Owner: "acme", Name: "first", PlatformHost: "github.com"},
			{Owner: "acme", Name: "second", PlatformHost: "github.com"},
		}
		for _, repo := range repos {
			_, err := reposeed.Seed(t.Context(), d, verifiedGitHubRepoIdentity(repo.PlatformHost, repo.Owner, repo.Name))
			require.NoError(err)
		}
		firstHookRan := make(chan struct{})
		var mu sync.Mutex
		var listed, hooked []string
		client := &mockClient{listOpenPRsFn: func(ctx context.Context, _, name string) ([]*gh.PullRequest, error) {
			mu.Lock()
			listed = append(listed, name)
			mu.Unlock()
			if name == "second" {
				select {
				case <-firstHookRan:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return nil, nil
		}}
		syncer := NewSyncer(map[string]Client{"github.com": client}, d, nil, repos, time.Minute, nil, nil)
		syncer.SetOnRepoSynced(func(results []RepoSyncResult) {
			require.Len(results, 1)
			mu.Lock()
			hooked = append(hooked, results[0].Name)
			mu.Unlock()
			if results[0].Name == "first" {
				close(firstHookRan)
			}
		})

		var hookedBeforePass []string
		syncer.SetOnSyncCompleted(func([]RepoSyncResult) {
			mu.Lock()
			defer mu.Unlock()
			hookedBeforePass = append([]string(nil), hooked...)
		})

		syncer.RunOnce(t.Context())

		mu.Lock()
		defer mu.Unlock()
		require.ElementsMatch([]string{"first", "second"}, listed)
		require.ElementsMatch([]string{"first", "second"}, hookedBeforePass,
			"every repository hook finishes before the end-of-pass hook runs")
	})
}
