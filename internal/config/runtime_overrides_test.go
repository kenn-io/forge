package config

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/tokenauth"
)

func runtimeConfigFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}

func TestRuntimeEnvironmentOverlayAndSave(t *testing.T) {
	path := runtimeConfigFile(t, `host="127.0.0.1"
port=8092
allowed_hosts=["file.example:8092"]
trust_reverse_proxy=false
[api]
require_auth=false
[roborev]
endpoint="http://file.example:7373"
`)
	t.Setenv("KENN_FORGE_HOST", "0.0.0.0")
	t.Setenv("KENN_FORGE_PORT", "8093")
	t.Setenv("KENN_FORGE_ALLOWED_HOSTS", " forge.example:8093 , proxy.example ")
	t.Setenv("KENN_FORGE_TRUST_REVERSE_PROXY", "true")
	t.Setenv("KENN_FORGE_REQUIRE_AUTH", "true")
	t.Setenv("KENN_FORGE_ROBOREV_ENDPOINT", "http://review.example:7373")
	dataRoot := t.TempDir()
	canonicalRoot, err := filepath.EvalSymlinks(dataRoot)
	require.NoError(t, err)
	dataDir := filepath.Join(dataRoot, "data")
	t.Setenv("KENN_FORGE_DATA_DIR", dataDir)
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "0.0.0.0", cfg.Host)
	assert.Equal(t, "env", cfg.RuntimeSources()["host"])
	sources := cfg.RuntimeSources()
	sources["host"] = "mutated"
	assert.Equal(t, "env", cfg.RuntimeSources()["host"])
	assert.Equal(t, 8093, cfg.Port)
	assert.Equal(t, []string{"forge.example:8093", "proxy.example"}, cfg.AllowedHosts)
	assert.True(t, cfg.API.RequireAuth)
	assert.True(t, cfg.TrustReverseProxy)
	assert.Equal(t, filepath.Join(canonicalRoot, "data"), cfg.DataDir)
	assert.Equal(t, "http://review.example:7373", cfg.Roborev.Endpoint)
	cfg.AirplaneMode = true
	require.NoError(t, cfg.Save(path))
	for _, name := range runtimeEnvNames {
		t.Setenv(name, "")
		require.NoError(t, os.Unsetenv(name))
	}
	saved, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", saved.Host)
	assert.Equal(t, 8092, saved.Port)
	assert.Equal(t, []string{"file.example:8092"}, saved.AllowedHosts)
	assert.Equal(t, DefaultDataDir(), saved.DataDir)
	assert.False(t, saved.API.RequireAuth)
	assert.False(t, saved.TrustReverseProxy)
	assert.Equal(t, "http://file.example:7373", saved.Roborev.Endpoint)
	assert.True(t, saved.AirplaneMode)
}

func TestRuntimeFlagsPrecedenceAndLatestFilePreservation(t *testing.T) {
	path := runtimeConfigFile(t, "host=\"invalid-file-host\"\nport=-1\n")
	t.Setenv("KENN_FORGE_HOST", "invalid-env-host")
	t.Setenv("KENN_FORGE_PORT", "invalid-env-port")
	host, port := "127.0.0.1", 8094
	cfg, err := LoadWithOverrides(path, Overrides{Host: &host, Port: &port})
	require.NoError(t, err)
	host, port = "invalid-mutated-host", -9
	assert.Equal(t, "127.0.0.1", *cfg.RuntimeOverrides().Host)
	assert.Equal(t, 8094, *cfg.RuntimeOverrides().Port)
	require.NoError(t, os.WriteFile(path, []byte("host=\"192.0.2.10\"\nport=8095\n"), 0o600))
	cfg.AirplaneMode = true
	require.NoError(t, cfg.Save(path))
	t.Setenv("KENN_FORGE_HOST", "127.0.0.1")
	t.Setenv("KENN_FORGE_PORT", "8096")
	saved, err := LoadWithOverrides(path, cfg.RuntimeOverrides())
	require.NoError(t, err)
	assert.Equal(t, 8094, saved.Port)
	for _, name := range []string{"KENN_FORGE_HOST", "KENN_FORGE_PORT"} {
		require.NoError(t, os.Unsetenv(name))
	}
	saved, err = Load(path)
	require.NoError(t, err)
	assert.Equal(t, "192.0.2.10", saved.Host)
	assert.Equal(t, 8095, saved.Port)
	assert.True(t, saved.AirplaneMode)
}

func TestRuntimeWinningInputValidation(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"KENN_FORGE_PORT", ""},
		{"KENN_FORGE_PORT", "0"},
		{"KENN_FORGE_PORT", "65536"},
		{"KENN_FORGE_PORT", "12x"},
		{"KENN_FORGE_REQUIRE_AUTH", "maybe"},
		{"KENN_FORGE_TRUST_REVERSE_PROXY", ""},
		{"KENN_FORGE_HOST", ""},
		{"KENN_FORGE_HOST", "forge.example"},
		{"KENN_FORGE_ROBOREV_ENDPOINT", ""},
		{"KENN_FORGE_ROBOREV_ENDPOINT", "invalid"},
		{"KENN_FORGE_ALLOWED_HOSTS", "a.example,,b.example"},
	} {
		t.Run(tc.name+"/"+tc.value, func(t *testing.T) {
			t.Setenv(tc.name, tc.value)
			_, err := Load(runtimeConfigFile(t, ""))
			require.Error(t, err)
		})
	}
	t.Run("empty allowlist clears file", func(t *testing.T) {
		t.Setenv("KENN_FORGE_ALLOWED_HOSTS", "")
		cfg, err := Load(runtimeConfigFile(t, `allowed_hosts=["old.example"]`))
		require.NoError(t, err)
		assert.Empty(t, cfg.AllowedHosts)
	})
	t.Run("bad TOML types cannot be shadowed", func(t *testing.T) {
		port := 8091
		_, err := LoadWithOverrides(runtimeConfigFile(t, `port="bad"`), Overrides{Port: &port})
		require.Error(t, err)
	})
}

func TestSaveWithoutRuntimeOverlaysCanReplaceMalformedFile(t *testing.T) {
	path := runtimeConfigFile(t, "")
	cfg, err := Load(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(`port="invalid"`), 0o600))
	cfg.AirplaneMode = true
	require.NoError(t, cfg.Save(path))
	saved, err := Load(path)
	require.NoError(t, err)
	assert.True(t, saved.AirplaneMode)
}

func TestRoborevAuthKeyValidation(t *testing.T) {
	for _, tc := range []struct {
		name, key, endpoint string
		invalid             bool
		fromEnv             bool
	}{
		{name: "disabled"},
		{name: "base path", key: strings.Repeat("ab", 32), endpoint: "https://review.example/base"},
		{name: "query", key: strings.Repeat("ab", 32), endpoint: "https://review.example/base?tenant=one", invalid: true},
		{name: "fragment from env", key: strings.Repeat("ab", 32), endpoint: "https://review.example/base#section", invalid: true, fromEnv: true},
		{name: "keyless query and fragment from env", endpoint: "https://review.example/base?tenant=one#section", fromEnv: true},
		{name: "default endpoint", key: strings.Repeat("ab", 32), invalid: true},
		{name: "relative endpoint", key: strings.Repeat("ab", 32), endpoint: "/review", invalid: true},
		{name: "userinfo", key: strings.Repeat("ab", 32), endpoint: "https://user:password@review.example", invalid: true},
		{name: "uppercase", key: strings.Repeat("AB", 32), endpoint: "http://review.example", invalid: true},
		{name: "nonhex", key: strings.Repeat("gg", 32), endpoint: "http://review.example", invalid: true},
		{name: "short", key: "short-secret", endpoint: "http://review.example", invalid: true},
		{name: "whitespace", key: strings.Repeat("ab", 32) + "\n", endpoint: "http://review.example", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KENN_FORGE_ROBOREV_AUTH_KEY", tc.key)
			if tc.fromEnv {
				t.Setenv("KENN_FORGE_ROBOREV_ENDPOINT", tc.endpoint)
			}
			cfg, err := Load(runtimeConfigFile(t, fmt.Sprintf("[roborev]\nendpoint=%q\n", tc.endpoint)))
			if tc.invalid {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), tc.key)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.key, cfg.RoborevAuthKey())
			assert.Equal(t, tc.endpoint, cfg.Roborev.Endpoint)
		})
	}
}

func TestRoborevAuthKeyRemainsPrivate(t *testing.T) {
	assert := assert.New(t)
	key := strings.Repeat("ac", 32)
	t.Setenv("KENN_FORGE_ROBOREV_AUTH_KEY", key)
	path := runtimeConfigFile(t, "[roborev]\nendpoint=\"https://review.example\"\n")
	cfg, err := Load(path)
	require.NoError(t, err)
	require.NoError(t, cfg.Save(path))
	saved, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(string(saved), key)
	var logs bytes.Buffer
	logger := slog.New(tokenauth.NewRedactingHandler(slog.NewTextHandler(&logs, nil)))
	logger.Error("daemon failed", "err", errors.New("rejected "+key))
	assert.NotContains(logs.String(), key)
	assert.Contains(logs.String(), "[REDACTED]")
}
