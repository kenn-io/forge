package main

import (
	"encoding/json/v2"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
	"go.kenn.io/forge/internal/config"
)

// mcpListenerStatus is the CLI discovery contract for existing HTTP listeners.
// TokenPath identifies credentials without placing their contents in output.
type mcpListenerStatus struct {
	PID        int    `json:"pid"`
	Transport  string `json:"transport"`
	URL        string `json:"url"`
	BackendURL string `json:"backend_url"`
	TokenPath  string `json:"token_path,omitempty"`
}

func newMCPStatusCommand(stdout io.Writer, load mcpQuickstartLoader) *cobra.Command {
	var configPath string
	var asJSON bool
	var timeout time.Duration
	command := &cobra.Command{
		Use:   "status",
		Short: "List the running HTTP MCP listener",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			info, err := load(command.Context(), configPath, timeout)
			if err != nil {
				return err
			}
			listeners := []mcpListenerStatus{}
			if info.Active {
				listeners = append(listeners, mcpListenerStatus{
					PID: info.pid, Transport: "http", URL: info.Endpoint,
					BackendURL: info.backendURL, TokenPath: info.Authentication.TokenPath,
				})
			}
			if asJSON {
				return json.MarshalWrite(stdout, listeners)
			}
			if len(listeners) == 0 {
				_, err := fmt.Fprintln(stdout, "No HTTP MCP listeners are running.")
				return err
			}
			_, err = fmt.Fprintf(stdout, "MCP %s (pid %d)\n", listeners[0].URL, listeners[0].PID)
			return err
		},
	}
	command.Flags().StringVar(&configPath, "config", config.DefaultConfigPath(), "path to config file")
	command.Flags().BoolVar(&asJSON, "json", false, "render output as JSON")
	command.Flags().DurationVar(&timeout, "timeout", 5*time.Second, "daemon request timeout")
	return command
}
