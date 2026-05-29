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

func TestStartRejectsNonLoopbackAddress(t *testing.T) {
	tests := []string{
		":6060",
		"0.0.0.0:6060",
		"[::]:6060",
		"192.0.2.10:6060",
		"example.com:6060",
		"localhost:6060",
	}

	for _, addr := range tests {
		t.Run(addr, func(t *testing.T) {
			srv, err := Start(addr)

			require.Error(t, err)
			Assert.Nil(t, srv)
			Assert.Contains(t, err.Error(), "loopback")
		})
	}
}

func TestStartAcceptsLoopbackAddress(t *testing.T) {
	tests := []string{
		"127.0.0.1:0",
		"[::1]:0",
	}

	for _, addr := range tests {
		t.Run(addr, func(t *testing.T) {
			srv, err := Start(addr)
			require.NoError(t, err)
			require.NotNil(t, srv)
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(
					context.Background(), time.Second,
				)
				defer cancel()
				require.NoError(t, srv.Shutdown(ctx))
			})
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

func TestStartRejectsNonBoundHostHeader(t *testing.T) {
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

	req, err := http.NewRequest(
		http.MethodGet,
		"http://"+srv.Addr().String()+"/debug/pprof/",
		nil,
	)
	require.NoError(err)
	req.Host = "localhost" + srv.Addr().String()[len("127.0.0.1"):]

	resp, err := http.DefaultClient.Do(req)
	require.NoError(err)
	defer resp.Body.Close()

	assert.Equal(http.StatusForbidden, resp.StatusCode)
}

func TestStartRejectsCrossSiteBrowserRequest(t *testing.T) {
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

	req, err := http.NewRequest(
		http.MethodGet,
		"http://"+srv.Addr().String()+"/debug/pprof/",
		nil,
	)
	require.NoError(err)
	req.Header.Set("Sec-Fetch-Site", "cross-site")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(err)
	defer resp.Body.Close()

	assert.Equal(http.StatusForbidden, resp.StatusCode)
}

func TestStartRejectsMismatchedOrigin(t *testing.T) {
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

	req, err := http.NewRequest(
		http.MethodGet,
		"http://"+srv.Addr().String()+"/debug/pprof/",
		nil,
	)
	require.NoError(err)
	req.Header.Set("Origin", "http://example.com")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(err)
	defer resp.Body.Close()

	assert.Equal(http.StatusForbidden, resp.StatusCode)
}

func TestStartRejectsBrowserRequestWithoutMetadata(t *testing.T) {
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

	req, err := http.NewRequest(
		http.MethodGet,
		"http://"+srv.Addr().String()+"/debug/pprof/",
		nil,
	)
	require.NoError(err)
	req.Header.Set(
		"User-Agent",
		"Mozilla/5.0 AppleWebKit/537.36 Chrome/125.0.0.0 Safari/537.36",
	)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(err)
	defer resp.Body.Close()

	assert.Equal(http.StatusForbidden, resp.StatusCode)
}

func TestStartAllowsNonBrowserRequestWithoutMetadata(t *testing.T) {
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

	req, err := http.NewRequest(
		http.MethodGet,
		"http://"+srv.Addr().String()+"/debug/pprof/",
		nil,
	)
	require.NoError(err)
	req.Header.Set("User-Agent", "curl/8.7.1")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(err)
	defer resp.Body.Close()

	assert.Equal(http.StatusOK, resp.StatusCode)
}

func TestStartCapsExpensiveProfileSeconds(t *testing.T) {
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

	resp, err := http.Get(
		"http://" + srv.Addr().String() + "/debug/pprof/profile?seconds=31",
	)
	require.NoError(err)
	defer resp.Body.Close()

	assert.Equal(http.StatusBadRequest, resp.StatusCode)
}

func TestStartCapsRuntimeProfileSeconds(t *testing.T) {
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

	resp, err := http.Get(
		"http://" + srv.Addr().String() + "/debug/pprof/heap?seconds=31",
	)
	require.NoError(err)
	defer resp.Body.Close()

	assert.Equal(http.StatusBadRequest, resp.StatusCode)
}
