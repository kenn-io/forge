// Package terminalwebsocket owns the transport policy for terminal streams.
package terminalwebsocket

import (
	"context"
	"net/http"

	"github.com/coder/websocket"
)

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
) (*websocket.Conn, *http.Response, error) {
	return websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader:      header,
		CompressionMode: websocket.CompressionContextTakeover,
	})
}
