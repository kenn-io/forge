package settingsservertest

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.kenn.io/forge/internal/server/streamapi"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
)

func TestAllowedHostsForListenerIncludesBoundLoopbackHost(t *testing.T) {
	assert := assert.New(t)

	allowed := streamapi.AllowedHostsForListener(serverfake.StaticListener{AddrValue: serverfake.StaticListenerAddr("127.0.0.2:8123")})

	assert.Contains(allowed, "127.0.0.2:8123")
	assert.Contains(allowed, "127.0.0.1:8123")
	assert.Contains(allowed, "localhost:8123")
	assert.Contains(allowed, "[::1]:8123")
}

func TestParseLastEventID_HeaderWins(t *testing.T) {
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events?since=42", nil)
	r.Header.Set("Last-Event-ID", "99")
	got, ok := streamapi.ParseLastEventID(r)
	assert.True(t, ok)
	assert.Equal(t, uint64(99), got)
}

func TestParseLastEventID_FallsBackToQuery(t *testing.T) {
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events?since=42", nil)
	got, ok := streamapi.ParseLastEventID(r)
	assert.True(t, ok)
	assert.Equal(t, uint64(42), got)
}

func TestParseLastEventID_AbsentMeansNoCursor(t *testing.T) {
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events", nil)
	_, ok := streamapi.ParseLastEventID(r)
	assert.False(t, ok)
}

func TestParseLastEventID_InvalidHeaderFallsBackToQuery(t *testing.T) {
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events?since=7", nil)
	r.Header.Set("Last-Event-ID", "garbage")
	got, ok := streamapi.ParseLastEventID(r)
	assert.True(t, ok)
	assert.Equal(t, uint64(7), got)
}

func TestParseLastEventID_AllUnparsableMeansNoCursor(t *testing.T) {
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events?since=abc", nil)
	r.Header.Set("Last-Event-ID", "xyz")
	_, ok := streamapi.ParseLastEventID(r)
	assert.False(t, ok)
}
