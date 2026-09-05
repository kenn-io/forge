package githubapp_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/githubapp"
)

func TestCacheInvalidationFencesInflightGeneration(t *testing.T) {
	require := require.New(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cache := githubapp.NewTokenCache(func() time.Time { return now })
	key := githubapp.CacheKey{Host: "github.com", AppID: 11, InstallationID: 21, Generation: 1, Scope: githubapp.TokenScope{AllRepositories: true}}
	started, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := cache.Token(ctx, key, func(ctx context.Context) (githubapp.InstallationToken, error) {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return githubapp.InstallationToken{}, ctx.Err()
			}
			return githubapp.InstallationToken{Token: "obsolete", ExpiresAt: now.Add(time.Hour)}, nil
		})
		done <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		require.NoError(ctx.Err())
	}
	cache.InvalidateGeneration(key)
	close(release)
	require.ErrorIs(<-done, githubapp.ErrStaleGeneration)
	var calls atomic.Int32
	_, err := cache.Token(ctx, key, func(context.Context) (githubapp.InstallationToken, error) {
		calls.Add(1)
		return githubapp.InstallationToken{}, nil
	})
	require.ErrorIs(err, githubapp.ErrStaleGeneration)
	assert.Zero(t, calls.Load())
}

func TestRejectedOldTokenDoesNotEvictReplacement(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cache := githubapp.NewTokenCache(func() time.Time { return now })
	key := githubapp.CacheKey{Host: "github.com", AppID: 11, InstallationID: 21, Generation: 1, Scope: githubapp.TokenScope{RepositoryIDs: []int64{31}}}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	calls := 0
	mint := func(context.Context) (githubapp.InstallationToken, error) {
		calls++
		token := "first"
		if calls > 1 {
			token = "second"
		}
		return githubapp.InstallationToken{Token: token, ExpiresAt: now.Add(time.Hour)}, nil
	}
	first, err := cache.Token(ctx, key, mint)
	require.NoError(err)
	cache.RejectToken(key, first.Token)
	second, err := cache.Token(ctx, key, mint)
	require.NoError(err)
	cache.RejectToken(key, first.Token)
	current, err := cache.Token(ctx, key, mint)
	require.NoError(err)
	assert.Equal("second", second.Token)
	assert.Equal("second", current.Token)
	assert.Equal(2, calls)
}

func TestCanceledCacheWaiterDoesNotBlockAnotherInstallation(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cache := githubapp.NewTokenCache(func() time.Time { return now })
	key := githubapp.CacheKey{Host: "github.com", AppID: 11, InstallationID: 21, Generation: 1, Scope: githubapp.TokenScope{AllRepositories: true}}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	waiter, cancelWaiter := context.WithCancel(ctx)
	defer cancelWaiter()
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := cache.Token(waiter, key, func(ctx context.Context) (githubapp.InstallationToken, error) {
			close(started)
			select {
			case <-release:
				return githubapp.InstallationToken{Token: "first-installation", ExpiresAt: now.Add(time.Hour)}, nil
			case <-ctx.Done():
				return githubapp.InstallationToken{}, ctx.Err()
			}
		})
		done <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		require.FailNow("mint did not start")
	}
	cancelWaiter()
	require.ErrorIs(<-done, context.Canceled)
	other := key
	other.InstallationID = 22
	token, err := cache.Token(ctx, other, func(context.Context) (githubapp.InstallationToken, error) {
		return githubapp.InstallationToken{Token: "second-installation", ExpiresAt: now.Add(time.Hour)}, nil
	})
	require.NoError(err)
	assert.Equal("second-installation", token.Token)
	cache.InvalidateGeneration(other)
	close(release)
	token, err = cache.Token(ctx, key, func(context.Context) (githubapp.InstallationToken, error) {
		return githubapp.InstallationToken{Token: "unexpected-remint", ExpiresAt: now.Add(time.Hour)}, nil
	})
	require.NoError(err)
	assert.Equal("first-installation", token.Token)
}
