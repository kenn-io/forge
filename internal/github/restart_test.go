package github

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	gh "github.com/google/go-github/v92/github"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/reposeed"
)

func TestRestartKeepsCredentialCadence(t *testing.T) {
	for _, tc := range []struct {
		name           string
		sameCredential bool
		secondAge      time.Duration
		lastError      string
		manual         bool
		startupFetches []string
		polls          int
	}{
		{name: "two credentials keep polling", secondAge: 65 * time.Minute, startupFetches: []string{"second"}, polls: 2},
		{name: "future completion is ignored", secondAge: -2 * time.Minute, startupFetches: []string{"second"}},
		{name: "oldest healthy repository sets shared cadence", sameCredential: true, secondAge: 65 * time.Minute, startupFetches: []string{"first", "second"}},
		{name: "failed repository retries alone", sameCredential: true, secondAge: -2 * time.Minute, lastError: "provider unavailable", startupFetches: []string{"second"}},
		{name: "failed repository retries after scoped manual sync", sameCredential: true, secondAge: 5 * time.Minute, lastError: "provider unavailable", manual: true, startupFetches: []string{"second"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				d := openTestDB(t)
				start := time.Now().UTC()
				listed := make(chan string, 10)
				releaseManual := make(chan struct{})
				client := &mockClient{listOpenPRsFn: func(ctx context.Context, _, name string) ([]*gh.PullRequest, error) {
					listed <- name
					if name == "manual" {
						select {
						case <-releaseManual:
						case <-ctx.Done():
							return nil, ctx.Err()
						}
					}
					return nil, nil
				}}
				var repos []RepoRef
				clients := map[string]Client{}
				trackers := map[string]*RateTracker{}
				names := []string{"first", "second"}
				if tc.manual {
					names = append(names, "manual")
				}
				for i, name := range names {
					host := name + ".example.com"
					if tc.sameCredential {
						host = "github.com"
					}
					repo := RepoRef{Owner: "acme", Name: name, PlatformHost: host}
					repos = append(repos, repo)
					clients[host] = client
					trackers[host] = NewRateTracker(d, host, "host", "rest")
					id, err := reposeed.Seed(t.Context(), d, verifiedGitHubRepoIdentity(host, repo.Owner, name))
					require.NoError(t, err)
					age := 5 * time.Minute
					var lastError string
					if i == 1 {
						age, lastError = tc.secondAge, tc.lastError
					}
					require.NoError(t, d.UpdateRepoSyncCompleted(t.Context(), id, start.Add(-age), lastError))
				}
				s := NewSyncer(clients, d, nil, repos, time.Hour, trackers, nil)
				defer s.Stop()
				if tc.manual {
					require.True(t, s.TriggerRunForRepos(t.Context(), repos[2:]))
					require.Equal(t, "manual", <-listed)
				}
				s.Start(t.Context())
				synctest.Wait()
				close(releaseManual)
				synctest.Wait()
				require.Len(t, listed, len(tc.startupFetches), "startup fetches")
				var fetched []string
				for range tc.startupFetches {
					fetched = append(fetched, <-listed)
				}
				require.ElementsMatch(t, tc.startupFetches, fetched)
				for range tc.polls {
					time.Sleep(time.Hour)
					synctest.Wait()
					require.Len(t, listed, 2, "both credentials fetch at %s", time.Since(start))
					require.ElementsMatch(t, []string{"first", "second"}, []string{<-listed, <-listed})
				}
			})
		})
	}
}
