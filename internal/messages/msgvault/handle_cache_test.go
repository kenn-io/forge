package msgvault

import (
	"sync"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleCacheSetGet(t *testing.T) {
	c := newHandleCache(10, time.Minute)
	urls := []string{"http://a", "http://b"}
	c.Set(42, "tok", 1, urls)
	got, ok := c.Get(42, "tok", 1)
	require.True(t, ok)
	Assert.Equal(t, urls, got)
}

func TestHandleCacheGenerationMismatch(t *testing.T) {
	assert := Assert.New(t)
	c := newHandleCache(10, time.Minute)
	c.Set(42, "tok", 1, []string{"u"})
	_, ok := c.Get(42, "tok", 2)
	assert.False(ok)
	_, ok = c.Get(42, "tok", 0)
	assert.False(ok)
	_, ok = c.Get(42, "tok", 1)
	assert.True(ok)
}

func TestHandleCacheTTLExpiry(t *testing.T) {
	c := newHandleCache(10, 10*time.Millisecond)
	c.Set(42, "tok", 1, []string{"u"})
	time.Sleep(25 * time.Millisecond)
	_, ok := c.Get(42, "tok", 1)
	Assert.False(t, ok)
}

func TestHandleCacheLRUEviction(t *testing.T) {
	assert := Assert.New(t)
	c := newHandleCache(2, time.Minute)
	c.Set(1, "t", 1, []string{"a"})
	c.Set(2, "t", 1, []string{"b"})
	c.Set(3, "t", 1, []string{"c"})
	_, ok := c.Get(1, "t", 1)
	assert.False(ok)
	_, ok = c.Get(2, "t", 1)
	assert.True(ok)
	_, ok = c.Get(3, "t", 1)
	assert.True(ok)
}

func TestHandleCacheStaleLookupsDoNotPromote(t *testing.T) {
	assert := Assert.New(t)
	c := newHandleCache(2, time.Minute)
	c.Set(1, "stale", 1, []string{"stale"})
	c.Set(2, "valid", 2, []string{"valid"})

	_, ok := c.Get(1, "stale", 2)
	assert.False(ok)
	c.Set(3, "new", 2, []string{"new"})

	_, ok = c.Get(1, "stale", 1)
	assert.False(ok)
	_, ok = c.Get(2, "valid", 2)
	assert.True(ok)
	_, ok = c.Get(3, "new", 2)
	assert.True(ok)
}

func TestHandleCachePurge(t *testing.T) {
	assert := Assert.New(t)
	c := newHandleCache(10, time.Minute)
	c.Set(1, "t", 1, []string{"a"})
	c.Set(2, "t", 1, []string{"b"})
	c.Purge()
	_, ok := c.Get(1, "t", 1)
	assert.False(ok)
	_, ok = c.Get(2, "t", 1)
	assert.False(ok)
}

func TestHandleCacheRotationRace(t *testing.T) {
	c := newHandleCache(10, time.Minute)
	c.Set(42, "tok", 1, []string{"u"})
	_, ok := c.Get(42, "tok", 2)
	Assert.False(t, ok)
}

func TestHandleCacheConcurrentAccess(t *testing.T) {
	c := newHandleCache(1000, time.Minute)
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Go(func() {
			c.Set(int64(i), "tok", 1, []string{"u"})
			_, _ = c.Get(int64(i), "tok", 1)
		})
	}
	wg.Wait()
}
