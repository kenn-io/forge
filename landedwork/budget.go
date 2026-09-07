package landedwork

import (
	"bytes"
	"sync"
)

type meter struct {
	mu     sync.Mutex
	limits Limits
	failed bool
}

func (m *meter) input(n int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n > m.limits.InputBytes {
		m.failed = true
		return ErrInputBudget
	}
	m.limits.InputBytes -= n
	return nil
}

func (m *meter) records(n int64) error {
	if n > m.limits.Records {
		return ErrInputBudget
	}
	m.limits.Records -= n
	return nil
}

func (m *meter) node() error {
	if m.limits.Nodes == 0 {
		return ErrInputBudget
	}
	m.limits.Nodes--
	return nil
}

// The process owns one writer per stream; the shared byte meter is synchronized.
type boundedBuffer struct {
	buf   bytes.Buffer
	meter *meter
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if err := b.meter.input(int64(len(p))); err != nil {
		return 0, err
	}
	return b.buf.Write(p)
}

func (b *boundedBuffer) Bytes() []byte { return b.buf.Bytes() }

func checkQueryOutput(q Query, l Limits) error {
	n := int64(len(q.Bounds.Base) + len(q.Bounds.Head) + len(q.Bounds.Repository.Host) + len(q.Bounds.Repository.ID) + len(q.Bounds.Repository.Provider))
	for _, id := range q.Commits {
		n += int64(len(id))
	}
	for _, g := range q.Gaps {
		n += int64(len(g.Reason) + len(g.ObjectID) + len(g.CandidateID))
	}
	if n > l.OutputBytes || int64(len(q.Commits)+len(q.Gaps)) > l.Records {
		return ErrOutputBudget
	}
	return nil
}
