package localruntime

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/gofrs/flock"

	"go.kenn.io/kit/atomicfile"
)

func configOffers(option ACPConfigOption, value string) bool {
	if option.Type != "select" {
		return false
	}
	for _, choice := range option.Options {
		if choice.Group == "" && choice.Value == value {
			return true
		}
		for _, item := range choice.Options {
			if item.Value == value {
				return true
			}
		}
	}
	return false
}

func (a *ACP) configure(id, value string, generation uint64) error {
	if a.onConfigLockContention == nil {
		a.turnMu.Lock()
	} else if !a.turnMu.TryLock() {
		a.onConfigLockContention()
		a.turnMu.Lock()
	}
	defer a.turnMu.Unlock()
	a.mu.Lock()
	if err := a.supervisionGateLocked(generation); err != nil {
		a.mu.Unlock()
		return err
	}
	index := slices.IndexFunc(a.state.ConfigOptions, func(option ACPConfigOption) bool { return option.ID == id })
	if index < 0 || !configOffers(a.state.ConfigOptions[index], value) {
		a.mu.Unlock()
		return errors.New("choose an option offered by the agent")
	}
	option := a.state.ConfigOptions[index]
	if !a.state.Connected || a.state.Configuring || (a.state.Busy && option.Category != "model" && option.Category != "thought_level") {
		a.mu.Unlock()
		return errors.New("wait for the current operation before changing this setting")
	}
	a.state.Configuring = true
	before := make(map[string]string, len(a.state.ConfigOptions))
	for _, option := range a.state.ConfigOptions {
		if option.Type == "select" {
			before[option.ID] = option.CurrentValue
		}
	}
	a.changedLocked()
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.state.Configuring = false
		a.changedLocked()
		a.mu.Unlock()
		// Prompts queued while settings changed run once the change settles.
		go a.drain()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := a.setConfig(ctx, id, value); err != nil {
		return err
	}
	a.mu.Lock()
	updates := make(map[string]string)
	for _, option := range a.state.ConfigOptions {
		previous, existed := before[option.ID]
		if option.Type == "select" && (option.ID == id || !existed || previous != option.CurrentValue) {
			updates[option.ID] = option.CurrentValue
		}
	}
	a.mu.Unlock()
	if a.saveConfig != nil {
		if err := a.saveConfig(updates); err != nil {
			return fmt.Errorf("setting applied but could not be remembered: %w", err)
		}
	}
	return nil
}

func (a *ACP) setConfig(ctx context.Context, id, value string) error {
	result, err := a.client.SetSessionConfigOption(ctx, acpsdk.SetSessionConfigOptionRequest{
		ValueId: &acpsdk.SetSessionConfigOptionValueId{SessionId: acpsdk.SessionId(a.sessionID), ConfigId: acpsdk.SessionConfigId(id), Value: acpsdk.SessionConfigValueId(value)},
	})
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.state.ConfigOptions = acpConfigOptions(result.ConfigOptions)
	a.changedLocked()
	a.mu.Unlock()
	return nil
}

// startACP restores the remembered options, then the launch's own options,
// which apply to this chat only and are never remembered.
func (m *Manager) startACP(ctx context.Context, info SessionInfo, command []string, cwd string, strip []string, saved *acpSavedSession, options map[string]string) (*ACP, error) {
	values, err := m.acpConfigValues(info.TargetKey, nil)
	if err != nil {
		return nil, err
	}
	a, err := startACPSession(ctx, command, cwd, strip, m.agentMCPServers(), saved)
	if err != nil {
		return nil, err
	}
	options = savedLaunchOptions(options, saved)
	remaining := maps.Clone(values)
	if remaining == nil {
		remaining = make(map[string]string)
	}
	maps.Copy(remaining, options)
	derivedMode := a.deriveCodexMode(ctx, command, SessionEnvironment(os.Environ(), strip), cwd, remaining["mode"])
	if derivedMode != "" {
		remaining["mode"] = derivedMode
	}
	restoreCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := a.applyConfigValues(restoreCtx, remaining, derivedMode); err != nil {
		_ = a.Stop(context.Background())
		return nil, a.withDiagnostics(err)
	}
	a.noteUnappliedLaunchOptions(options)
	a.saveConfig = func(values map[string]string) error { _, err := m.acpConfigValues(info.TargetKey, values); return err }
	a.mu.Lock()
	a.recordPath = m.acpSessionPath(info.Key)
	err = a.persistLocked()
	a.mu.Unlock()
	if err != nil {
		_ = a.Stop(context.Background())
		return nil, err
	}
	return a, nil
}

// applyConfigValues sets each value the agent offers, the model first because
// a model change can replace the available effort levels and other options.
// Values the agent no longer offers are skipped. derivedMode marks a mode Forge
// derived rather than one the user chose; the agent may refuse it.
func (a *ACP) applyConfigValues(ctx context.Context, values map[string]string, derivedMode string) error {
	remaining := maps.Clone(values)
	for len(remaining) > 0 {
		a.mu.Lock()
		options := slices.Clone(a.state.ConfigOptions)
		a.mu.Unlock()
		slices.SortStableFunc(options, func(a, b ACPConfigOption) int {
			if a.Category == "model" && b.Category != "model" {
				return -1
			}
			if b.Category == "model" && a.Category != "model" {
				return 1
			}
			return 0
		})
		considered := false
		for _, option := range options {
			value, ok := remaining[option.ID]
			if !ok || !configOffers(option, value) {
				continue
			}
			delete(remaining, option.ID)
			considered = true
			if option.CurrentValue == value {
				break
			}
			if err := a.setConfig(ctx, option.ID, value); err != nil {
				if option.ID == "mode" && derivedMode != "" {
					a.mu.Lock()
					a.state.Notices = append(a.state.Notices, fmt.Sprintf("Forge couldn't set Codex's resolved mode, so this chat uses codex-acp's default mode: %v", err))
					a.changedLocked()
					a.mu.Unlock()
					break
				}
				return fmt.Errorf("restore ACP setting %s: %w", option.ID, err)
			}
			break
		}
		if !considered {
			break
		}
	}
	return nil
}

// savedLaunchOptions keeps a reloaded chat on the values it last confirmed for
// its launch options, including ones changed in the chat since its launch.
func savedLaunchOptions(options map[string]string, saved *acpSavedSession) map[string]string {
	if saved == nil || len(options) == 0 {
		return options
	}
	confirmed := maps.Clone(options)
	for _, option := range saved.State.ConfigOptions {
		if _, ok := confirmed[option.ID]; ok && option.Type == "select" {
			confirmed[option.ID] = option.CurrentValue
		}
	}
	return confirmed
}

// noteUnappliedLaunchOptions tells the chat which of its launch options the
// agent did not offer, rather than silently starting it on other values.
func (a *ACP) noteUnappliedLaunchOptions(options map[string]string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	noted := false
	for _, id := range slices.Sorted(maps.Keys(options)) {
		value := options[id]
		index := slices.IndexFunc(a.state.ConfigOptions, func(option ACPConfigOption) bool { return option.ID == id })
		if index >= 0 && a.state.ConfigOptions[index].CurrentValue == value {
			continue
		}
		name := id
		if index >= 0 && a.state.ConfigOptions[index].Name != "" {
			name = a.state.ConfigOptions[index].Name
		}
		a.state.Notices = append(a.state.Notices, fmt.Sprintf("The agent doesn't offer %q for %s, so this chat uses its default.", value, name))
		noted = true
	}
	if noted {
		a.changedLocked()
	}
}

// ProbeACPOptions starts a configured ACP target the way a chat launch does,
// without a workspace or Forge tools, applies the remembered options and then
// the given ones, and returns the options the agent then offers. It retains no
// process. Settings use it to offer real choices for a launch's options.
func (m *Manager) ProbeACPOptions(ctx context.Context, targetKey string, options map[string]string) ([]ACPConfigOption, error) {
	target, err := m.target(targetKey)
	if err != nil {
		return nil, err
	}
	if target.Kind != LaunchTargetACP {
		return nil, fmt.Errorf("target %q is not an ACP agent", targetKey)
	}
	if !target.Available {
		reason := target.DisabledReason
		if reason == "" {
			reason = "target not available"
		}
		return nil, fmt.Errorf("target %q not available: %s", targetKey, reason)
	}
	values, err := m.acpConfigValues(target.Key, nil)
	if err != nil {
		return nil, err
	}
	if values == nil {
		values = make(map[string]string)
	}
	maps.Copy(values, options)
	cwd, err := os.MkdirTemp("", "forge-acp-options-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(cwd)
	agent, err := startACPSession(ctx, target.Command, cwd, m.currentStripEnvVars(), []acpsdk.McpServer{}, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = agent.Stop(context.Background()) }()
	applyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := agent.applyConfigValues(applyCtx, values, ""); err != nil {
		return nil, err
	}
	agent.mu.Lock()
	defer agent.mu.Unlock()
	return slices.Clone(agent.state.ConfigOptions), nil
}

// Preferences belong to the execution host and configured client, not a browser
// or workspace. Keep them separate from the user's executable configuration.
func (m *Manager) acpConfigValues(key string, values map[string]string) (map[string]string, error) {
	m.acpPreferencesMu.Lock()
	defer m.acpPreferencesMu.Unlock()
	if m.acpPreferencesPath != "" {
		if err := os.MkdirAll(filepath.Dir(m.acpPreferencesPath), 0o700); err != nil {
			return nil, err
		}
		lock := flock.New(m.acpPreferencesPath + ".lock")
		if err := lock.Lock(); err != nil {
			return nil, err
		}
		defer lock.Close()
		data, err := os.ReadFile(m.acpPreferencesPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err == nil {
			if err := json.Unmarshal(data, &m.acpPreferences); err != nil {
				return nil, err
			}
		}
	}
	if values == nil {
		return maps.Clone(m.acpPreferences[key]), nil
	}
	next := maps.Clone(m.acpPreferences)
	if next == nil {
		next = make(map[string]map[string]string)
	}
	remembered := maps.Clone(next[key])
	if remembered == nil {
		remembered = make(map[string]string)
	}
	maps.Copy(remembered, values)
	next[key] = remembered
	if m.acpPreferencesPath != "" {
		data, err := json.Marshal(next)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(m.acpPreferencesPath), 0o700); err != nil {
			return nil, err
		}
		if err := atomicfile.WriteFile(m.acpPreferencesPath, data, atomicfile.WithPerm(0o600)); err != nil && !errors.Is(err, atomicfile.ErrPublished) {
			return nil, err
		}
	}
	m.acpPreferences = next
	return maps.Clone(remembered), nil
}

// Project agent-owned settings into the browser's select controls. ACP union
// decoding belongs to the SDK; these are Forge view models.
func acpConfigOptions(options []acpsdk.SessionConfigOption) []ACPConfigOption {
	result := make([]ACPConfigOption, 0, len(options))
	for _, option := range options {
		selectOption := option.Select
		if selectOption == nil {
			continue
		}
		view := ACPConfigOption{ID: string(selectOption.Id), Name: selectOption.Name, Type: "select", CurrentValue: string(selectOption.CurrentValue)}
		if selectOption.Category != nil {
			view.Category = string(*selectOption.Category)
		}
		if selectOption.Description != nil {
			view.Description = *selectOption.Description
		}
		if selectOption.Options.Ungrouped != nil {
			for _, choice := range *selectOption.Options.Ungrouped {
				view.Options = append(view.Options, ACPConfigChoice{Value: string(choice.Value), Name: choice.Name})
			}
		}
		if selectOption.Options.Grouped != nil {
			for _, group := range *selectOption.Options.Grouped {
				choices := make([]ACPConfigChoice, 0, len(group.Options))
				for _, choice := range group.Options {
					choices = append(choices, ACPConfigChoice{Value: string(choice.Value), Name: choice.Name})
				}
				view.Options = append(view.Options, ACPConfigChoice{Group: string(group.Group), Name: group.Name, Options: choices})
			}
		}
		result = append(result, view)
	}
	return result
}
