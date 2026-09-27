package ratelimit

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	platformpkg "go.kenn.io/forge/platform"
)

func TestUnknownQuotaCooldownBacksOffAndExpires(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	rt := NewPlatformRateTracker(openTestDB(t), "bitbucket", "bitbucket.org", "host", "rest")
	rt.UpdateFromRate(platformpkg.Rate{Remaining: 0, Limit: -1})
	paused, wait := rt.ShouldBackoff()
	assert.True(paused)
	assert.Positive(wait)
	assert.True(rt.IsPaused())
	require.NotNil(rt.RetryAt())
	assert.WithinDuration(time.Now().Add(time.Minute), *rt.RetryAt(), time.Second)
	assert.Nil(rt.ResetAt(), "local cooldown is not provider reset data")

	rt.UpdateFromRate(platformpkg.Rate{Remaining: 0, Limit: -1})
	assert.WithinDuration(time.Now().Add(2*time.Minute), *rt.RetryAt(), time.Second)
	for range 4 {
		rt.UpdateFromRate(platformpkg.Rate{Remaining: 0, Limit: -1})
	}
	assert.WithinDuration(time.Now().Add(5*time.Minute), *rt.RetryAt(), time.Second)

	rt.mu.Lock()
	rt.localRetryAt = time.Now().Add(-time.Second)
	rt.mu.Unlock()
	paused, wait = rt.ShouldBackoff()
	assert.False(paused)
	assert.Zero(wait)
	assert.False(rt.IsPaused())
	assert.Nil(rt.RetryAt())
	assert.Zero(rt.Remaining(), "cooldown expiry does not invent remaining quota")

	rt.UpdateFromRate(platformpkg.Rate{Remaining: -1, Limit: -1})
	assert.False(rt.IsPaused())
	rt.UpdateFromRate(platformpkg.Rate{Remaining: 0, Limit: -1})
	require.NotNil(rt.RetryAt())
	assert.WithinDuration(time.Now().Add(time.Minute), *rt.RetryAt(), time.Second, "successful unknown response resets the schedule")
}

func TestUnknownQuotaCooldownClearsOnKnownObservation(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	rt := NewPlatformRateTracker(openTestDB(t), "bitbucket", "bitbucket.org", "host", "rest")
	rt.UpdateFromRate(platformpkg.Rate{Remaining: 0, Limit: -1})
	reset := time.Now().UTC().Add(time.Hour)
	rt.UpdateFromRate(platformpkg.Rate{Remaining: 500, Limit: 1000, Reset: reset})
	paused, wait := rt.ShouldBackoff()
	assert.False(paused)
	assert.Zero(wait)
	assert.False(rt.IsPaused())
	require.NotNil(rt.RetryAt())
	assert.Equal(reset, *rt.RetryAt())
	rt.UpdateFromRate(platformpkg.Rate{Remaining: 0, Limit: -1})
	require.NotNil(rt.RetryAt())
	assert.WithinDuration(time.Now().Add(time.Minute), *rt.RetryAt(), time.Second)
}

func TestHydratedUnknownQuotaGetsOneBoundedCooldown(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	original := NewPlatformRateTracker(d, "bitbucket", "bitbucket.org", "host", "rest")
	original.UpdateFromRate(platformpkg.Rate{Remaining: 0, Limit: -1})
	original.UpdateFromRate(platformpkg.Rate{Remaining: 0, Limit: -1})
	reopened := NewPlatformRateTracker(d, "bitbucket", "bitbucket.org", "host", "rest")
	paused, wait := reopened.ShouldBackoff()
	assert.True(paused)
	assert.Positive(wait)
	require.NotNil(reopened.RetryAt())
	assert.WithinDuration(time.Now().Add(time.Minute), *reopened.RetryAt(), time.Second, "retry schedule is local, not persisted quota data")
	assert.True(reopened.IsPaused())
	assert.Nil(reopened.ResetAt())
	reopened.mu.Lock()
	reopened.localRetryAt = time.Now().Add(-time.Second)
	reopened.mu.Unlock()
	paused, wait = reopened.ShouldBackoff()
	assert.False(paused)
	assert.Zero(wait)
	assert.False(reopened.IsPaused())
}
