package httpapi

import "sync"

// KeyedLocks serializes work per key, such as one item's read-modify-write
// mutations. The zero value is ready to use.
type KeyedLocks struct {
	mu    sync.Mutex
	locks map[string]*keyedLock
}

// keyedLock is a one-slot channel rather than a mutex, so waiting on it is
// a durable block that testing/synctest can observe.
type keyedLock struct {
	slot chan struct{}
	refs int
}

// Lock blocks until key is free and returns its release. Entries are removed
// once nothing holds or waits on them.
func (l *KeyedLocks) Lock(key string) func() {
	l.mu.Lock()
	if l.locks == nil {
		l.locks = make(map[string]*keyedLock)
	}
	entry := l.locks[key]
	if entry == nil {
		entry = &keyedLock{slot: make(chan struct{}, 1)}
		l.locks[key] = entry
	}
	entry.refs++
	l.mu.Unlock()

	entry.slot <- struct{}{}
	return func() {
		<-entry.slot
		l.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(l.locks, key)
		}
		l.mu.Unlock()
	}
}
