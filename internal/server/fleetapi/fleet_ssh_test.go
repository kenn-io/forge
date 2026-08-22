package fleetapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/fleet"
	"go.kenn.io/forge/internal/ptysize"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/sshfleet"
	"go.kenn.io/kit/openssh"
)

// fakeSSHExec scripts the remote api -i verb: requests are recorded
// (method+path plus stdin body) and answered from the routes map by
// "METHOD path" key.
type fakeSSHExec struct {
	calls  []string
	bodies [][]byte
	routes map[string]string // "METHOD /path" -> framed output
}

const testSSHControlPath = "/tmp/forge-test-control.sock"

type fakePersistentConnections struct {
	mu         sync.Mutex
	state      string
	connectErr error
}

func (f *fakePersistentConnections) Connect(
	context.Context,
	string,
	openssh.Target,
) (openssh.Generation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.connectErr != nil {
		return 0, f.connectErr
	}
	f.state = openssh.StateConnected
	return 1, nil
}

func TestSSHFleetRelayFallsBackToMasterlessSSH(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	connections := &fakePersistentConnections{
		connectErr: openssh.ErrPersistentUnsupported,
	}
	var argv []string
	exec := func(
		_ context.Context,
		arguments []string,
		_ []byte,
	) ([]byte, []byte, int, error) {
		argv = append([]string(nil), arguments...)
		return []byte(framedJSON(200, `{"ok":true}`)), nil, 0, nil
	}
	transport := &sshFleetTransport{
		conns:  connections,
		runner: sshfleet.NewRunnerWithExec(connections, exec),
	}

	result, err := transport.relay(
		context.Background(),
		config.FleetSSHPeer{Key: "build", Destination: "maintainer@build.example"},
		http.MethodGet,
		"/api/v1/snapshot/raw",
		nil,
	)

	require.NoError(err)
	assert.Equal(http.StatusOK, result.response.Status)
	assert.False(result.connection.Persistent)
	assert.Contains(argv, "none")
}

func TestFleetSnapshotReportsMasterlessSSHState(t *testing.T) {
	tests := []struct {
		name          string
		relayErr      error
		wantState     string
		wantReachable bool
		wantEvents    []string
	}{
		{
			name:          "successful relay is online",
			wantState:     "online",
			wantReachable: true,
			wantEvents: []string{
				openssh.StateConnecting,
				openssh.StateConnected,
			},
		},
		{
			name:      "failed relay is offline",
			relayErr:  errors.New("ssh unavailable"),
			wantState: "offline",
			wantEvents: []string{
				openssh.StateConnecting,
				openssh.StateError,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			connections := &fakePersistentConnections{
				connectErr: openssh.ErrPersistentUnsupported,
			}
			exec := func(
				context.Context,
				[]string,
				[]byte,
			) ([]byte, []byte, int, error) {
				if tt.relayErr != nil {
					return nil, nil, -1, tt.relayErr
				}
				raw := `{"schemaVersion":2,"host":{"hostname":"build","platform":"linux"}}`
				return []byte(framedJSON(http.StatusOK, raw)), nil, 0, nil
			}
			var eventMu sync.Mutex
			var eventStates []string
			transport := &sshFleetTransport{
				conns:  connections,
				runner: sshfleet.NewRunnerWithExec(connections, exec),
				peers: []config.FleetSSHPeer{{
					Key: "build", Destination: "maintainer@build.example",
				}},
				broadcast: func(event Event) uint64 {
					stateEvent, ok := event.Data.(sshFleetConnectionEvent)
					if event.Type == "fleet_host_state" && ok {
						eventMu.Lock()
						eventStates = append(eventStates, stateEvent.State)
						eventMu.Unlock()
					}
					return 0
				},
			}

			srv, _ := setupTestServer(t)
			setTestFleetConfig(srv, func(cfg *config.Config) {
				cfg.Fleet.Enabled = true
				cfg.Fleet.Key = "studio"
			})
			srv.sshFleet = transport
			ts := httptest.NewServer(srv.localHandler())
			defer ts.Close()

			resp := httpDo(
				t, ts, http.MethodGet,
				"/api/v1/snapshot?include_peers=true", nil,
			)
			require.Equal(http.StatusOK, resp.StatusCode)
			var snapshot struct {
				Hosts []struct {
					ConfigKey       string  `json:"configKey"`
					Reachable       bool    `json:"reachable"`
					ConnectionState *string `json:"connectionState"`
				} `json:"hosts"`
			}
			require.NoError(json.NewDecoder(resp.Body).Decode(&snapshot))
			resp.Body.Close()

			var buildHost *struct {
				ConfigKey       string  `json:"configKey"`
				Reachable       bool    `json:"reachable"`
				ConnectionState *string `json:"connectionState"`
			}
			for i := range snapshot.Hosts {
				if snapshot.Hosts[i].ConfigKey == "build" {
					buildHost = &snapshot.Hosts[i]
				}
			}
			require.NotNil(buildHost)
			require.NotNil(buildHost.ConnectionState)
			assert.Equal(tt.wantState, *buildHost.ConnectionState)
			assert.Equal(tt.wantReachable, buildHost.Reachable)
			eventMu.Lock()
			assert.Equal(tt.wantEvents, eventStates)
			eventMu.Unlock()
		})
	}
}

func (f *fakePersistentConnections) State(string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state == "" {
		return openssh.StateDisconnected
	}
	return f.state
}

func (*fakePersistentConnections) StartIdleMonitor(context.Context) {}

func (*fakePersistentConnections) ConnectionArguments(
	string,
	openssh.Generation,
) ([]string, error) {
	return openssh.ClientArguments(testSSHControlPath)
}

func (*fakePersistentConnections) TouchActivity(
	string,
	openssh.Generation,
) bool {
	return true
}

func (f *fakeSSHExec) exec(
	_ context.Context, argv []string, stdin []byte,
) ([]byte, []byte, int, error) {
	// argv ends with: sh -lc '<PATH=...; kenn-forge api -i [-d @-] METHOD PATH'
	fragment := argv[len(argv)-1]
	fields := strings.Fields(fragment)
	trim := func(v string) string {
		return strings.Trim(v, `'\`)
	}
	method := trim(fields[len(fields)-2])
	path := trim(fields[len(fields)-1])
	key := method + " " + path
	f.calls = append(f.calls, key)
	f.bodies = append(f.bodies, stdin)
	framed, ok := f.routes[key]
	if !ok {
		return []byte("HTTP/1.1 404 Not Found\r\n\r\n{\"code\":\"notFound\"}"),
			nil, 1, nil
	}
	return []byte(framed), nil, 0, nil
}

func newSSHTestTransport(
	t *testing.T, fake *fakeSSHExec, peers ...config.FleetSSHPeer,
) *sshFleetTransport {
	return newSSHTestTransportWithExec(t, fake.exec, peers...)
}

func newSSHTestTransportWithExec(
	t *testing.T,
	exec func(
		context.Context, []string, []byte,
	) ([]byte, []byte, int, error),
	peers ...config.FleetSSHPeer,
) *sshFleetTransport {
	t.Helper()
	conns := &fakePersistentConnections{}
	return &sshFleetTransport{
		conns:  conns,
		runner: sshfleet.NewRunnerWithExec(conns, exec),
		peers:  peers,
	}
}

// setTestFleetConfig publishes the same committed snapshot as root reload.
func setTestFleetConfig(srv *Handler, mutate func(*config.Config)) {
	cfg := &config.Config{}
	mutate(cfg)
	srv.ApplyConfig(ConfigSnapshot{
		Fleet: cfg.Fleet,
		PlatformAuthConfig: config.Config{
			GitHubTokenEnv:      cfg.GitHubTokenEnv,
			DefaultPlatformHost: cfg.DefaultPlatformHost,
			Repos:               cfg.Repos,
			Platforms:           cfg.Platforms,
		},
		PlatformAuthEnabled: true,
		TmuxCommand:         []string{"kenn-forge-no-such-tmux"},
	})
}

func framedJSON(status int, body string) string {
	return fmt.Sprintf("HTTP/1.1 %d %s\r\n\r\n%s",
		status, http.StatusText(status), body)
}

// TestFleetSnapshotIncludesSSHPeers pins the read path over the wire:
// an ssh peer's raw snapshot merges into /api/v1/snapshot as a remote
// host with ssh transport, a mapped connection state, and — because
// the host is routable through the relay — NO hub availability
// suppression.
func TestFleetSnapshotIncludesSSHPeers(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	raw := `{"schemaVersion":2,"host":{"hostname":"epyc","platform":"linux"},` +
		`"capabilities":{"commands":{"worktreeCreate":true,"worktreeImportPullRequest":true,` +
		`"worktreeDelete":true,"sessionEnsure":true,"sessionKill":true,` +
		`"repositoryClone":true,"projectAdd":true,"projectRemove":true},` +
		`"dependencies":{"git":true,"gh":true,"tmux":true},` +
		`"features":{"resourceMetrics":false,"setupHook":false,"teardownHook":false,"moshAttach":false}}}`
	fake := &fakeSSHExec{routes: map[string]string{
		"GET /api/v1/snapshot/raw": framedJSON(200, raw),
	}}

	srv, _ := setupTestServer(t)
	setTestFleetConfig(srv, func(cfg *config.Config) {
		cfg.Fleet.Enabled = true
		cfg.Fleet.Key = "studio"
	})
	srv.sshFleet = newSSHTestTransport(t, fake, config.FleetSSHPeer{
		Key: "epyc", Name: "epyc", Destination: "wes@epyc.local",
		Platform: "linux",
	})
	ts := httptest.NewServer(srv.localHandler())
	defer ts.Close()

	resp := httpDo(t, ts, http.MethodGet, "/api/v1/snapshot?include_peers=true", nil)
	require.Equal(http.StatusOK, resp.StatusCode)
	var snapshot struct {
		Hosts []struct {
			ConfigKey             string  `json:"configKey"`
			Kind                  string  `json:"kind"`
			PreferredTransport    string  `json:"preferredTransport"`
			SSHDestination        *string `json:"sshDestination"`
			ConnectionState       *string `json:"connectionState"`
			OperationAvailability map[string]struct {
				Available bool `json:"available"`
			} `json:"operationAvailability"`
		} `json:"hosts"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&snapshot))
	resp.Body.Close()

	var epyc *struct {
		ConfigKey             string  `json:"configKey"`
		Kind                  string  `json:"kind"`
		PreferredTransport    string  `json:"preferredTransport"`
		SSHDestination        *string `json:"sshDestination"`
		ConnectionState       *string `json:"connectionState"`
		OperationAvailability map[string]struct {
			Available bool `json:"available"`
		} `json:"operationAvailability"`
	}
	for i := range snapshot.Hosts {
		if snapshot.Hosts[i].ConfigKey == "epyc" {
			epyc = &snapshot.Hosts[i]
		}
	}
	require.NotNil(epyc, "ssh peer must appear in the snapshot")
	assert.Equal("remote", epyc.Kind)
	assert.Equal("ssh", epyc.PreferredTransport)
	require.NotNil(epyc.SSHDestination)
	assert.Equal("wes@epyc.local", *epyc.SSHDestination)
	require.NotNil(epyc.ConnectionState)
	assert.Equal("online", *epyc.ConnectionState)
	assert.True(epyc.OperationAvailability["worktreeCreate"].Available,
		"ssh peers are routable; the hub must not suppress their mutations")
	assert.True(epyc.OperationAvailability["repositoryClone"].Available)
}

// TestSSHFleetProxyRelaysWrites pins the write path: a fleet proxy
// route for an ssh peer rides the relay with the request body and
// surfaces the remote's exact status and body.
func TestSSHFleetProxyRelaysWrites(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	fake := &fakeSSHExec{routes: map[string]string{
		"POST /api/v1/projects/prj_1/worktrees": framedJSON(
			201, `{"id":"wtr_9","branch":"feat"}`,
		),
	}}
	srv, _ := setupTestServer(t)
	setTestFleetConfig(srv, func(cfg *config.Config) {
		cfg.Fleet.Enabled = true
	})
	srv.sshFleet = newSSHTestTransport(t, fake, config.FleetSSHPeer{
		Key: "epyc", Destination: "wes@epyc.local",
	})
	ts := httptest.NewServer(srv.localHandler())
	defer ts.Close()

	resp := httpDo(t, ts, http.MethodPost,
		"/api/v1/fleet/hosts/epyc/projects/prj_1/worktrees",
		[]byte(`{"branch":"feat","create_on_disk":true}`),
	)
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		require.Failf("unexpected relay status",
			"relay status %d: %s (calls: %v)", resp.StatusCode, raw, fake.calls)
	}
	var created struct {
		ID string `json:"id"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&created))
	resp.Body.Close()
	assert.Equal("wtr_9", created.ID)

	require.Contains(fake.calls, "POST /api/v1/projects/prj_1/worktrees")
	var relayedBody []byte
	for i, c := range fake.calls {
		if c == "POST /api/v1/projects/prj_1/worktrees" {
			relayedBody = fake.bodies[i]
		}
	}
	assert.Contains(string(relayedBody), "create_on_disk",
		"request body must ride the relay verbatim")
}

func TestSSHFleetProxyRelaysWorkspaceDiffReads(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	fake := &fakeSSHExec{routes: map[string]string{
		"GET /api/v1/workspaces/ws_1/diff/watch?version=snapshot-7": framedJSON(
			http.StatusOK,
			`{"changed":true,"version":"snapshot-8"}`,
		),
		"GET /api/v1/workspaces/ws_1/diff?base=merge-target&revision=snapshot-7&whitespace=hide": framedJSON(
			http.StatusOK,
			`{"stale":false,"files":[{"path":"remote.go","status":"modified"}]}`,
		),
		"GET /api/v1/workspaces/ws_1/file-preview?base=merge-target&path=remote.go&revision=snapshot-7": framedJSON(
			http.StatusOK,
			`{"path":"remote.go","content":"package remote"}`,
		),
		"GET /api/v1/workspaces/ws_1/diff?base=merge-target&revision=stale": framedJSON(
			http.StatusConflict,
			`{"code":"conflict","detail":"snapshot changed","details":{"reason":"snapshot_changed"}}`,
		),
	}}
	srv, _ := setupTestServer(t)
	setTestFleetConfig(srv, func(cfg *config.Config) {
		cfg.Fleet.Enabled = true
	})
	srv.sshFleet = newSSHTestTransport(t, fake, config.FleetSSHPeer{
		Key: "epyc", Destination: "wes@epyc.local",
	})
	ts := httptest.NewServer(srv.localHandler())
	defer ts.Close()

	resp := httpDo(t, ts, http.MethodGet,
		"/api/v1/fleet/hosts/epyc/workspaces/ws_1/diff?base=merge-target&revision=snapshot-7&whitespace=hide", nil)
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		require.Failf("unexpected relay status",
			"relay status %d: %s (calls: %v)", resp.StatusCode, raw, fake.calls)
	}
	var diff struct {
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&diff))
	resp.Body.Close()

	require.Len(diff.Files, 1)
	assert.Equal("remote.go", diff.Files[0].Path)
	assert.Contains(fake.calls,
		"GET /api/v1/workspaces/ws_1/diff?base=merge-target&revision=snapshot-7&whitespace=hide")

	previewResp := httpDo(t, ts, http.MethodGet,
		"/api/v1/fleet/hosts/epyc/workspaces/ws_1/file-preview?base=merge-target&path=remote.go&revision=snapshot-7", nil)
	require.Equal(http.StatusOK, previewResp.StatusCode)
	previewResp.Body.Close()
	assert.Contains(fake.calls,
		"GET /api/v1/workspaces/ws_1/file-preview?base=merge-target&path=remote.go&revision=snapshot-7")

	staleResp := httpDo(t, ts, http.MethodGet,
		"/api/v1/fleet/hosts/epyc/workspaces/ws_1/diff?base=merge-target&revision=stale", nil)
	assert.Equal(http.StatusConflict, staleResp.StatusCode)
	staleBody, err := io.ReadAll(staleResp.Body)
	require.NoError(err)
	staleResp.Body.Close()
	assert.Contains(string(staleBody), `"reason":"snapshot_changed"`)

	watchResp := httpDo(t, ts, http.MethodGet,
		"/api/v1/fleet/hosts/epyc/workspaces/ws_1/diff/watch?version=snapshot-7", nil)
	require.Equal(http.StatusOK, watchResp.StatusCode)
	var watch struct {
		Changed bool   `json:"changed"`
		Version string `json:"version"`
	}
	require.NoError(json.NewDecoder(watchResp.Body).Decode(&watch))
	require.NoError(watchResp.Body.Close())
	assert.True(watch.Changed)
	assert.Equal("snapshot-8", watch.Version)
	assert.Contains(fake.calls,
		"GET /api/v1/workspaces/ws_1/diff/watch?version=snapshot-7")
}

func TestSSHFleetDiffWatchCancellationReachesRemoteRelay(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	started := make(chan struct{})
	canceled := make(chan struct{})
	exec := func(
		ctx context.Context, _ []string, _ []byte,
	) ([]byte, []byte, int, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return nil, nil, 0, ctx.Err()
	}

	srv, _ := setupTestServer(t)
	setTestFleetConfig(srv, func(cfg *config.Config) {
		cfg.Fleet.Enabled = true
	})
	srv.sshFleet = newSSHTestTransportWithExec(t, exec, config.FleetSSHPeer{
		Key: "epyc", Destination: "wes@epyc.local",
	})
	ts := httptest.NewServer(srv.localHandler())
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithCancel(t.Context())
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		ts.URL+"/api/v1/fleet/hosts/epyc/workspaces/ws_1/diff/watch",
		nil,
	)
	require.NoError(err)
	done := make(chan error, 1)
	go func() {
		response, requestErr := http.DefaultClient.Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		done <- requestErr
	}()

	<-started
	cancel()
	assert.Eventually(func() bool {
		select {
		case <-canceled:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)
	require.ErrorIs(<-done, context.Canceled)
}

// TestSSHFleetAttachSpecWrapped pins the attach contract: a peer's attach-spec
// comes back wrapped in the hub's generation-bound ssh invocation and drops
// requires_local_host, so a client runs it from the hub host.
func TestSSHFleetAttachSpecWrapped(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	remoteSpec := `{"version":1,"kind":"tmux","session_key":"s1","target_key":"",` +
		`"tmux_session":"mm-s1","command":["tmux","-u","attach-session","-t","mm-s1"],` +
		`"requires_local_host":true}`
	fake := &fakeSSHExec{routes: map[string]string{
		"GET /api/v1/runtime/sessions/s1/attach-spec": framedJSON(200, remoteSpec),
	}}
	srv, _ := setupTestServer(t)
	setTestFleetConfig(srv, func(cfg *config.Config) {
		cfg.Fleet.Enabled = true
	})
	srv.sshFleet = newSSHTestTransport(t, fake, config.FleetSSHPeer{
		Key: "epyc", Destination: "wes@epyc.local",
	})
	ts := httptest.NewServer(srv.localHandler())
	defer ts.Close()

	resp := httpDo(t, ts, http.MethodGet,
		"/api/v1/fleet/hosts/epyc/runtime/sessions/s1/attach-spec", nil)
	require.Equal(http.StatusOK, resp.StatusCode)
	var spec struct {
		Command           []string `json:"command"`
		RequiresLocalHost bool     `json:"requires_local_host"`
		TmuxSession       string   `json:"tmux_session"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&spec))
	resp.Body.Close()

	require.NotEmpty(spec.Command)
	assert.Equal("ssh", spec.Command[0])
	joined := strings.Join(spec.Command, " ")
	assert.Contains(joined, testSSHControlPath)
	assert.Contains(spec.Command, "-t")
	assert.Contains(spec.Command, "wes@epyc.local")
	assert.Contains(joined, "tmux -u attach-session -t mm-s1")
	assert.False(spec.RequiresLocalHost,
		"the wrapped spec runs from the hub host")
	assert.Equal("mm-s1", spec.TmuxSession)
}

// TestSSHFleetSnapshotDegradesColdPeerFast pins the bounded fan-out
// over the wire: a snapshot read against a cold (blocked) ssh peer
// returns within the fleet peer timeout with a degraded host carrying
// the warming diagnostic, repeated reads share ONE in-flight fetch,
// and once the fetch completes a later read reports the host
// reachable.
func TestSSHFleetSnapshotDegradesColdPeerFast(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	raw := `{"schemaVersion":2,"host":{"hostname":"epyc","platform":"linux"}}`
	release := make(chan struct{})
	var fetches atomic.Int32
	started := make(chan struct{})
	var startedOnce sync.Once
	exec := func(
		_ context.Context, argv []string, _ []byte,
	) ([]byte, []byte, int, error) {
		fetches.Add(1)
		startedOnce.Do(func() { close(started) })
		<-release
		return []byte(framedJSON(200, raw)), nil, 0, nil
	}

	conns := &fakePersistentConnections{}
	srv, _ := setupTestServer(t)
	setTestFleetConfig(srv, func(cfg *config.Config) {
		cfg.Fleet.Enabled = true
		cfg.Fleet.Key = "studio"
		cfg.Fleet.PeerTimeout = "150ms"
	})
	srv.sshFleet = &sshFleetTransport{
		conns:  conns,
		runner: sshfleet.NewRunnerWithExec(conns, exec),
		peers: []config.FleetSSHPeer{{
			Key: "epyc", Destination: "wes@epyc.local",
		}},
	}
	ts := httptest.NewServer(srv.localHandler())
	defer ts.Close()

	hostByKey := func() map[string]struct {
		Reachable bool    `json:"reachable"`
		Error     *string `json:"error"`
	} {
		resp := httpDo(t, ts, http.MethodGet,
			"/api/v1/snapshot?include_peers=true", nil)
		require.Equal(http.StatusOK, resp.StatusCode)
		var snapshot struct {
			Hosts []struct {
				ConfigKey string  `json:"configKey"`
				Reachable bool    `json:"reachable"`
				Error     *string `json:"error"`
			} `json:"hosts"`
		}
		require.NoError(json.NewDecoder(resp.Body).Decode(&snapshot))
		resp.Body.Close()
		out := make(map[string]struct {
			Reachable bool    `json:"reachable"`
			Error     *string `json:"error"`
		})
		for _, h := range snapshot.Hosts {
			out[h.ConfigKey] = struct {
				Reachable bool    `json:"reachable"`
				Error     *string `json:"error"`
			}{h.Reachable, h.Error}
		}
		return out
	}

	start := time.Now()
	first := hostByKey()
	// peer_timeout is 150ms; the bound leaves scheduler and local
	// snapshot/enrichment slack but stays below the 2s default so a
	// regression that ignores the configured timeout fails here.
	require.Less(time.Since(start), 1500*time.Millisecond,
		"cold peer must degrade within the configured peer timeout")
	epyc, ok := first["epyc"]
	require.True(ok, "degraded ssh host still appears")
	assert.False(epyc.Reachable)
	require.NotNil(epyc.Error)
	assert.Contains(*epyc.Error, "warming")

	select {
	case <-started:
	case <-time.After(time.Second):
		require.Fail("cold snapshot fetch did not start")
	}

	// A second read while the fetch is still blocked must not start
	// another fetch.
	second := hostByKey()
	assert.False(second["epyc"].Reachable)
	assert.Equal(int32(1), fetches.Load(),
		"snapshot reads share one in-flight fetch per peer")

	close(release)
	require.Eventually(func() bool {
		return hostByKey()["epyc"].Reachable
	}, 5*time.Second, 100*time.Millisecond,
		"the warmed fetch must surface on a later read")
}

func TestSSHFleetWarmupIsCanceledAndDrainedByShutdown(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	exec := func(
		ctx context.Context, _ []string, _ []byte,
	) ([]byte, []byte, int, error) {
		calls.Add(1)
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return nil, nil, 1, ctx.Err()
	}
	conns := &fakePersistentConnections{}
	h := New(Deps{})
	transport := &sshFleetTransport{
		conns: conns, runner: sshfleet.NewRunnerWithExec(conns, exec),
		peers: []config.FleetSSHPeer{{Key: "epyc", Destination: "dev@epyc"}},
	}
	h.sshFleet = transport
	peer := transport.peers[0]
	fetchDone := make(chan fleet.PeerResult, 1)
	go func() {
		fetchDone <- h.fetchSSHPeerRawBounded(t.Context(), transport, peer, time.Second)
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		require.Fail("warm-up did not start")
	}
	shutdownCtx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	require.ErrorIs(h.Shutdown(shutdownCtx), context.DeadlineExceeded,
		"Shutdown must wait for the canceled warm-up to return")
	select {
	case <-canceled:
	case <-time.After(time.Second):
		require.Fail("warm-up did not observe Fleet cancellation")
	}

	close(release)
	require.NoError(h.Shutdown(t.Context()))
	assert.NoError(h.Shutdown(t.Context()))
	select {
	case <-fetchDone:
	case <-time.After(time.Second):
		require.Fail("canceled warm-up did not publish completion")
	}

	result := h.fetchSSHPeerRawBounded(t.Context(), transport, peer, time.Second)
	assert.False(result.Reachable)
	assert.Equal(int32(1), calls.Load(), "stopped Fleet must reject new warm-ups")
}

// TestSSHFleetRelayAutoStartsRemoteDaemon pins the ensure-then-retry
// contract over the wire: a proxied write that finds no daemon on the
// peer (api verb exit 2) starts one detached (`nohup ... serve`),
// waits for the status probe to report running, retries the relay
// once, and the client sees only the successful response.
func TestSSHFleetRelayAutoStartsRemoteDaemon(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	// The fake daemon mirrors real startup: serve takes the lock
	// (running flips true) but the runtime metadata trails it, and
	// the api verb keeps exiting 2 until metadata is published.
	var mu sync.Mutex
	started := false
	metadataLagProbes := 2
	serveStarts := 0
	ready := func() bool { return started && metadataLagProbes <= 0 }
	exec := func(
		_ context.Context, argv []string, _ []byte,
	) ([]byte, []byte, int, error) {
		mu.Lock()
		defer mu.Unlock()
		fragment := argv[len(argv)-1]
		switch {
		case strings.Contains(fragment, "status"):
			metadata := "null"
			if started {
				if metadataLagProbes > 0 {
					metadataLagProbes--
				} else {
					metadata = `{"pid":1234}`
				}
			}
			return fmt.Appendf(nil,
				`{"running":%v,"metadata":%s}`, started, metadata,
			), nil, 0, nil
		case strings.Contains(fragment, "serve"):
			serveStarts++
			assert.Contains(fragment, "nohup")
			started = true
			return nil, nil, 0, nil
		case strings.Contains(fragment, "api"):
			if !ready() {
				return nil, []byte("no kenn-forge daemon is running"),
					2, nil
			}
			return []byte(framedJSON(201, `{"id":"wtr_1"}`)), nil, 0, nil
		}
		return nil, []byte("unexpected fragment: " + fragment), 1, nil
	}

	fake := &fakeSSHExec{}
	transport := newSSHTestTransport(t, fake, config.FleetSSHPeer{
		Key: "epyc", Destination: "wes@epyc.local",
	})
	transport.runner = sshfleet.NewRunnerWithExec(transport.conns, exec)
	srv, _ := setupTestServer(t)
	setTestFleetConfig(srv, func(cfg *config.Config) {
		cfg.Fleet.Enabled = true
		cfg.Fleet.Key = "studio"
	})
	srv.sshFleet = transport
	ts := httptest.NewServer(srv.localHandler())
	defer ts.Close()

	resp := httpDo(t, ts, http.MethodPost,
		"/api/v1/fleet/hosts/epyc/projects/prj_1/worktrees",
		[]byte(`{"branch":"feat"}`))
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	require.NoError(err)
	require.Equal(http.StatusCreated, resp.StatusCode, string(out))
	assert.Contains(string(out), "wtr_1")
	mu.Lock()
	assert.Equal(1, serveStarts)
	assert.True(ready(),
		"the relay retry must wait out the metadata lag")
	mu.Unlock()
}

func TestSSHFleetWebSocketTerminalUsesAttachSpecCommand(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	recorder := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	prevProp := otel.GetTextMapPropagator()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
		otel.SetTextMapPropagator(prevProp)
	})

	srv, _ := setupTestServer(t)
	setTestFleetConfig(srv, func(cfg *config.Config) {
		cfg.Fleet.Enabled = true
	})
	writeFakeSSHForAttach(t)
	remoteSpec := workspaceapi.RuntimeAttachSpecResponse{
		Version:           1,
		Kind:              "tmux",
		SessionKey:        "sess-1",
		TargetKey:         "shell",
		TmuxSession:       "mm-sess-1",
		Command:           serverRuntimeHelperCommand("echo"),
		RequiresLocalHost: true,
	}
	remoteSpecBody, err := json.Marshal(remoteSpec)
	require.NoError(err)
	fake := &fakeSSHExec{routes: map[string]string{
		"GET /api/v1/workspaces/ws_1/runtime/sessions/sess-1/attach-spec": framedJSON(200, string(remoteSpecBody)),
	}}
	srv.sshFleet = newSSHTestTransport(
		t, fake, config.FleetSSHPeer{
			Key: "epyc", Destination: "wes@epyc.local",
		},
	)

	ts := httptest.NewServer(srv.localHandler())
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") +
		"/ws/v1/fleet/hosts/epyc/workspaces/ws_1/runtime/sessions/sess-1/terminal" +
		"?cols=80&rows=24" +
		"&traceparent=00-33333333333333333333333333333333-4444444444444444-01" +
		"&baggage=interaction%3Dworkspace-switch%2Chost.key%3Depyc"
	conn, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		CompressionMode: websocket.CompressionContextTakeover,
	})
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "test done")
	require.NotNil(resp)
	extensions := resp.Header.Get("Sec-WebSocket-Extensions")
	assert.Contains(extensions, "permessage-deflate")
	assert.NotContains(extensions, "client_no_context_takeover")
	assert.NotContains(extensions, "server_no_context_takeover")

	require.NoError(conn.Write(ctx, websocket.MessageBinary, []byte("ping\n")))
	readWebSocketBinaryUntil(t, ctx, conn, 5*time.Second, "echo:ping")
	require.Contains(fake.calls,
		"GET /api/v1/workspaces/ws_1/runtime/sessions/sess-1/attach-spec")

	var attachSpan sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		if span.Name() == "terminal.attach" {
			attachSpan = span
		}
	}
	require.NotNil(attachSpan, "bounded attach span must end before the websocket bridge")
	assert.Equal("33333333333333333333333333333333", attachSpan.SpanContext().TraceID().String())
	assert.Equal("4444444444444444", attachSpan.Parent().SpanID().String())
	attrs := map[string]string{}
	for _, kv := range attachSpan.Attributes() {
		attrs[string(kv.Key)] = kv.Value.AsString()
	}
	assert.Equal("epyc", attrs["host.key"])
}

func TestSSHFleetWebSocketTerminalClaimsSharedResizeOwnership(t *testing.T) {
	require := require.New(t)
	const readTimeout = 5 * time.Second

	srv, _ := setupTestServer(t)
	setTestFleetConfig(srv, func(cfg *config.Config) {
		cfg.Fleet.Enabled = true
	})
	writeFakeSSHForAttach(t)
	remoteSpec := workspaceapi.RuntimeAttachSpecResponse{
		Version:     1,
		Kind:        "tmux",
		SessionKey:  "sess-1",
		TargetKey:   "shell",
		TmuxSession: "mm-sess-1",
		Command: []string{
			"sh",
			"-lc",
			`while IFS= read -r line; do set -- $(stty size); printf 'size:%s:%s:%s\n' "$1" "$2" "$line"; done`,
		},
	}
	remoteSpecBody, err := json.Marshal(remoteSpec)
	require.NoError(err)
	fake := &fakeSSHExec{routes: map[string]string{
		"GET /api/v1/workspaces/ws_1/runtime/sessions/sess-1/attach-spec": framedJSON(200, string(remoteSpecBody)),
	}}
	srv.sshFleet = newSSHTestTransport(
		t, fake, config.FleetSSHPeer{
			Key: "epyc", Destination: "wes@epyc.local",
		},
	)

	ts := httptest.NewServer(srv.localHandler())
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") +
		"/ws/v1/fleet/hosts/epyc/workspaces/ws_1/runtime/sessions/sess-1/terminal"
	first, _, err := websocket.Dial(
		ctx, wsURL+"?cols=80&rows=24&resize_active=1", nil,
	)
	require.NoError(err)
	defer first.Close(websocket.StatusNormalClosure, "test done")
	second, _, err := websocket.Dial(
		ctx, wsURL+"?cols=50&rows=12&resize_active=0", nil,
	)
	require.NoError(err)

	require.NoError(first.Write(ctx, websocket.MessageBinary, []byte("before\n")))
	readWebSocketBinaryUntil(t, ctx, first, readTimeout, "size:24:80:before")

	require.NoError(second.Write(
		ctx,
		websocket.MessageText,
		[]byte(`{"type":"claim_resize","cols":90,"rows":25}`),
	))
	require.NoError(second.Write(ctx, websocket.MessageBinary, []byte("second\n")))
	readWebSocketBinaryUntil(t, ctx, second, readTimeout, "size:25:90:second")
	require.NoError(first.Write(ctx, websocket.MessageBinary, []byte("shared\n")))
	readWebSocketBinaryUntil(t, ctx, first, readTimeout, "size:25:90:shared")

	require.NoError(first.Write(
		ctx,
		websocket.MessageText,
		[]byte(`{"type":"resize","cols":100,"rows":31}`),
	))
	require.NoError(first.Write(ctx, websocket.MessageBinary, []byte("stale\n")))
	readWebSocketBinaryUntil(t, ctx, first, readTimeout, "size:25:90:stale")

	require.NoError(second.Close(websocket.StatusNormalClosure, "test done"))
	require.NoError(first.Write(ctx, websocket.MessageBinary, []byte("fallback\n")))
	readWebSocketBinaryUntil(t, ctx, first, readTimeout, "size:31:100:fallback")
}

func TestFleetResizeClaimActivatesAndAppliesCompleteGeometry(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	firstPTMX, firstTTY, err := pty.Open()
	require.NoError(err)
	t.Cleanup(func() {
		_ = firstPTMX.Close()
		_ = firstTTY.Close()
	})
	secondPTMX, secondTTY, err := pty.Open()
	require.NoError(err)
	t.Cleanup(func() {
		_ = secondPTMX.Close()
		_ = secondTTY.Close()
	})

	group := new(fleetSSHResizeGroup)
	group.register(
		&fleetSSHPTYAttachment{ptmx: firstPTMX}, true,
		ptysize.Geometry{Cols: 80, Rows: 24, PixelWidth: 640, PixelHeight: 384},
	)
	second := group.register(
		&fleetSSHPTYAttachment{ptmx: secondPTMX}, false,
		ptysize.Geometry{Cols: 50, Rows: 12, PixelWidth: 400, PixelHeight: 192},
	)

	handleFleetSSHAttachControl(second, []byte(
		`{"type":"claim_resize","cols":100,"rows":40,"pixel_width":875,"pixel_height":740}`,
	))

	want := &pty.Winsize{Cols: 100, Rows: 40, X: 875, Y: 740}
	firstSize, err := pty.GetsizeFull(firstPTMX)
	require.NoError(err)
	secondSize, err := pty.GetsizeFull(secondPTMX)
	require.NoError(err)
	assert.Equal(want, firstSize)
	assert.Equal(want, secondSize)
}

func TestSSHFleetAttachPTYWritesExitFrameWhenPTYEOFPrecedesWait(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	ptyReader, ptyWriter, err := os.Pipe()
	require.NoError(err)
	done := make(chan int, 1)
	attach := &fleetSSHPTYAttachment{
		ptmx: ptyReader,
		done: done,
	}
	resizeMember := new(fleetSSHResizeGroup).register(
		attach, true, ptysize.Geometry{Cols: 120, Rows: 30},
	)

	bridgeDone := make(chan struct{})
	acceptErrors := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
				InsecureSkipVerify: true,
			})
			if err != nil {
				acceptErrors <- err
				return
			}
			bridgeFleetSSHAttachPTY(r.Context(), conn, attach, resizeMember)
			conn.Close(websocket.StatusNormalClosure, "test done")
			close(bridgeDone)
		},
	))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	_, err = ptyWriter.Write([]byte("ssh-output"))
	require.NoError(err)
	require.NoError(ptyWriter.Close())
	time.AfterFunc(25*time.Millisecond, func() {
		done <- 7
		close(done)
	})

	var sawOutput bool
	for {
		typ, data, readErr := conn.Read(ctx)
		require.NoError(readErr)
		if typ == websocket.MessageBinary {
			sawOutput = sawOutput || strings.Contains(string(data), "ssh-output")
			continue
		}
		if typ != websocket.MessageText {
			continue
		}
		var msg struct {
			Type string `json:"type"`
			Code int    `json:"code"`
		}
		require.NoError(json.Unmarshal(data, &msg))
		assert.True(sawOutput)
		assert.Equal("exited", msg.Type)
		assert.Equal(7, msg.Code)
		break
	}

	select {
	case <-bridgeDone:
	case err := <-acceptErrors:
		require.NoError(err)
	case <-ctx.Done():
		require.NoError(ctx.Err())
	}
}

func writeFakeSSHForAttach(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ssh")
	script := `#!/bin/sh
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o)
      shift 2
      ;;
    -S|-p)
      shift 2
      ;;
    -t)
      shift
      ;;
    --)
      shift
      break
      ;;
    *)
      break
      ;;
  esac
done
if [ "$#" -gt 0 ]; then
  shift
fi
# Real OpenSSH joins the remote arguments with spaces and hands the
# result to the remote shell; mirror that so quoting behavior matches.
exec sh -c "$*"
`
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestServerRuntimeHelperProcessForFleetSSH(t *testing.T) {
	args := os.Args
	if sep := slices.Index(args, "--"); sep >= 0 {
		args = args[sep+1:]
	}
	if len(args) > 0 && args[0] == serverRuntimeHelperMarker {
		TestServerRuntimeHelperProcess(t)
	}
}
