package codexhooks

import (
	"context"
	"maps"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	gitcmd "go.kenn.io/kit/git/cmd"
	gitworktree "go.kenn.io/kit/git/worktree"
)

type hookState struct {
	Enabled     *bool  `json:"enabled"`
	TrustedHash string `json:"trusted_hash"`
}

type hookConfig struct {
	Hooks struct {
		State map[string]hookState `json:"state"`
	} `json:"hooks"`
}

type hookMetadata struct {
	Key         string `json:"key"`
	Source      string `json:"source"`
	SourcePath  string `json:"sourcePath"`
	CurrentHash string `json:"currentHash"`
	TrustStatus string `json:"trustStatus"`
	Enabled     bool   `json:"enabled"`
}

type configEdit struct {
	KeyPath       string `json:"keyPath"`
	Value         string `json:"value"`
	MergeStrategy string `json:"mergeStrategy"`
}

// ReuseApprovals carries user approvals into sibling worktrees, only when
// Codex reports the same hook hash. It never enables hooks or grants new trust.
func ReuseApprovals(ctx context.Context, command []string, cwd string) error {
	if len(command) == 0 || strings.TrimSuffix(filepath.Base(command[0]), ".exe") != "codex" {
		return nil
	}
	if command[0] != filepath.Base(command[0]) && !filepath.IsAbs(command[0]) {
		return nil // The launcher rejects worktree-relative executables.
	}
	options, supported := configOptions(command[1:])
	if !supported {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cwd, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return err
	}
	output, err := gitcmd.New().Output(ctx, cwd, "worktree", "list", "--porcelain")
	if err != nil {
		return err
	}
	var siblings []string
	for _, worktree := range gitworktree.ParsePorcelain(string(output)) {
		if worktree.Bare || worktree.Prunable {
			continue
		}
		path, err := filepath.EvalSymlinks(worktree.Path)
		if err == nil && path != cwd {
			siblings = append(siblings, path)
		}
	}
	if len(siblings) == 0 {
		return nil
	}
	c, closeClient, err := startClient(ctx, command[0], options, cwd)
	if err != nil {
		return err
	}
	defer closeClient()

	var config struct {
		Layers []struct {
			Name struct {
				Type string `json:"type"`
				File string `json:"file"`
			} `json:"name"`
			Version        string     `json:"version"`
			DisabledReason *string    `json:"disabledReason"`
			Config         hookConfig `json:"config"`
		} `json:"layers"`
	}
	if err := c.call("config/read", map[string]any{"includeLayers": true}, &config); err != nil {
		return err
	}
	states := make(map[string]hookState)
	var file, version string
	for _, layer := range config.Layers {
		// Project-supplied trust entries must never become user approvals.
		if layer.Name.Type != "user" || layer.DisabledReason != nil {
			continue
		}
		file, version = layer.Name.File, layer.Version
		maps.Copy(states, layer.Config.Hooks.State)
	}
	if len(states) == 0 {
		return nil
	}
	var listed struct {
		Data []struct {
			Hooks []hookMetadata `json:"hooks"`
		} `json:"data"`
	}
	if err := c.call("hooks/list", map[string]any{"cwds": []string{cwd}}, &listed); err != nil {
		return err
	}
	var edits []configEdit
	for _, entry := range listed.Data {
		edits = append(edits, approvalEdits(cwd, siblings, states, entry.Hooks)...)
	}
	if len(edits) == 0 {
		return nil
	}
	// Let Codex preserve user config formatting and reject concurrent edits.
	return c.call("config/batchWrite", map[string]any{
		"edits": edits, "filePath": file, "expectedVersion": version,
	}, nil)
}

func approvalEdits(cwd string, siblings []string, states map[string]hookState, hooks []hookMetadata) []configEdit {
	var edits []configEdit
	for _, hook := range hooks {
		if hook.Source != "project" || !hook.Enabled || hook.TrustStatus != "untrusted" || hook.CurrentHash == "" {
			continue
		}
		if _, exists := states[hook.Key]; exists {
			continue // Preserve decisions already made for this worktree.
		}
		rel, err := filepath.Rel(cwd, hook.SourcePath)
		if err != nil || (rel != filepath.Join(".codex", "hooks.json") && rel != filepath.Join(".codex", "config.toml")) {
			continue
		}
		suffix, ok := strings.CutPrefix(hook.Key, hook.SourcePath+":")
		if !ok {
			continue
		}
		for _, sibling := range siblings {
			state := states[filepath.Join(sibling, rel)+":"+suffix]
			if state.TrustedHash != hook.CurrentHash || (state.Enabled != nil && !*state.Enabled) {
				continue
			}
			edits = append(edits, configEdit{
				KeyPath:       toml.Key{"hooks", "state", hook.Key, "trusted_hash"}.String(),
				Value:         hook.CurrentHash,
				MergeStrategy: "replace",
			})
			break
		}
	}
	return edits
}

// Codex's app-server does not accept named profiles or allow editing their
// config files. Leave those launches to native review, preserving profile
// approvals and disabled hooks instead of inspecting the wrong configuration.
func configOptions(args []string) ([]string, bool) {
	var options []string
	for i := 0; i < len(args) && args[i] != "--"; i++ {
		key, _, inline := strings.Cut(args[i], "=")
		switch key {
		case "-p", "--profile":
			return nil, false
		case "-c", "--config", "--enable", "--disable":
			if inline {
				options = append(options, args[i])
			} else if i+1 < len(args) {
				options = append(options, args[i], args[i+1])
				i++
			} else {
				return nil, false
			}
		default:
			if strings.HasPrefix(args[i], "-p") && !strings.HasPrefix(args[i], "--") {
				return nil, false
			}
			if strings.HasPrefix(args[i], "-c") && !strings.HasPrefix(args[i], "--") {
				options = append(options, args[i])
			}
		}
	}
	return options, true
}
