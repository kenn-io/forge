package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"
)

// StreamContext returns ctx that also ends when stop does, so a handler
// waiting on an upstream for a stream does not hold server shutdown. Call
// release once the stream is done.
func StreamContext(ctx, stop context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	unregister := context.AfterFunc(stop, cancel)
	return ctx, func() {
		unregister()
		cancel()
	}
}

// streamStallTimeout bounds how long a client that stopped reading keeps a
// stream. Browsers stop reading a video once they have buffered enough and
// can leave that connection open indefinitely, which holds one of the few
// HTTP/1.1 connections a browser allows to the daemon. They ask again with a
// byte range when they need more, so ending the stream costs a reconnect.
var streamStallTimeout = 15 * time.Second

// CopyStream sends a streamed body after the status line is written. It
// flushes the headers first so a video can start before the upstream sends
// a full buffer, and aborts the connection on a failed copy so a truncated
// body never looks complete to the browser cache. A write the client leaves
// unread for streamStallTimeout fails the copy. When stop ends it closes
// body and expires pending writes, so shutdown waits on neither a long video
// nor a client that stopped reading.
func CopyStream(stop context.Context, w io.Writer, body io.ReadCloser) {
	rw, _ := w.(http.ResponseWriter)
	stopped := make(chan struct{})
	unregister := context.AfterFunc(stop, func() {
		defer close(stopped)
		_ = body.Close()
		if rw != nil {
			_ = http.NewResponseController(rw).SetWriteDeadline(time.Now())
		}
	})
	// A callback that already started may still be using the response, which
	// must not outlive the handler.
	defer func() {
		if !unregister() {
			<-stopped
		}
	}()
	var limited *stallLimitedWriter
	if rw != nil {
		controller := http.NewResponseController(rw)
		_ = controller.Flush()
		limited = &stallLimitedWriter{stop: stop, w: rw, controller: controller}
		w = limited
	}
	if _, err := io.Copy(w, body); err != nil {
		panic(http.ErrAbortHandler)
	}
	// The server sends what is still buffered after the handler returns,
	// under the last write's deadline, which an upstream slow to end the
	// body may already have spent.
	if limited != nil {
		if err := limited.arm(); err != nil {
			panic(http.ErrAbortHandler)
		}
	}
}

// stallLimitedWriter gives each write streamStallTimeout to drain.
type stallLimitedWriter struct {
	stop       context.Context
	w          io.Writer
	controller *http.ResponseController
}

func (w *stallLimitedWriter) Write(p []byte) (int, error) {
	if err := w.arm(); err != nil {
		return 0, err
	}
	return w.w.Write(p)
}

// arm gives the next write to the client streamStallTimeout to drain.
func (w *stallLimitedWriter) arm() error {
	// A writer without deadlines, such as a test recorder, never blocks.
	err := w.controller.SetWriteDeadline(time.Now().Add(streamStallTimeout))
	if err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	// Shutdown expires the deadline after stop ends. Checking afterwards
	// means a shutdown that came before this deadline still wins.
	return w.stop.Err()
}
