package servertest

import (
	"context"
	"io/fs"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/gitclone"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/server"
)

const shutdownTimeout = 5 * time.Second

// New constructs a server and registers graceful shutdown with t.
func New(
	t testing.TB,
	database *db.DB,
	syncer *ghclient.Syncer,
	frontend fs.FS,
	basePath string,
	cfg *config.Config,
	opts server.ServerOptions,
) *server.Server {
	t.Helper()
	return registerCleanup(t, server.New(
		database, syncer, frontend, basePath, cfg, opts,
	))
}

// NewWithConfig constructs a configured server and registers graceful shutdown with t.
func NewWithConfig(
	t testing.TB,
	database *db.DB,
	syncer *ghclient.Syncer,
	clones *gitclone.Manager,
	frontend fs.FS,
	cfg *config.Config,
	cfgPath string,
	opts server.ServerOptions,
) *server.Server {
	t.Helper()
	return registerCleanup(t, server.NewWithConfig(
		database, syncer, clones, frontend, cfg, cfgPath, opts,
	))
}

func registerCleanup(t testing.TB, srv *server.Server) *server.Server {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		require.NoError(t, srv.Shutdown(ctx))
	})
	return srv
}
