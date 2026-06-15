package server

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCopyProxyRequestHeadersStripsBrowserHeaders verifies the hub does not
// forward a browser's Origin or Sec-Fetch-* metadata onto a server-to-server
// fleet proxy request. Forwarding them trips the peer's host-authority guard,
// which validates Origin against its own allowed hosts and rejects the
// fan-out because the origin is the hub, not the peer.
func TestCopyProxyRequestHeadersStripsBrowserHeaders(t *testing.T) {
	assert := assert.New(t)
	src := http.Header{}
	src.Set("Origin", "http://hub.local:8091")
	src.Set("Sec-Fetch-Site", "same-origin")
	src.Set("Sec-Fetch-Mode", "cors")
	src.Set("Authorization", "Bearer token")
	src.Set("Content-Type", "application/json")
	src.Set("Cookie", "session=abc")
	src.Set("Connection", "keep-alive") // hop-by-hop
	src.Set("Forwarded", "host=hub.local:8091;proto=https")
	src.Set("X-Forwarded-Host", "hub.local:8091")
	src.Set("X-Forwarded-Proto", "https")

	dst := http.Header{}
	copyProxyRequestHeaders(dst, src)

	assert.Empty(dst.Get("Origin"), "browser Origin must not reach the peer")
	assert.Empty(dst.Get("Sec-Fetch-Site"), "Sec-Fetch-* must not reach the peer")
	assert.Empty(dst.Get("Sec-Fetch-Mode"), "Sec-Fetch-* must not reach the peer")
	assert.Empty(dst.Get("Connection"), "hop-by-hop headers are still dropped")
	assert.Empty(dst.Get("Forwarded"), "forwarded host metadata must not reach the peer")
	assert.Empty(dst.Get("X-Forwarded-Host"), "forwarded host metadata must not reach the peer")
	assert.Empty(dst.Get("X-Forwarded-Proto"), "forwarded proxy metadata must not reach the peer")
	assert.Equal("Bearer token", dst.Get("Authorization"), "auth must pass through")
	assert.Equal("application/json", dst.Get("Content-Type"), "content type must pass through")
	assert.Equal("session=abc", dst.Get("Cookie"), "cookies must pass through")
}

// TestCopyProxyWebSocketRequestHeadersStripsBrowserHeaders verifies the same
// browser-header stripping applies to fleet websocket dials, on top of the
// existing Sec-WebSocket-* exclusion the dialer sets itself.
func TestCopyProxyWebSocketRequestHeadersStripsBrowserHeaders(t *testing.T) {
	assert := assert.New(t)
	src := http.Header{}
	src.Set("Origin", "http://hub.local:8091")
	src.Set("Sec-Fetch-Dest", "websocket")
	src.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	src.Set("Authorization", "Bearer token")
	src.Set("Forwarded", "host=hub.local:8091")
	src.Set("X-Forwarded-Host", "hub.local:8091")

	dst := http.Header{}
	copyProxyWebSocketRequestHeaders(dst, src)

	assert.Empty(dst.Get("Origin"), "browser Origin must not reach the peer")
	assert.Empty(dst.Get("Sec-Fetch-Dest"), "Sec-Fetch-* must not reach the peer")
	assert.Empty(dst.Get("Sec-WebSocket-Key"), "Sec-WebSocket-* stays dialer-owned")
	assert.Empty(dst.Get("Forwarded"), "forwarded host metadata must not reach the peer")
	assert.Empty(dst.Get("X-Forwarded-Host"), "forwarded host metadata must not reach the peer")
	assert.Equal("Bearer token", dst.Get("Authorization"), "auth must pass through")
}

func TestIsPeerProxyClientHeader(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want bool
	}{
		{"Origin", true},
		{"origin", true},
		{"Sec-Fetch-Site", true},
		{"sec-fetch-mode", true},
		{"Sec-Fetch-Dest", true},
		{"Forwarded", true},
		{"X-Forwarded-Host", true},
		{"x-forwarded-proto", true},
		{"X-Forwarded-For", true},
		{"Authorization", false},
		{"Content-Type", false},
		{"Sec-WebSocket-Key", false},
		{"X-Middleman-Fleet-Host", false},
	} {
		assert.Equal(t, tc.want, isPeerProxyClientHeader(tc.key), "header %q", tc.key)
	}
}
