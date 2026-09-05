package githubapp

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

const DefaultHomepageURL = "https://go.kenn.io/forge"

// DefaultPermissions is the standalone application's read-only sync policy.
// Writes continue to use the user's credential, not the App.
func DefaultPermissions() map[string]string {
	return map[string]string{
		"actions": "read", "checks": "read", "contents": "read",
		"issues": "read", "metadata": "read", "pull_requests": "read", "statuses": "read",
	}
}

// RandomAppName supplies an editable default within GitHub's name limit.
func RandomAppName() (string, error) {
	var buf [3]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generating app name suffix: %w", err)
	}
	return "kenn-forge-" + hex.EncodeToString(buf[:]), nil
}
