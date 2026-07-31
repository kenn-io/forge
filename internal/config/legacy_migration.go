package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/gofrs/flock"
)

const (
	legacyLockFile     = "middleman.lock"
	legacyDatabaseFile = "middleman.db"
	forgeDatabaseFile  = "forge.db"
)

// LoadOrCreate relocates legacy config and database state before loading it.
func LoadOrCreate(path string) (*Config, error) {
	if err := migrateLegacyConfig(path); err != nil {
		return nil, err
	}
	if err := migrateLegacyDatabase(path); err != nil {
		return nil, err
	}
	if err := EnsureDefault(path); err != nil {
		return nil, err
	}
	return Load(path)
}

func migrateLegacyDatabase(configPath string) error {
	sourceDir, destinationDir, err := legacyDatabaseDirectories(configPath)
	if err != nil {
		return err
	}
	if sourceDir == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(sourceDir, legacyDatabaseFile)); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect legacy database: %w", err)
	}

	legacyLock := flock.New(filepath.Join(sourceDir, legacyLockFile))
	locked, err := legacyLock.TryLock()
	if err != nil {
		return fmt.Errorf("acquire legacy runtime lock %s: %w", legacyLock.Path(), err)
	}
	if !locked {
		return fmt.Errorf("middleman daemon is still using %s", sourceDir)
	}
	defer func() { _ = legacyLock.Unlock() }()

	if err := os.MkdirAll(destinationDir, 0o700); err != nil {
		return fmt.Errorf("create Kenn Forge data directory %s: %w", destinationDir, err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		oldPath := filepath.Join(sourceDir, legacyDatabaseFile+suffix)
		newPath := filepath.Join(destinationDir, forgeDatabaseFile+suffix)
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
	for _, suffix := range []string{"-wal", "-shm", ""} {
		oldPath := filepath.Join(sourceDir, legacyDatabaseFile+suffix)
		newPath := filepath.Join(destinationDir, forgeDatabaseFile+suffix)
		if _, err := os.Stat(oldPath); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := os.Rename(oldPath, newPath); err != nil {
			return fmt.Errorf("move legacy database %s to %s: %w", oldPath, newPath, err)
		}
	}
	return nil
}

func legacyDatabaseDirectories(configPath string) (string, string, error) {
	if _, err := os.Stat(configPath); err == nil {
		dataDir, err := configuredDataDir(configPath)
		if err != nil {
			return "", "", err
		}
		if os.Getenv("KENN_FORGE_HOME") == "" && filepath.Clean(configPath) == filepath.Clean(DefaultConfigPath()) {
			exists, err := legacyDatabaseExists(dataDir)
			if err != nil || exists {
				return dataDir, dataDir, err
			}
			oldDefault := legacyDefaultHome()
			legacyDataDir, mapped := legacyDataDirectory(dataDir, oldDefault)
			if mapped && filepath.Clean(legacyDataDir) != filepath.Clean(dataDir) {
				exists, err = legacyDatabaseExists(legacyDataDir)
				if err != nil || exists {
					return legacyDataDir, dataDir, err
				}
			}
			exists, err = legacyDatabaseExists(oldDefault)
			if err != nil || exists {
				return oldDefault, dataDir, err
			}
		}
		return dataDir, dataDir, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	if os.Getenv("KENN_FORGE_HOME") != "" || filepath.Clean(configPath) != filepath.Clean(DefaultConfigPath()) {
		return "", "", nil
	}
	return legacyDefaultHome(), DefaultDataDir(), nil
}

func legacyDataDirectory(dataDir, oldDefault string) (string, bool) {
	relative, err := filepath.Rel(DefaultDataDir(), dataDir)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	if relative == "." {
		return oldDefault, true
	}
	return filepath.Join(oldDefault, relative), true
}

func legacyDatabaseExists(dataDir string) (bool, error) {
	_, err := os.Stat(filepath.Join(dataDir, legacyDatabaseFile))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func configuredDataDir(configPath string) (string, error) {
	body, err := os.ReadFile(configPath)
	if err != nil {
		return "", err
	}
	var raw struct {
		DataDir string `toml:"data_dir"`
	}
	if _, err := toml.Decode(string(body), &raw); err != nil {
		return "", fmt.Errorf("parse config %s: %w", configPath, err)
	}
	if raw.DataDir == "" {
		return DefaultDataDir(), nil
	}
	if filepath.IsAbs(raw.DataDir) {
		return filepath.Clean(raw.DataDir), nil
	}
	abs, err := filepath.Abs(raw.DataDir)
	if err != nil {
		return "", err
	}
	return abs, nil
}
