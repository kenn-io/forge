package activityrelay

import (
	"sync"
	"time"
)

// subscriberBuffer reserves room for ordinary activity beyond one full Actions
// batch. A full buffer drops hints.
const subscriberBuffer = 256

const (
	actionsBatchInterval = time.Minute
	maxPendingActions    = 1024
	workflowReserve      = 256 // PR-check bursts cannot consume these workflow slots.
)

// Broadcaster fans hints out to every open subscription. Its zero value is
// ready to use.
type Broadcaster struct {
	mu           sync.Mutex
	subscribers  map[chan Hint]struct{}
	actions      map[Hint]struct{}
	actionsTimer *time.Timer
}

// Subscribe registers a receiver. The returned cancel function releases it.
func (b *Broadcaster) Subscribe() (<-chan Hint, func()) {
	ch := make(chan Hint, maxPendingActions+workflowReserve+subscriberBuffer)
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

// Publish delivers ordinary hints immediately. Actions share a one-minute
// collection window and reach subscribers once per target when it ends.
func (b *Broadcaster) Publish(hints []Hint) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, hint := range hints {
		if hint.Target != PullRequestChecks && hint.Target != WorkflowRuns {
			b.publishLocked(hint)
			continue
		}
		limit := maxPendingActions
		if hint.Target == WorkflowRuns {
			limit += workflowReserve
		}
		if len(b.actions) >= limit {
			continue
		}
		if b.actions == nil {
			b.actions = make(map[Hint]struct{})
		}
		b.actions[hint] = struct{}{}
		if b.actionsTimer == nil {
			b.actionsTimer = time.AfterFunc(actionsBatchInterval, func() {
				b.mu.Lock()
				defer b.mu.Unlock()
				// Fill the check allowance before using the reserved workflow slots.
				for _, target := range []string{PullRequestChecks, WorkflowRuns} {
					for hint := range b.actions {
						if hint.Target == target {
							b.publishLocked(hint)
						}
					}
				}
				clear(b.actions)
				b.actionsTimer = nil
			})
		}
	}
}

func (b *Broadcaster) publishLocked(hint Hint) {
	limit := maxPendingActions
	if hint.Target == WorkflowRuns {
		limit += workflowReserve
	}
	for ch := range b.subscribers {
		// Later batches must also leave room for ordinary activity when a
		// subscriber has not drained the previous batch.
		if (hint.Target == PullRequestChecks || hint.Target == WorkflowRuns) && len(ch) >= limit {
			continue
		}
		select {
		case ch <- hint:
		default:
		}
	}
}

// Close discards pending Actions and stops their timer after ingress has stopped.
func (b *Broadcaster) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.actionsTimer != nil {
		b.actionsTimer.Stop()
		b.actionsTimer = nil
	}
	clear(b.actions)
}

// Subscribers reports how many connections are open.
func (b *Broadcaster) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subscribers)
}
