package httpapi

import (
	"context"
	"io"
	"net/http"
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
