package runtimetest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/server"
)

type recordingTerminalClipboard struct {
	texts []string
	err   error
}

func (c *recordingTerminalClipboard) WriteText(
	_ context.Context,
	text string,
) error {
	c.texts = append(c.texts, text)
	return c.err
}

func TestTerminalClipboardWriteThroughTrustedReverseProxyRequiresLocalClient(
	t *testing.T,
) {
	tests := []struct {
		name         string
		remoteAddr   string
		forwardedFor string
		wantStatus   int
		wantTexts    []string
	}{
		{
			name:         "local client",
			remoteAddr:   "127.0.0.1:54321",
			forwardedFor: "127.0.0.1",
			wantStatus:   http.StatusNoContent,
			wantTexts:    []string{"proxied copy"},
		},
		{
			name:         "spoofed local client from remote peer",
			remoteAddr:   "203.0.113.8:54321",
			forwardedFor: "127.0.0.1",
			wantStatus:   http.StatusForbidden,
		},
		{
			name:         "remote client",
			remoteAddr:   "127.0.0.1:54321",
			forwardedFor: "203.0.113.7",
			wantStatus:   http.StatusForbidden,
		},
		{
			name:       "missing forwarded client",
			remoteAddr: "127.0.0.1:54321",
			wantStatus: http.StatusForbidden,
		},
		{
			name:         "multiple forwarded clients",
			remoteAddr:   "127.0.0.1:54321",
			forwardedFor: "127.0.0.1, 203.0.113.7",
			wantStatus:   http.StatusForbidden,
		},
		{
			name:         "malformed forwarded client",
			remoteAddr:   "127.0.0.1:54321",
			forwardedFor: "not-an-ip",
			wantStatus:   http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clipboard := &recordingTerminalClipboard{}
			srv := server.New(
				openTestDB(t), nil, nil, "/", nil,
				server.ServerOptions{
					TerminalClipboard: clipboard,
					HostCheck: server.HostCheckOptions{
						Bind: config.HostKey{
							Host: "127.0.0.1",
							Port: "8091",
						},
						Allowed: []config.HostKey{
							{Host: "forge.example"},
						},
						TrustReverseProxy: true,
					},
				},
			)
			body := strings.NewReader(`{"text":"proxied copy"}`)
			req := httptest.NewRequestWithContext(t.Context(),
				http.MethodPost,
				"/api/v1/terminal/clipboard",
				body,
			)
			req.Host = "127.0.0.1:8091"
			req.RemoteAddr = tt.remoteAddr
			req.Header.Set("X-Forwarded-Host", "forge.example")
			if tt.forwardedFor != "" {
				req.Header.Set("X-Forwarded-For", tt.forwardedFor)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Sec-Fetch-Site", "same-origin")
			rr := httptest.NewRecorder()

			srv.ServeHTTP(rr, req)

			assert.Equal(t, tt.wantStatus, rr.Code, rr.Body.String())
			assert.Equal(t, tt.wantTexts, clipboard.texts)
		})
	}
}
