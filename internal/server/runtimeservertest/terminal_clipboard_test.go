package runtimeservertest

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.kenn.io/forge/internal/server/authapi"
)

func TestLocalTerminalClipboardRequestRecognizesNonLoopbackInterface(
	t *testing.T,
) {
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/terminal/clipboard", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("X-Forwarded-For", "192.0.2.10")
	interfaceAddrs := func() ([]net.Addr, error) {
		return []net.Addr{
			&net.IPNet{
				IP:   net.ParseIP("192.0.2.10"),
				Mask: net.CIDRMask(24, 32),
			},
		}, nil
	}

	assert.True(t, authapi.IsLocalTerminalClipboardRequestWithAddrs(
		req,
		true,
		interfaceAddrs,
	))
}
