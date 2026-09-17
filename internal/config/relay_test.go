package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	cfg.Relay = Relay{URL: "https://relay.example.com", PollInterval: "45s"}
	require.NoError(cfg.Save(path))
	loaded, err := Load(path)
	require.NoError(err)
	assert.Equal(cfg.Relay, loaded.Relay)
	assert.Equal(45*time.Second, loaded.Relay.Interval())
}

func TestRelayConfigValidation(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	t.Parallel()
	for _, endpoint := range []string{"https://relay.example.com", "http://127.0.0.1:4321", "http://[::1]:4321"} {
		cfg := Relay{URL: endpoint}
		require.NoError(cfg.Validate())
		assert.Equal(15*time.Second, cfg.Interval())
	}
	for _, cfg := range []Relay{
		{URL: "http://relay.example.com"}, {URL: "https://user:secret@relay.example.com"},
		{URL: "https://relay.example.com/path"}, {URL: "https://relay.example.com?token=secret"},
		{URL: "https://relay.example.com", PollInterval: "-1s"},
		{URL: "https://relay.example.com", PollInterval: "bad"},
	} {
		require.Error(cfg.Validate())
		path := filepath.Join(t.TempDir(), "config.toml")
		require.NoError(os.WriteFile(path, fmt.Appendf(nil, "[relay]\nurl = %q\npoll_interval = %q\n", cfg.URL, cfg.PollInterval), 0o600))
		_, err := Load(path)
		require.Error(err)
	}
}
