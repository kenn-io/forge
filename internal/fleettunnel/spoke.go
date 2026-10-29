package fleettunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

const (
	handshakeTimeout  = 15 * time.Second
	readHeaderTimeout = 10 * time.Second
	maxRetryDelay     = 30 * time.Second
	maxErrorBodyBytes = 4 << 10
)

// Serve answers the hub's requests arriving over conn with handler until ctx
// ends or the tunnel closes. Requests carry Addr as their remote address and
// are canceled when the tunnel closes.
func Serve(ctx context.Context, conn net.Conn, handler http.Handler) error {
	session, err := yamux.Server(conn, sessionConfig())
	if err != nil {
		_ = conn.Close()
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = session.Close() })
	defer stop()
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug),
	}
	err = server.Serve(listener{session: session})
	_ = server.Close()
	_ = session.Close()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

type listener struct {
	session *yamux.Session
}

func (l listener) Accept() (net.Conn, error) {
	conn, err := l.session.Accept()
	if err != nil {
		return nil, err
	}
	return tunnelConn{Conn: conn}, nil
}

func (l listener) Close() error   { return l.session.Close() }
func (l listener) Addr() net.Addr { return Addr{} }

type tunnelConn struct {
	net.Conn
}

func (tunnelConn) LocalAddr() net.Addr  { return Addr{} }
func (tunnelConn) RemoteAddr() net.Addr { return Addr{} }

// ClientOptions configures the spoke's end of the tunnel.
type ClientOptions struct {
	// URL is the hub's tunnel endpoint.
	URL string
	// Header returns the headers that authenticate the spoke to the hub.
	// It runs before every connection attempt so a renewed credential applies.
	Header func() (http.Header, error)
	// HTTPClient dials the hub. Its whole-request timeout and redirect policy
	// are replaced: the tunnel outlives any request deadline, and following a
	// redirect would send the spoke's credential to another origin.
	HTTPClient *http.Client
	// Handler serves the hub's requests.
	Handler    http.Handler
	RetryDelay func(failures int) time.Duration
	Wait       func(context.Context, time.Duration) error
}

// Client keeps the spoke's tunnel to the hub open.
type Client struct {
	options ClientOptions
}

func NewClient(options ClientOptions) (*Client, error) {
	if options.URL == "" || options.Header == nil || options.Handler == nil {
		return nil, errors.New("fleet tunnel client requires a URL, headers, and a handler")
	}
	if options.RetryDelay == nil {
		options.RetryDelay = jitteredRetryDelay
	}
	if options.Wait == nil {
		options.Wait = waitForRetry
	}
	client := &http.Client{}
	if options.HTTPClient != nil {
		*client = *options.HTTPClient
	}
	client.Timeout = 0
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	options.HTTPClient = client
	return &Client{options: options}, nil
}

// Run reconnects until ctx ends. Every failure, including a rejection by a hub
// that does not offer tunnels, passes through the bounded backoff.
func (c *Client) Run(ctx context.Context) {
	failures := 0
	for ctx.Err() == nil {
		connected, err := c.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if connected {
			failures = 0
		}
		failures++
		delay := c.options.RetryDelay(failures)
		slog.Debug("retrying fleet tunnel", "next", delay, "err", err)
		if c.options.Wait(ctx, delay) != nil {
			return
		}
	}
}

func (c *Client) runOnce(ctx context.Context) (bool, error) {
	header, err := c.options.Header()
	if err != nil {
		return false, err
	}
	dialContext, cancel := context.WithTimeout(ctx, handshakeTimeout)
	conn, response, err := websocket.Dial(dialContext, c.options.URL, &websocket.DialOptions{
		HTTPClient: c.options.HTTPClient,
		HTTPHeader: header,
	})
	cancel()
	if err != nil {
		return false, dialError(err, response)
	}
	slog.Debug("fleet tunnel connected")
	err = Serve(ctx, websocket.NetConn(ctx, conn, websocket.MessageBinary), c.options.Handler)
	slog.Debug("fleet tunnel disconnected", "err", err)
	return true, err
}

func dialError(err error, response *http.Response) error {
	if response == nil || response.Body == nil {
		return err
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes))
	if readErr != nil || len(body) == 0 {
		return err
	}
	return fmt.Errorf("%w: %s", err, body)
}

func jitteredRetryDelay(failures int) time.Duration {
	delay := time.Second
	for range max(failures, 1) - 1 {
		if delay >= maxRetryDelay {
			break
		}
		delay *= 2
	}
	delay = min(delay, maxRetryDelay)
	// Symmetric 20% jitter keeps a fleet from reconnecting in lockstep after
	// a hub restart.
	spread := delay / 5
	return delay - spread + time.Duration(rand.Int64N(int64(spread*2)+1))
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
