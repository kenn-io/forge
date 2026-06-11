package tokenauth

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRedactingHandlerScrubsMessagesAndAttrs(t *testing.T) {
	assert := assert.New(t)
	var buf bytes.Buffer
	logger := slog.New(NewRedactingHandler(slog.NewTextHandler(&buf, nil)))

	logger.Error(
		"request failed with ghp_message_secret",
		"err", errors.New("clone https://x-access-token:ghp_error_secret@github.com/acme/widgets.git failed"),
		"token", "plain-provider-secret",
		"group", slog.GroupValue(
			slog.String("authorization", "Bearer glpat-group-secret"),
		),
	)

	out := buf.String()
	require.NotEmpty(t, out)
	for _, secret := range []string{
		"ghp_message_secret",
		"ghp_error_secret",
		"plain-provider-secret",
		"glpat-group-secret",
		"x-access-token",
	} {
		assert.NotContains(out, secret)
	}
	assert.Contains(out, "[REDACTED]")
}

func TestRedactingHandlerScrubsRegisteredOpaqueTokens(t *testing.T) {
	assert := assert.New(t)
	resetRegisteredSecretsForTest(t)
	t.Setenv("OPAQUE_LOG_TOKEN", "opaque-log-token-67890")
	src := NewManagedSource(Descriptor{
		Key: Key{Platform: "github", Host: "github.com"},
		Candidates: []Candidate{{
			Kind:    SourceKindEnv,
			EnvName: "OPAQUE_LOG_TOKEN",
		}},
	}, Options{})
	_, err := src.Token(context.Background())
	require.NoError(t, err)

	var buf bytes.Buffer
	logger := slog.New(NewRedactingHandler(slog.NewTextHandler(&buf, nil)))

	logger.Error(
		"provider returned opaque-log-token-67890",
		"detail", "Authorization: Bearer opaque-log-token-67890",
	)

	out := buf.String()
	require.NotEmpty(t, out)
	assert.NotContains(out, "opaque-log-token-67890")
	assert.Contains(out, "[REDACTED]")
}
