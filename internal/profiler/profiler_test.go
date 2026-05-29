package profiler

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewHandlerRegistersStandardProfilerEndpoints(t *testing.T) {
	mux := NewHandler()

	for _, path := range []string{
		"/debug/pprof/",
		"/debug/pprof/cmdline",
		"/debug/pprof/profile",
		"/debug/pprof/symbol",
		"/debug/pprof/trace",
	} {
		t.Run(path, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, path, nil)
			require.NoError(t, err)

			_, pattern := mux.Handler(req)

			Assert.Equal(t, path, pattern)
		})
	}
}

func TestStartServesProfilerIndex(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	srv, err := Start("127.0.0.1:0")
	require.NoError(err)
	require.NotNil(srv)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(srv.Shutdown(ctx))
	})

	resp, err := http.Get("http://" + srv.Addr().String() + "/debug/pprof/")
	require.NoError(err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(err)

	assert.Equal(http.StatusOK, resp.StatusCode)
	assert.Contains(string(body), "Types of profiles available")
}
