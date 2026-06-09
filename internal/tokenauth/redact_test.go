package tokenauth

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRedactKnownSecrets(t *testing.T) {
	got := RedactKnownSecrets(
		"Authorization: Bearer plain-provider-secret",
		"plain-provider-secret",
	)
	assert.Equal(t, "Authorization: Bearer [REDACTED]", got)
}

func TestRedactTokenBearingURL(t *testing.T) {
	got := RedactKnownSecrets(
		"https://x-access-token:ghp_sentinel_secret@github.com/acme/repo.git",
		"ghp_sentinel_secret",
	)
	assert.NotContains(t, got, "ghp_sentinel_secret")
	assert.Contains(t, got, "[REDACTED]")
}

func TestRedactQuotedTokenBearingURL(t *testing.T) {
	got := RedactKnownSecrets(
		"fatal: unable to access 'https://oauth2:plain-provider-secret@gitlab.example.com/acme/repo.git/': Authentication failed",
	)
	assert.NotContains(t, got, "plain-provider-secret")
	assert.NotContains(t, got, "oauth2")
	assert.Contains(t, got, "[REDACTED]@gitlab.example.com")
}

func TestRedactError(t *testing.T) {
	err := RedactError(
		errors.New("token ghp_sentinel_secret failed"),
		"ghp_sentinel_secret",
	)
	assert.EqualError(t, err, "token [REDACTED] failed")
}

func TestRedactErrorRedactsTokenLikeStringsWithoutExplicitSecret(t *testing.T) {
	err := RedactError(errors.New("git stderr contained ghp_sentinel_secret"))
	assert.EqualError(t, err, "git stderr contained [REDACTED]")
}

func TestRegisterKnownSecretIgnoresShortOrdinaryValues(t *testing.T) {
	resetRegisteredSecretsForTest(t)
	RegisterKnownSecret("new")

	got := RedactKnownSecrets("status must be one of: new, reviewing")

	assert.Equal(t, "status must be one of: new, reviewing", got)
}

func TestRegisterKnownSecretBoundsRegistry(t *testing.T) {
	resetRegisteredSecretsForTest(t)
	const expectedLimit = 1024
	for i := 0; i <= expectedLimit; i++ {
		RegisterKnownSecret(fmt.Sprintf("opaque-bounded-token-%04d", i))
	}

	secrets := registeredSecretsSnapshot()

	assert.LessOrEqual(t, len(secrets), expectedLimit)
	assert.Equal(
		t,
		"provider returned opaque-bounded-token-0000",
		RedactKnownSecrets("provider returned opaque-bounded-token-0000"),
	)
	assert.Equal(
		t,
		"provider returned [REDACTED]",
		RedactKnownSecrets("provider returned opaque-bounded-token-1024"),
	)
}

func TestRegisterKnownSecretRefreshesDuplicateRecency(t *testing.T) {
	resetRegisteredSecretsForTest(t)
	const expectedLimit = 1024
	for i := range expectedLimit {
		RegisterKnownSecret(fmt.Sprintf("opaque-refresh-token-%04d", i))
	}

	RegisterKnownSecret("opaque-refresh-token-0000")
	RegisterKnownSecret("opaque-refresh-token-1024")

	assert.Equal(
		t,
		"provider returned [REDACTED]",
		RedactKnownSecrets("provider returned opaque-refresh-token-0000"),
	)
	assert.Equal(
		t,
		"provider returned opaque-refresh-token-0001",
		RedactKnownSecrets("provider returned opaque-refresh-token-0001"),
	)
	assert.Equal(
		t,
		"provider returned [REDACTED]",
		RedactKnownSecrets("provider returned opaque-refresh-token-1024"),
	)
}

func TestManagedSourceRegistersOpaqueTokenForRedaction(t *testing.T) {
	resetRegisteredSecretsForTest(t)
	t.Setenv("OPAQUE_REDACTION_TOKEN", "opaque-active-token-12345")
	src := NewManagedSource(Descriptor{
		Key: Key{Platform: "gitlab", Host: "gitlab.com"},
		Candidates: []Candidate{{
			Kind:    SourceKindEnv,
			EnvName: "OPAQUE_REDACTION_TOKEN",
		}},
	}, Options{})

	token, err := src.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "opaque-active-token-12345", token)

	err = RedactError(errors.New("provider returned opaque-active-token-12345 in stderr"))
	assert.EqualError(t, err, "provider returned [REDACTED] in stderr")
}

func resetRegisteredSecretsForTest(t *testing.T) {
	t.Helper()
	resetRegisteredSecrets()
	t.Cleanup(resetRegisteredSecrets)
}

func resetRegisteredSecrets() {
	registeredSecretMu.Lock()
	registeredSecrets = map[string]struct{}{}
	registeredSecretOrder = nil
	registeredSecretMu.Unlock()
}
