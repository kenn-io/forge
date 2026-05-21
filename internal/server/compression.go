package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/klauspost/compress/zstd"
)

const (
	responseCompressionMinSize  = 1024
	responseCompressionMaxBytes = 1 << 20
)

type bufferedHumaContext struct {
	inner     huma.Context
	w         http.ResponseWriter
	body      bytes.Buffer
	status    int
	maxBuffer int
	streaming bool
	writeErr  error
}

func (c *bufferedHumaContext) Operation() *huma.Operation {
	return c.inner.Operation()
}

func (c *bufferedHumaContext) Context() context.Context {
	return c.inner.Context()
}

func (c *bufferedHumaContext) TLS() *tls.ConnectionState {
	return c.inner.TLS()
}

func (c *bufferedHumaContext) Version() huma.ProtoVersion {
	return c.inner.Version()
}

func (c *bufferedHumaContext) Method() string {
	return c.inner.Method()
}

func (c *bufferedHumaContext) Host() string {
	return c.inner.Host()
}

func (c *bufferedHumaContext) RemoteAddr() string {
	return c.inner.RemoteAddr()
}

func (c *bufferedHumaContext) URL() url.URL {
	return c.inner.URL()
}

func (c *bufferedHumaContext) Param(name string) string {
	return c.inner.Param(name)
}

func (c *bufferedHumaContext) Query(name string) string {
	return c.inner.Query(name)
}

func (c *bufferedHumaContext) Header(name string) string {
	return c.inner.Header(name)
}

func (c *bufferedHumaContext) EachHeader(cb func(name string, value string)) {
	c.inner.EachHeader(cb)
}

func (c *bufferedHumaContext) BodyReader() io.Reader {
	return c.inner.BodyReader()
}

func (c *bufferedHumaContext) GetMultipartForm() (*multipart.Form, error) {
	return c.inner.GetMultipartForm()
}

func (c *bufferedHumaContext) SetReadDeadline(deadline time.Time) error {
	return c.inner.SetReadDeadline(deadline)
}

func (c *bufferedHumaContext) SetStatus(code int) {
	c.status = code
}

func (c *bufferedHumaContext) Status() int {
	return c.status
}

func (c *bufferedHumaContext) SetHeader(name string, value string) {
	c.inner.SetHeader(name, value)
}

func (c *bufferedHumaContext) AppendHeader(name string, value string) {
	c.inner.AppendHeader(name, value)
}

func (c *bufferedHumaContext) BodyWriter() io.Writer {
	return c
}

func (c *bufferedHumaContext) Unwrap() huma.Context {
	return c.inner
}

func (c *bufferedHumaContext) Write(p []byte) (int, error) {
	if c.streaming {
		return c.w.Write(p)
	}
	if c.body.Len()+len(p) <= c.maxBuffer {
		return c.body.Write(p)
	}
	c.streaming = true
	status := c.status
	if status == 0 {
		status = http.StatusOK
	}
	c.w.WriteHeader(status)
	if c.body.Len() > 0 {
		if _, err := c.w.Write(c.body.Bytes()); err != nil {
			c.writeErr = err
			return 0, err
		}
		c.body.Reset()
	}
	n, err := c.w.Write(p)
	if err != nil {
		c.writeErr = err
	}
	return n, err
}

func newResponseCompressionMiddleware(
	minSize int,
) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		r, w := humago.Unwrap(ctx)
		addVaryHeader(w.Header(), "Accept-Encoding")
		encoding := selectResponseEncoding(r.Header.Get("Accept-Encoding"))

		if encoding == "" || shouldBypassCompression(ctx, r) {
			next(ctx)
			return
		}

		buffered := &bufferedHumaContext{
			inner:     ctx,
			w:         w,
			maxBuffer: responseCompressionMaxBytes,
		}
		next(buffered)
		if buffered.streaming || buffered.writeErr != nil {
			return
		}

		status := buffered.status
		if status == 0 {
			status = http.StatusOK
		}

		if shouldCompressResponse(w.Header(), status, buffered.body.Len(), minSize, encoding) {
			w.Header().Set("Content-Encoding", encoding)
			w.Header().Del("Content-Length")
			w.WriteHeader(status)
			if err := writeCompressedBody(w, encoding, buffered.body.Bytes()); err != nil {
				return
			}
			return
		}

		w.WriteHeader(status)
		_, _ = w.Write(buffered.body.Bytes())
	}
}

func shouldBypassCompression(ctx huma.Context, r *http.Request) bool {
	if r.Method == http.MethodHead {
		return true
	}
	if strings.EqualFold(r.Header.Get("Connection"), "upgrade") ||
		r.Header.Get("Upgrade") != "" {
		return true
	}
	if ctx.Operation() != nil && ctx.Operation().Path == "/events" {
		return true
	}
	return false
}

func shouldCompressResponse(
	header http.Header,
	status int,
	bodyLen int,
	minSize int,
	encoding string,
) bool {
	if encoding == "" || bodyLen < minSize {
		return false
	}
	if status == http.StatusNoContent || status == http.StatusNotModified {
		return false
	}
	if header.Get("Content-Encoding") != "" || header.Get("Content-Range") != "" {
		return false
	}
	if hasNoTransform(header.Get("Cache-Control")) {
		return false
	}
	return isCompressibleContentType(header.Get("Content-Type"))
}

func selectResponseEncoding(header string) string {
	const (
		zstdEncoding   = "zstd"
		brotliEncoding = "br"
	)

	best := ""
	bestQ := 0.0
	preferred := map[string]int{
		brotliEncoding: 0,
		zstdEncoding:   1,
	}

	for part := range strings.SplitSeq(header, ",") {
		name, q := parseAcceptEncodingPart(part)
		if q <= 0 {
			continue
		}
		if name == "*" {
			name = zstdEncoding
		}
		rank, ok := preferred[name]
		if !ok {
			continue
		}
		if best == "" || q > bestQ || (q == bestQ && rank < preferred[best]) {
			best = name
			bestQ = q
		}
	}

	return best
}

func parseAcceptEncodingPart(part string) (string, float64) {
	pieces := strings.Split(part, ";")
	name := strings.ToLower(strings.TrimSpace(pieces[0]))
	if name == "" {
		return "", 0
	}

	q := 1.0
	for _, param := range pieces[1:] {
		param = strings.TrimSpace(param)
		rawQ, ok := strings.CutPrefix(param, "q=")
		if !ok {
			continue
		}
		parsed, err := strconv.ParseFloat(rawQ, 64)
		if err != nil {
			return name, 0
		}
		q = parsed
	}
	return name, q
}

func hasNoTransform(cacheControl string) bool {
	for directive := range strings.SplitSeq(cacheControl, ",") {
		if strings.EqualFold(strings.TrimSpace(directive), "no-transform") {
			return true
		}
	}
	return false
}

func isCompressibleContentType(contentType string) bool {
	if contentType == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = strings.ToLower(strings.TrimSpace(contentType))
	}
	if strings.HasPrefix(mediaType, "text/") {
		return mediaType != "text/event-stream"
	}
	switch mediaType {
	case "application/json",
		"application/javascript",
		"application/x-javascript",
		"application/xml",
		"application/xhtml+xml",
		"application/x-ndjson",
		"image/svg+xml":
		return true
	default:
		return false
	}
}

func writeCompressedBody(w io.Writer, encoding string, body []byte) error {
	switch encoding {
	case "zstd":
		zw, err := zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.SpeedFastest))
		if err != nil {
			return err
		}
		if _, err := zw.Write(body); err != nil {
			zw.Close()
			return err
		}
		return zw.Close()
	case "br":
		bw := brotli.NewWriterLevel(w, brotli.BestSpeed)
		if _, err := bw.Write(body); err != nil {
			bw.Close()
			return err
		}
		return bw.Close()
	default:
		_, err := w.Write(body)
		return err
	}
}

func addVaryHeader(header http.Header, value string) {
	for _, existing := range header.Values("Vary") {
		for part := range strings.SplitSeq(existing, ",") {
			if strings.EqualFold(strings.TrimSpace(part), value) {
				return
			}
		}
	}
	header.Add("Vary", value)
}
