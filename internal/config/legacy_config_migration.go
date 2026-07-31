package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const legacyConfigMigrationMarker = ".legacy-config-migrated"

var legacyConfigValueReplacements = [][2]string{
	{"MIDDLEMAN_GITHUB_TOKEN", "KENN_FORGE_GITHUB_TOKEN"},
	{"MIDDLEMAN_GITLAB_TOKEN", "KENN_FORGE_GITLAB_TOKEN"},
	{"MIDDLEMAN_FORGEJO_TOKEN", "KENN_FORGE_FORGEJO_TOKEN"},
	{"MIDDLEMAN_GITEA_TOKEN", "KENN_FORGE_GITEA_TOKEN"},
}

func migrateLegacyConfig(configPath string) error {
	if os.Getenv("KENN_FORGE_HOME") != "" ||
		filepath.Clean(configPath) != filepath.Clean(DefaultConfigPath()) {
		return nil
	}

	oldHome := legacyDefaultHome()
	newHome := DefaultDataDir()
	markerPath := filepath.Join(newHome, legacyConfigMigrationMarker)
	markerVersion, err := readLegacyConfigMigrationMarker(markerPath)
	if err != nil {
		return err
	}
	if markerVersion == "v2\n" {
		return nil
	}

	sourcePath := filepath.Join(oldHome, "config.toml")
	source, err := os.ReadFile(sourcePath)
	if errors.Is(err, os.ErrNotExist) {
		if markerVersion == "v1\n" {
			return fmt.Errorf("complete legacy config migration from %s: source config is missing", sourcePath)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read legacy config %s: %w", sourcePath, err)
	}
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return fmt.Errorf("inspect legacy config %s: %w", sourcePath, err)
	}
	legacyConfig, err := Load(sourcePath)
	if err != nil {
		return fmt.Errorf("load legacy config %s for migration to %s: %w", sourcePath, configPath, err)
	}
	transformed, err := transformLegacyConfig(source, oldHome, newHome)
	if err != nil {
		return fmt.Errorf("transform legacy config %s: %w", sourcePath, err)
	}

	if err := os.MkdirAll(newHome, 0o700); err != nil {
		return fmt.Errorf("create Kenn Forge config directory %s: %w", newHome, err)
	}
	tmp, err := os.CreateTemp(newHome, ".legacy-config-*.toml")
	if err != nil {
		return fmt.Errorf("create migrated config beside %s: %w", configPath, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(sourceInfo.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("preserve legacy config mode from %s: %w", sourcePath, err)
	}
	if _, err := tmp.Write(transformed); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write migrated config for %s: %w", configPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close migrated config for %s: %w", configPath, err)
	}
	if _, err := Load(tmpPath); err != nil {
		return fmt.Errorf("validate legacy config migration from %s to %s: %w", sourcePath, configPath, err)
	}

	if err := copyLegacyCredentialPaths(legacyConfig, oldHome, newHome); err != nil {
		return err
	}

	destination, err := os.ReadFile(configPath)
	switch {
	case markerVersion == "v1\n":
		if err != nil {
			return fmt.Errorf("complete legacy config migration to %s: %w", configPath, err)
		}
	case errors.Is(err, os.ErrNotExist):
		if err := os.Rename(tmpPath, configPath); err != nil {
			return fmt.Errorf("publish migrated config %s: %w", configPath, err)
		}
	case err != nil:
		return fmt.Errorf("inspect Kenn Forge config %s: %w", configPath, err)
	case bytes.Equal(destination, transformed):
	case bytes.Equal(destination, []byte(defaultConfigContents())):
		if err := os.Rename(tmpPath, configPath); err != nil {
			return fmt.Errorf("replace generated config %s: %w", configPath, err)
		}
	default:
		return fmt.Errorf("legacy config %s conflicts with existing Kenn Forge config %s", sourcePath, configPath)
	}

	return writeLegacyConfigMigrationMarker(markerPath)
}

func transformLegacyConfig(body []byte, oldHome, newHome string) ([]byte, error) {
	out := bytes.Clone(body)
	for _, replacement := range legacyConfigValueReplacements {
		out = bytes.ReplaceAll(out, []byte(`"`+replacement[0]+`"`), []byte(`"`+replacement[1]+`"`))
		out = bytes.ReplaceAll(out, []byte(`'`+replacement[0]+`'`), []byte(`'`+replacement[1]+`'`))
	}
	out = bytes.ReplaceAll(out, []byte(filepath.Clean(oldHome)), []byte(filepath.Clean(newHome)))
	return out, nil
}

func copyLegacyCredentialPaths(cfg *Config, oldHome, newHome string) error {
	var paths []string
	for _, repo := range cfg.Repos {
		paths = append(paths, repo.TokenFile)
	}
	for _, platform := range cfg.Platforms {
		paths = append(paths, platform.TokenFile)
	}
	for _, owner := range cfg.GitHubOwnerTokens {
		paths = append(paths, owner.TokenFile)
	}
	for _, app := range cfg.GitHubApps {
		paths = append(paths, app.PrivateKeyPath)
	}

	seen := make(map[string]struct{})
	for _, sourcePath := range paths {
		destinationPath, contained := rebaseLegacyPath(sourcePath, oldHome, newHome)
		if sourcePath == "" || !contained {
			continue
		}
		if _, ok := seen[destinationPath]; ok {
			continue
		}
		seen[destinationPath] = struct{}{}
		if err := copyLegacyCredential(sourcePath, destinationPath); err != nil {
			return err
		}
	}
	return nil
}

func rebaseLegacyPath(sourcePath, oldHome, newHome string) (string, bool) {
	relative, err := filepath.Rel(filepath.Clean(oldHome), filepath.Clean(sourcePath))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.Join(newHome, relative), true
}

func copyLegacyCredential(sourcePath, destinationPath string) error {
	body, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("read legacy credential %s for %s: %w", sourcePath, destinationPath, err)
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		return fmt.Errorf("inspect legacy credential %s for %s: %w", sourcePath, destinationPath, err)
	}
	if destination, err := os.ReadFile(destinationPath); err == nil {
		if bytes.Equal(destination, body) {
			return nil
		}
		return fmt.Errorf("legacy credential %s conflicts with existing destination %s", sourcePath, destinationPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect credential destination %s: %w", destinationPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(destinationPath), 0o700); err != nil {
		return fmt.Errorf("create credential directory for %s: %w", destinationPath, err)
	}
	if err := os.WriteFile(destinationPath, body, info.Mode().Perm()); err != nil {
		return fmt.Errorf("copy legacy credential %s to %s: %w", sourcePath, destinationPath, err)
	}
	return nil
}

func readLegacyConfigMigrationMarker(markerPath string) (string, error) {
	body, err := os.ReadFile(markerPath)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read legacy config migration marker %s: %w", markerPath, err)
	}
	if string(body) != "v1\n" && string(body) != "v2\n" {
		return "", fmt.Errorf("legacy config migration marker %s has unknown contents", markerPath)
	}
	return string(body), nil
}

func writeLegacyConfigMigrationMarker(markerPath string) error {
	if err := os.WriteFile(markerPath, []byte("v2\n"), 0o600); err != nil {
		return fmt.Errorf("write legacy config migration marker %s: %w", markerPath, err)
	}
	return nil
}

func legacyDefaultHome() string {
	return filepath.Join(homeDir(), ".config", "middleman")
}
