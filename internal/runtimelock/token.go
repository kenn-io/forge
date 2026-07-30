package runtimelock

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/flock"
)

// authTokenFileName is the well-known name of the API auth token under
// data_dir. Thin clients (the CLI api verb, supervising apps) read it
// to authenticate against the running daemon; its 0600 mode is the
// authorization boundary — only the daemon's user can read it.
const authTokenFileName = "auth_token"

// AuthTokenPath returns the well-known token location under dataDir.
func AuthTokenPath(dataDir string) string {
	return filepath.Join(dataDir, authTokenFileName)
}

// EnsureAuthToken returns the API auth token under dataDir, minting a
// new random token (0600) when none exists. An existing token is
// reused so restarts do not invalidate connected clients.
func EnsureAuthToken(dataDir string) (token string, returnErr error) {
	creationLock := flock.New(authTokenLockPath(dataDir))
	if err := creationLock.Lock(); err != nil {
		return "", fmt.Errorf("lock auth token creation: %w", err)
	}
	defer func() {
		if err := creationLock.Unlock(); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("unlock auth token creation: %w", err)
		}
	}()

	return ensureAuthTokenLocked(dataDir)
}

func ensureAuthTokenLocked(dataDir string) (string, error) {
	path := AuthTokenPath(dataDir)
	existing, err := os.ReadFile(path)
	if err == nil {
		token := strings.TrimSpace(string(existing))
		if token != "" {
			// The 0600 mode is the authorization boundary; correct a
			// pre-existing file that was created more permissively.
			if err := os.Chmod(path, 0o600); err != nil {
				return "", fmt.Errorf("restrict auth token mode: %w", err)
			}
			return token, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read auth token: %w", err)
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate auth token: %w", err)
	}
	token := hex.EncodeToString(raw)
	if err := writeAuthToken(dataDir, path, token); err != nil {
		return "", err
	}
	return token, nil
}

func writeAuthToken(dataDir, path, token string) error {
	tmp, err := os.CreateTemp(dataDir, ".auth_token.*.tmp")
	if err != nil {
		return fmt.Errorf("create auth token temp file: %w", err)
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
		return fmt.Errorf("restrict auth token temp file: %w", err)
	}
	if _, err := tmp.WriteString(token + "\n"); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write auth token temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync auth token temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close auth token temp file: %w", err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("replace empty auth token: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("publish auth token: %w", err)
	}
	committed = true
	return nil
}

// ReadAuthToken returns the token under dataDir, or "" when absent.
func ReadAuthToken(dataDir string) (string, error) {
	raw, err := os.ReadFile(AuthTokenPath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read auth token: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}
