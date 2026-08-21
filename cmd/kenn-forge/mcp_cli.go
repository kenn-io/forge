package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"

	shellquote "github.com/kballard/go-shellquote"
	"github.com/spf13/cobra"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/runtimelock"
)

const mcpTokenEnvironment = "KENN_FORGE_API_TOKEN"

type mcpQuickstartLoader func(context.Context, string, time.Duration) (mcpQuickstartInfo, error)

type mcpQuickstartInfo struct {
	Enabled         bool                    `json:"enabled"`
	Active          bool                    `json:"active"`
	RestartRequired bool                    `json:"restart_required"`
	Transport       string                  `json:"transport"`
	Endpoint        string                  `json:"endpoint,omitempty"`
	Authentication  mcpAuthenticationInfo   `json:"authentication"`
	ClientConfig    *mcpClientConfiguration `json:"client_config,omitempty"`
	NextSteps       []string                `json:"next_steps,omitempty"`
}

type mcpAuthenticationInfo struct {
	Required         bool   `json:"required"`
	TokenEnvironment string `json:"token_environment,omitempty"`
	TokenPath        string `json:"token_path,omitempty"`
}

type mcpClientConfiguration struct {
	MCPServers map[string]mcpClientServer `json:"mcpServers"`
}

type mcpClientServer struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

type mcpSettingsEnvelope struct {
	MCP *mcpSettingsInfo `json:"mcp"`
}

type mcpSettingsInfo struct {
	Enabled            bool   `json:"enabled"`
	RestartRequired    bool   `json:"restart_required"`
	ActiveURL          string `json:"active_url"`
	ActiveRequiresAuth bool   `json:"active_requires_auth"`
}

func newMCPCommand(stdout io.Writer, load mcpQuickstartLoader) *cobra.Command {
	var configPath string
	var asJSON bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Discover and configure the MCP companion",
		Args:  cobra.NoArgs,
	}
	quickstart := &cobra.Command{
		Use:   "quickstart",
		Short: "Print MCP connectivity and client configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info, err := load(cmd.Context(), configPath, timeout)
			if err != nil {
				return err
			}
			if asJSON {
				encoder := json.NewEncoder(stdout)
				encoder.SetIndent("", "  ")
				return encoder.Encode(info)
			}
			return writeMCPQuickstart(stdout, info)
		},
	}
	quickstart.Flags().StringVar(
		&configPath, "config", config.DefaultConfigPath(), "path to config file",
	)
	quickstart.Flags().BoolVar(&asJSON, "json", false, "render output as JSON")
	quickstart.Flags().DurationVar(&timeout, "timeout", 5*time.Second, "daemon request timeout")
	cmd.AddCommand(quickstart)
	return cmd
}

func loadMCPQuickstart(
	ctx context.Context,
	configPath string,
	timeout time.Duration,
) (mcpQuickstartInfo, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return mcpQuickstartInfo{}, fmt.Errorf("mcp quickstart: load config: %w", err)
	}
	if _, err := os.Stat(runtimelock.LockPath(cfg.DataDir)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return stoppedMCPQuickstart(cfg.MCP.Enabled, configPath), nil
		}
		return mcpQuickstartInfo{}, fmt.Errorf("mcp quickstart: inspect daemon lock: %w", err)
	}
	status, err := runtimelock.Read(cfg.DataDir)
	if err != nil {
		return mcpQuickstartInfo{}, fmt.Errorf("mcp quickstart: read daemon status: %w", err)
	}
	if !status.Running {
		return stoppedMCPQuickstart(cfg.MCP.Enabled, configPath), nil
	}
	if status.Metadata == nil {
		return mcpQuickstartInfo{}, fmt.Errorf(
			"mcp quickstart: daemon metadata unavailable: %s",
			status.MetadataUnavailable,
		)
	}

	daemon, err := discoverDaemonHTTP(configPath, timeout)
	if err != nil {
		return mcpQuickstartInfo{}, fmt.Errorf("mcp quickstart: discover daemon: %w", err)
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		strings.TrimRight(daemon.BaseURL, "/")+"/api/v1/settings",
		nil,
	)
	if err != nil {
		return mcpQuickstartInfo{}, fmt.Errorf("mcp quickstart: build settings request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := daemon.Client.Do(request)
	if err != nil {
		return mcpQuickstartInfo{}, fmt.Errorf("mcp quickstart: read settings: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return mcpQuickstartInfo{}, fmt.Errorf(
			"mcp quickstart: read settings: daemon returned %s",
			response.Status,
		)
	}
	var settings mcpSettingsEnvelope
	decoder := json.NewDecoder(response.Body)
	if err := decoder.Decode(&settings); err != nil {
		return mcpQuickstartInfo{}, fmt.Errorf("mcp quickstart: decode settings: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return mcpQuickstartInfo{}, fmt.Errorf("mcp quickstart: decode settings: trailing JSON data")
	}
	if settings.MCP == nil {
		return mcpQuickstartInfo{}, fmt.Errorf("mcp quickstart: daemon did not publish MCP settings")
	}
	return daemonMCPQuickstart(*settings.MCP, daemon.TokenPath), nil
}

func stoppedMCPQuickstart(enabled bool, configPath string) mcpQuickstartInfo {
	startArgs := []string{"kenn-forge", "daemon", "start"}
	if configPath != config.DefaultConfigPath() {
		startArgs = append(startArgs, "--config", configPath)
	}
	startStep := fmt.Sprintf(
		"Start the Forge daemon with `%s`.",
		shellquote.Join(startArgs...),
	)
	info := mcpQuickstartInfo{
		Enabled:        enabled,
		Transport:      "streamable-http",
		Authentication: mcpAuthenticationInfo{},
	}
	if enabled {
		info.NextSteps = []string{startStep}
	} else {
		info.NextSteps = []string{
			"Enable the MCP companion in Forge Settings or set `[mcp].enabled = true`.",
			startStep,
		}
	}
	return info
}

func daemonMCPQuickstart(
	settings mcpSettingsInfo,
	tokenPath string,
) mcpQuickstartInfo {
	info := mcpQuickstartInfo{
		Enabled:         settings.Enabled,
		Active:          settings.ActiveURL != "",
		RestartRequired: settings.RestartRequired,
		Transport:       "streamable-http",
		Endpoint:        settings.ActiveURL,
		Authentication: mcpAuthenticationInfo{
			Required: settings.ActiveRequiresAuth,
		},
	}
	if settings.ActiveRequiresAuth {
		info.Authentication.TokenEnvironment = mcpTokenEnvironment
		info.Authentication.TokenPath = tokenPath
	}
	if info.Active {
		server := mcpClientServer{Type: "http", URL: info.Endpoint}
		if settings.ActiveRequiresAuth {
			server.Headers = map[string]string{
				"Authorization": "Bearer ${" + mcpTokenEnvironment + "}",
			}
		}
		info.ClientConfig = &mcpClientConfiguration{
			MCPServers: map[string]mcpClientServer{"kenn-forge": server},
		}
	}
	switch {
	case !settings.Enabled && info.Active:
		info.NextSteps = []string{"The MCP companion remains active until the Forge daemon restarts."}
	case settings.Enabled && !info.Active:
		info.NextSteps = []string{"Restart the Forge daemon to start the MCP companion."}
	case settings.RestartRequired:
		info.NextSteps = []string{"Restart the Forge daemon to apply the saved MCP settings."}
	case !settings.Enabled:
		info.NextSteps = []string{
			"Enable the MCP companion in Forge Settings, then restart the Forge daemon.",
		}
	}
	return info
}

func writeMCPQuickstart(stdout io.Writer, info mcpQuickstartInfo) error {
	if _, err := fmt.Fprintln(stdout, "OK mcp quickstart"); err != nil {
		return err
	}
	status := "inactive"
	if info.Active {
		status = "active"
	}
	if _, err := fmt.Fprintf(stdout, "status: %s\n", status); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "enabled: %t\n", info.Enabled); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "transport: %s\n", info.Transport); err != nil {
		return err
	}
	if info.Endpoint != "" {
		if _, err := fmt.Fprintf(stdout, "endpoint: %s\n", info.Endpoint); err != nil {
			return err
		}
	}
	if info.Authentication.Required {
		if _, err := fmt.Fprintf(
			stdout,
			"authentication: bearer token via %s\ntoken_path: %s\n",
			info.Authentication.TokenEnvironment,
			info.Authentication.TokenPath,
		); err != nil {
			return err
		}
	} else if info.Active {
		if _, err := fmt.Fprintln(stdout, "authentication: not required"); err != nil {
			return err
		}
	}
	if info.RestartRequired {
		if _, err := fmt.Fprintln(stdout, "restart_required: true"); err != nil {
			return err
		}
	}
	if info.ClientConfig != nil {
		if _, err := fmt.Fprintln(stdout, "client_config:"); err != nil {
			return err
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("  ", "  ")
		if err := encoder.Encode(info.ClientConfig); err != nil {
			return err
		}
	}
	if len(info.NextSteps) > 0 {
		if _, err := fmt.Fprintln(stdout, "next:"); err != nil {
			return err
		}
		for _, step := range info.NextSteps {
			if _, err := fmt.Fprintf(stdout, "- %s\n", step); err != nil {
				return err
			}
		}
	}
	return nil
}
