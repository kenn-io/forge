// Package terminalwebsocket owns the transport policy for terminal streams.
package terminalwebsocket

import (
	"context"
	"net/http"

	"github.com/coder/websocket"
)

// HeartbeatMessage is the terminal liveness probe and reply payload.
const HeartbeatMessage = `{"type":"heartbeat"}`

// ACPCommandReadLimit allows a 64 KiB UTF-8 prompt with six-byte JSON escapes
// plus its command envelope on every local and relayed chat connection.
const ACPCommandReadLimit = 512 << 10

// ACPStateReadLimit allows the bounded retained transcript plus JSON escaping,
// configuration options, and protocol overhead on relay-facing connections.
const ACPStateReadLimit = 32 << 20

// WriteHeartbeat acknowledges a terminal liveness probe.
func WriteHeartbeat(ctx context.Context, conn *websocket.Conn) error {
	return conn.Write(ctx, websocket.MessageText, []byte(HeartbeatMessage))
}

// Accept upgrades a terminal connection with compression suited to repetitive
// terminal repaint streams.
func Accept(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	return websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionContextTakeover,
	})
}

// Dial opens an upstream terminal connection with the same compression policy
// used for browser-facing terminal connections.
func Dial(
	ctx context.Context,
	url string,
	header http.Header,
	client *http.Client,
) (*websocket.Conn, *http.Response, error) {
	return websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPClient:      client,
		HTTPHeader:      header,
		CompressionMode: websocket.CompressionContextTakeover,
	})
}
