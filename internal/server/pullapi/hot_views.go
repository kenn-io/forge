package pullapi

import (
	"context"
	"log/slog"
	"maps"
	"time"
)

// recordHotView notes a pull request detail view for the hot refresh set
// without waiting for the write. A view needs the single SQLite writer, which
// queues behind sync commits whose fsyncs can stall for seconds, and opening a
// pull request must not wait on that. Views pending while a write is in flight
// coalesce into the next write, so a burst of detail reads costs one commit.
func (s *Handler) recordHotView(mergeRequestID int64, viewedAt time.Time) {
	s.hotViewMu.Lock()
	defer s.hotViewMu.Unlock()
	if s.hotViewPending == nil {
		s.hotViewPending = make(map[int64]time.Time)
	}
	if prior, ok := s.hotViewPending[mergeRequestID]; !ok || viewedAt.After(prior) {
		s.hotViewPending[mergeRequestID] = viewedAt
	}
	if s.hotViewFlushing {
		return
	}
	s.hotViewFlushing = s.runBackground(s.flushHotViews)
}

// flushHotViews writes pending views until none remain. A failed write drops
// its views: the hot set only speeds up background refresh, and the next view
// of the same pull request records it again.
func (s *Handler) flushHotViews(ctx context.Context) {
	for {
		s.hotViewMu.Lock()
		views := maps.Clone(s.hotViewPending)
		clear(s.hotViewPending)
		if len(views) == 0 {
			s.hotViewFlushing = false
			s.hotViewMu.Unlock()
			return
		}
		s.hotViewMu.Unlock()
		if err := s.db.RecordHotMergeRequestViews(ctx, views); err != nil {
			slog.Warn("record hot pull request views", "views", len(views), "err", err)
		}
		if ctx.Err() != nil {
			s.hotViewMu.Lock()
			s.hotViewFlushing = false
			s.hotViewMu.Unlock()
			return
		}
	}
}
