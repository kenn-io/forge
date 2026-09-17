package activityrelay

import (
	"sync"
	"time"
)

// subscriberBuffer bounds how far one connection may fall behind. A full
// buffer drops hints for that subscriber; ordinary syncing covers the gap.
const subscriberBuffer = 256

const (
	checksBatchInterval = time.Minute
	maxPendingChecks    = 1024
)

// Broadcaster fans hints out to every open subscription. Its zero value is
// ready to use.
type Broadcaster struct {
	mu          sync.Mutex
	subscribers map[chan Hint]struct{}
	checks      map[Hint]struct{}
	checksTimer *time.Timer
}

// Subscribe registers a receiver. The returned cancel function releases it.
func (b *Broadcaster) Subscribe() (<-chan Hint, func()) {
	ch := make(chan Hint, subscriberBuffer)
	b.mu.Lock()
	if b.subscribers == nil {
		b.subscribers = make(map[chan Hint]struct{})
	}
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subscribers, ch)
		b.mu.Unlock()
	}
}

// Publish delivers ordinary hints immediately. Checks share a one-minute
// collection window and reach subscribers once per target when it ends.
func (b *Broadcaster) Publish(hints []Hint) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, hint := range hints {
		if hint.Target != PullRequestChecks {
			b.publishLocked(hint)
			continue
		}
		if len(b.checks) >= maxPendingChecks {
			continue // Ordinary syncing covers overflow; other activity has no delay.
		}
		if b.checks == nil {
			b.checks = make(map[Hint]struct{})
		}
		b.checks[hint] = struct{}{}
		if b.checksTimer == nil {
			b.checksTimer = time.AfterFunc(checksBatchInterval, func() {
				b.mu.Lock()
				defer b.mu.Unlock()
				for hint := range b.checks {
					b.publishLocked(hint)
				}
				clear(b.checks)
				b.checksTimer = nil
			})
		}
	}
}

func (b *Broadcaster) publishLocked(hint Hint) {
	for ch := range b.subscribers {
		select {
		case ch <- hint:
		default:
		}
	}
}

// Close discards pending checks and stops their timer after ingress has stopped.
func (b *Broadcaster) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.checksTimer != nil {
		b.checksTimer.Stop()
		b.checksTimer = nil
	}
	clear(b.checks)
}

// Subscribers reports how many connections are open.
func (b *Broadcaster) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subscribers)
}
