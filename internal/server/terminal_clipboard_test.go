package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/config"
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

func TestTerminalClipboardWriteRequiresLoopbackAndCSRF(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		fetchSite  string
		wantStatus int
		wantTexts  []string
	}{
		{
			name:       "same origin loopback",
			remoteAddr: "127.0.0.1:54321",
			fetchSite:  "same-origin",
			wantStatus: http.StatusNoContent,
			wantTexts:  []string{"copied through middleman"},
		},
		{
			name:       "remote client",
			remoteAddr: "203.0.113.7:54321",
			fetchSite:  "same-origin",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "cross site",
			remoteAddr: "127.0.0.1:54321",
			fetchSite:  "cross-site",
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clipboard := &recordingTerminalClipboard{}
			srv := New(
				openTestDB(t), nil, nil, "/", nil,
				ServerOptions{TerminalClipboard: clipboard},
			)
			body, err := json.Marshal(map[string]string{
				"text": "copied through middleman",
			})
			require.NoError(t, err)
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/terminal/clipboard",
				bytes.NewReader(body),
			)
			setAcceptedHostForServerTest(req, srv)
			req.RemoteAddr = tt.remoteAddr
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Sec-Fetch-Site", tt.fetchSite)
			rr := httptest.NewRecorder()

			srv.ServeHTTP(rr, req)

			assert.Equal(t, tt.wantStatus, rr.Code, rr.Body.String())
			assert.Equal(t, tt.wantTexts, clipboard.texts)
		})
	}
}

func TestTerminalClipboardWriteRejectsTrustedReverseProxy(t *testing.T) {
	clipboard := &recordingTerminalClipboard{}
	srv := New(
		openTestDB(t), nil, nil, "/", nil,
		ServerOptions{
			TerminalClipboard: clipboard,
			HostCheck: HostCheckOptions{
				Bind:              config.HostKey{Host: "127.0.0.1", Port: "8091"},
				Allowed:           []config.HostKey{{Host: "middleman.example"}},
				TrustReverseProxy: true,
			},
		},
	)
	body := strings.NewReader(`{"text":"proxied copy"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/terminal/clipboard", body)
	req.Host = "127.0.0.1:8091"
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("X-Forwarded-Host", "middleman.example")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
	assert.Empty(t, clipboard.texts)
}

func TestTerminalClipboardWriteRejectsOversizedText(t *testing.T) {
	clipboard := &recordingTerminalClipboard{}
	srv := New(
		openTestDB(t), nil, nil, "/", nil,
		ServerOptions{TerminalClipboard: clipboard},
	)
	body, err := json.Marshal(map[string]string{
		"text": strings.Repeat("x", 1024*1024+1),
	})
	require.NoError(t, err)
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/terminal/clipboard",
		bytes.NewReader(body),
	)
	setAcceptedHostForServerTest(req, srv)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rr.Code, rr.Body.String())
	assert.Empty(t, clipboard.texts)
}

func TestTerminalClipboardWriteReportsNativeFailure(t *testing.T) {
	clipboard := &recordingTerminalClipboard{
		err: errors.New("clipboard unavailable"),
	}
	srv := New(
		openTestDB(t), nil, nil, "/", nil,
		ServerOptions{TerminalClipboard: clipboard},
	)
	body := strings.NewReader(`{"text":"copy me"}`)
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/terminal/clipboard",
		body,
	)
	setAcceptedHostForServerTest(req, srv)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusServiceUnavailable, rr.Code, rr.Body.String())
	assert.Equal(t, []string{"copy me"}, clipboard.texts)
}
