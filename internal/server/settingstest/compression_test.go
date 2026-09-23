package settingstest

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
)

func TestServerUsesResponseCompressionMiddleware(t *testing.T) {
	database := dbtest.Open(t)
	_, err := testutil.SeedFixtures(t.Context(), database)
	require.NoError(t, err)
	srv := server.New(database, nil, nil, "/", nil, server.ServerOptions{})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/pulls", nil)
	req.Header.Set("Accept-Encoding", "zstd")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	assert := assert.New(t)
	assert.Equal(http.StatusOK, rr.Code)
	assert.Equal("zstd", rr.Header().Get("Content-Encoding"))
	assert.Equal("Accept-Encoding", rr.Header().Get("Vary"))
	assert.Contains(serverfake.DecodeZstdBody(t, rr.Body), "Add widget caching layer")
}
