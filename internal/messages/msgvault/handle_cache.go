package msgvault

import (
	"strings"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
)

type cacheKey struct {
	messageID int64
	token     string
}

type cacheEntry struct {
	urls       []string
	generation uint64
	expires    time.Time
}

type handleCache struct {
	lru *lru.Cache[cacheKey, cacheEntry]
	ttl time.Duration
}

func newHandleCache(size int, ttl time.Duration) *handleCache {
	c, err := lru.New[cacheKey, cacheEntry](size)
	if err != nil {
		panic("handleCache: bad size: " + err.Error())
	}
	return &handleCache{lru: c, ttl: ttl}
}

func (c *handleCache) Set(messageID int64, token string, generation uint64, urls []string) {
	cloned := make([]string, len(urls))
	for i, u := range urls {
		cloned[i] = strings.Clone(u)
	}
	c.lru.Add(cacheKey{messageID, token}, cacheEntry{
		urls:       cloned,
		generation: generation,
		expires:    time.Now().Add(c.ttl),
	})
}

func (c *handleCache) Get(messageID int64, token string, currentGeneration uint64) ([]string, bool) {
	key := cacheKey{messageID, token}
	entry, ok := c.lru.Peek(key)
	if !ok {
		return nil, false
	}
	if entry.generation != currentGeneration {
		return nil, false
	}
	if time.Now().After(entry.expires) {
		c.lru.Remove(key)
		return nil, false
	}
	entry, _ = c.lru.Get(key)
	return entry.urls, true
}

func (c *handleCache) Purge() {
	c.lru.Purge()
}
