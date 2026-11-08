package federation

import (
	"fmt"
	"net/http"
	"os"
	"strings"
)

// MinDeclaredSecretLength admits 128 random bits in hex, such as the output
// of `openssl rand -hex 16`.
const MinDeclaredSecretLength = 32

// ReadDeclaredSecret reads a declared enrollment secret from path. The file
// may end in a newline; the secret itself must be one whitespace-free token.
// The path is followed through symlinks because secret managers publish
// files that way.
func ReadDeclaredSecret(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read fleet enrollment secret: %w", err)
	}
	secret := strings.TrimSpace(string(contents))
	if len(secret) < MinDeclaredSecretLength || len(secret) > MaxCredentialLength ||
		strings.ContainsAny(secret, " \t\r\n") {
		return "", fmt.Errorf(
			"fleet enrollment secret in %s must be one token of %d to %d characters",
			path, MinDeclaredSecretLength, MaxCredentialLength,
		)
	}
	return secret, nil
}

// RetryableHubStatus reports whether a hub HTTP status describes a passing
// condition, such as an outage or rate limit, that a later attempt can clear.
func RetryableHubStatus(status int) bool {
	return status >= http.StatusInternalServerError ||
		status == http.StatusRequestTimeout || status == http.StatusTooManyRequests
}
