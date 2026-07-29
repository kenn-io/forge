package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kit/daemon"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/runtimelock"
)

func TestValidateBackgroundConfigRejectsUnsafeDirectVerification(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		want string
	}{
		{
			name: "non-loopback listener",
			cfg: config.Config{
				Host: "192.0.2.1", API: config.API{RequireAuth: true},
			},
			want: "loopback TCP listener",
		},
		{
			name: "unauthenticated reverse proxy",
			cfg: config.Config{
				Host: "127.0.0.1", TrustReverseProxy: true,
			},
			want: "require_auth=true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBackgroundConfig(&tt.cfg)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

// TestBackgroundLifecycleSerializesConcurrentStarts protects the launch
// owner invariant. If the shared start lock is removed or discovery is not
// repeated under it, concurrent callers invoke the detached starter twice.
func TestBackgroundLifecycleSerializesConcurrentStarts(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	token := "daemon-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w,
			`{"ok":true,"service":"middleman","version":"v-test","pid":%d}`,
			os.Getpid(),
		)
	}))
	t.Cleanup(server.Close)

	runtimeDir := t.TempDir()
	dataDir := t.TempDir()
	require.NoError(os.WriteFile(
		runtimelock.AuthTokenPath(dataDir), []byte(token+"\n"), 0o600,
	))
	store := daemon.RuntimeStore{Dir: runtimeDir}
	startEntered := make(chan struct{})
	releaseStart := make(chan struct{})
	var enteredOnce sync.Once
	var starts atomic.Int32
	lifecycle := backgroundLifecycle{
		store:   store,
		dataDir: dataDir,
		token:   token,
		version: "v-test",
		start: func(context.Context) error {
			starts.Add(1)
			enteredOnce.Do(func() { close(startEntered) })
			<-releaseStart
			_, err := store.Write(daemon.RuntimeRecord{
				PID: os.Getpid(), Network: daemon.NetworkTCP,
				Address: server.Listener.Addr().String(),
				Service: "middleman", Version: "v-test",
				StartedAt: time.Now().UTC(),
				Metadata: map[string]string{
					"host":            "127.0.0.1",
					"port":            strconv.Itoa(server.Listener.Addr().(*net.TCPAddr).Port),
					"read_only":       "false",
					"require_auth":    "true",
					"data_dir":        dataDir,
					"auth_token_path": dataDir + "/auth_token",
				},
			})
			return err
		},
	}

	type result struct {
		record daemon.RuntimeRecord
		err    error
	}
	results := make(chan result, 2)
	go func() {
		record, _, err := lifecycle.Ensure(t.Context(), time.Second)
		results <- result{record: record, err: err}
	}()
	<-startEntered
	go func() {
		record, _, err := lifecycle.Ensure(t.Context(), time.Second)
		results <- result{record: record, err: err}
	}()
	close(releaseStart)

	first := <-results
	second := <-results
	require.NoError(first.err)
	require.NoError(second.err)
	assert.Equal(int32(1), starts.Load())
	assert.Equal(first.record.PID, second.record.PID)
}

// TestBackgroundLifecycleAllowsIndependentDataDirectoryStarts protects the
// lock scope. A stalled launch for one data directory must not block an
// unrelated instance that shares the config-home discovery directory.
func TestBackgroundLifecycleAllowsIndependentDataDirectoryStarts(t *testing.T) {
	store := daemon.RuntimeStore{Dir: t.TempDir()}
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	first := backgroundLifecycle{
		store: store, dataDir: t.TempDir(), version: "v-test",
		start: func(ctx context.Context) error {
			close(firstStarted)
			<-ctx.Done()
			return ctx.Err()
		},
	}
	second := backgroundLifecycle{
		store: store, dataDir: t.TempDir(), version: "v-test",
		start: func(ctx context.Context) error {
			close(secondStarted)
			<-ctx.Done()
			return ctx.Err()
		},
	}

	go func() {
		_, _, _ = first.Ensure(ctx, 2*time.Second)
	}()
	require.Eventually(t, func() bool {
		select {
		case <-firstStarted:
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond)
	go func() {
		_, _, _ = second.Ensure(ctx, 2*time.Second)
	}()

	require.Eventually(t, func() bool {
		select {
		case <-secondStarted:
			return true
		default:
			return false
		}
	}, 500*time.Millisecond, 10*time.Millisecond)
}

// TestBackgroundLifecycleRejectsUnverifiedLiveRecord protects the boundary
// between process discovery and daemon identity. A live PID and plausible
// metadata are insufficient until the authenticated ping succeeds.
func TestBackgroundLifecycleRejectsUnverifiedLiveRecord(t *testing.T) {
	require := require.New(t)
	var allowPing atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !allowPing.Load() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w,
			`{"ok":true,"service":"middleman","version":"v-test","pid":%d}`,
			os.Getpid(),
		)
	}))
	t.Cleanup(server.Close)

	runtimeDir := t.TempDir()
	dataDir := t.TempDir()
	store := daemon.RuntimeStore{Dir: runtimeDir}
	address := server.Listener.Addr().String()
	host, port, err := net.SplitHostPort(address)
	require.NoError(err)
	_, err = store.Write(daemon.RuntimeRecord{
		PID: os.Getpid(), Network: daemon.NetworkTCP, Address: address,
		Service: "middleman", Version: "v-test", StartedAt: time.Now().UTC(),
		Metadata: map[string]string{
			"host": host, "port": port, "read_only": "false",
			"require_auth": "true", "data_dir": dataDir,
			"auth_token_path": dataDir + "/auth_token",
		},
	})
	require.NoError(err)
	var starts atomic.Int32
	lifecycle := backgroundLifecycle{
		store: store, dataDir: dataDir, token: "daemon-secret",
		version: "v-test",
		start: func(context.Context) error {
			starts.Add(1)
			allowPing.Store(true)
			return nil
		},
	}

	_, _, err = lifecycle.Ensure(t.Context(), time.Second)

	require.NoError(err)
	assert.Equal(t, int32(1), starts.Load())
}
