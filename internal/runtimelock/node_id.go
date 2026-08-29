package runtimelock

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

const (
	nodeIDFileName = "node_id"
	nodeIDLockName = ".node_id.lock"
)

// NodeIDPath returns the stable fleet identity location under dataDir.
func NodeIDPath(dataDir string) string {
	return filepath.Join(dataDir, nodeIDFileName)
}

func nodeIDLockPath(dataDir string) string {
	return filepath.Join(dataDir, nodeIDLockName)
}

// EnsureNodeID returns the data directory's stable random 128-bit identity.
// Concurrent first-start callers serialize through a persistent flock file.
func EnsureNodeID(dataDir string) (nodeID string, returnErr error) {
	creationLock := flock.New(nodeIDLockPath(dataDir))
	if err := creationLock.Lock(); err != nil {
		return "", fmt.Errorf("lock node ID creation: %w", err)
	}
	defer func() {
		if err := creationLock.Unlock(); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("unlock node ID creation: %w", err)
		}
	}()

	path := NodeIDPath(dataDir)
	existing, err := os.ReadFile(path)
	if err == nil {
		nodeID, valid := parseNodeIDFile(existing)
		if !valid {
			return "", fmt.Errorf("invalid node ID in %s", path)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return "", fmt.Errorf("restrict node ID mode: %w", err)
		}
		return nodeID, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read node ID: %w", err)
	}

	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate node ID: %w", err)
	}
	nodeID = hex.EncodeToString(raw)
	if err := writeNodeID(dataDir, path, nodeID); err != nil {
		return "", err
	}
	return nodeID, nil
}

func parseNodeIDFile(contents []byte) (string, bool) {
	if len(contents) == 33 && contents[32] == '\n' {
		contents = contents[:32]
	}
	if len(contents) != 32 {
		return "", false
	}
	nodeID := string(contents)
	return nodeID, validNodeID(nodeID)
}

func validNodeID(nodeID string) bool {
	if len(nodeID) != 32 {
		return false
	}
	for _, char := range nodeID {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func writeNodeID(dataDir, path, nodeID string) error {
	tmp, err := os.CreateTemp(dataDir, ".node_id.*.tmp")
	if err != nil {
		return fmt.Errorf("create node ID temp file: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("restrict node ID temp file: %w", err)
	}
	if _, err := tmp.WriteString(nodeID + "\n"); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write node ID temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync node ID temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close node ID temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("publish node ID: %w", err)
	}
	committed = true
	return nil
}
