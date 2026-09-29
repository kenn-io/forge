// Package terminalwebsocket owns the transport policy for terminal streams.
package terminalwebsocket

import (
	"context"
	"net/http"

	"github.com/coder/websocket"
)

// HeartbeatMessage is the terminal liveness probe and reply payload.
const HeartbeatMessage = `{"type":"heartbeat"}`

// ACPReadLimit removes the websocket frame limit on ACP chat connections,
// local and relayed. Chat prompts, transcript pages, and agent content have no
// size limit; the library would otherwise default to 32 KiB.
const ACPReadLimit = -1

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
