package statuslog

import (
	"bufio"
	"io"
	"net"
	"net/http"
)

type StatusLoggingResponseWriter struct {
	http.ResponseWriter
	Status int
	Bytes  int64
}

func (w *StatusLoggingResponseWriter) WriteHeader(status int) {
	if w.Status != 0 {
		return
	}
	w.Status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *StatusLoggingResponseWriter) Write(data []byte) (int, error) {
	if w.Status == 0 {
		w.Status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(data)
	w.Bytes += int64(n)
	return n, err
}

func (w *StatusLoggingResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *StatusLoggingResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *StatusLoggingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hijacker.Hijack()
}

func (w *StatusLoggingResponseWriter) Push(
	target string, opts *http.PushOptions,
) error {
	pusher, ok := w.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, opts)
}

func (w *StatusLoggingResponseWriter) ReadFrom(r io.Reader) (int64, error) {
	if w.Status == 0 {
		w.Status = http.StatusOK
	}
	if readerFrom, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		n, err := readerFrom.ReadFrom(r)
		w.Bytes += n
		return n, err
	}
	return io.Copy(struct{ io.Writer }{w}, r)
}
