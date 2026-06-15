package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/fleet"
	"go.kenn.io/middleman/internal/sshfleet"
)

// SSH fleet peers are hosts the hub reaches over ssh(1) instead of
// HTTP: the hub owns one ControlMaster per peer (sshfleet) and
// relays every API exchange by executing the peer's CLI api verb
// through it. Reads (raw snapshots) and writes (fleet proxy routes)
// share that channel; attach-specs come back wrapped in the ssh
// invocation so clients run them from the hub's host. Connection
// state transitions broadcast on the event hub as fleet_host_state
// and feed the enriched snapshot's connectionState.

// sshFleetTransport bundles the connection manager, relay runner,
// and the configured peer set.
type sshFleetTransport struct {
	conns  *sshfleet.ConnectionManager
	runner *sshfleet.Runner

	mu    sync.RWMutex
	peers []config.FleetSSHPeer

	// stop ends the idle monitor on shutdown. ControlMaster processes
	// are deliberately left running (ControlPersist semantics): a
	// restarted daemon adopts live sockets instead of re-dialing, and
	// the idle monitor reaps masters with no activity.
	stop     chan struct{}
	stopOnce sync.Once

	// inflight single-flights the per-peer snapshot fetch so repeated
	// snapshot reads against a cold peer share one connect/fetch
	// instead of piling goroutines behind the connect mutex.
	inflightMu sync.Mutex
	inflight   map[string]*inflightFetch
}

// inflightFetch is one in-progress peer snapshot fetch; done closes
// when res is populated.
type inflightFetch struct {
	done chan struct{}
	res  fleet.PeerResult
}

// newSSHFleetTransport builds the transport; events broadcast on hub.
func newSSHFleetTransport(
	socketDir string,
	peers []config.FleetSSHPeer,
	hub *EventHub,
) *sshFleetTransport {
	conns := sshfleet.NewConnectionManager(socketDir, sshfleet.Config{
		IdleTimeout: 30 * time.Minute,
		OnEvent: func(e sshfleet.Event) {
			if hub != nil {
				hub.Broadcast(Event{
					Type: "fleet_host_state", Data: e,
				})
			}
		},
	})
	t := &sshFleetTransport{
		conns:  conns,
		runner: sshfleet.NewRunner(conns),
		peers:  peers,
		stop:   make(chan struct{}),
	}
	conns.StartIdleMonitor(t.stop)
	return t
}

// shutdown stops the idle monitor. Masters stay alive by design (see
// the stop field comment).
func (t *sshFleetTransport) shutdown() {
	if t == nil {
		return
	}
	t.stopOnce.Do(func() {
		if t.stop != nil {
			close(t.stop)
		}
	})
}

// peer returns the configured peer for hostKey.
func (t *sshFleetTransport) peer(hostKey string) (config.FleetSSHPeer, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, p := range t.peers {
		if p.Key == hostKey {
			return p, true
		}
	}
	return config.FleetSSHPeer{}, false
}

// snapshotPeers lists the current peer set.
func (t *sshFleetTransport) snapshotPeers() []config.FleetSSHPeer {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]config.FleetSSHPeer, len(t.peers))
	copy(out, t.peers)
	return out
}

// connectionState resolves the fleet connectionState string for
// hostKey, nil for hosts this transport does not own.
func (t *sshFleetTransport) connectionState(hostKey string) *string {
	if t == nil {
		return nil
	}
	if _, ok := t.peer(hostKey); !ok {
		return nil
	}
	return fleet.MapConnectionState(t.conns.State(hostKey))
}

// relay ensures the peer's master is up and relays one API exchange.
// A relay that finds no daemon on the peer (exit-2 contract) starts
// one detached, waits for its status probe, and retries once — so a
// freshly booted host serves the first request instead of erroring.
func (t *sshFleetTransport) relay(
	ctx context.Context,
	peer config.FleetSSHPeer,
	method, path string,
	body []byte,
) (sshfleet.Response, error) {
	if err := t.conns.Connect(peer.Key, peer.Destination); err != nil {
		return sshfleet.Response{}, err
	}
	remoteCommand := peer.RemoteCommandOrDefault()
	resp, err := t.runner.Relay(
		ctx, peer.Key, peer.Destination, remoteCommand,
		method, path, body,
	)
	if !errors.Is(err, sshfleet.ErrRemoteDaemonUnavailable) {
		return resp, err
	}
	if err := t.runner.EnsureDaemon(
		ctx, peer.Key, peer.Destination, remoteCommand,
	); err != nil {
		return sshfleet.Response{}, fmt.Errorf(
			"ensure remote daemon: %w", err,
		)
	}
	return t.runner.Relay(
		ctx, peer.Key, peer.Destination, remoteCommand,
		method, path, body,
	)
}

// fetchSSHPeerResults fans out raw-snapshot fetches to every SSH
// peer concurrently, mirroring the HTTP peer fan-out: a failed peer
// degrades (Reachable=false, Err set) instead of failing the merge.
// Each peer's wait is bounded by fleet.peer_timeout — a cold or
// blackholed peer degrades fast while its connect/fetch keeps warming
// in the background, so the next snapshot read benefits.
func (s *Server) fetchSSHPeerResults(ctx context.Context) []fleet.PeerResult {
	t := s.sshFleet
	if t == nil {
		return nil
	}
	peers := t.snapshotPeers()
	if len(peers) == 0 {
		return nil
	}
	timeout := 2 * time.Second
	if s.cfg != nil {
		timeout = s.cfg.Fleet.PeerTimeoutOrDefault()
	}
	results := make([]fleet.PeerResult, len(peers))
	var wg sync.WaitGroup
	for i, p := range peers {
		wg.Add(1)
		go func(i int, p config.FleetSSHPeer) {
			defer wg.Done()
			results[i] = s.fetchSSHPeerRawBounded(ctx, t, p, timeout)
		}(i, p)
	}
	wg.Wait()
	return results
}

// fetchSSHPeerRawBounded waits at most timeout for the peer fetch;
// on expiry it returns a degraded result while the underlying
// connect/fetch keeps running to warm the master. Concurrent and
// repeated snapshot reads share ONE in-flight fetch per peer instead
// of stacking goroutines behind the per-host connect mutex.
func (s *Server) fetchSSHPeerRawBounded(
	ctx context.Context,
	t *sshFleetTransport,
	p config.FleetSSHPeer,
	timeout time.Duration,
) fleet.PeerResult {
	t.inflightMu.Lock()
	if t.inflight == nil {
		t.inflight = make(map[string]*inflightFetch)
	}
	f := t.inflight[p.Key]
	if f == nil {
		f = &inflightFetch{done: make(chan struct{})}
		t.inflight[p.Key] = f
		go func() {
			f.res = s.fetchSSHPeerRaw(context.WithoutCancel(ctx), t, p)
			// Publish completion before retiring the entry: a reader
			// landing between the two either waits on the closed done
			// (and gets res) or misses the entry and starts a fresh
			// fetch — never both for the same in-flight result.
			close(f.done)
			t.inflightMu.Lock()
			delete(t.inflight, p.Key)
			t.inflightMu.Unlock()
		}()
	}
	t.inflightMu.Unlock()

	select {
	case <-f.done:
		return f.res
	case <-time.After(timeout):
	case <-ctx.Done():
	}
	dest := p.Destination
	msg := "ssh peer not ready within " + timeout.String() +
		" (connection warming in the background)"
	return fleet.PeerResult{
		Key:                p.Key,
		Name:               p.Name,
		Platform:           p.Platform,
		ObservedAt:         s.now().UTC().Format(time.RFC3339),
		SSHDestination:     &dest,
		PreferredTransport: "ssh",
		Err:                &msg,
	}
}

func (s *Server) fetchSSHPeerRaw(
	ctx context.Context,
	t *sshFleetTransport,
	p config.FleetSSHPeer,
) fleet.PeerResult {
	dest := p.Destination
	res := fleet.PeerResult{
		Key:                p.Key,
		Name:               p.Name,
		Platform:           p.Platform,
		ObservedAt:         s.now().UTC().Format(time.RFC3339),
		SSHDestination:     &dest,
		PreferredTransport: "ssh",
	}
	resp, err := t.relay(
		ctx, p, http.MethodGet, "/api/v1/snapshot/raw", nil,
	)
	if err != nil {
		res.Err = errPtr(err)
		return res
	}
	if resp.Status/100 != 2 {
		msg := "peer returned HTTP " + http.StatusText(resp.Status)
		res.Err = &msg
		return res
	}
	var raw fleet.RawSnapshot
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		msg := "decode raw snapshot: " + err.Error()
		res.Err = &msg
		return res
	}
	if raw.SchemaVersion != fleet.SchemaVersion {
		msg := "unsupported schemaVersion"
		res.Err = &msg
		return res
	}
	res.Reachable = true
	res.Raw = &raw
	return res
}

// serveSSHFleetRESTProxy relays a fleet proxy route to an SSH peer:
// the request body rides the relay verbatim, and the relayed status
// and body come back framed by the remote api -i verb. Attach-spec
// responses are wrapped so the returned command runs from this host.
func (s *Server) serveSSHFleetRESTProxy(
	w http.ResponseWriter,
	r *http.Request,
	peer config.FleetSSHPeer,
	targetPath string,
) {
	t := s.sshFleet
	if t == nil {
		writeProblemResponse(w, fleetHostNotFoundProblem(peer.Key))
		return
	}
	var body []byte
	if r.Body != nil {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			writeProblemResponse(w, newProblem(
				http.StatusBadRequest,
				CodeBadRequest,
				"read fleet relay body: "+err.Error(),
				map[string]any{"hostKey": peer.Key},
			))
			return
		}
		body = raw
	}
	path := targetPath
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	resp, err := t.relay(r.Context(), peer, r.Method, path, body)
	if err != nil {
		writeProblemResponse(w, newProblem(
			http.StatusBadGateway,
			CodeUpstreamError,
			"fleet ssh relay failed: "+err.Error(),
			map[string]any{"hostKey": peer.Key},
		))
		return
	}
	out := resp.Body
	if resp.Status/100 == 2 && isAttachSpecPath(targetPath) {
		if wrapped, ok := wrapAttachSpecForSSH(
			out, t.conns.SocketPath(peer.Key), peer.Destination,
		); ok {
			out = wrapped
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.Status)
	_, _ = w.Write(out)
}

func isAttachSpecPath(path string) bool {
	return strings.HasSuffix(path, "/attach-spec")
}

// wrapAttachSpecForSSH rewrites a peer's attach-spec so the command
// runs from the hub host through the peer's ControlMaster: the
// remote tmux attach rides `ssh -t`, and requires_local_host drops
// because the spec is now executable wherever the hub's socket is.
func wrapAttachSpecForSSH(
	body []byte, socketPath, destination string,
) ([]byte, bool) {
	var spec runtimeAttachSpecResponse
	if err := json.Unmarshal(body, &spec); err != nil {
		return nil, false
	}
	if len(spec.Command) == 0 {
		return nil, false
	}
	spec.Command = append([]string{
		"ssh",
		"-o", "ControlPath=" + socketPath,
		"-o", "ControlMaster=no",
		"-t", destination,
	}, spec.Command...)
	spec.RequiresLocalHost = false
	wrapped, err := json.Marshal(spec)
	if err != nil {
		return nil, false
	}
	return wrapped, true
}
