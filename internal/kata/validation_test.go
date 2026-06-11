package kata

import (
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateTarget(t *testing.T) {
	tests := []struct {
		name    string
		daemon  Daemon
		wantErr bool
	}{
		{"https public ok", Daemon{ID: "a", URL: "https://kata.example.com"}, false},
		{"unix ok", Daemon{ID: "a", URL: "unix:///run/kata.sock"}, false},
		{"http loopback ok", Daemon{ID: "a", URL: "http://127.0.0.1:8080"}, false},
		{"http localhost ok", Daemon{ID: "a", URL: "http://localhost:8080"}, false},
		{"http localhost fqdn ok", Daemon{ID: "a", URL: "http://localhost.:8080"}, false},
		{"http rfc1918 ok", Daemon{ID: "a", URL: "http://10.1.2.3:8080"}, false},
		{"http cgnat ok", Daemon{ID: "a", URL: "http://100.64.0.5:7777"}, false},
		{"http link local ok", Daemon{ID: "a", URL: "http://169.254.1.2:7777"}, false},
		{"http unspecified ok", Daemon{ID: "a", URL: "http://0.0.0.0:7777"}, false},
		{"http public rejected", Daemon{ID: "a", URL: "http://203.0.113.5:8080"}, true},
		{"http hostname rejected", Daemon{ID: "a", URL: "http://kata.internal:8080"}, true},
		{"http public allow_insecure ok", Daemon{ID: "a", URL: "http://203.0.113.5:8080", AllowInsecure: true}, false},
		{"https opaque rejected", Daemon{ID: "a", URL: "https:kata.example.com"}, true},
		{"https hostless rejected", Daemon{ID: "a", URL: "https:///api/v1"}, true},
		{"https port only rejected", Daemon{ID: "a", URL: "https://:443"}, true},
		{"http hostless rejected", Daemon{ID: "a", URL: "http:///api/v1", AllowInsecure: true}, true},
		{"http port only rejected", Daemon{ID: "a", URL: "http://:8080", AllowInsecure: true}, true},
		{"unix empty path rejected", Daemon{ID: "a", URL: "unix://"}, true},
		{"empty url skipped", Daemon{ID: "a", Local: true}, false},
		{"invalid url rejected", Daemon{ID: "a", URL: "://nonsense"}, true},
		{"unsupported scheme rejected", Daemon{ID: "a", URL: "ftp://10.0.0.1"}, true},
		{"http ipv6 loopback ok", Daemon{ID: "a", URL: "http://[::1]:8080"}, false},
		{"http ipv6 ula ok", Daemon{ID: "a", URL: "http://[fd00::1]:7777"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTarget(tt.daemon)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateLocalTarget(t *testing.T) {
	tests := []struct {
		name    string
		daemon  Daemon
		wantErr bool
	}{
		{"empty dynamic local ok", Daemon{ID: "local", Local: true}, false},
		{"unix ok", Daemon{ID: "local", Local: true, URL: "unix:///tmp/kata.sock"}, false},
		{"http loopback ok", Daemon{ID: "local", Local: true, URL: "http://127.0.0.1:7777"}, false},
		{"https loopback ok", Daemon{ID: "local", Local: true, URL: "https://127.0.0.1:7777"}, false},
		{"localhost ok", Daemon{ID: "local", Local: true, URL: "http://localhost:7777"}, false},
		{"unix empty path rejected", Daemon{ID: "local", Local: true, URL: "unix://"}, true},
		{"http hostless rejected", Daemon{ID: "local", Local: true, URL: "http:///api/v1"}, true},
		{"private lan rejected", Daemon{ID: "local", Local: true, URL: "http://192.168.1.10:7777"}, true},
		{"public rejected", Daemon{ID: "local", Local: true, URL: "http://203.0.113.5:7777", AllowInsecure: true}, true},
		{"hostname rejected", Daemon{ID: "local", Local: true, URL: "https://kata.example.com"}, true},
		{"unsupported scheme rejected", Daemon{ID: "local", Local: true, URL: "ftp://127.0.0.1"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateLocalTarget(tt.daemon)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestResolveDaemonResolvesTokenEnv(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	t.Setenv("KATA_TEST_TOKEN", "secret-from-env")

	daemon, err := ResolveDaemon(Daemon{ID: "a", URL: "https://x.example", TokenEnv: "KATA_TEST_TOKEN"})
	require.NoError(err)

	assert.Equal("secret-from-env", daemon.Token)
}

func TestResolveDaemonRejectsUnsetTokenEnv(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	t.Setenv("KATA_TEST_TOKEN", "")

	_, err := ResolveDaemon(Daemon{ID: "a", URL: "https://x.example", TokenEnv: "KATA_TEST_TOKEN"})

	require.Error(err)
	assert.Contains(err.Error(), "token_env")
	assert.Contains(err.Error(), "KATA_TEST_TOKEN")
}

func TestResolveDaemonAppliesSecurityGuard(t *testing.T) {
	require := require.New(t)

	_, err := ResolveDaemon(Daemon{ID: "a", URL: "http://203.0.113.5:8080", Token: "t"})

	require.Error(err)
}

func TestResolveDaemonInlineTokenPreserved(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	daemon, err := ResolveDaemon(Daemon{ID: "a", URL: "https://x.example", Token: "inline"})
	require.NoError(err)

	assert.Equal("inline", daemon.Token)
}

func TestResolveDaemonAllowsDynamicLocalEntry(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	daemon, err := ResolveDaemon(Daemon{ID: "local", Local: true})
	require.NoError(err)

	assert.True(daemon.Local)
	assert.Empty(daemon.URL)
}

func TestRedactURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"http userinfo query fragment path", "http://user:s3cr3t@example.test/kata/pathsecret?access_token=leak#frag", "http://example.test"},
		{"https path", "https://gateway.example.test/kata/secret", "https://gateway.example.test"},
		{"unix path preserved", "unix:///tmp/kata.sock", "unix:///tmp/kata.sock"},
		{"invalid unchanged", "://nonsense", "://nonsense"},
		{"clean unchanged", "https://kata.example.test", "https://kata.example.test"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			Assert.Equal(t, tt.want, RedactURL(tt.raw))
		})
	}
}
