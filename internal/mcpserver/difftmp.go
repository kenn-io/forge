package mcpserver

import (
	"os"
	"path/filepath"
)

type diffFileStore struct {
	dir string
}

func newDiffFileStore() (*diffFileStore, error) {
	dir, err := os.MkdirTemp("", "middleman-mcp-")
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
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", 0, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", 0, err
	}
	return abs, int64(len(data)), nil
}

func (d *diffFileStore) Close() error {
	return os.RemoveAll(d.dir)
}
