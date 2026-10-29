// Package fleettunnel carries hub-to-spoke HTTP over a connection the spoke
// opens to the hub. A spoke that can reach its hub, but cannot be reached by
// it, still serves the hub's snapshot, proxy, and terminal requests.
//
// The spoke dials one WebSocket to the hub and multiplexes it with yamux.
// libp2p's yamux creates its timers per session and stream rather than in a
// process-wide pool, so tunnels can run inside testing/synctest bubbles. The
// hub opens one stream per HTTP connection and the spoke serves each stream
// with its ordinary HTTP handler, so every existing route, bearer check, and
// WebSocket upgrade works unchanged inside the tunnel.
package fleettunnel

import (
	"errors"
	"log/slog"

	"github.com/libp2p/go-yamux/v5"
)

// ErrNotConnected reports that a node has no live tunnel and its traffic may
// not fall back to a direct connection.
var ErrNotConnected = errors.New("fleet tunnel is not connected")

// Addr identifies the tunnel as the remote address of every request it carries.
// It is not an IP address, so no loopback or network-locality privilege applies
// to tunneled requests.
type Addr struct{}

func (Addr) Network() string { return "fleet-tunnel" }
func (Addr) String() string  { return "fleet-tunnel" }

func sessionConfig() *yamux.Config {
	config := yamux.DefaultConfig()
	// Keepalive pings detect a half-open tunnel; both ends reconnect or
	// fall back once the session closes.
	config.EnableKeepAlive = true
	config.LogOutput = slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug).Writer()
	return config
}
