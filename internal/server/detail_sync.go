package server

import (
	"context"
	"errors"
	"log/slog"

	ghclient "go.kenn.io/middleman/internal/github"
)

func (s *Server) enqueueDetailSync(
	key string,
	attrs []any,
	fn func(context.Context) error,
) bool {
	return s.enqueueDetailSyncWithCompletion(key, attrs, fn, nil)
}

func (s *Server) enqueueDetailSyncWithCompletion(
	key string,
	attrs []any,
	fn func(context.Context) error,
	after func(context.Context),
) bool {
	s.detailSyncMu.Lock()
	if s.detailSyncInFlight == nil {
		s.detailSyncInFlight = make(map[string]struct{})
	}
	if _, ok := s.detailSyncInFlight[key]; ok {
		s.detailSyncMu.Unlock()
		return false
	}
	s.detailSyncInFlight[key] = struct{}{}
	s.detailSyncMu.Unlock()

	started := s.runBackground(func(ctx context.Context) {
		defer func() {
			s.detailSyncMu.Lock()
			delete(s.detailSyncInFlight, key)
			s.detailSyncMu.Unlock()
		}()

		err := fn(ctx)
		var diffErr *ghclient.DiffSyncError
		if err != nil && !errors.As(err, &diffErr) {
			slog.Warn("background detail sync failed", append(attrs, "err", err)...)
			return
		}
		if diffErr != nil {
			slog.Warn(
				"background PR diff sync failed",
				append(attrs, "code", diffErr.Code, "err", diffErr.Err)...,
			)
		}
		if after != nil {
			after(ctx)
		}
		s.hub.Broadcast(Event{
			Type: "data_changed",
			Data: struct{}{},
		})
	})
	if started {
		return true
	}

	s.detailSyncMu.Lock()
	delete(s.detailSyncInFlight, key)
	s.detailSyncMu.Unlock()
	return false
}
