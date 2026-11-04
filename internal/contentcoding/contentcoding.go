// Package contentcoding owns the HTTP content codings Forge speaks, in
// preference order, so the server's negotiation and every Forge client agree
// on one ladder and one implementation per coding.
package contentcoding

import (
	"fmt"
	"io"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
)

const (
	Brotli = "br"
	Zstd   = "zstd"
	Gzip   = "gzip"
)

// Ladder lists supported codings from most to least preferred.
var Ladder = []string{Brotli, Zstd, Gzip}

// AcceptEncoding is the Accept-Encoding value Forge clients send.
var AcceptEncoding = strings.Join(Ladder, ", ")

// Rank returns a coding's position in Ladder, or false if unsupported.
func Rank(coding string) (int, bool) {
	for i, supported := range Ladder {
		if coding == supported {
			return i, true
		}
	}
	return 0, false
}

// NewWriter returns a fast encoder for coding. Close flushes it.
func NewWriter(w io.Writer, coding string) (io.WriteCloser, error) {
	switch coding {
	case Brotli:
		return brotli.NewWriterLevel(w, brotli.BestSpeed), nil
	case Zstd:
		return zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.SpeedFastest))
	case Gzip:
		return gzip.NewWriterLevel(w, gzip.BestSpeed)
	default:
		return nil, fmt.Errorf("unsupported content coding %q", coding)
	}
}

// NewReader decodes body according to a Content-Encoding value. An empty or
// identity coding returns body unchanged.
func NewReader(body io.Reader, coding string) (io.ReadCloser, error) {
	switch strings.ToLower(strings.TrimSpace(coding)) {
	case "", "identity":
		return io.NopCloser(body), nil
	case Brotli:
		return io.NopCloser(brotli.NewReader(body)), nil
	case Zstd:
		decoder, err := zstd.NewReader(body, zstd.WithDecoderConcurrency(1))
		if err != nil {
			return nil, err
		}
		return decoder.IOReadCloser(), nil
	case Gzip:
		return gzip.NewReader(body)
	default:
		return nil, fmt.Errorf("unsupported content coding %q", coding)
	}
}
