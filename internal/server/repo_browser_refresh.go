package server

import (
	"context"
	"log/slog"
	"time"
)

const defaultRepoBrowserRefreshInterval = 5 * time.Minute

func (s *Server) runRepoBrowserRefreshLoop(ctx context.Context) {
	if s.clones == nil {
		return
	}
	interval := s.repoBrowserRefreshInterval()
	s.runRepoBrowserRefreshPass(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runRepoBrowserRefreshPass(ctx)
		}
	}
}

func (s *Server) repoBrowserRefreshInterval() time.Duration {
	if s.cfg == nil {
		return defaultRepoBrowserRefreshInterval
	}
	if interval := s.cfg.SyncDuration(); interval > 0 {
		return interval
	}
	return defaultRepoBrowserRefreshInterval
}

func (s *Server) runRepoBrowserRefreshPass(ctx context.Context) {
	if s.clones == nil {
		return
	}
	slog.Debug("refreshing repo browser clones")
	s.clones.RefreshRepoBrowserClones(ctx)
}
