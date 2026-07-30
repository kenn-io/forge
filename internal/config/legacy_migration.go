package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/gofrs/flock"
)

const (
	legacyLockFile      = "middleman.lock"
	legacyDatabaseFile  = "middleman.db"
	migrationMarkerFile = ".kenn-forge-migration.json"
)

type homeMigrationMarker struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

// LoadOrCreate migrates persisted state before creating or loading a config.
func LoadOrCreate(path string) (*Config, error) {
	path, err := migrateLegacyState(path)
	if err != nil {
		return nil, err
	}
	if err := EnsureDefault(path); err != nil {
		return nil, err
	}
	return Load(path)
}

// LoadExisting migrates persisted state without creating a missing config.
func LoadExisting(path string) (*Config, error) {
	path, err := migrateLegacyState(path)
	if err != nil {
		return nil, err
	}
	return Load(path)
}

func migrateLegacyState(path string) (string, error) {
	if os.Getenv("KENN_FORGE_HOME") != "" || filepath.Clean(path) != filepath.Clean(DefaultConfigPath()) {
		return path, migrateLegacyDataFiles(path, "", "")
	}

	oldHome := filepath.Join(homeDir(), ".config", "middleman")
	newHome := DefaultDataDir()
	oldConfig := filepath.Join(oldHome, "config.toml")
	if _, err := os.Stat(oldConfig); errors.Is(err, os.ErrNotExist) {
		return path, migrateLegacyDataFiles(path, oldHome, newHome)
	} else if err != nil {
		return path, fmt.Errorf("inspect legacy config %s: %w", oldConfig, err)
	}

	dataDir, err := legacyDataDir(oldConfig, oldHome)
	if err != nil {
		return path, err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return path, fmt.Errorf("create legacy data directory %s: %w", dataDir, err)
	}
	legacyLock := flock.New(filepath.Join(dataDir, legacyLockFile))
	locked, err := legacyLock.TryLock()
	if err != nil {
		return path, fmt.Errorf("acquire legacy runtime lock %s: %w", legacyLock.Path(), err)
	}
	if !locked {
		return path, fmt.Errorf("legacy Kenn Forge daemon is still using %s", dataDir)
	}
	defer func() { _ = legacyLock.Unlock() }()

	if err := prepareHomeMigration(oldHome, newHome); err != nil {
		return path, err
	}
	entries, err := os.ReadDir(oldHome)
	if err != nil {
		return path, fmt.Errorf("read legacy home %s: %w", oldHome, err)
	}
	for _, entry := range entries {
		switch entry.Name() {
		case legacyLockFile, "middleman.run.json", ".middleman.run.json.tmp", "config.toml":
			continue
		}
		source := filepath.Join(oldHome, entry.Name())
		destination := filepath.Join(newHome, entry.Name())
		if err := os.Rename(source, destination); err != nil {
			return path, fmt.Errorf("move legacy state %s to %s: %w", source, destination, err)
		}
	}
	if err := os.Rename(oldConfig, path); err != nil {
		return path, fmt.Errorf("move legacy config %s to %s: %w", oldConfig, path, err)
	}
	for _, name := range []string{"middleman.run.json", ".middleman.run.json.tmp"} {
		if err := os.Remove(filepath.Join(oldHome, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return path, fmt.Errorf("remove stale legacy runtime metadata: %w", err)
		}
	}

	canonicalDataDir := dataDir
	if filepath.Clean(dataDir) == filepath.Clean(oldHome) {
		canonicalDataDir = newHome
	}
	if err := rewriteLegacyConfig(path, oldHome, newHome); err != nil {
		return path, err
	}
	if err := renameLegacyDatabase(canonicalDataDir); err != nil {
		return path, err
	}
	if err := os.Remove(filepath.Join(newHome, migrationMarkerFile)); err != nil {
		return path, fmt.Errorf("complete Kenn Forge home migration: %w", err)
	}
	return path, nil
}

func legacyDataDir(configPath, defaultDir string) (string, error) {
	body, err := os.ReadFile(configPath)
	if err != nil {
		return "", fmt.Errorf("read legacy config %s: %w", configPath, err)
	}
	var raw struct {
		DataDir string `toml:"data_dir"`
	}
	if _, err := toml.Decode(string(body), &raw); err != nil {
		return "", fmt.Errorf("parse legacy config %s: %w", configPath, err)
	}
	if raw.DataDir == "" {
		return defaultDir, nil
	}
	if !filepath.IsAbs(raw.DataDir) {
		return filepath.Abs(raw.DataDir)
	}
	return filepath.Clean(raw.DataDir), nil
}

func prepareHomeMigration(oldHome, newHome string) error {
	entries, err := os.ReadDir(newHome)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(newHome, 0o700); err != nil {
			return fmt.Errorf("create Kenn Forge home %s: %w", newHome, err)
		}
		entries = nil
		err = nil
	}
	if err != nil {
		return fmt.Errorf("inspect Kenn Forge home %s: %w", newHome, err)
	}
	markerPath := filepath.Join(newHome, migrationMarkerFile)
	if len(entries) != 0 {
		body, readErr := os.ReadFile(markerPath)
		if readErr == nil {
			var marker homeMigrationMarker
			if json.Unmarshal(body, &marker) == nil && filepath.Clean(marker.Source) == filepath.Clean(oldHome) && filepath.Clean(marker.Destination) == filepath.Clean(newHome) {
				return nil
			}
		}
		return fmt.Errorf("cannot migrate legacy home %s: Kenn Forge home %s is not empty", oldHome, newHome)
	}
	body, err := json.Marshal(homeMigrationMarker{Source: oldHome, Destination: newHome})
	if err != nil {
		return err
	}
	if err := os.WriteFile(markerPath, body, 0o600); err != nil {
		return fmt.Errorf("record Kenn Forge home migration: %w", err)
	}
	return nil
}

func migrateLegacyDataFiles(configPath, oldHome, newHome string) error {
	if _, err := os.Stat(configPath); err != nil {
		return nil
	}
	if err := rewriteLegacyConfig(configPath, oldHome, newHome); err != nil {
		return err
	}
	cfg, err := Load(configPath)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, legacyDatabaseFile)); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	legacyLock := flock.New(filepath.Join(cfg.DataDir, legacyLockFile))
	locked, err := legacyLock.TryLock()
	if err != nil {
		return fmt.Errorf("acquire legacy runtime lock %s: %w", legacyLock.Path(), err)
	}
	if !locked {
		return fmt.Errorf("legacy Kenn Forge daemon is still using %s", cfg.DataDir)
	}
	defer func() { _ = legacyLock.Unlock() }()
	return renameLegacyDatabase(cfg.DataDir)
}

func rewriteLegacyConfig(path, oldHome, newHome string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config for migration %s: %w", path, err)
	}
	rewritten := strings.ReplaceAll(string(body), "MIDDLEMAN_", "KENN_FORGE_")
	if oldHome != "" {
		rewritten = strings.ReplaceAll(rewritten, oldHome, newHome)
	}
	if rewritten == string(body) {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".kenn-forge-config-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(info.Mode().Perm()); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.WriteString(rewritten); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func renameLegacyDatabase(dataDir string) error {
	oldMain := filepath.Join(dataDir, legacyDatabaseFile)
	if _, err := os.Stat(oldMain); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		oldPath := filepath.Join(dataDir, legacyDatabaseFile+suffix)
		newPath := filepath.Join(dataDir, "forge.db"+suffix)
		if _, err := os.Stat(oldPath); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if _, err := os.Stat(newPath); err == nil {
			return fmt.Errorf("both legacy database %s and Kenn Forge database %s exist", oldPath, newPath)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		oldPath := filepath.Join(dataDir, legacyDatabaseFile+suffix)
		newPath := filepath.Join(dataDir, "forge.db"+suffix)
		if _, err := os.Stat(oldPath); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := os.Rename(oldPath, newPath); err != nil {
			return fmt.Errorf("rename legacy database %s to %s: %w", oldPath, newPath, err)
		}
	}
	return nil
}
