package settingsservertest

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/streamapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	return dbtest.Open(t)
}

type staticListenerAddr string

func (a staticListenerAddr) Network() string { return "tcp" }

func (a staticListenerAddr) String() string { return string(a) }

type staticListener struct {
	addr net.Addr
}

func (l staticListener) Accept() (net.Conn, error) { return nil, errors.New("unused listener") }

func (l staticListener) Close() error { return nil }

func (l staticListener) Addr() net.Addr { return l.addr }

func TestAllowedHostsForListenerIncludesBoundLoopbackHost(t *testing.T) {
	assert := assert.New(t)

	allowed := streamapi.AllowedHostsForListener(staticListener{addr: staticListenerAddr("127.0.0.2:8123")})

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
