package settingsservertest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.kenn.io/forge/internal/config"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"

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
			server.New(database, syncer, serverfake.EmptyFrontend(), "/", &config.Config{
				Host: "0.0.0.0",
				Port: 8091,
			}, server.ServerOptions{})
		},
	)
}
