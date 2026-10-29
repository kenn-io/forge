package fleettunnel

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
	"github.com/libp2p/go-yamux/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pipeListener serves in-memory connections so tunnel lifetimes can run under
// synctest's fake clock.
type pipeListener struct {
	accepted chan net.Conn
	closed   chan struct{}
	once     sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{accepted: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *pipeListener) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	client, server := net.Pipe()
	select {
	case l.accepted <- server:
		return client, nil
	case <-l.closed:
		return nil, net.ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.accepted:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (*pipeListener) Addr() net.Addr { return Addr{} }

// A federation HTTP client carries a whole-request timeout. The tunnel it
// dials must outlive that timeout: the run context owns the tunnel, not the
// client's request deadline.
func TestTunnelOutlivesSpokeClientTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		registry := NewRegistry()
		var tunnels atomic.Int32
		listener := newPipeListener()
		hub := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			tunnels.Add(1)
			_ = registry.Serve(r.Context(), testNodeID, websocket.NetConn(r.Context(), conn, websocket.MessageBinary))
		})}
		served := make(chan error, 1)
		go func() { served <- hub.Serve(listener) }()

		dialer := &http.Transport{DialContext: listener.dial}
		client, err := NewClient(ClientOptions{
			URL:        "http://hub.test/api/v1/federation/tunnel",
			Header:     func() (http.Header, error) { return http.Header{}, nil },
			HTTPClient: &http.Client{Transport: dialer, Timeout: time.Second},
			Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, "still connected")
			}),
		})
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(t.Context())
		running := make(chan struct{})
		go func() {
			defer close(running)
			client.Run(ctx)
		}()
		synctest.Wait()
		require.True(t, registry.Connected(testNodeID))

		time.Sleep(time.Hour)
		synctest.Wait()
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://spoke.invalid/", nil)
		require.NoError(t, err)
		tunneled := registry.Transport(testNodeID, nil)
		response, err := tunneled.RoundTrip(request)
		require.NoError(t, err)
		body, err := io.ReadAll(response.Body)
		require.NoError(t, response.Body.Close())
		require.NoError(t, err)
		assert.Equal(t, "still connected", string(body))
		assert.Equal(t, int32(1), tunnels.Load(), "the tunnel never reconnected")

		cancel()
		<-running
		synctest.Wait()
		tunneled.(*transport).CloseIdleConnections()
		dialer.CloseIdleConnections()
		require.NoError(t, listener.Close())
		require.ErrorIs(t, <-served, net.ErrClosed)
	})
}

// yamux blocks a stream open while the spoke has not acknowledged earlier
// streams. The caller's context must own that wait.
func TestDialReturnsWhenContextEndsDuringBlockedOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		registry := NewRegistry()
		hubEnd, spokeEnd := net.Pipe()
		// A spoke that never accepts leaves every opened stream unacknowledged.
		spoke, err := yamux.Server(spokeEnd, sessionConfig(), nil)
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(t.Context())
		served := make(chan error, 1)
		go func() { served <- registry.Serve(ctx, testNodeID, hubEnd) }()
		// Closing the session releases any open still blocked, even after a
		// failed assertion.
		defer func() {
			cancel()
			<-served
			_ = spoke.Close()
		}()
		synctest.Wait()
		require.True(t, registry.Connected(testNodeID))

		for range sessionConfig().AcceptBacklog {
			_, err := registry.Dial(t.Context(), testNodeID)
			require.NoError(t, err)
		}
		dialContext, stop := context.WithTimeout(t.Context(), time.Second)
		defer stop()
		dialed := make(chan error, 1)
		go func() {
			_, err := registry.Dial(dialContext, testNodeID)
			dialed <- err
		}()
		select {
		case err := <-dialed:
			require.ErrorIs(t, err, context.DeadlineExceeded)
		case <-time.After(time.Minute):
			require.FailNow(t, "Dial kept waiting after its context ended")
		}
		synctest.Wait()
	})
}
