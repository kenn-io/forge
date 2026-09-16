package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayConfigRoundTrip(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(os.WriteFile(path, []byte(""), 0o600))
	cfg, err := Load(path)
	require.NoError(err)
	cfg.Relay = Relay{URL: "https://relay.example.com"}
	require.NoError(cfg.Save(path))
	loaded, err := Load(path)
	require.NoError(err)
	assert.Equal(cfg.Relay, loaded.Relay)
}

func TestRelayConfigValidation(t *testing.T) {
	require := require.New(t)
	t.Parallel()
	for _, endpoint := range []string{"https://relay.example.com", "http://127.0.0.1:4321", "http://[::1]:4321"} {
		cfg := Relay{URL: endpoint}
		require.NoError(cfg.Validate())
	}
	for _, endpoint := range []string{
		"http://relay.example.com", "https://user:secret@relay.example.com",
		"https://relay.example.com/path", "https://relay.example.com?token=secret",
	} {
		cfg := Relay{URL: endpoint}
		require.Error(cfg.Validate())
		path := filepath.Join(t.TempDir(), "config.toml")
		require.NoError(os.WriteFile(path, fmt.Appendf(nil, "[relay]\nurl = %q\n", endpoint), 0o600))
		_, err := Load(path)
		require.Error(err)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(os.WriteFile(path, []byte("[relay]\nurl = \"https://relay.example.com\"\npoll_interval = \"15s\"\n"), 0o600))
	_, err := Load(path)
	require.Error(err, "the relay no longer polls, so a poll interval must be rejected instead of ignored")
}
