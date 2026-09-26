package mcpserver

import (
	"container/list"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"go.kenn.io/kit/atomicfile"
)

var errDiffCacheFileTooLarge = errors.New("diff file exceeds MCP diff cache")

type diffFileStore struct {
	mu         sync.Mutex
	dir        string
	maxBytes   int64
	totalBytes int64
	lru        *list.List
	entries    map[string]*list.Element
	// commit publishes a staged diff and remove deletes an evicted one;
	// tests replace them to force failures.
	commit func(*atomicfile.File) error
	remove func(string) error
}

type diffFileEntry struct {
	name string
	path string
	size int64
}

func newDiffFileStore(maxBytes int64) (*diffFileStore, error) {
	if maxBytes <= 0 {
		return nil, errors.New("MCP diff cache size must be positive")
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
		commit: (*atomicfile.File).Commit, remove: os.Remove,
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
	staged, err := atomicfile.Create(path, atomicfile.WithoutSync())
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = staged.Abort() }()
	if _, err := staged.Write(data); err != nil {
		return "", 0, err
	}

	// Publish before touching the cache so a failed commit leaves every
	// existing entry in place. ErrPublished means the diff is already visible
	// at path.
	if err := d.commit(staged); err != nil && !errors.Is(err, atomicfile.ErrPublished) {
		return "", 0, err
	}
	if existing := d.entries[base]; existing != nil {
		d.totalBytes -= existing.Value.(diffFileEntry).size
		d.lru.Remove(existing)
		delete(d.entries, base)
	}
	added := d.lru.PushBack(diffFileEntry{name: base, path: abs, size: size})
	d.entries[base] = added
	d.totalBytes += size

	// The new diff already fits on its own (checked above), so evicting older
	// entries normally gets back under the budget. The write has landed, so a
	// failed removal does not fail it: that entry stays counted for a later
	// write to retry, and eviction moves on to the next-oldest entry so one
	// undeletable file cannot stop the cache from shrinking.
	for elem := d.lru.Front(); d.totalBytes > d.maxBytes && elem != nil && elem != added; {
		next := elem.Next()
		entry := elem.Value.(diffFileEntry)
		if err := d.remove(entry.path); err == nil || errors.Is(err, os.ErrNotExist) {
			d.totalBytes -= entry.size
			d.lru.Remove(elem)
			delete(d.entries, entry.name)
		}
		elem = next
	}
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
