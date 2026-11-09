package github

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	gh "github.com/google/go-github/v93/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAutomaticSyncResumesAfterLongPass(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := openTestDB(t)
		started := make(chan time.Time, 4)
		calls := 0
		client := &mockClient{listOpenPRsFn: func(context.Context, string, string) ([]*gh.PullRequest, error) {
			calls++
			started <- time.Now()
			if calls == 2 {
				time.Sleep(11 * time.Minute)
			}
			return nil, nil
		}}
		syncer := NewSyncer(
			map[string]Client{"github.com": client}, d, nil,
			[]RepoRef{{Owner: "acme", Name: "widget", PlatformHost: "github.com"}},
			5*time.Minute,
			map[string]*RateTracker{"github.com": NewRateTracker(d, "github.com", "host", "rest")}, nil,
		)
		syncer.Start(t.Context())
		defer syncer.Stop()

		first := <-started
		second := <-started
		require.Equal(t, first.Add(5*time.Minute), second)
		third := <-started
		assert.Equal(t, first.Add(16*time.Minute), third,
			"the pending tick must refresh repositories when the long pass ends")
	})
}

func TestLongSyncStillHonorsProviderThrottle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		d := openTestDB(t)
		tracker := NewRateTracker(d, "github.com", "host", "rest")
		tracker.UpdateFromRate(Rate{Limit: 5000, Remaining: 2000, Reset: time.Now().Add(time.Hour)})
		calls := 0
		client := &mockClient{listOpenPRsFn: func(context.Context, string, string) ([]*gh.PullRequest, error) {
			calls++
			if calls == 1 {
				time.Sleep(6 * time.Minute)
			}
			return nil, nil
		}}
		syncer := NewSyncer(
			map[string]Client{"github.com": client}, d, nil,
			[]RepoRef{{Owner: "acme", Name: "widget", PlatformHost: "github.com"}},
			5*time.Minute, map[string]*RateTracker{"github.com": tracker}, nil,
		)

		syncer.RunOnce(t.Context())
		assert.Equal(1, calls)
		syncer.RunOnce(t.Context())
		assert.Equal(1, calls, "half-full provider quota still requires a ten-minute cadence")
		time.Sleep(4 * time.Minute)
		syncer.RunOnce(t.Context())
		assert.Equal(2, calls, "elapsed sync work counts toward the throttled cadence")
	})
}
