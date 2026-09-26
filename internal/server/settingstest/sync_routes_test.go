package settingstest

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/testutil"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
)

func TestArchiveStartRejectsDisabledSyncer(t *testing.T) {
	require := require.New(t)
	database := serverfake.OpenTestDB(t)
	syncer := github.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	syncer.DisableSync()
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})

	rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/archive/start", map[string]bool{"all": true})
	require.Equal(http.StatusServiceUnavailable, rr.Code, rr.Body.String())
}
