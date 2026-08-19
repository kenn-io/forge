package mcpserver

import (
	"container/list"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var errDiffCacheFileTooLarge = errors.New("diff file exceeds MCP diff cache")

type diffFileStore struct {
	mu         sync.Mutex
	dir        string
	maxBytes   int64
	totalBytes int64
	lru        *list.List
	entries    map[string]*list.Element
}

type diffFileEntry struct {
	name string
	path string
	size int64
}

func newDiffFileStore(maxBytes int64) (*diffFileStore, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("MCP diff cache size must be positive")
	}
	dir, err := os.MkdirTemp("", "kenn-forge-mcp-")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &diffFileStore{
		dir: dir, maxBytes: maxBytes,
		lru: list.New(), entries: make(map[string]*list.Element),
	}, nil
}

func (d *diffFileStore) write(name string, data []byte) (string, int64, error) {
	size := int64(len(data))
	if size > d.maxBytes {
		return "", 0, fmt.Errorf(
			"%w: file is %d bytes, cache is %d bytes",
			errDiffCacheFileTooLarge, size, d.maxBytes,
		)
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	base := filepath.Base(name)
	path := filepath.Join(d.dir, base)
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", 0, err
	}

	// Stage the replacement fully before touching published state so a failed
	// write never removes the current same-name diff or evicts other entries.
	tmp, err := os.CreateTemp(d.dir, base+".*.tmp")
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

	// A same-name entry is replaced by the rename below, never evicted, so it
	// stays reachable for concurrent consumers until the atomic swap.
	var existingSize int64
	if existing := d.entries[base]; existing != nil {
		existingSize = existing.Value.(diffFileEntry).size
	}
	evictCursor := d.lru.Front()
	for d.totalBytes-existingSize+size > d.maxBytes {
		for evictCursor != nil && evictCursor.Value.(diffFileEntry).name == base {
			evictCursor = evictCursor.Next()
		}
		if evictCursor == nil {
			return "", 0, fmt.Errorf("%w: no evictable files", errDiffCacheFileTooLarge)
		}
		entry := evictCursor.Value.(diffFileEntry)
		next := evictCursor.Next()
		if err := os.Remove(entry.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", 0, err
		}
		d.totalBytes -= entry.size
		d.lru.Remove(evictCursor)
		delete(d.entries, entry.name)
		evictCursor = next
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return "", 0, err
	}
	cleanup = false
	if existing := d.entries[base]; existing != nil {
		d.totalBytes -= existing.Value.(diffFileEntry).size
		d.lru.Remove(existing)
		delete(d.entries, base)
	}
	entry := diffFileEntry{name: base, path: abs, size: size}
	d.entries[base] = d.lru.PushBack(entry)
	d.totalBytes += size
	return abs, size, nil
}

func (d *diffFileStore) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	err := os.RemoveAll(d.dir)
	d.totalBytes = 0
	d.lru.Init()
	clear(d.entries)
	return err
}
