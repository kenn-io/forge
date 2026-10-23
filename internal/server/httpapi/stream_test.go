package httpapi

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

// heldDeadlineWriter blocks SetWriteDeadline until released, so a test can
// hold CopyStream's shutdown callback mid-flight.
type heldDeadlineWriter struct {
	header  http.Header
	entered chan struct{}
	release chan struct{}
}

func (w *heldDeadlineWriter) Header() http.Header         { return w.header }
func (w *heldDeadlineWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *heldDeadlineWriter) WriteHeader(int)             {}

func (w *heldDeadlineWriter) SetWriteDeadline(time.Time) error {
	close(w.entered)
	<-w.release
	return nil
}

// stalledBody blocks reads until closed.
type stalledBody struct{ closed chan struct{} }

func (b *stalledBody) Read([]byte) (int, error) {
	<-b.closed
	return 0, io.ErrClosedPipe
}

func (b *stalledBody) Close() error {
	close(b.closed)
	return nil
}

func TestCopyStreamWaitsForShutdownCallbackBeforeReturning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := &heldDeadlineWriter{header: http.Header{}, entered: make(chan struct{}), release: make(chan struct{})}
		stop, cancel := context.WithCancel(t.Context())
		returned := make(chan struct{})
		go func() {
			defer close(returned)
			defer func() { _ = recover() }()
			CopyStream(stop, w, &stalledBody{closed: make(chan struct{})})
		}()

		cancel()
		<-w.entered
		// The callback closed the body, so the copy has failed; the handler
		// must still not finish while the callback uses the response.
		synctest.Wait()
		select {
		case <-returned:
			require.FailNow(t, "CopyStream returned while its shutdown callback still used the response")
		default:
		}

		close(w.release)
		<-returned
	})
}

// zeroBody yields zeros forever, like a long video upstream.
type zeroBody struct{}

func (zeroBody) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func (zeroBody) Close() error { return nil }

func TestCopyStreamEndsStreamToClientThatStoppedReading(t *testing.T) {
	previous := streamStallTimeout
	streamStallTimeout = 200 * time.Millisecond
	t.Cleanup(func() { streamStallTimeout = previous })
	returned := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		defer close(returned)
		defer func() { _ = recover() }()
		w.WriteHeader(http.StatusOK)
		CopyStream(t.Context(), w, zeroBody{})
	}))
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, conn.(*net.TCPConn).SetReadBuffer(4096))
	_, err = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: "+addr+"\r\n\r\n")
	require.NoError(t, err)

	// The client never reads, so only the stall limit can end an endless body.
	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		require.FailNow(t, "CopyStream kept a stream the client stopped reading")
	}
}

// lateEOFBody sends its data at once and ends only after a delay, like an
// upstream slow to close.
type lateEOFBody struct {
	data  []byte
	delay time.Duration
}

func (b *lateEOFBody) Read(p []byte) (int, error) {
	if len(b.data) > 0 {
		n := copy(p, b.data)
		b.data = b.data[n:]
		return n, nil
	}
	<-time.After(b.delay)
	return 0, io.EOF
}

func (b *lateEOFBody) Close() error { return nil }

func TestCopyStreamFinishesStreamWhoseUpstreamEndsLate(t *testing.T) {
	previous := streamStallTimeout
	streamStallTimeout = 100 * time.Millisecond
	t.Cleanup(func() { streamStallTimeout = previous })
	const size = 64 * 1024
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		CopyStream(t.Context(), w, &lateEOFBody{data: make([]byte, size), delay: time.Second})
	}))
	t.Cleanup(srv.Close)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)

	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	// The client reads throughout, so the end of the stream must arrive even
	// though the upstream took longer than the stall limit to end it.
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Len(t, body, size)
}

// deadlineWriter fails a write that starts after its deadline, as a
// connection whose deadline passed would.
type deadlineWriter struct {
	header   http.Header
	deadline time.Time
	written  int
}

func (w *deadlineWriter) Header() http.Header { return w.header }
func (w *deadlineWriter) WriteHeader(int)     {}

func (w *deadlineWriter) Write(p []byte) (int, error) {
	if !w.deadline.IsZero() && time.Now().After(w.deadline) {
		return 0, os.ErrDeadlineExceeded
	}
	w.written += len(p)
	return len(p), nil
}

func (w *deadlineWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}

// slowBody returns one chunk per delay, like a slow upstream.
type slowBody struct {
	chunks int
	delay  time.Duration
}

func (b *slowBody) Read(p []byte) (int, error) {
	if b.chunks == 0 {
		return 0, io.EOF
	}
	b.chunks--
	<-time.After(b.delay) // Fake time: tests read it inside a synctest bubble.
	return copy(p, make([]byte, 1024)), nil
}

func (b *slowBody) Close() error { return nil }

func TestCopyStreamKeepsStreamThatOutlastsStallLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := &deadlineWriter{header: http.Header{}}
		body := &slowBody{chunks: 5, delay: streamStallTimeout * 2 / 3}

		// Each write gets its own window, so a stream several times longer
		// than the limit keeps going while every write drains.
		CopyStream(t.Context(), w, body)

		require.Equal(t, 5*1024, w.written)
	})
}
