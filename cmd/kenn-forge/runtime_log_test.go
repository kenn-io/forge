package main

import (
	"bytes"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
)

func TestRuntimeConfigLogOmitsEndpointCredentials(t *testing.T) {
	var output bytes.Buffer
	cfg := &config.Config{Host: "0.0.0.0", Port: 8091, Roborev: config.Roborev{Endpoint: "https://credential-user:credential-secret@review.example:7373/path-secret?token=query-secret"}}
	logRuntimeConfig(slog.New(slog.NewJSONHandler(&output, nil)), cfg)
	assert.Contains(t, output.String(), "https://review.example:7373")
	for _, secret := range []string{"credential-user", "credential-secret", "path-secret", "query-secret"} {
		assert.NotContains(t, output.String(), secret)
	}
}

func TestListenerFlagsAreRejectedOutsideServe(t *testing.T) {
	for _, flag := range []string{"--host=127.0.0.1", "--port=8093"} {
		cmd := newRootCommand(cliOptions{Stdout: io.Discard, Stderr: io.Discard})
		cmd.SetArgs([]string{"version", flag})
		require.ErrorContains(t, cmd.Execute(), "unknown flag")
	}
}
