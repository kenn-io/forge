package ratelimit

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kit/stacktest"

	"go.kenn.io/forge/internal/testutil/dbtest"

	platformpkg "go.kenn.io/forge/platform"
)

func TestRateTrackerPersistsBeforeDatabaseCloses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		path := filepath.Join(t.TempDir(), "rate.db")
		d := dbtest.OpenAt(t, path)
		rt := newGitHubRateTracker(d, "github.com", "rest")

		var logs bytes.Buffer
		previous := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
		defer slog.SetDefault(previous)

		rt.RecordRequest()
		require.NoError(d.Close())
		reopened := dbtest.OpenPreparedAt(t, path)
		row, err := reopened.GetPlatformRateLimit("github", "github.com", "host", "rest")
		require.NoError(err)

		time.Sleep(persistDelay)
		synctest.Wait()
		assert.Empty(logs.String(), "the delayed flush must not write to the closed database")
		require.NotNil(row, "closing the database must persist pending rate state")
		assert.Equal(1, row.RequestsHour)
	})
}

// A slow fsync holds SQLite's single writer for seconds. Request-path calls
// must keep answering from memory while a flush waits behind it.
func TestRateTrackerRequestPathDoesNotWaitOnDatabaseWriter(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	rt := newGitHubRateTracker(d, "github.com", "rest")
	rt.RecordRequest()

	held := make(chan struct{})
	release := make(chan struct{})
	txDone := make(chan error, 1)
	go func() {
		txDone <- d.Tx(context.Background(), func(*sql.Tx) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	released := false
	releaseWriter := func() {
		if !released {
			released = true
			close(release)
		}
	}
	t.Cleanup(releaseWriter)

	flushDone := make(chan struct{})
	go func() {
		rt.Flush()
		close(flushDone)
	}()
	// Once the flush has claimed the change it is on its way to the blocked
	// writer; everything below must still finish without it.
	require.Eventually(func() bool { return !rt.dirty.Load() }, 10*time.Second, time.Millisecond)

	reset := time.Now().Add(30 * time.Minute)
	requestPathDone := make(chan struct{})
	go func() {
		defer close(requestPathDone)
		for range 3 {
			rt.RecordRequest()
		}
		rt.UpdateFromRate(platformpkg.Rate{Limit: 5000, Remaining: 100, Reset: reset})
		rt.UpdateFromSnapshot(platformpkg.Rate{Limit: 5000, Remaining: 90, Reset: reset})
	}()
	select {
	case <-requestPathDone:
	case <-time.After(10 * time.Second):
		stacktest.Dump(t)
		require.FailNow("request path waited on the database writer")
	}
	assert.True(rt.IsPaused())
	assert.Equal(4, rt.RequestsThisHour())
	assert.Equal(90, rt.Remaining())

	releaseWriter()
	require.NoError(<-txDone)
	<-flushDone

	// Changes made while the earlier flush was in flight still reach the
	// database, and a restarted tracker restores them.
	rt.Flush()
	row, err := d.GetPlatformRateLimit("github", "github.com", "host", "rest")
	require.NoError(err)
	require.NotNil(row)
	assert.Equal(4, row.RequestsHour)
	assert.Equal(90, row.RateRemaining)
	assert.Equal(5000, row.RateLimit)
	reopened := newGitHubRateTracker(d, "github.com", "rest")
	assert.Equal(4, reopened.RequestsThisHour())
	assert.True(reopened.IsPaused())
}

// Nothing on the request path flushes, so the background write alone must
// carry state across a restart.
func TestRateTrackerPersistsInBackground(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		d := openTestDB(t)
		rt := newGitHubRateTracker(d, "github.com", "graphql")

		reset := time.Now().Add(30 * time.Minute)
		rt.RecordRequest()
		rt.RecordRequest()
		rt.UpdateFromRate(platformpkg.Rate{Limit: 5000, Remaining: 4998, Reset: reset})

		row, err := d.GetPlatformRateLimit("github", "github.com", "host", "graphql")
		require.NoError(err)
		assert.Nil(row, "the request path must not write")

		time.Sleep(persistDelay)
		synctest.Wait()

		row, err = d.GetPlatformRateLimit("github", "github.com", "host", "graphql")
		require.NoError(err)
		require.NotNil(row)
		assert.Equal(2, row.RequestsHour)
		assert.Equal(4998, row.RateRemaining)
		reopened := newGitHubRateTracker(d, "github.com", "graphql")
		assert.Equal(2, reopened.RequestsThisHour())
		assert.Equal(4998, reopened.Remaining())
	})
}
