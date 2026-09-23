// Package browserlogin owns the in-memory secrets that let a browser move
// between Forge daemons of one fleet: short-lived single-use login tickets
// that a federation peer requests, and the browser sessions a ticket
// establishes. Only SHA-256 digests of the secrets are retained, and both
// stores are bounded so a peer cannot grow them without limit. Nothing is
// persisted; a daemon restart signs every browser session out.
package browserlogin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	// TicketTTL bounds how long a peer-issued login link stays usable.
	TicketTTL = 60 * time.Second
	// SessionTTL is the absolute lifetime of a ticket-established session.
	SessionTTL = 24 * time.Hour
	// MaxTickets caps outstanding login tickets; issuing past the cap evicts
	// the oldest ticket.
	MaxTickets = 256
	// MaxSessions caps live browser sessions; creating past the cap evicts
	// the oldest session.
	MaxSessions = 1024

	secretBytes = 32
	// maxSecretLength rejects oversized presented secrets before hashing.
	maxSecretLength = 128
)

// Grant is the principal a secret was issued for and when it stops working.
type Grant struct {
	NodeID    string
	ExpiresAt time.Time
}

type digest [sha256.Size]byte

type entry struct {
	grant  Grant
	issued uint64
}

// Store holds digest-only secrets with a fixed lifetime and capacity.
type Store struct {
	mu       sync.Mutex
	now      func() time.Time
	ttl      time.Duration
	capacity int
	issued   uint64
	entries  map[digest]entry
}

// NewTicketStore returns the single-use login-ticket store.
func NewTicketStore(now func() time.Time) *Store {
	return newStore(now, TicketTTL, MaxTickets)
}

// NewSessionStore returns the browser-session store.
func NewSessionStore(now func() time.Time) *Store {
	return newStore(now, SessionTTL, MaxSessions)
}

func newStore(now func() time.Time, ttl time.Duration, capacity int) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{
		now: now, ttl: ttl, capacity: capacity,
		entries: make(map[digest]entry),
	}
}

// Issue mints a random secret bound to nodeID and returns it exactly once.
// Expiry is whole-second UTC so the wire timestamp is the enforced deadline.
func (s *Store) Issue(nodeID string) (string, Grant, error) {
	if nodeID == "" {
		return "", Grant{}, errors.New("browser login principal is required")
	}
	raw := make([]byte, secretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", Grant{}, fmt.Errorf("generate browser login secret: %w", err)
	}
	secret := base64.RawURLEncoding.EncodeToString(raw)
	now := s.now().UTC()
	grant := Grant{
		NodeID:    nodeID,
		ExpiresAt: now.Add(s.ttl).Truncate(time.Second),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	for len(s.entries) >= s.capacity {
		s.evictOldestLocked()
	}
	s.issued++
	s.entries[sha256.Sum256([]byte(secret))] = entry{grant: grant, issued: s.issued}
	return secret, grant, nil
}

// Consume atomically removes a secret and reports its grant when it was
// present and unexpired. A second Consume of the same secret always fails.
func (s *Store) Consume(secret string) (Grant, bool) {
	key, ok := secretDigest(secret)
	if !ok {
		return Grant{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, found := s.entries[key]
	if !found {
		return Grant{}, false
	}
	delete(s.entries, key)
	if !s.now().UTC().Before(stored.grant.ExpiresAt) {
		return Grant{}, false
	}
	return stored.grant, true
}

// Lookup reports the grant for a live secret without consuming it.
func (s *Store) Lookup(secret string) (Grant, bool) {
	key, ok := secretDigest(secret)
	if !ok {
		return Grant{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, found := s.entries[key]
	if !found {
		return Grant{}, false
	}
	if !s.now().UTC().Before(stored.grant.ExpiresAt) {
		delete(s.entries, key)
		return Grant{}, false
	}
	return stored.grant, true
}

func secretDigest(secret string) (digest, bool) {
	if secret == "" || len(secret) > maxSecretLength {
		return digest{}, false
	}
	return sha256.Sum256([]byte(secret)), true
}

func (s *Store) pruneLocked(now time.Time) {
	for key, stored := range s.entries {
		if !now.Before(stored.grant.ExpiresAt) {
			delete(s.entries, key)
		}
	}
}

func (s *Store) evictOldestLocked() {
	var (
		victim digest
		oldest uint64
		found  bool
	)
	for key, stored := range s.entries {
		if !found || stored.issued < oldest {
			victim, oldest, found = key, stored.issued, true
		}
	}
	if found {
		delete(s.entries, victim)
	}
}
