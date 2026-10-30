package fleettunnel

import (
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
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
		// The test's context owns both ends: the spoke's client and the fake hub.
		ctx, cancel := context.WithCancel(t.Context())
		registry := NewRegistry()
		var tunnels atomic.Int32
		listener := newPipeListener()
		hub := &http.Server{BaseContext: func(net.Listener) context.Context { return ctx }, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			tunnels.Add(1)
			_ = registry.Serve(r.Context(), testNodeID, websocket.NetConn(r.Context(), conn, websocket.MessageBinary))
		})}
		context.AfterFunc(ctx, func() { _ = hub.Close() })
		served := make(chan struct{})
		go func() {
			defer close(served)
			_ = hub.Serve(listener)
		}()

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
		tunneled := registry.Transport(testNodeID, nil, false)
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
		<-served
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

// A spoke must recover from a rejected handshake and from a dropped tunnel,
// rebuilding its credential headers for every attempt, and stop promptly when
// its context ends during a backoff wait.
func TestClientReconnectsAfterRejectionAndDisconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// The test's context owns both ends: the spoke's client and the fake hub.
		ctx, cancel := context.WithCancel(t.Context())
		registry := NewRegistry()
		var (
			mu         sync.Mutex
			attempts   []string
			dropTunnel context.CancelFunc
			rejectAll  bool
		)
		listener := newPipeListener()
		hub := &http.Server{BaseContext: func(net.Listener) context.Context { return ctx }, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			attempts = append(attempts, r.Header.Get("Authorization"))
			reject := rejectAll || len(attempts) <= 2
			mu.Unlock()
			if reject {
				http.Error(w, "not yet", http.StatusUnauthorized)
				return
			}
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			mu.Lock()
			dropTunnel = cancel
			mu.Unlock()
			_ = registry.Serve(ctx, testNodeID, websocket.NetConn(ctx, conn, websocket.MessageBinary))
		})}
		context.AfterFunc(ctx, func() { _ = hub.Close() })
		served := make(chan struct{})
		go func() {
			defer close(served)
			_ = hub.Serve(listener)
		}()
		drop := func() {
			mu.Lock()
			cancel := dropTunnel
			mu.Unlock()
			cancel()
			synctest.Wait()
			require.False(t, registry.Connected(testNodeID))
		}

		var tokens atomic.Int32
		var failures []int
		dialer := &http.Transport{DialContext: listener.dial}
		client, err := NewClient(ClientOptions{
			URL: "http://hub.test/api/v1/federation/tunnel",
			Header: func() (http.Header, error) {
				token := "Bearer token-" + strconv.Itoa(int(tokens.Add(1)))
				return http.Header{"Authorization": {token}}, nil
			},
			HTTPClient: &http.Client{Transport: dialer},
			Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, "connected")
			}),
			RetryDelay: func(failure int) time.Duration {
				mu.Lock()
				defer mu.Unlock()
				failures = append(failures, failure)
				if rejectAll {
					return time.Hour
				}
				return time.Second
			},
		})
		require.NoError(t, err)
		running := make(chan struct{})
		go func() {
			defer close(running)
			client.Run(ctx)
		}()
		tunneled := registry.Transport(testNodeID, nil, false)
		requireTraffic := func() {
			t.Helper()
			require.True(t, registry.Connected(testNodeID))
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://spoke.invalid/", nil)
			require.NoError(t, err)
			response, err := tunneled.RoundTrip(request)
			require.NoError(t, err)
			body, err := io.ReadAll(response.Body)
			require.NoError(t, response.Body.Close())
			require.NoError(t, err)
			require.Equal(t, "connected", string(body))
		}

		// Two rejections back off, then the third attempt connects.
		time.Sleep(10 * time.Second)
		synctest.Wait()
		requireTraffic()

		// A dropped tunnel reconnects after one backoff.
		drop()
		time.Sleep(10 * time.Second)
		synctest.Wait()
		requireTraffic()

		// Canceling during a long backoff stops Run without waiting it out.
		mu.Lock()
		rejectAll = true
		mu.Unlock()
		drop()
		start := time.Now()
		cancel()
		<-running
		assert := assert.New(t)
		assert.Zero(time.Since(start), "Run stopped during its backoff wait")
		assert.Equal([]string{
			"Bearer token-1", "Bearer token-2", "Bearer token-3", "Bearer token-4",
		}, attempts, "every attempt rebuilds its credential headers")
		assert.Equal([]int{1, 2, 1, 1}, failures,
			"rejections grow the backoff and a connection resets it")

		tunneled.(*transport).CloseIdleConnections()
		dialer.CloseIdleConnections()
		<-served
	})
}

// A tunnel to a spoke the hub may not dial directly must still keep the base
// transport's limits, so a spoke that stalls before answering cannot hold a
// hub request open.
func TestTunnelKeepsBaseResponseHeaderTimeoutWithoutDirectAccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		registry := NewRegistry()
		hubEnd, spokeEnd := net.Pipe()
		go func() {
			_ = Serve(ctx, spokeEnd, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				<-r.Context().Done()
			}))
		}()
		go func() { _ = registry.Serve(ctx, testNodeID, hubEnd) }()
		synctest.Wait()
		require.True(t, registry.Connected(testNodeID))

		base := &http.Transport{ResponseHeaderTimeout: 10 * time.Second}
		tunneled := registry.Transport(testNodeID, base, false)
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://spoke.invalid/ws/v1/terminal", nil)
		require.NoError(t, err)
		result := make(chan error, 1)
		go func() {
			response, err := tunneled.RoundTrip(request)
			if response != nil {
				_ = response.Body.Close()
			}
			result <- err
		}()
		select {
		case err := <-result:
			require.ErrorContains(t, err, "timeout awaiting response headers")
		case <-time.After(time.Minute):
			require.FailNow(t, "the stalled spoke held the request past the base response-header timeout")
		}
		tunneled.(*transport).CloseIdleConnections()
	})
}

// A hub that redirects the tunnel handshake must not move the spoke's
// credential to another origin, even when the supplied client follows
// redirects.
func TestClientRefusesRedirectedHandshake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		serve := func(listener *pipeListener, handler http.HandlerFunc) <-chan struct{} {
			server := &http.Server{BaseContext: func(net.Listener) context.Context { return ctx }, Handler: handler}
			context.AfterFunc(ctx, func() { _ = server.Close() })
			served := make(chan struct{})
			go func() {
				defer close(served)
				_ = server.Serve(listener)
			}()
			return served
		}
		var hubAttempts, redirectedRequests atomic.Int32
		hub, elsewhere := newPipeListener(), newPipeListener()
		hubServed := serve(hub, func(w http.ResponseWriter, r *http.Request) {
			hubAttempts.Add(1)
			http.Redirect(w, r, "http://elsewhere.test/api/v1/federation/tunnel", http.StatusTemporaryRedirect)
		})
		elsewhereServed := serve(elsewhere, func(w http.ResponseWriter, _ *http.Request) {
			redirectedRequests.Add(1)
			w.WriteHeader(http.StatusTeapot)
		})

		dialer := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address == "elsewhere.test:80" {
				return elsewhere.dial(ctx, network, address)
			}
			return hub.dial(ctx, network, address)
		}}
		client, err := NewClient(ClientOptions{
			URL: "http://hub.test/api/v1/federation/tunnel",
			Header: func() (http.Header, error) {
				return http.Header{"Authorization": {"Bearer spoke-secret"}}, nil
			},
			// A client that follows redirects, as http.Client does by default.
			HTTPClient: &http.Client{Transport: dialer},
			Handler:    http.NotFoundHandler(),
			RetryDelay: func(int) time.Duration { return time.Hour },
		})
		require.NoError(t, err)
		running := make(chan struct{})
		go func() {
			defer close(running)
			client.Run(ctx)
		}()
		synctest.Wait()

		assert.Equal(t, int32(1), hubAttempts.Load())
		assert.Zero(t, redirectedRequests.Load(), "the redirect target never sees the handshake")
		cancel()
		<-running
		dialer.CloseIdleConnections()
		<-hubServed
		<-elsewhereServed
	})
}
