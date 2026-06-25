package github

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDisplayNameCacheGetMiss(t *testing.T) {
	c := newDisplayNameCache(4, time.Hour, time.Minute)
	_, fresh := c.get("missing")
	require.False(t, fresh)
}

func TestDisplayNameCacheSuccessHit(t *testing.T) {
	c := newDisplayNameCache(4, time.Hour, time.Minute)
	c.putSuccess("k", "Alice")

	entry, fresh := c.get("k")
	assert := assert.New(t)
	assert.True(fresh)
	assert.True(entry.ok)
	assert.Equal("Alice", entry.name)
}

func TestDisplayNameCacheFailureHit(t *testing.T) {
	c := newDisplayNameCache(4, time.Hour, time.Minute)
	c.putFailure("k")

	entry, fresh := c.get("k")
	assert := assert.New(t)
	assert.True(fresh)
	assert.False(entry.ok)
	assert.Empty(entry.name)
}

func TestDisplayNameCacheExpiry(t *testing.T) {
	fakeNow := time.Unix(0, 0)
	c := newDisplayNameCache(4, time.Hour, time.Minute)
	c.now = func() time.Time { return fakeNow }

	c.putSuccess("k", "Alice")
	fakeNow = fakeNow.Add(time.Hour + time.Second)

	entry, fresh := c.get("k")
	assert := assert.New(t)
	assert.False(fresh, "entry should be expired")
	assert.True(entry.ok, "stale entry still returned")
	assert.Equal("Alice", entry.name)
}

func TestDisplayNameCacheFailureShorterTTL(t *testing.T) {
	fakeNow := time.Unix(0, 0)
	c := newDisplayNameCache(4, time.Hour, time.Minute)
	c.now = func() time.Time { return fakeNow }

	c.putFailure("k")
	fakeNow = fakeNow.Add(2 * time.Minute)

	_, fresh := c.get("k")
	require.False(t, fresh)
}

func TestDisplayNameCacheLRUEviction(t *testing.T) {
	require := require.New(t)
	c := newDisplayNameCache(3, time.Hour, time.Minute)

	c.putSuccess("a", "A")
	c.putSuccess("b", "B")
	c.putSuccess("c", "C")
	require.Equal(3, c.len())

	// Access "a" so "b" becomes least-recently-used.
	_, fresh := c.get("a")
	require.True(fresh)

	// Insert "d" — "b" should be evicted.
	c.putSuccess("d", "D")
	require.Equal(3, c.len())

	_, freshB := c.get("b")
	require.False(freshB)

	_, freshA := c.get("a")
	require.True(freshA)
	_, freshC := c.get("c")
	require.True(freshC)
	_, freshD := c.get("d")
	require.True(freshD)
}

func TestDisplayNameCacheUpdate(t *testing.T) {
	c := newDisplayNameCache(4, time.Hour, time.Minute)
	c.putSuccess("k", "Old")
	c.putSuccess("k", "New")
	entry, fresh := c.get("k")

	assert := assert.New(t)
	assert.True(fresh)
	assert.Equal("New", entry.name)
	assert.Equal(1, c.len())
}
