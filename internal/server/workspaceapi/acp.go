package workspaceapi

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"sync"

	"github.com/coder/websocket"
	"go.kenn.io/forge/internal/terminalwebsocket"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

func serveACP(w http.ResponseWriter, r *http.Request, agent *localruntime.ACP) {
	conn, err := terminalwebsocket.Accept(w, r)
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "chat detached")
	conn.SetReadLimit(terminalwebsocket.ACPCommandReadLimit)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	changes, unsubscribe := agent.Subscribe()
	defer unsubscribe()
	var readers sync.WaitGroup
	readers.Go(func() {
		defer cancel()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var command localruntime.ACPCommand
			if err := json.Unmarshal(data, &command); err != nil {
				return
			}
			if command.Type == "heartbeat" {
				if conn.Write(ctx, websocket.MessageText, []byte(`{"type":"heartbeat"}`)) != nil {
					return
				}
				continue
			}
			if err := agent.Command(command); err != nil {
				// Command failures belong to this caller, not every attached browser.
				data, marshalErr := json.Marshal(map[string]string{"commandError": err.Error()})
				if marshalErr != nil || conn.Write(ctx, websocket.MessageText, data) != nil {
					return
				}
			}
		}
	})
	defer readers.Wait()
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case <-changes:
			data, err := agent.Snapshot()
			if err != nil || conn.Write(ctx, websocket.MessageText, data) != nil {
				return
			}
		}
	}
}
