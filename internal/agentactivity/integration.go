package agentactivity

import (
	"bytes"
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

// HookSource marks middleman's own hook invocations. The receiver refuses input
// without it, and uninstall recognizes installed handlers by it.
const HookSource = "middleman-agent-activity"

const (
	hookCommandMarker  = "--source " + HookSource
	hookTimeoutSeconds = 2
)

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

func Install(integration Integration, executable, stateDir string) (InstallResult, error) {
	configPath, err := integrationConfigPath(integration)
	if err != nil {
		return InstallResult{}, err
	}
	if strings.TrimSpace(executable) == "" || strings.TrimSpace(stateDir) == "" {
		return InstallResult{}, errors.New("agent hook executable and state directory are required")
	}
	root, err := readJSONObject(configPath)
	if err != nil {
		return InstallResult{}, err
	}
	hooks, err := objectField(root, "hooks", configPath)
	if err != nil {
		return InstallResult{}, err
	}
	// Installing is idempotent: drop any handler from an earlier install, which
	// may point at a stale binary path, before adding the current one.
	removeMiddlemanHooks(hooks)

	command, commandWindows := hookCommands(executable, stateDir)
	for _, event := range integrationHookEvents[integration] {
		existing, err := arrayField(hooks, event, configPath)
		if err != nil {
			return InstallResult{}, err
		}
		hooks[event] = append(existing, hookEntry(integration, command, commandWindows))
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

// hookCommands renders the receiver invocation for the running platform and,
// separately, for Windows. Codex configs carry both.
func hookCommands(executable, stateDir string) (command, commandWindows string) {
	args := []string{
		executable, "agent-hook", "run",
		"--state-dir", stateDir,
		"--source", HookSource,
	}
	commandWindows = windowsCommand(args...)
	if runtime.GOOS == "windows" {
		return commandWindows, commandWindows
	}
	return shellquote.Join(args...), commandWindows
}

// hookEntry builds one config entry that runs the middleman receiver.
func hookEntry(integration Integration, command, commandWindows string) map[string]any {
	handler := map[string]any{
		"type":    "command",
		"command": command,
		"timeout": hookTimeoutSeconds,
	}
	if integration == IntegrationCodex {
		handler["commandWindows"] = commandWindows
	}
	return map[string]any{"hooks": []any{handler}}
}

func integrationConfigPath(integration Integration) (string, error) {
	switch integration {
	case IntegrationClaude:
		return agentConfigPath("CLAUDE_CONFIG_DIR", ".claude", "settings.json")
	case IntegrationCodex:
		return agentConfigPath("CODEX_HOME", ".codex", "hooks.json")
	default:
		return "", fmt.Errorf("unsupported agent hook integration %q", integration)
	}
}

func agentConfigPath(dirEnv, homeDirName, fileName string) (string, error) {
	dir := strings.TrimSpace(os.Getenv(dirEnv))
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, homeDirName)
	}
	return filepath.Join(dir, fileName), nil
}

func removeMiddlemanHooks(hooks map[string]any) {
	for event, rawEntries := range hooks {
		entries, ok := rawEntries.([]any)
		if !ok {
			continue
		}
		keptEntries := make([]any, 0, len(entries))
		for _, rawEntry := range entries {
			if entry, keep := entryWithoutMiddlemanHandlers(rawEntry); keep {
				keptEntries = append(keptEntries, entry)
			}
		}
		if len(keptEntries) == 0 {
			delete(hooks, event)
			continue
		}
		hooks[event] = keptEntries
	}
}

// entryWithoutMiddlemanHandlers strips middleman handlers from one hook entry.
// Entries middleman does not recognize are kept verbatim, and an entry that
// held nothing but middleman handlers is dropped instead of left empty.
func entryWithoutMiddlemanHandlers(rawEntry any) (any, bool) {
	entry, ok := rawEntry.(map[string]any)
	if !ok {
		return rawEntry, true
	}
	handlers, ok := entry["hooks"].([]any)
	if !ok {
		return rawEntry, true
	}
	keptHandlers := make([]any, 0, len(handlers))
	for _, handler := range handlers {
		if !isMiddlemanHandler(handler) {
			keptHandlers = append(keptHandlers, handler)
		}
	}
	if len(keptHandlers) == 0 {
		return nil, false
	}
	entry["hooks"] = keptHandlers
	return entry, true
}

func isMiddlemanHandler(rawHandler any) bool {
	handler, ok := rawHandler.(map[string]any)
	if !ok {
		return false
	}
	command, _ := handler["command"].(string)
	return strings.Contains(command, hookCommandMarker)
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
	decoder := json.NewDecoder(bytes.NewReader(data))
	// Agent configs may hold integers past float64 precision, and rewriting the
	// file must not round values middleman does not own.
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
	writePath, err := resolvedConfigWritePath(path)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFileAtomic(writePath, ".middleman-hooks-*", data)
}

// resolvedConfigWritePath follows a symlinked agent config to its target so an
// install rewrites the file the user linked to instead of replacing the link.
func resolvedConfigWritePath(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return path, nil
		}
		return "", fmt.Errorf("inspect agent hook config path: %w", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return path, nil
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve agent hook config symlink: %w", err)
	}
	return resolved, nil
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
