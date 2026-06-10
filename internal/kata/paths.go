package kata

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// Home resolves Kata's home directory: $KATA_HOME, else ~/.kata.
func Home() (string, error) {
	if home := os.Getenv("KATA_HOME"); home != "" {
		return home, nil
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(userHome, ".kata"), nil
}

// CatalogPath returns Kata's config file path: <Home>/config.toml.
func CatalogPath() (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "config.toml"), nil
}

// RuntimeDir mirrors Kata's runtime directory shape:
// <Home>/runtime/<dbhash>, where dbhash is the first 12 lower-hex chars of
// sha256(abs(dbPath)).
func RuntimeDir() (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	db := os.Getenv("KATA_DB")
	if db == "" {
		db = filepath.Join(home, "kata.db")
	}
	abs, err := filepath.Abs(db)
	if err != nil {
		abs = db
	}
	sum := sha256.Sum256([]byte(abs))
	dbhash := hex.EncodeToString(sum[:])[:12]
	return filepath.Join(home, "runtime", dbhash), nil
}
