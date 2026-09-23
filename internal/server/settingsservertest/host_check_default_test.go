package settingsservertest

import (
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"go.kenn.io/forge/internal/config"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/streamapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestNewRejectsUnvalidatedConfigWithNonLoopbackHost(t *testing.T) {
	old := streamapi.AllowUnvalidatedConfigHostCheckFallbackForTests
	streamapi.AllowUnvalidatedConfigHostCheckFallbackForTests = false
	t.Cleanup(func() {
		streamapi.AllowUnvalidatedConfigHostCheckFallbackForTests = old
	})

	database := dbtest.Open(t)
	syncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)

	assert.PanicsWithError(t,
		`server: config did not provide valid Host check options: config: host "0.0.0.0" is not loopback; only loopback addresses are supported`,
		func() {
			server.New(database, syncer, emptyFrontend(), "/", &config.Config{
				Host: "0.0.0.0",
				Port: 8091,
			}, server.ServerOptions{})
		},
	)
}

func emptyFrontend() fs.FS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte("<!DOCTYPE html><html><body>ok</body></html>"),
		},
	}
}
