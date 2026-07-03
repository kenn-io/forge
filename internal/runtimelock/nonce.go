package runtimelock

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Auth bootstrap nonces let `middleman auth url` print a login link
// without placing the long-lived daemon token in a URL, where proxy
// and access logs would capture it. The CLI mints a nonce into
// <data_dir>/auth_nonces and the daemon consumes it on the bootstrap
// request; both sides share only the filesystem, so minting works
// whether or not the daemon is up and survives the startup window.
// A nonce is single-use and expires after AuthNonceTTL.

const authNonceDirName = "auth_nonces"

// AuthNonceTTL is how long a minted bootstrap nonce stays valid. Long
// enough to copy a link between machines, short enough that a logged
// URL is stale by the time logs are read.
const AuthNonceTTL = 10 * time.Minute

func authNonceDir(dataDir string) string {
	return filepath.Join(dataDir, authNonceDirName)
}

// nonce files are named by the SHA-256 of the nonce so the directory
// itself never contains a usable credential.
func authNoncePath(dataDir, nonce string) string {
	sum := sha256.Sum256([]byte(nonce))
	return filepath.Join(authNonceDir(dataDir), hex.EncodeToString(sum[:]))
}

// MintAuthNonce creates a fresh single-use bootstrap nonce under
// dataDir and returns it. Expired leftovers are swept opportunistically
// so abandoned links do not accumulate.
func MintAuthNonce(dataDir string) (string, error) {
	dir := authNonceDir(dataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create auth nonce dir: %w", err)
	}
	sweepExpiredNonces(dir)

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate auth nonce: %w", err)
	}
	nonce := hex.EncodeToString(raw)
	if err := os.WriteFile(authNoncePath(dataDir, nonce), nil, 0o600); err != nil {
		return "", fmt.Errorf("write auth nonce: %w", err)
	}
	return nonce, nil
}

// ConsumeAuthNonce atomically claims nonce under dataDir and reports
// whether it was valid (existed and had not expired). The claim is the
// os.Remove: only one concurrent caller gets a nil error, so a nonce
// can never authorize two requests.
func ConsumeAuthNonce(dataDir, nonce string) bool {
	if nonce == "" {
		return false
	}
	path := authNoncePath(dataDir, nonce)
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if err := os.Remove(path); err != nil {
		return false
	}
	return time.Since(info.ModTime()) <= AuthNonceTTL
}

func sweepExpiredNonces(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || time.Since(info.ModTime()) <= AuthNonceTTL {
			continue
		}
		// Best effort: a leftover expired nonce is rejected on
		// consume anyway.
		_ = os.Remove(filepath.Join(dir, entry.Name()))
	}
}
