package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.kenn.io/kit/agenthook"
	"go.kenn.io/middleman/internal/agentactivity"
	"go.kenn.io/middleman/internal/config"
)

const (
	agentHookSource = "middleman-agent-activity"
	agentHookMarker = "--source " + agentHookSource
)

func runAgentHookCLI(args []string, stdin io.Reader, stdout io.Writer) error {
	cmd := newAgentHookCommand(stdin, stdout)
	cmd.SetArgs(normalizeSingleDashLongFlags(args))
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	return cmd.Execute()
}

func receiveAgentHook(
	ctx context.Context,
	agent, configPath, source string,
	stdin io.Reader,
	stdout io.Writer,
) error {
	if source != agentHookSource {
		return nil
	}
	integration, err := agenthook.ParseAgent(agent)
	if err != nil {
		return nil
	}
	handler := agentHookRelay{
		agent:      integration,
		configPath: configPath,
	}
	if err := agenthook.Handle(ctx, integration, stdin, stdout, handler); err != nil {
		return nil
	}
	return nil
}

type agentHookRelay struct {
	agenthook.NoopHandler
	agent      agenthook.Agent
	configPath string
}

func (h agentHookRelay) SessionStart(
	ctx context.Context,
	input agenthook.SessionStartInput,
) (agenthook.SessionStartOutput, error) {
	return agenthook.SessionStartOutput{
		AdditionalContext: h.relay(ctx, input.CommonInput),
	}, nil
}

func (h agentHookRelay) UserPromptSubmit(
	ctx context.Context,
	input agenthook.UserPromptSubmitInput,
) (agenthook.UserPromptSubmitOutput, error) {
	h.relay(ctx, input.CommonInput)
	return agenthook.UserPromptSubmitOutput{}, nil
}

func (h agentHookRelay) PreToolUse(
	ctx context.Context,
	input agenthook.PreToolUseInput,
) (agenthook.PreToolUseOutput, error) {
	h.relay(ctx, input.CommonInput)
	return agenthook.PreToolUseOutput{}, nil
}

func (h agentHookRelay) PostToolUse(
	ctx context.Context,
	input agenthook.PostToolUseInput,
) (agenthook.PostToolUseOutput, error) {
	h.relay(ctx, input.CommonInput)
	return agenthook.PostToolUseOutput{}, nil
}

func (h agentHookRelay) PostToolUseFailure(
	ctx context.Context,
	input agenthook.PostToolUseFailureInput,
) (agenthook.PostToolUseFailureOutput, error) {
	h.relay(ctx, input.CommonInput)
	return agenthook.PostToolUseFailureOutput{}, nil
}

func (h agentHookRelay) PermissionRequest(
	ctx context.Context,
	input agenthook.PermissionRequestInput,
) (agenthook.PermissionRequestOutput, error) {
	h.relay(ctx, input.CommonInput)
	return agenthook.PermissionRequestOutput{}, nil
}

func (h agentHookRelay) Notification(
	ctx context.Context,
	input agenthook.NotificationInput,
) (agenthook.NotificationOutput, error) {
	h.relay(ctx, input.CommonInput)
	return agenthook.NotificationOutput{}, nil
}

func (h agentHookRelay) Stop(
	ctx context.Context,
	input agenthook.StopInput,
) (agenthook.StopOutput, error) {
	h.relay(ctx, input.CommonInput)
	return agenthook.StopOutput{}, nil
}

func (h agentHookRelay) SessionEnd(
	ctx context.Context,
	input agenthook.SessionEndInput,
) (agenthook.SessionEndOutput, error) {
	h.relay(ctx, input.CommonInput)
	return agenthook.SessionEndOutput{}, nil
}

func (h agentHookRelay) relay(ctx context.Context, input agenthook.CommonInput) string {
	daemon, err := discoverDaemonHTTP(h.configPath, 1500*time.Millisecond)
	if err != nil {
		return ""
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		daemon.BaseURL+"/api/v1/agent-hooks/"+url.PathEscape(string(h.agent)),
		bytes.NewReader(input.Raw),
	)
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(
		"X-Middleman-Runtime-Session-Key",
		os.Getenv(agentactivity.RuntimeSessionKeyEnv),
	)
	resp, err := daemon.Client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return ""
	}
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(responseBody) > 1<<20 {
		return ""
	}
	var output struct {
		HookOutput *struct {
			HookSpecificOutput struct {
				AdditionalContext string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		} `json:"hook_output"`
	}
	if err := json.Unmarshal(responseBody, &output); err != nil || output.HookOutput == nil {
		return ""
	}
	return output.HookOutput.HookSpecificOutput.AdditionalContext
}

func runAgentHookInstall(action string, args []string, stdout io.Writer) error {
	cmd := newAgentHookCommand(strings.NewReader(""), stdout)
	cmd.SetArgs(normalizeSingleDashLongFlags(append([]string{action}, args...)))
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	return cmd.Execute()
}

func selectedAgentHookProfiles(raw string) ([]agenthook.Agent, error) {
	if strings.TrimSpace(raw) != "" {
		agent, err := agenthook.ParseAgent(raw)
		if err != nil {
			return nil, err
		}
		return []agenthook.Agent{agent}, nil
	}
	profiles := agenthook.Profiles()
	agents := make([]agenthook.Agent, 0, len(profiles))
	for _, profile := range profiles {
		agents = append(agents, profile.Agent)
	}
	return agents, nil
}

func installAgentHooks(action, configPath, rawAgent, binary string, stdout io.Writer) error {
	agents, err := selectedAgentHookProfiles(rawAgent)
	if err != nil {
		return err
	}
	if action == "uninstall" {
		for _, agent := range agents {
			result, err := agenthook.Uninstall(agent, "", agentHookMarker)
			if err != nil {
				return err
			}
			profile, _ := agenthook.LookupProfile(agent)
			if _, err := fmt.Fprintf(
				stdout,
				"Removed middleman %s hooks from %s\n",
				profile.DisplayName,
				result.ConfigPath,
			); err != nil {
				return err
			}
		}
		return nil
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if cfg.DataDirWasRelative() {
		return fmt.Errorf("agent hook install requires an absolute data_dir")
	}
	absoluteConfigPath, err := filepath.Abs(configPath)
	if err != nil {
		return fmt.Errorf("resolve agent hook config path: %w", err)
	}
	executable := strings.TrimSpace(binary)
	if executable == "" {
		executable, err = os.Executable()
		if err != nil {
			return fmt.Errorf("resolve middleman executable: %w", err)
		}
	}
	for _, agent := range agents {
		profile, _ := agenthook.LookupProfile(agent)
		hooks := make([]agenthook.Hook, 0, len(profile.SupportedEvents))
		for _, event := range profile.SupportedEvents {
			hooks = append(hooks, agenthook.Hook{Event: event, Timeout: 2 * time.Second})
		}
		result, err := agenthook.Install(agent, agenthook.InstallOptions{
			Executable: executable,
			Arguments: []string{
				"agent-hook", "run",
				"--agent", string(agent),
				"--config", absoluteConfigPath,
				"--source", agentHookSource,
			},
			Marker: agentHookMarker,
			Hooks:  hooks,
		})
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(
			stdout,
			"Installed middleman %s hooks in %s\n",
			profile.DisplayName,
			result.ConfigPath,
		); err != nil {
			return err
		}
		if agent == agenthook.AgentCodex {
			if _, err := fmt.Fprintln(
				stdout,
				"Open /hooks in Codex once to review and trust the new hook commands.",
			); err != nil {
				return err
			}
		}
	}
	return nil
}
