package browserlogin

import (
	"encoding/base64"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testNodeID = "0123456789abcdef0123456789abcdef"

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 9, 22, 12, 0, 0, 500, time.UTC)}
}

func TestTicketIsSingleUse(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	clock := newTestClock()
	tickets := NewTicketStore(clock.Now)

	secret, grant, err := tickets.Issue(testNodeID)
	require.NoError(err)
	raw, err := base64.RawURLEncoding.DecodeString(secret)
	require.NoError(err)
	assert.GreaterOrEqual(len(raw), 32)
	assert.Equal(time.Date(2026, 9, 22, 12, 1, 0, 0, time.UTC), grant.ExpiresAt)

	consumed, ok := tickets.Consume(secret)
	require.True(ok)
	assert.Equal(testNodeID, consumed.NodeID)
	_, ok = tickets.Consume(secret)
	assert.False(ok, "a consumed ticket must not authenticate again")
}

func TestTicketExpiresAfterTTL(t *testing.T) {
	clock := newTestClock()
	tickets := NewTicketStore(clock.Now)
	secret, grant, err := tickets.Issue(testNodeID)
	require.NoError(t, err)

	clock.now = grant.ExpiresAt
	_, ok := tickets.Consume(secret)
	assert.False(t, ok, "a ticket is invalid at its advertised expiry")
}

func TestTicketRejectsUnknownSecrets(t *testing.T) {
	tickets := NewTicketStore(newTestClock().Now)
	_, _, err := tickets.Issue(testNodeID)
	require.NoError(t, err)
	for _, secret := range []string{"", "not-a-ticket", string(make([]byte, 4096))} {
		_, ok := tickets.Consume(secret)
		assert.False(t, ok, "secret %q", secret)
	}
}

func TestTicketStoreEvictsOldestAtCapacity(t *testing.T) {
	require := require.New(t)
	tickets := NewTicketStore(newTestClock().Now)
	first, _, err := tickets.Issue(testNodeID)
	require.NoError(err)
	second, _, err := tickets.Issue(testNodeID)
	require.NoError(err)
	for range MaxTickets - 1 {
		_, _, err := tickets.Issue(testNodeID)
		require.NoError(err)
	}

	_, ok := tickets.Consume(first)
	assert.False(t, ok, "issuing past the cap evicts the oldest ticket")
	_, ok = tickets.Consume(second)
	assert.True(t, ok, "only the overflow is evicted")
}

func TestSessionLookupDoesNotConsumeAndExpires(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	clock := newTestClock()
	sessions := NewSessionStore(clock.Now)
	secret, grant, err := sessions.Issue(testNodeID)
	require.NoError(err)
	assert.Equal(clock.Now().Add(SessionTTL).Truncate(time.Second), grant.ExpiresAt)

	for range 2 {
		found, ok := sessions.Lookup(secret)
		require.True(ok)
		assert.Equal(testNodeID, found.NodeID)
	}
	clock.Advance(SessionTTL)
	_, ok := sessions.Lookup(secret)
	assert.False(ok, "sessions have an absolute lifetime")
}
