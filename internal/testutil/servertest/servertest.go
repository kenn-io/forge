package servertest

import (
	"context"
	"io/fs"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server"
)

const shutdownTimeout = 5 * time.Second

// New constructs a server and registers graceful shutdown with t.
func New(
	tb testing.TB,
	database *db.DB,
	syncer *ghclient.Syncer,
	frontend fs.FS,
	basePath string,
	cfg *config.Config,
	opts server.ServerOptions,
) *server.Server {
	tb.Helper()
	return registerCleanup(tb, server.New(
		database, syncer, frontend, basePath, cfg, opts,
	))
}

// NewWithConfig constructs a configured server and registers graceful shutdown with t.
func NewWithConfig(
	tb testing.TB,
	database *db.DB,
	syncer *ghclient.Syncer,
	clones *gitclone.Manager,
	frontend fs.FS,
	cfg *config.Config,
	cfgPath string,
	opts server.ServerOptions,
) *server.Server {
	tb.Helper()
	return registerCleanup(tb, server.NewWithConfig(
		database, syncer, clones, frontend, cfg, cfgPath, opts,
	))
}

func registerCleanup(tb testing.TB, srv *server.Server) *server.Server {
	tb.Helper()
	tb.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(tb.Context()), shutdownTimeout)
		defer cancel()
		require.NoError(tb, srv.Shutdown(ctx))
	})
	return srv
}
