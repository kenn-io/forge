package agentactivity

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	shellquote "github.com/kballard/go-shellquote"
)

type Integration string

const (
	IntegrationClaude Integration = "claude"
	IntegrationCodex  Integration = "codex"
)

const hookCommandMarker = "--source middleman-agent-activity"

var integrationHookEvents = map[Integration][]string{
	IntegrationClaude: {
		"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse",
		"PostToolUseFailure", "PermissionRequest", "Notification", "Stop", "SessionEnd",
	},
	IntegrationCodex: {
		"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse",
		"PermissionRequest", "Stop", "SessionEnd",
	},
}

type InstallResult struct {
	Integration Integration
	ConfigPath  string
}

func Install(integration Integration, executable, middlemanConfigPath string) (InstallResult, error) {
	configPath, err := integrationConfigPath(integration)
	if err != nil {
		return InstallResult{}, err
	}
	if strings.TrimSpace(executable) == "" || strings.TrimSpace(middlemanConfigPath) == "" {
		return InstallResult{}, errors.New("agent hook executable and config path are required")
	}
	root, err := readJSONObject(configPath)
	if err != nil {
		return InstallResult{}, err
	}
	hooks, err := objectField(root, "hooks", configPath)
	if err != nil {
		return InstallResult{}, err
	}
	removeMiddlemanHooks(hooks)

	command := shellquote.Join(
		executable, "agent-hook", "run", "--agent", string(integration),
		"--config", middlemanConfigPath,
		"--source", "middleman-agent-activity",
	)
	commandWindows := windowsCommand(
		executable, "agent-hook", "run", "--agent", string(integration),
		"--config", middlemanConfigPath,
		"--source", "middleman-agent-activity",
	)
	if runtime.GOOS == "windows" {
		command = commandWindows
	}
	for _, event := range integrationHookEvents[integration] {
		handler := map[string]any{
			"type":    "command",
			"command": command,
			"timeout": 2,
		}
		if integration == IntegrationCodex {
			handler["commandWindows"] = commandWindows
		}
		entry := map[string]any{"hooks": []any{handler}}
		existing, err := arrayField(hooks, event, configPath)
		if err != nil {
			return InstallResult{}, err
		}
		hooks[event] = append(existing, entry)
	}
	if err := writeJSONObject(configPath, root); err != nil {
		return InstallResult{}, err
	}
	return InstallResult{Integration: integration, ConfigPath: configPath}, nil
}

func Uninstall(integration Integration) (InstallResult, error) {
	configPath, err := integrationConfigPath(integration)
	if err != nil {
		return InstallResult{}, err
	}
	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		return InstallResult{Integration: integration, ConfigPath: configPath}, nil
	} else if err != nil {
		return InstallResult{}, err
	}
	root, err := readJSONObject(configPath)
	if err != nil {
		return InstallResult{}, err
	}
	hooks, err := existingObjectField(root, "hooks", configPath)
	if err != nil {
		return InstallResult{}, err
	}
	if hooks != nil {
		removeMiddlemanHooks(hooks)
	}
	if err := writeJSONObject(configPath, root); err != nil {
		return InstallResult{}, err
	}
	return InstallResult{Integration: integration, ConfigPath: configPath}, nil
}

func ParseIntegration(raw string) (Integration, error) {
	integration := Integration(strings.ToLower(strings.TrimSpace(raw)))
	if _, ok := integrationHookEvents[integration]; !ok {
		return "", fmt.Errorf("unsupported agent hook integration %q (expected claude or codex)", raw)
	}
	return integration, nil
}

func integrationConfigPath(integration Integration) (string, error) {
	dir := ""
	switch integration {
	case IntegrationClaude:
		dir = strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
		if dir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			dir = filepath.Join(home, ".claude")
		}
		return filepath.Join(dir, "settings.json"), nil
	case IntegrationCodex:
		dir = strings.TrimSpace(os.Getenv("CODEX_HOME"))
		if dir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			dir = filepath.Join(home, ".codex")
		}
		return filepath.Join(dir, "hooks.json"), nil
	default:
		return "", fmt.Errorf("unsupported agent hook integration %q", integration)
	}
}

func removeMiddlemanHooks(hooks map[string]any) {
	for event, rawEntries := range hooks {
		entries, ok := rawEntries.([]any)
		if !ok {
			continue
		}
		keptEntries := make([]any, 0, len(entries))
		for _, rawEntry := range entries {
			entry, ok := rawEntry.(map[string]any)
			if !ok {
				keptEntries = append(keptEntries, rawEntry)
				continue
			}
			rawHandlers, ok := entry["hooks"].([]any)
			if !ok {
				keptEntries = append(keptEntries, rawEntry)
				continue
			}
			keptHandlers := make([]any, 0, len(rawHandlers))
			for _, rawHandler := range rawHandlers {
				handler, ok := rawHandler.(map[string]any)
				command, _ := handler["command"].(string)
				if ok && strings.Contains(command, hookCommandMarker) {
					continue
				}
				keptHandlers = append(keptHandlers, rawHandler)
			}
			if len(keptHandlers) == 0 {
				continue
			}
			entry["hooks"] = keptHandlers
			keptEntries = append(keptEntries, entry)
		}
		if len(keptEntries) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = keptEntries
		}
	}
}

func readJSONObject(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var root map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if root == nil {
		root = map[string]any{}
	}
	return root, nil
}

func objectField(root map[string]any, key, path string) (map[string]any, error) {
	existing, err := existingObjectField(root, key, path)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}
	value := map[string]any{}
	root[key] = value
	return value, nil
}

func existingObjectField(root map[string]any, key, path string) (map[string]any, error) {
	raw, ok := root[key]
	if !ok || raw == nil {
		return nil, nil
	}
	value, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s field %q must be an object", path, key)
	}
	return value, nil
}

func arrayField(root map[string]any, key, path string) ([]any, error) {
	raw, ok := root[key]
	if !ok || raw == nil {
		return nil, nil
	}
	value, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s hook event %q must be an array", path, key)
	}
	return value, nil
}

func writeJSONObject(path string, value map[string]any) error {
	writePath := path
	info, err := os.Lstat(path)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		resolved, resolveErr := filepath.EvalSymlinks(path)
		if resolveErr != nil {
			return fmt.Errorf("resolve agent hook config symlink: %w", resolveErr)
		}
		writePath = resolved
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect agent hook config path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(writePath), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(writePath), ".middleman-hooks-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, writePath)
}

func windowsCommand(args ...string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		if arg != "" && !strings.ContainsAny(arg, " \t\"") {
			quoted = append(quoted, arg)
			continue
		}
		quoted = append(quoted, `"`+strings.ReplaceAll(arg, `"`, `\"`)+`"`)
	}
	return strings.Join(quoted, " ")
}
