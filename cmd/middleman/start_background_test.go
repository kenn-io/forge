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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBackgroundConfig(&tt.cfg)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestValidateBackgroundConfigAllowsUnauthenticatedReverseProxy(t *testing.T) {
	cfg := config.Config{Host: "127.0.0.1", TrustReverseProxy: true}

	require.NoError(t, validateBackgroundConfig(&cfg))
}

// TestBackgroundManagerSerializesConcurrentStarts protects the launch
// owner invariant. If the shared start lock is removed or discovery is not
// repeated under it, concurrent callers invoke the detached starter twice.
func TestBackgroundManagerSerializesConcurrentStarts(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	token := "daemon-secret"
	runtimeDir := t.TempDir()
	dataDir := t.TempDir()
	_, record := newBackgroundProofServer(t, dataDir, token, "v-test", nil)
	require.NoError(os.WriteFile(
		runtimelock.AuthTokenPath(dataDir), []byte(token+"\n"), 0o600,
	))
	store := daemon.RuntimeStore{Dir: runtimeDir}
	startEntered := make(chan struct{})
	releaseStart := make(chan struct{})
	var enteredOnce sync.Once
	var starts atomic.Int32
	manager := newBackgroundManager(
		store, dataDir, "v-test",
		func(context.Context) error {
			starts.Add(1)
			enteredOnce.Do(func() { close(startEntered) })
			<-releaseStart
			_, err := store.Write(record)
			return err
		},
	)

	type result struct {
		record daemon.RuntimeRecord
		err    error
	}
	results := make(chan result, 2)
	go func() {
		record, _, err := manager.Ensure(t.Context(), time.Second)
		results <- result{record: record, err: err}
	}()
	<-startEntered
	go func() {
		record, _, err := manager.Ensure(t.Context(), time.Second)
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

// TestBackgroundManagerAllowsIndependentDataDirectoryStarts protects the
// lock scope. A stalled launch for one data directory must not block an
// unrelated instance that shares the config-home discovery directory.
func TestBackgroundManagerAllowsIndependentDataDirectoryStarts(t *testing.T) {
	store := daemon.RuntimeStore{Dir: t.TempDir()}
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	first := newBackgroundManager(
		store, t.TempDir(), "v-test",
		func(ctx context.Context) error {
			close(firstStarted)
			<-ctx.Done()
			return ctx.Err()
		},
	)
	second := newBackgroundManager(
		store, t.TempDir(), "v-test",
		func(ctx context.Context) error {
			close(secondStarted)
			<-ctx.Done()
			return ctx.Err()
		},
	)

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

// TestBackgroundManagerRejectsUnverifiedLiveRecord protects the boundary
// between process discovery and daemon identity. A live PID and plausible
// metadata are insufficient until the authenticated ping succeeds.
func TestBackgroundManagerRejectsUnverifiedLiveRecord(t *testing.T) {
	require := require.New(t)
	var allowPing atomic.Bool
	runtimeDir := t.TempDir()
	dataDir := t.TempDir()
	token := "daemon-secret"
	_, record := newBackgroundProofServer(
		t, dataDir, token, "v-test", allowPing.Load,
	)
	require.NoError(os.WriteFile(
		runtimelock.AuthTokenPath(dataDir), []byte(token+"\n"), 0o600,
	))
	store := daemon.RuntimeStore{Dir: runtimeDir}
	_, err := store.Write(record)
	require.NoError(err)
	var starts atomic.Int32
	manager := newBackgroundManager(
		store, dataDir, "v-test",
		func(context.Context) error {
			starts.Add(1)
			allowPing.Store(true)
			return nil
		},
	)

	_, _, err = manager.Ensure(t.Context(), time.Second)

	require.NoError(err)
	assert.Equal(t, int32(1), starts.Load())
}

// TestBackgroundDiscoveryDoesNotDiscloseBearerBeforeProof protects the
// credential boundary for stale runtime records. If discovery attaches the
// bearer before the endpoint proves possession of the daemon token, a process
// that inherits the recorded loopback port can steal the persistent token.
func TestBackgroundDiscoveryDoesNotDiscloseBearerBeforeProof(t *testing.T) {
	var receivedAuthorization atomic.Value
	var requests atomic.Int32
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		receivedAuthorization.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w,
			`{"ok":true,"service":"middleman","version":"v-test","pid":%d}`,
			os.Getpid(),
		)
	}))
	t.Cleanup(attacker.Close)

	dataDir := t.TempDir()
	record := daemon.RuntimeRecord{
		PID: os.Getpid(), Network: daemon.NetworkTCP,
		Address: attacker.Listener.Addr().String(),
		Service: "middleman", Version: "v-test",
		StartedAt: time.Now().UTC(),
		Metadata: map[string]string{
			"host":            attacker.Listener.Addr().(*net.TCPAddr).IP.String(),
			"port":            strconv.Itoa(attacker.Listener.Addr().(*net.TCPAddr).Port),
			"read_only":       "false",
			"require_auth":    "true",
			"data_dir":        dataDir,
			"auth_token_path": runtimelock.AuthTokenPath(dataDir),
		},
	}
	discovery := backgroundDiscovery{dataDir: dataDir, version: "v-test"}

	_, compatible, err := discovery.probe(t.Context(), record, "daemon-secret")

	require.NoError(t, err)
	assert.False(t, compatible)
	assert.NotZero(t, requests.Load())
	assert.Empty(t, receivedAuthorization.Load())
}

func newBackgroundProofServer(
	t *testing.T,
	dataDir, token, version string,
	ready func() bool,
) (*httptest.Server, daemon.RuntimeRecord) {
	t.Helper()
	server := httptest.NewUnstartedServer(nil)
	address := server.Listener.Addr().String()
	host, port, err := net.SplitHostPort(address)
	require.NoError(t, err)
	record := daemon.RuntimeRecord{
		PID: os.Getpid(), Network: daemon.NetworkTCP, Address: address,
		Service: "middleman", Version: version, StartedAt: time.Now().UTC(),
		Metadata: map[string]string{
			"host": host, "port": port, "read_only": "false",
			"require_auth": "true", "data_dir": dataDir,
			"auth_token_path": runtimelock.AuthTokenPath(dataDir),
		},
	}
	proof, err := daemon.NewProof([]byte(token))
	require.NoError(t, err)
	ping, err := proof.NewPingHandler(record)
	require.NoError(t, err)
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ready != nil && !ready() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		ping.ServeHTTP(w, r)
	})
	server.Start()
	t.Cleanup(server.Close)
	return server, record
}
