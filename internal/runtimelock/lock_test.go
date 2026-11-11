package runtimelock

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAcquireWaitsForBriefLockHolder(t *testing.T) {
	dir := t.TempDir()
	holder, err := Acquire(t.Context(), dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = holder.Release() })

	// Prove a held lock is retried before allowing the holder to release it.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	_, err = Acquire(ctx, dir)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	timer := time.AfterFunc(20*time.Millisecond, func() { _ = holder.Release() })
	t.Cleanup(func() { timer.Stop() })

	h, err := Acquire(t.Context(), dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = h.Release() })
}

func TestAcquireStopsWhenCallerCancels(t *testing.T) {
	dir := t.TempDir()
	holder, err := Acquire(t.Context(), dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = holder.Release() })

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	_, err = Acquire(ctx, dir)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestAcquireSucceedsOnEmptyDataDir(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()

	h, err := Acquire(t.Context(), dir)
	require.NoError(err)
	require.NotNil(h)
	t.Cleanup(func() { _ = h.Release() })

	// Lock file is created.
	_, err = os.Stat(LockPath(dir))
	require.NoError(err)

	// Metadata file is NOT created until WriteMetadata is called.
	_, err = os.Stat(MetadataPath(dir))
	require.True(os.IsNotExist(err))
}

func TestAcquireSecondCallReturnsCollision(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()

	h1, err := Acquire(t.Context(), dir)
	require.NoError(err)
	t.Cleanup(func() { _ = h1.Release() })

	_, err = Acquire(t.Context(), dir)
	require.Error(err)

	var cerr *CollisionError
	require.ErrorAs(err, &cerr)
	require.Equal(dir, cerr.DataDir)
	require.Equal(LockPath(dir), cerr.LockPath)
}

func TestAcquireCollisionWithMetadata(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()

	h1, err := Acquire(t.Context(), dir)
	require.NoError(err)
	t.Cleanup(func() { _ = h1.Release() })

	meta := Metadata{PID: 1234, ListenAddr: "127.0.0.1:8091"}
	require.NoError(h1.WriteMetadata(meta))

	_, err = Acquire(t.Context(), dir)
	var cerr *CollisionError
	require.ErrorAs(err, &cerr)
	require.NotNil(cerr.Metadata)
	require.Equal(meta, *cerr.Metadata)
}

func TestAcquireCollisionWithoutMetadata(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()

	h1, err := Acquire(t.Context(), dir)
	require.NoError(err)
	t.Cleanup(func() { _ = h1.Release() })

	_, err = Acquire(t.Context(), dir)
	var cerr *CollisionError
	require.ErrorAs(err, &cerr)
	require.Nil(cerr.Metadata)
	require.Equal(ReasonMetadataMissing, cerr.MetadataUnavailable)
}

func TestAcquireRemovesStaleMetadata(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()

	// Simulate a previous unclean shutdown: metadata file exists but no
	// holder of the lock.
	stale := Metadata{PID: 9999, ListenAddr: "127.0.0.1:9999"}
	require.NoError(writeMetadata(dir, stale))

	h, err := Acquire(t.Context(), dir)
	require.NoError(err)
	t.Cleanup(func() { _ = h.Release() })

	_, err = os.Stat(MetadataPath(dir))
	require.True(os.IsNotExist(err), "stale metadata should be removed under the held lock")
}

func TestReleaseRemovesMetadataKeepsLockFile(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()

	h, err := Acquire(t.Context(), dir)
	require.NoError(err)

	require.NoError(h.WriteMetadata(Metadata{PID: 1, ListenAddr: "127.0.0.1:1"}))
	require.NoError(h.Release())

	// Metadata file is gone.
	_, err = os.Stat(MetadataPath(dir))
	require.True(os.IsNotExist(err))

	// Lock file persists.
	_, err = os.Stat(LockPath(dir))
	require.NoError(err)

	// A fresh Acquire on the same dir now succeeds.
	h2, err := Acquire(t.Context(), dir)
	require.NoError(err)
	t.Cleanup(func() { _ = h2.Release() })
}

func TestReleaseIsIdempotent(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()

	h, err := Acquire(t.Context(), dir)
	require.NoError(err)

	require.NoError(h.Release())
	require.NoError(h.Release(), "second Release call must not error")
}
