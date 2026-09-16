package activityrelay

import "sync"

// subscriberBuffer bounds how far one connection may fall behind. A full
// buffer drops hints for that subscriber; ordinary syncing covers the gap.
const subscriberBuffer = 256

// Broadcaster fans hints out to every open subscription. Its zero value is
// ready to use.
type Broadcaster struct {
	mu          sync.Mutex
	subscribers map[chan Hint]struct{}
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

// Publish delivers hints to every subscriber without blocking the caller.
func (b *Broadcaster) Publish(hints []Hint) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subscribers {
		for _, hint := range hints {
			select {
			case ch <- hint:
			default:
			}
		}
	}
}

// Subscribers reports how many connections are open.
func (b *Broadcaster) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subscribers)
}
