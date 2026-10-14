package pullapi

import "sync"

// prMergeLocks serializes merge and enqueue mutations per pull request, so an
// immediate merge and a deferred worker never reach the provider at the same
// time. The zero value is ready to use.
type prMergeLocks struct {
	mu    sync.Mutex
	locks map[string]*prMergeLock
}

type prMergeLock struct {
	mu   sync.Mutex
	refs int
}

// lock blocks until key is free and returns its release. Entries are removed
// once nothing holds or waits on them.
func (l *prMergeLocks) lock(key string) func() {
	l.mu.Lock()
	if l.locks == nil {
		l.locks = make(map[string]*prMergeLock)
	}
	entry := l.locks[key]
	if entry == nil {
		entry = &prMergeLock{}
		l.locks[key] = entry
	}
	entry.refs++
	l.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		l.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(l.locks, key)
		}
		l.mu.Unlock()
	}
}
