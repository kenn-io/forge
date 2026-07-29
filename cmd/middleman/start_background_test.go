package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kit/daemon"
	"go.kenn.io/middleman/internal/config"
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

func TestCanonicalDataDirUsesOneFilesystemIdentity(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "state")
	require.NoError(t, os.Mkdir(realDir, 0o700))
	t.Chdir(root)

	relative, err := config.CanonicalDataDir("./state")
	require.NoError(t, err)
	absolute, err := config.CanonicalDataDir(realDir)
	require.NoError(t, err)

	assert.Equal(t, absolute, relative)
	link := filepath.Join(root, "state-link")
	if err := os.Symlink(realDir, link); err != nil {
		if runtime.GOOS == "windows" {
			return
		}
		require.NoError(t, err)
	}
	linked, err := config.CanonicalDataDir(link)
	require.NoError(t, err)
	assert.Equal(t, absolute, linked)
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
	store := daemon.RuntimeStore{Dir: runtimeDir}
	startEntered := make(chan struct{})
	releaseStart := make(chan struct{})
	var enteredOnce sync.Once
	var starts atomic.Int32
	lifecycle := backgroundLifecycle{
		store:         store,
		dataDir:       dataDir,
		token:         token,
		authTokenPath: dataDir + "/auth_token",
		version:       "v-test",
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
		authTokenPath: dataDir + "/auth_token", version: "v-test",
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
