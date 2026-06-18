package server

import (
	"context"
	"encoding/json"
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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/sshfleet"
)

// fakeSSHExec scripts the remote api -i verb: requests are recorded
// (method+path plus stdin body) and answered from the routes map by
// "METHOD path" key. The fake connection RunSSH creates socket files
// so Connect succeeds without a real master.
type fakeSSHExec struct {
	calls  []string
	bodies [][]byte
	routes map[string]string // "METHOD /path" -> framed output
}

func (f *fakeSSHExec) exec(
	_ context.Context, argv []string, stdin []byte,
) ([]byte, []byte, int, error) {
	// argv ends with: sh -lc '<PATH=...; middleman api -i [-d @-] METHOD PATH'
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
	t.Helper()
	conns := sshfleet.NewConnectionManager(t.TempDir(), sshfleet.Config{
		RunSSH: func(args []string) (int, error) {
			for i, a := range args {
				if a == "-o" && strings.HasPrefix(args[i+1], "ControlPath=") {
					_ = os.WriteFile(
						strings.TrimPrefix(args[i+1], "ControlPath="),
						nil, 0o600,
					)
				}
			}
			return 0, nil
		},
	})
	return &sshFleetTransport{
		conns:  conns,
		runner: sshfleet.NewRunnerWithExec(conns, fake.exec),
		peers:  peers,
	}
}

// setTestFleetConfig swaps srv.cfg under cfgMu — the platform-auth
// monitor goroutine reads it through the same lock.
func setTestFleetConfig(srv *Server, mutate func(*config.Config)) {
	cfg := &config.Config{}
	mutate(cfg)
	srv.cfgMu.Lock()
	srv.cfg = cfg
	srv.cfgMu.Unlock()
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
	ts := httptest.NewServer(srv)
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
	ts := httptest.NewServer(srv)
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
		"GET /api/v1/workspaces/ws_1/diff?base=merge-target&whitespace=hide": framedJSON(
			http.StatusOK,
			`{"stale":false,"files":[{"path":"remote.go","status":"modified"}]}`,
		),
	}}
	srv, _ := setupTestServer(t)
	setTestFleetConfig(srv, func(cfg *config.Config) {
		cfg.Fleet.Enabled = true
	})
	srv.sshFleet = newSSHTestTransport(t, fake, config.FleetSSHPeer{
		Key: "epyc", Destination: "wes@epyc.local",
	})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp := httpDo(t, ts, http.MethodGet,
		"/api/v1/fleet/hosts/epyc/workspaces/ws_1/diff?base=merge-target&whitespace=hide", nil)
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
		"GET /api/v1/workspaces/ws_1/diff?base=merge-target&whitespace=hide")
}

// TestSSHFleetAttachSpecWrapped pins the attach contract: a peer's
// attach-spec comes back wrapped in the hub's ControlMaster ssh
// invocation and drops requires_local_host, so a client runs it from
// the hub host. The hub is the single ControlMaster owner — the
// socket path in the command is the hub's.
func TestSSHFleetAttachSpecWrapped(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	remoteSpec := `{"version":1,"kind":"tmux","session_key":"s1","target_key":"",` +
		`"tmux_session":"mm-s1","command":["tmux","attach-session","-t","mm-s1"],` +
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
	ts := httptest.NewServer(srv)
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
	assert.Contains(joined, "ControlPath="+srv.sshFleet.conns.SocketPath("epyc"))
	assert.Contains(joined, "-t wes@epyc.local")
	assert.Contains(joined, "tmux attach-session -t mm-s1")
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
	exec := func(
		_ context.Context, argv []string, _ []byte,
	) ([]byte, []byte, int, error) {
		fetches.Add(1)
		<-release
		return []byte(framedJSON(200, raw)), nil, 0, nil
	}

	conns := sshfleet.NewConnectionManager(t.TempDir(), sshfleet.Config{
		RunSSH: func(args []string) (int, error) {
			for i, a := range args {
				if a == "-o" && strings.HasPrefix(args[i+1], "ControlPath=") {
					_ = os.WriteFile(
						strings.TrimPrefix(args[i+1], "ControlPath="),
						nil, 0o600,
					)
				}
			}
			return 0, nil
		},
	})
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
	ts := httptest.NewServer(srv)
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
				return nil, []byte("no middleman daemon is running"),
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
	ts := httptest.NewServer(srv)
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

	fixture := setupWorkspaceServerFixture(t, nil)
	setTestFleetConfig(fixture.server, func(cfg *config.Config) {
		cfg.Fleet.Enabled = true
	})
	writeFakeSSHForAttach(t)
	remoteSpec := runtimeAttachSpecResponse{
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
	fixture.server.sshFleet = newSSHTestTransport(
		t, fake, config.FleetSSHPeer{
			Key: "epyc", Destination: "wes@epyc.local",
		},
	)

	ts := httptest.NewServer(fixture.server)
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") +
		"/ws/v1/fleet/hosts/epyc/workspaces/ws_1/runtime/sessions/sess-1/terminal?cols=80&rows=24"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "test done")

	require.NoError(conn.Write(ctx, websocket.MessageBinary, []byte("ping\n")))
	readWebSocketBinaryUntil(t, ctx, conn, 2*time.Second, "echo:ping")
	require.Contains(fake.calls,
		"GET /api/v1/workspaces/ws_1/runtime/sessions/sess-1/attach-spec")
}

func TestSSHFleetWebSocketTerminalHonorsResizeActive(t *testing.T) {
	require := require.New(t)

	fixture := setupWorkspaceServerFixture(t, nil)
	setTestFleetConfig(fixture.server, func(cfg *config.Config) {
		cfg.Fleet.Enabled = true
	})
	writeFakeSSHForAttach(t)
	remoteSpec := runtimeAttachSpecResponse{
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
	fixture.server.sshFleet = newSSHTestTransport(
		t, fake, config.FleetSSHPeer{
			Key: "epyc", Destination: "wes@epyc.local",
		},
	)

	ts := httptest.NewServer(fixture.server)
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") +
		"/ws/v1/fleet/hosts/epyc/workspaces/ws_1/runtime/sessions/sess-1/terminal?cols=80&rows=24&resize_active=0"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "test done")

	require.NoError(conn.Write(ctx, websocket.MessageBinary, []byte("before\n")))
	readWebSocketBinaryUntil(t, ctx, conn, 2*time.Second, "size:30:120:before")

	require.NoError(conn.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":81,"rows":25}`)))
	require.NoError(conn.Write(ctx, websocket.MessageBinary, []byte("inactive\n")))
	readWebSocketBinaryUntil(t, ctx, conn, 2*time.Second, "size:30:120:inactive")

	require.NoError(conn.Write(ctx, websocket.MessageText, []byte(`{"type":"resize_active","active":true}`)))
	require.NoError(conn.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":82,"rows":26}`)))
	require.NoError(conn.Write(ctx, websocket.MessageBinary, []byte("active\n")))
	readWebSocketBinaryUntil(t, ctx, conn, 2*time.Second, "size:26:82:active")
}

func TestSSHFleetAttachPTYWritesExitFrameWhenPTYEOFPrecedesWait(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	ptyReader, ptyWriter, err := os.Pipe()
	require.NoError(err)
	done := make(chan int, 1)
	attach := &fleetSSHPTYAttachment{
		ptmx:   ptyReader,
		done:   done,
		active: true,
	}

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
			bridgeFleetSSHAttachPTY(r.Context(), conn, attach)
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
    -t)
      shift
      if [ "$#" -gt 0 ]; then
        shift
      fi
      break
      ;;
    *)
      shift
      ;;
  esac
done
exec "$@"
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
