package config

import (
	"os"
	"path/filepath"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMsgvaultStateAbsent(t *testing.T) {
	assert := Assert.New(t)
	cfg := Config{}
	state, canonURL, key, err := cfg.MsgvaultState()
	require.NoError(t, err)
	assert.Equal(MsgvaultAbsent, state)
	assert.Empty(canonURL)
	assert.Empty(key)
}

func TestMsgvaultStateWellFormedEnvKey(t *testing.T) {
	t.Setenv("MSGVAULT_API_KEY_TEST", "env-key")
	cfg := Config{Msgvault: &Msgvault{URL: "http://127.0.0.1:8123", APIKeyEnv: "MSGVAULT_API_KEY_TEST"}}
	state, canonURL, key, err := cfg.MsgvaultState()
	require.NoError(t, err)
	assert := Assert.New(t)
	assert.Equal(MsgvaultOK, state)
	assert.Equal("http://127.0.0.1:8123", canonURL)
	assert.Equal("env-key", key)
}

func TestMsgvaultStateMissingEnvVar(t *testing.T) {
	t.Setenv("MSGVAULT_DEFINITELY_NOT_SET", "")
	cfg := Config{Msgvault: &Msgvault{URL: "http://127.0.0.1:8123", APIKeyEnv: "MSGVAULT_DEFINITELY_NOT_SET"}}
	state, _, _, err := cfg.MsgvaultState()
	Assert.Equal(t, MsgvaultMisconfigured, state)
	require.Error(t, err)
}

func TestMsgvaultStateRejectsBadURLs(t *testing.T) {
	t.Setenv("MSGVAULT_API_KEY", "env-key")
	cases := []struct {
		name string
		url  string
	}{
		{name: "malformed", url: "not a url"},
		{name: "missing host", url: "http://"},
		{name: "empty host", url: "http:///"},
		{name: "no scheme", url: "//127.0.0.1"},
		{name: "javascript scheme", url: "javascript:alert(1)"},
		{name: "file scheme", url: "file:///etc/passwd"},
		{name: "ftp scheme", url: "ftp://example.com"},
		{name: "userinfo", url: "https://user:pass@msgvault.example.com"},
		{name: "query string", url: "https://msgvault.example.com?token=secret"},
		{name: "fragment", url: "https://msgvault.example.com#token"},
		{name: "plaintext http to public host", url: "http://msgvault.example.com"},
		{name: "plaintext http to private network host", url: "http://192.168.1.5:8080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Msgvault: &Msgvault{URL: tc.url, APIKeyEnv: "MSGVAULT_API_KEY"}}
			state, _, _, err := cfg.MsgvaultState()
			Assert.Equal(t, MsgvaultMisconfigured, state)
			require.Error(t, err)
			Assert.Contains(t, err.Error(), "url")
		})
	}
}

func TestMsgvaultStateAllowsLoopbackHTTP(t *testing.T) {
	t.Setenv("MSGVAULT_API_KEY_TEST", "env-key")
	for _, rawURL := range []string{"http://localhost:8123", "http://127.0.0.1:8123", "http://[::1]:8123"} {
		t.Run(rawURL, func(t *testing.T) {
			cfg := Config{Msgvault: &Msgvault{URL: rawURL, APIKeyEnv: "MSGVAULT_API_KEY_TEST"}}
			state, _, _, err := cfg.MsgvaultState()
			require.NoError(t, err)
			Assert.Equal(t, MsgvaultOK, state)
		})
	}
}

func TestMsgvaultStateCanonicalizesSurroundingWhitespace(t *testing.T) {
	t.Setenv("MSGVAULT_API_KEY_TEST", "env-key")
	cfg := Config{Msgvault: &Msgvault{URL: "  http://127.0.0.1:8123/  ", APIKeyEnv: " MSGVAULT_API_KEY_TEST "}}
	state, canonURL, key, err := cfg.MsgvaultState()
	require.NoError(t, err)
	assert := Assert.New(t)
	assert.Equal(MsgvaultOK, state)
	assert.Equal("http://127.0.0.1:8123", canonURL)
	assert.Equal("env-key", key)
}

func TestMsgvaultStateRejectsWhitespaceOnly(t *testing.T) {
	cases := []struct {
		name string
		mv   Msgvault
	}{
		{name: "whitespace url", mv: Msgvault{URL: "   ", APIKeyEnv: "MSGVAULT_API_KEY"}},
		{name: "whitespace api key env", mv: Msgvault{URL: "http://127.0.0.1:8123", APIKeyEnv: "   "}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Msgvault: &tc.mv}
			state, _, _, err := cfg.MsgvaultState()
			Assert.Equal(t, MsgvaultMisconfigured, state)
			require.Error(t, err)
		})
	}
}

func TestMsgvaultConfigRoundTrip(t *testing.T) {
	t.Setenv("MSGVAULT_API_KEY_TEST", "env-key")
	cfg, cfg2 := roundTripConfigString(t, `
[[repos]]
owner = "a"
name = "b"

[msgvault]
url = "http://127.0.0.1:8123"
api_key_env = "MSGVAULT_API_KEY_TEST"
`)
	require.NotNil(t, cfg.Msgvault)
	require.NotNil(t, cfg2.Msgvault)
	assert := Assert.New(t)
	assert.Equal("http://127.0.0.1:8123", cfg2.Msgvault.URL)
	assert.Equal("MSGVAULT_API_KEY_TEST", cfg2.Msgvault.APIKeyEnv)
	state, canonURL, key, err := cfg2.MsgvaultState()
	require.NoError(t, err)
	assert.Equal(MsgvaultOK, state)
	assert.Equal("http://127.0.0.1:8123", canonURL)
	assert.Equal("env-key", key)
}

func TestMsgvaultConfigRejectsPlaintextAPIKey(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "literal api_key value",
			body: `
[[repos]]
owner = "a"
name = "b"

[msgvault]
url = "http://127.0.0.1:8123"
api_key = "leak"
api_key_env = "MSGVAULT_API_KEY_TEST"
`,
		},
		{
			name: "empty api_key value",
			body: `
[[repos]]
owner = "a"
name = "b"

[msgvault]
url = "http://127.0.0.1:8123"
api_key = ""
api_key_env = "MSGVAULT_API_KEY_TEST"
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.body))
			require.Error(t, err)
			Assert.Contains(t, err.Error(), "api_key")
		})
	}
}

func TestMsgvaultConfigSaveDoesNotEmitSecretField(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
[[repos]]
owner = "a"
name = "b"

[msgvault]
url = "http://127.0.0.1:8123"
api_key_env = "MSGVAULT_API_KEY_TEST"
`))
	require.NoError(t, err)
	savePath := filepath.Join(t.TempDir(), "saved.toml")
	require.NoError(t, cfg.Save(savePath))
	dataBytes, err := os.ReadFile(savePath)
	require.NoError(t, err)
	data := string(dataBytes)
	assert := Assert.New(t)
	assert.Contains(data, "[msgvault]")
	assert.Contains(data, `api_key_env = "MSGVAULT_API_KEY_TEST"`)
	assert.NotContains(data, "api_key =")
}
