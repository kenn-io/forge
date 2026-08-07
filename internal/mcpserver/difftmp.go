package mcpserver

import (
	"os"
	"path/filepath"
)

type diffFileStore struct {
	dir string
}

func newDiffFileStore() (*diffFileStore, error) {
	dir, err := os.MkdirTemp("", "kenn-forge-mcp-")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &diffFileStore{dir: dir}, nil
}

func (d *diffFileStore) write(name string, data []byte) (string, int64, error) {
	path := filepath.Join(d.dir, filepath.Base(name))
	tmp, err := os.CreateTemp(d.dir, filepath.Base(name)+".*.tmp")
	if err != nil {
		return "", 0, err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		_ = tmp.Close()
		return "", 0, err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", 0, err
	}
	if err := tmp.Close(); err != nil {
		return "", 0, err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return "", 0, err
	}
	cleanup = false
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", 0, err
	}
	return abs, int64(len(data)), nil
}

func (d *diffFileStore) Close() error {
	return os.RemoveAll(d.dir)
}
