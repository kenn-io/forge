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

func (a *ACP) configure(id, value string) error {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	a.mu.Lock()
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
	defer func() { a.mu.Lock(); a.state.Configuring = false; a.changedLocked(); a.mu.Unlock() }()
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
	data, err := a.call(ctx, "session/set_config_option", map[string]string{"sessionId": a.sessionID, "configId": id, "value": value})
	if err != nil {
		return err
	}
	var result struct {
		ConfigOptions []ACPConfigOption `json:"configOptions"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return err
	}
	a.mu.Lock()
	a.state.ConfigOptions = result.ConfigOptions
	a.changedLocked()
	a.mu.Unlock()
	return nil
}

func (m *Manager) startACP(ctx context.Context, info SessionInfo, command []string, cwd string, strip []string) (*session, error) {
	values, err := m.acpConfigValues(info.TargetKey, nil)
	if err != nil {
		return nil, err
	}
	s, err := startACPSession(ctx, info, command, cwd, strip, m.agentMCPServers())
	if err != nil {
		return nil, err
	}
	a := s.acp
	restoreCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	remaining := maps.Clone(values)
	for len(remaining) > 0 {
		a.mu.Lock()
		options := slices.Clone(a.state.ConfigOptions)
		a.mu.Unlock()
		// A model change can replace the available effort levels and other options.
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
			if err := a.setConfig(restoreCtx, option.ID, value); err != nil {
				_ = a.Stop(context.Background())
				return nil, fmt.Errorf("restore ACP setting %s: %w", option.ID, err)
			}
			break
		}
		if !considered {
			break
		}
	}
	a.saveConfig = func(values map[string]string) error { _, err := m.acpConfigValues(info.TargetKey, values); return err }
	return s, nil
}

// Preferences belong to the execution host and configured client, not a browser
// or workspace. Keep them separate from the user's executable configuration.
func (m *Manager) acpConfigValues(key string, values map[string]string) (map[string]string, error) {
	m.acpPreferencesMu.Lock()
	defer m.acpPreferencesMu.Unlock()
	if m.acpPreferencesPath != "" {
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
