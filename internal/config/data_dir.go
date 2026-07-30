package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// canonicalDataDir returns one stable identity for a configured data directory.
// Existing symlink aliases resolve to their target; a not-yet-created path is
// still made absolute so reload comparisons do not depend on process cwd.
func canonicalDataDir(dataDir string) (string, error) {
	absolute, err := filepath.Abs(dataDir)
	if err != nil {
		return "", fmt.Errorf("resolve data directory: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		return filepath.Clean(resolved), nil
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
