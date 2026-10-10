package httpapi

import (
	"context"
	"io"
	"net/http"
)

// CopyStream sends a streamed body after the status line is written. It
// flushes the headers first so a video can start before the upstream sends
// a full buffer, closes body when stop ends so server shutdown does not wait
// on a long video, and aborts the connection on a failed copy so a truncated
// body never looks complete to the browser cache.
func CopyStream(stop context.Context, w io.Writer, body io.ReadCloser) {
	defer context.AfterFunc(stop, func() { _ = body.Close() })()
	if rw, ok := w.(http.ResponseWriter); ok {
		_ = http.NewResponseController(rw).Flush()
	}
	if _, err := io.Copy(w, body); err != nil {
		panic(http.ErrAbortHandler)
	}
}
