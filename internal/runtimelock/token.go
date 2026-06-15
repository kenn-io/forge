package runtimelock

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
func EnsureAuthToken(dataDir string) (string, error) {
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
		// An empty file is replaced below; remove it first so the
		// fresh write applies the restricted mode.
		if err := os.Remove(path); err != nil {
			return "", fmt.Errorf("replace empty auth token: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read auth token: %w", err)
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate auth token: %w", err)
	}
	token := hex.EncodeToString(raw)
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write auth token: %w", err)
	}
	return token, nil
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
