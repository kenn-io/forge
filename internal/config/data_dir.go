package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"go.kenn.io/forge/internal/pathidentity"
)

// CanonicalDataDir returns one stable identity for a configured data directory.
// Existing symlink and filename-case aliases resolve to the filesystem's stored
// path; a not-yet-created path is still made absolute so reload comparisons do
// not depend on process cwd.
func CanonicalDataDir(dataDir string) (string, error) {
	absolute, err := filepath.Abs(dataDir)
	if err != nil {
		return "", fmt.Errorf("resolve data directory: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		return canonicalExistingDataDir(resolved)
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("resolve data directory symlinks: %w", err)
	}

	missing := make([]string, 0, 2)
	ancestor := absolute
	for {
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return filepath.Clean(absolute), nil
		}
		missing = append(missing, filepath.Base(ancestor))
		ancestor = parent
		resolved, err = filepath.EvalSymlinks(ancestor)
		if err == nil {
			resolved, err = canonicalExistingDataDir(resolved)
			if err != nil {
				return "", err
			}
			for _, component := range slices.Backward(missing) {
				resolved = filepath.Join(resolved, component)
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("resolve data directory symlinks: %w", err)
		}
	}
}

func canonicalExistingDataDir(dataDir string) (string, error) {
	canonical, err := pathidentity.CanonicalExisting(dataDir)
	if err != nil {
		return "", fmt.Errorf("resolve data directory casing: %w", err)
	}
	return filepath.Clean(canonical), nil
}
