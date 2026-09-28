// Package codexhooks reuses reviewed Codex hooks between repository worktrees.
package codexhooks

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"time"

	"go.kenn.io/forge/internal/procutil"
)

// Codex owns hook hashing and config edits. Use its local stdio API without
// starting an agent thread or connecting to the user's running app server.
// https://developers.openai.com/codex/app-server
type client struct {
	input  io.Writer
	output *jsontext.Decoder
	id     int
}

func startClient(ctx context.Context, executable string, options []string, cwd string) (*client, func(), error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	release, err := procutil.TryAcquire(ctx, "Codex hook approvals")
	if err != nil {
		cancel()
		return nil, nil, err
	}
	cmd := procutil.CommandContext(ctx, executable, append(options, "app-server", "--stdio")...)
	cmd.Dir = cwd
	input, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		release()
		return nil, nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		_ = input.Close()
		cancel()
		release()
		return nil, nil, err
	}
	// The npm launcher has a child process that inherits these pipes. Close
	// them on cancellation so a blocked RPC read does not outlive the launcher.
	kill := cmd.Cancel
	cmd.Cancel = func() error {
		_ = input.Close()
		_ = output.Close()
		return kill()
	}
	if err := cmd.Start(); err != nil {
		cancel()
		release()
		return nil, nil, err
	}
	closeClient := func() {
		// EOF lets the npm launcher reap its child before cancellation.
		_ = input.Close()
		_ = cmd.Wait()
		cancel()
		release()
	}
	c := &client{input: input, output: jsontext.NewDecoder(output)}
	if err := c.initialize(); err != nil {
		closeClient()
		return nil, nil, err
	}
	return c, closeClient, nil
}

func (c *client) initialize() error {
	if err := c.call("initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "kenn-forge-hooks", "version": "1"},
		"capabilities": map[string]bool{"experimentalApi": true},
	}, nil); err != nil {
		return err
	}
	if _, err := io.WriteString(c.input, "{\"method\":\"initialized\",\"params\":{}}\n"); err != nil {
		return fmt.Errorf("codex initialized: %w", err)
	}
	return nil
}

func (c *client) call(method string, params, result any) error {
	c.id++
	request, err := json.Marshal(map[string]any{"id": c.id, "method": method, "params": params})
	if err != nil {
		return err
	}
	if _, err := c.input.Write(append(request, '\n')); err != nil {
		return err
	}
	for {
		var response struct {
			ID     int            `json:"id"`
			Result jsontext.Value `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.UnmarshalDecode(c.output, &response); err != nil {
			return fmt.Errorf("codex %s: %w", method, err)
		}
		if response.ID != c.id {
			continue
		}
		if response.Error != nil {
			return fmt.Errorf("codex %s: %s", method, response.Error.Message)
		}
		if result == nil {
			return nil
		}
		return json.Unmarshal(response.Result, result)
	}
}
