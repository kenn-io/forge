package fleetapi

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

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/fleet"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/sshfleet"
	"go.kenn.io/kit/openssh"
)

// SSH fleet peers are hosts the hub reaches over ssh(1) instead of HTTP. Kit's
// OpenSSH manager owns one persistent ControlMaster per peer where supported;
// otherwise the relay uses explicit masterless commands. Reads, writes, and
// terminal attach all use generation-bound arguments from the same manager.
// Connection state transitions broadcast on the event hub as fleet_host_state
// and feed the enriched snapshot's connectionState.

// sshFleetTransport bundles the shared connection manager, Forge relay runner,
// and the configured peer set.
type persistentConnectionManager interface {
	sshfleet.ConnectionManager
	Connect(context.Context, string, openssh.Target) (openssh.Generation, error)
	State(string) string
	StartIdleMonitor(context.Context)
}

type sshFleetTransport struct {
	conns   persistentConnectionManager
	runner  *sshfleet.Runner
	initErr error

	mu    sync.RWMutex
	peers []config.FleetSSHPeer

	// stop ends the idle monitor on shutdown. ControlMaster processes
	// are deliberately left running (ControlPersist semantics): a
	// restarted daemon adopts live sockets instead of re-dialing, and
	// the idle monitor reaps masters with no activity.
	monitorCtx context.Context
	stop       context.CancelFunc
	startOnce  sync.Once
	stopOnce   sync.Once

	// inflight single-flights the per-peer snapshot fetch so repeated
	// snapshot reads against a cold peer share one connect/fetch instead of piling
	// goroutines behind the connect mutex. Warm-ups belong to Fleet's lifecycle so
	// shutdown cancels and drains them.
	inflightMu sync.Mutex
	inflight   map[string]*inflightFetch
}

// inflightFetch is one in-progress peer snapshot fetch; done closes
// when res is populated.
type inflightFetch struct {
	done chan struct{}
	res  fleet.PeerResult
}

type sshFleetConnectionEvent struct {
	HostKey string `json:"host_key"`
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
}

type sshRelayResult struct {
	response   sshfleet.Response
	connection sshfleet.Connection
}

// newSSHFleetTransport builds the transport; events broadcast on hub.
func newSSHFleetTransport(
	socketDir string,
	peers []config.FleetSSHPeer,
	broadcast func(Event) uint64,
) *sshFleetTransport {
	monitorCtx, stop := context.WithCancel(context.Background())
	t := &sshFleetTransport{
		peers:      peers,
		monitorCtx: monitorCtx,
		stop:       stop,
	}
	conns, err := sshfleet.NewPersistentManager(socketDir, openssh.PersistentConfig{
		IdleTimeout: 30 * time.Minute,
		OnEvent: func(e openssh.Event) {
			if broadcast != nil {
				broadcast(Event{
					Type: "fleet_host_state",
					Data: sshFleetConnectionEvent{
						HostKey: e.Identity,
						State:   e.State,
						Message: e.Message,
					},
				})
			}
		},
	})
	if err != nil {
		t.initErr = err
		stop()
		return t
	}
	t.conns = conns
	t.runner = sshfleet.NewRunner(conns)
	return t
}

func (t *sshFleetTransport) start() {
	if t == nil {
		return
	}
	t.startOnce.Do(func() {
		if t.conns != nil {
			t.conns.StartIdleMonitor(t.monitorCtx)
		}
	})
}

// shutdown stops the idle monitor. Masters stay alive by design (see
// the stop field comment).
func (t *sshFleetTransport) shutdown() {
	if t == nil {
		return
	}
	t.stopOnce.Do(func() {
		if t.stop != nil {
			t.stop()
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
	if t.initErr != nil {
		return fleet.MapConnectionState(openssh.StateError)
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
) (sshRelayResult, error) {
	if t.initErr != nil {
		return sshRelayResult{}, t.initErr
	}
	target, err := openssh.ParseTarget(peer.Destination)
	if err != nil {
		return sshRelayResult{}, err
	}
	generation, err := t.conns.Connect(ctx, peer.Key, target)
	if err != nil && !errors.Is(err, openssh.ErrPersistentUnsupported) {
		return sshRelayResult{}, err
	}
	connection := sshfleet.Connection{
		Identity:   peer.Key,
		Generation: generation,
		Target:     target,
		Persistent: err == nil,
	}
	remoteCommand := peer.RemoteCommandOrDefault()
	resp, err := t.runner.Relay(
		ctx, connection, remoteCommand,
		method, path, body,
	)
	if !errors.Is(err, sshfleet.ErrRemoteDaemonUnavailable) {
		return sshRelayResult{response: resp, connection: connection}, err
	}
	if err := t.runner.EnsureDaemon(
		ctx, connection, remoteCommand,
	); err != nil {
		return sshRelayResult{}, fmt.Errorf(
			"ensure remote daemon: %w", err,
		)
	}
	resp, err = t.runner.Relay(
		ctx, connection, remoteCommand,
		method, path, body,
	)
	return sshRelayResult{response: resp, connection: connection}, err
}

// fetchSSHPeerResults fans out raw-snapshot fetches to every SSH
// peer concurrently, mirroring the HTTP peer fan-out: a failed peer
// degrades (Reachable=false, Err set) instead of failing the merge.
// Each peer's wait is bounded by fleet.peer_timeout — a cold or
// blackholed peer degrades fast while its connect/fetch keeps warming
// in the background, so the next snapshot read benefits.
func (s *Handler) fetchSSHPeerResults(ctx context.Context) []fleet.PeerResult {
	t := s.sshFleet
	if t == nil {
		return nil
	}
	peers := t.snapshotPeers()
	if len(peers) == 0 {
		return nil
	}
	timeout := s.configSnapshot().Fleet.PeerTimeoutOrDefault()
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
func (s *Handler) fetchSSHPeerRawBounded(
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
		admitted := s.runBackground(func(lifecycleCtx context.Context) {
			f.res = s.fetchSSHPeerRaw(lifecycleCtx, t, p)
			// Publish completion before retiring the entry: a reader
			// landing between the two either waits on the closed done
			// (and gets res) or misses the entry and starts a fresh
			// fetch — never both for the same in-flight result.
			close(f.done)
			t.inflightMu.Lock()
			delete(t.inflight, p.Key)
			t.inflightMu.Unlock()
		})
		if !admitted {
			delete(t.inflight, p.Key)
			f = nil
		}
	}
	t.inflightMu.Unlock()
	if f == nil {
		msg := "fleet is shutting down"
		destination := p.Destination
		return fleet.PeerResult{
			Key: p.Key, Name: p.Name, Platform: p.Platform,
			ObservedAt:     s.now().UTC().Format(time.RFC3339),
			SSHDestination: &destination, PreferredTransport: "ssh",
			Err: &msg,
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-f.done:
		return f.res
	case <-timer.C:
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

func (s *Handler) fetchSSHPeerRaw(
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
	result, err := t.relay(
		ctx, p, http.MethodGet, "/api/v1/snapshot/raw", nil,
	)
	if err != nil {
		res.Err = errPtr(err)
		return res
	}
	if result.response.Status/100 != 2 {
		msg := "peer returned HTTP " + http.StatusText(result.response.Status)
		res.Err = &msg
		return res
	}
	var raw fleet.RawSnapshot
	if err := json.Unmarshal(result.response.Body, &raw); err != nil {
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
func (s *Handler) serveSSHFleetRESTProxy(
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
			writeProblemResponse(w, httpapi.NewProblem(
				http.StatusBadRequest,
				httpapi.CodeBadRequest,
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
	result, err := t.relay(r.Context(), peer, r.Method, path, body)
	if err != nil {
		writeProblemResponse(w, httpapi.NewProblem(
			http.StatusBadGateway,
			httpapi.CodeUpstreamError,
			"fleet ssh relay failed: "+err.Error(),
			map[string]any{"hostKey": peer.Key},
		))
		return
	}
	resp := result.response
	out := resp.Body
	if resp.Status/100 == 2 && isAttachSpecPath(targetPath) {
		command, commandErr := t.runner.SSHCommand(result.connection, true)
		if commandErr != nil {
			writeProblemResponse(w, httpapi.NewProblem(
				http.StatusBadGateway,
				httpapi.CodeUpstreamError,
				"fleet ssh connection changed: "+commandErr.Error(),
				map[string]any{"hostKey": peer.Key},
			))
			return
		}
		if wrapped, ok := wrapAttachSpecForSSH(out, command); ok {
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

// wrapAttachSpecForSSH rewrites a peer's attach-spec so the remote tmux attach
// rides `ssh -t`. requires_local_host drops because the spec is now executable
// from the hub using its persistent or masterless connection arguments.
func wrapAttachSpecForSSH(
	body []byte, sshCommand []string,
) ([]byte, bool) {
	var spec workspaceapi.RuntimeAttachSpecResponse
	if err := json.Unmarshal(body, &spec); err != nil {
		return nil, false
	}
	if len(spec.Command) == 0 {
		return nil, false
	}
	spec.Command = append(append([]string(nil), sshCommand...), spec.Command...)
	spec.RequiresLocalHost = false
	wrapped, err := json.Marshal(spec)
	if err != nil {
		return nil, false
	}
	return wrapped, true
}
