package tokenauth

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"sync"
)

const redacted = "[REDACTED]"

const minRegisteredSecretLength = 8
const maxRegisteredSecrets = 1024

var (
	tokenLikePattern      = regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr|github_pat)_[A-Za-z0-9_=-]{8,}|\bglpat-[A-Za-z0-9_-]{8,}`)
	urlUserinfoPattern    = regexp.MustCompile(`(?i)(https?://)([^/\s'"<>]+@)`)
	registeredSecretMu    sync.RWMutex
	registeredSecrets     = map[string]struct{}{}
	registeredSecretOrder []string
)

func RedactKnownSecrets(message string, secrets ...string) string {
	out := redactURLUserinfo(message)
	out = redactTokenLikeStrings(out)
	for _, secret := range registeredSecretsSnapshot() {
		out = strings.ReplaceAll(out, secret, redacted)
	}
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		out = strings.ReplaceAll(out, secret, redacted)
	}
	return out
}

func RedactError(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	return errors.New(RedactKnownSecrets(err.Error(), secrets...))
}

func redactTokenLikeStrings(message string) string {
	return tokenLikePattern.ReplaceAllString(message, redacted)
}

func redactURLUserinfo(message string) string {
	return urlUserinfoPattern.ReplaceAllString(message, "${1}"+redacted+"@")
}

func RegisterKnownSecret(secret string) {
	secret = strings.TrimSpace(secret)
	if len(secret) < minRegisteredSecretLength {
		return
	}
	registeredSecretMu.Lock()
	if _, exists := registeredSecrets[secret]; exists {
		refreshRegisteredSecret(secret)
		registeredSecretMu.Unlock()
		return
	}
	for len(registeredSecrets) >= maxRegisteredSecrets {
		evictOldestRegisteredSecret()
	}
	registeredSecrets[secret] = struct{}{}
	registeredSecretOrder = append(registeredSecretOrder, secret)
	registeredSecretMu.Unlock()
}

func refreshRegisteredSecret(secret string) {
	for i, registered := range registeredSecretOrder {
		if registered != secret {
			continue
		}
		copy(registeredSecretOrder[i:], registeredSecretOrder[i+1:])
		registeredSecretOrder[len(registeredSecretOrder)-1] = secret
		return
	}
	registeredSecretOrder = append(registeredSecretOrder, secret)
}

func evictOldestRegisteredSecret() {
	if len(registeredSecretOrder) == 0 {
		clear(registeredSecrets)
		return
	}
	oldest := registeredSecretOrder[0]
	delete(registeredSecrets, oldest)
	copy(registeredSecretOrder, registeredSecretOrder[1:])
	registeredSecretOrder[len(registeredSecretOrder)-1] = ""
	registeredSecretOrder = registeredSecretOrder[:len(registeredSecretOrder)-1]
}

func registeredSecretsSnapshot() []string {
	registeredSecretMu.RLock()
	defer registeredSecretMu.RUnlock()
	out := make([]string, 0, len(registeredSecrets))
	for secret := range registeredSecrets {
		out = append(out, secret)
	}
	slices.SortFunc(out, func(a, b string) int {
		return len(b) - len(a)
	})
	return out
}
