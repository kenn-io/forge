package agentactivity

import (
	"os"
	"path/filepath"
)

// writeFileAtomic replaces path with data through a temporary file in the same
// directory, so a hook firing while middleman reads the state never observes a
// half-written file. tempPattern names the temporary file (see os.CreateTemp).
func writeFileAtomic(path, tempPattern string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, tempPattern)
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
	return os.Rename(tmpPath, path)
}
