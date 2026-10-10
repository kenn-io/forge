package httpapi

import (
	"context"
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

// CopyStream sends a streamed body after the status line is written. It
// flushes the headers first so a video can start before the upstream sends
// a full buffer, and aborts the connection on a failed copy so a truncated
// body never looks complete to the browser cache. When stop ends it closes
// body and expires pending writes, so shutdown waits on neither a long video
// nor a client that stopped reading.
func CopyStream(stop context.Context, w io.Writer, body io.ReadCloser) {
	rw, _ := w.(http.ResponseWriter)
	defer context.AfterFunc(stop, func() {
		_ = body.Close()
		if rw != nil {
			_ = http.NewResponseController(rw).SetWriteDeadline(time.Now())
		}
	})()
	if rw != nil {
		_ = http.NewResponseController(rw).Flush()
	}
	if _, err := io.Copy(w, body); err != nil {
		panic(http.ErrAbortHandler)
	}
}
