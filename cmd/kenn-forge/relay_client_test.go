package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/activityrelay"
)

// A Mac that sleeps can come back holding a TCP connection the relay host has
// forgotten. The subscription must move to a new connection instead of sending
// every retry down the dead one.
func TestRelayClientLeavesSilentHTTP2Connection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		require := require.New(t)
		cert, roots := relayTestCertificate(t)
		feed := new(activityrelay.Broadcaster)
		_, handler := activityrelay.Handlers(feed, nil)
		listener := newPipeListener()
		server := &http.Server{Handler: handler, TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}}}
		served := make(chan error, 1)
		go func() { served <- server.ServeTLS(listener, "", "") }()

		client := relayHTTPClient()
		transport := client.Transport.(*http.Transport)
		transport.Proxy = nil
		transport.TLSClientConfig = &tls.Config{RootCAs: roots}
		transport.DialContext = listener.dial

		ctx, cancel := context.WithCancel(t.Context())
		hints := make(chan activityrelay.Hint, 1)
		subscribed := make(chan struct{})
		go func() {
			defer close(subscribed)
			for ctx.Err() == nil {
				stream, err := activityrelay.Open(ctx, client, "https://relay.test")
				if err == nil {
					_ = stream.Read(func(hint activityrelay.Hint) { hints <- hint })
					_ = stream.Close()
				}
				time.Sleep(time.Second)
			}
		}()
		// A failed assertion must still release every goroutine in the bubble.
		t.Cleanup(func() {
			cancel()
			require.NoError(server.Close())
			require.ErrorIs(<-served, http.ErrServerClosed)
			transport.CloseIdleConnections()
			<-subscribed
		})

		synctest.Wait()
		require.Equal(1, feed.Subscribers())
		listener.conn(0).goSilent()

		// Long enough for many retries at the relay client's timeouts.
		time.Sleep(3 * time.Minute)
		synctest.Wait()
		hint := activityrelay.Hint{Provider: "github", Host: "github.com", RepositoryID: 42, Target: activityrelay.Repository}
		feed.Publish([]activityrelay.Hint{hint})
		synctest.Wait()
		select {
		case got := <-hints:
			require.Equal(hint, got)
		default:
			require.FailNow("no hint arrived after the first connection went silent")
		}
	})
}

// relayTestCertificate returns a self-signed certificate for relay.test and a
// pool that trusts it.
func relayTestCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "relay.test"},
		DNSNames:     []string{"relay.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	parsed, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, roots
}

// pipeListener connects clients to the server over in-memory pipes, which
// block durably and so let fake time advance.
type pipeListener struct {
	accepted chan net.Conn
	closed   chan struct{}
	once     sync.Once
	mu       sync.Mutex
	conns    []*silenceableConn
}

func newPipeListener() *pipeListener {
	return &pipeListener{accepted: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *pipeListener) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	client, server := net.Pipe()
	conn := &silenceableConn{Conn: server}
	select {
	case l.accepted <- conn:
	case <-l.closed:
		return nil, errors.New("relay test listener closed")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	l.mu.Lock()
	l.conns = append(l.conns, conn)
	l.mu.Unlock()
	return client, nil
}

func (l *pipeListener) conn(i int) *silenceableConn {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.conns[i]
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

func (*pipeListener) Addr() net.Addr { return pipeAddr{} }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "relay.test:443" }

// silenceableConn is the server end of a connection. Once silent it behaves
// like a half-open TCP connection: it never closes, accepts writes that never
// arrive, and discards everything the client sends.
type silenceableConn struct {
	net.Conn
	silent atomic.Bool
}

func (c *silenceableConn) goSilent() { c.silent.Store(true) }

func (c *silenceableConn) Read(b []byte) (int, error) {
	for {
		n, err := c.Conn.Read(b)
		if err != nil || !c.silent.Load() {
			return n, err
		}
	}
}

func (c *silenceableConn) Write(b []byte) (int, error) {
	if c.silent.Load() {
		return len(b), nil
	}
	return c.Conn.Write(b)
}
