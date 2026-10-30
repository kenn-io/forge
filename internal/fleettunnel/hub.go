package fleettunnel

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"sync"

	"github.com/libp2p/go-yamux/v5"
)

// Registry holds the hub's end of every connected spoke tunnel, keyed by the
// spoke's node ID. A nil Registry has no tunnels.
type Registry struct {
	mu       sync.Mutex
	sessions map[string]*yamux.Session
}

func NewRegistry() *Registry {
	return &Registry{sessions: make(map[string]*yamux.Session)}
}

// Serve runs the hub's end of a tunnel for nodeID over conn until ctx ends or
// the spoke disconnects. The caller must have authenticated conn as nodeID. A
// newer tunnel from the same node replaces this one.
func (r *Registry) Serve(ctx context.Context, nodeID string, conn net.Conn) error {
	session, err := yamux.Client(conn, sessionConfig(), nil)
	if err != nil {
		_ = conn.Close()
		return err
	}
	r.mu.Lock()
	previous := r.sessions[nodeID]
	r.sessions[nodeID] = session
	r.mu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}
	defer func() {
		r.mu.Lock()
		if r.sessions[nodeID] == session {
			delete(r.sessions, nodeID)
		}
		r.mu.Unlock()
		_ = session.Close()
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-session.CloseChan():
		return nil
	}
}

// Connected reports whether nodeID has a live tunnel.
func (r *Registry) Connected(nodeID string) bool {
	return r.session(nodeID) != nil
}

// Dial opens a new connection to nodeID through its tunnel. ctx owns the
// open, which waits while the spoke has not acknowledged earlier streams.
func (r *Registry) Dial(ctx context.Context, nodeID string) (net.Conn, error) {
	session := r.session(nodeID)
	if session == nil {
		return nil, ErrNotConnected
	}
	return session.Open(ctx)
}

func (r *Registry) session(nodeID string) *yamux.Session {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	session := r.sessions[nodeID]
	if session == nil || session.IsClosed() {
		return nil
	}
	return session
}

// Transport returns a round tripper that sends nodeID's requests through its
// tunnel while one is connected. While the tunnel is down, it sends them
// through base when direct is set and fails them with ErrNotConnected
// otherwise. A nil base means http.DefaultTransport.
//
// Tunneled requests keep base's limits, such as its response-header timeout,
// whether or not direct dials are allowed. They skip TLS: the spoke opened the
// tunnel to the hub over TLS and authenticated it as nodeID, which replaces
// the server authentication a direct HTTPS dial would provide.
func (r *Registry) Transport(nodeID string, base http.RoundTripper, direct bool) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	var tunnel *http.Transport
	if template, ok := base.(*http.Transport); ok {
		tunnel = template.Clone()
	} else {
		tunnel = http.DefaultTransport.(*http.Transport).Clone()
	}
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		return r.Dial(ctx, nodeID)
	}
	tunnel.Proxy = nil
	tunnel.DialContext = dial
	// The returned stream counts as an established TLS connection, so HTTPS
	// member origins keep working without a handshake inside the tunnel.
	tunnel.DialTLSContext = dial
	tunnel.TLSClientConfig = nil
	tunnel.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	tunnel.ForceAttemptHTTP2 = false
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	tunnel.Protocols = protocols
	var fallback http.RoundTripper
	if direct {
		fallback = base
	}
	return &transport{registry: r, nodeID: nodeID, tunnel: tunnel, direct: fallback}
}

type transport struct {
	registry *Registry
	nodeID   string
	tunnel   *http.Transport
	direct   http.RoundTripper
}

func (t *transport) RoundTrip(request *http.Request) (*http.Response, error) {
	if t.registry.Connected(t.nodeID) {
		return t.tunnel.RoundTrip(request)
	}
	if t.direct == nil {
		if request.Body != nil {
			_ = request.Body.Close()
		}
		return nil, ErrNotConnected
	}
	return t.direct.RoundTrip(request)
}

func (t *transport) CloseIdleConnections() {
	t.tunnel.CloseIdleConnections()
	if closer, ok := t.direct.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}
