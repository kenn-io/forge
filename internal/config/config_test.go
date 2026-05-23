package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func roundTripConfigString(t *testing.T, content string) (*Config, *Config) {
	t.Helper()
	cfg, err := Load(writeConfig(t, content))
	require.NoError(t, err)
	savePath := filepath.Join(t.TempDir(), "saved.toml")
	require.NoError(t, cfg.Save(savePath))
	cfg2, err := Load(savePath)
	require.NoError(t, err)
	return cfg, cfg2
}

func setFakeGHCLI(t *testing.T, stdout string) {
	t.Helper()
	setFakeGHCLIScript(t, fakeGHCLIOptions{Stdout: stdout})
}

type fakeGHCLIOptions struct {
	// Stdout is echoed verbatim on success.
	Stdout string
	// Stderr is echoed to stderr regardless of exit code.
	Stderr string
	// ExitCode is the exit status of the fake gh. Default 0.
	ExitCode int
	// SleepSeconds, if >0, makes the fake sleep before exiting.
	SleepSeconds int
}

// setFakeGHCLIScript writes a fake `gh` to a temp dir and points PATH
// at it. The fake records its argv to <tempdir>/argv (one entry per
// invocation, newline-separated), then emits stdout/stderr/exit per
// opts. The argv-file path is returned and also exported via
// FAKE_GH_ARGV so the script can locate it.
//
// To keep PATH minimal (the fake gh should be the only resolvable
// `gh`), the helper embeds absolute paths for any external tools the
// script needs — currently just `sleep` when SleepSeconds > 0.
func setFakeGHCLIScript(t *testing.T, opts fakeGHCLIOptions) string {
	t.Helper()
	dir := t.TempDir()
	argvPath := filepath.Join(dir, "argv")
	writeFakeGHCLI(t, dir, fakeGHCLIScript(t, opts))
	t.Setenv("FAKE_GH_ARGV", argvPath)
	return argvPath
}

func setFakeGHCLIWithScript(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	argvPath := filepath.Join(dir, "argv")
	writeFakeGHCLI(t, dir, script)
	t.Setenv("FAKE_GH_ARGV", argvPath)
	return argvPath
}

func writeFakeGHCLI(t *testing.T, dir, script string) {
	t.Helper()
	require.NoError(t, os.WriteFile(fakeGHCLIPath(dir), []byte(script), 0o755))
	t.Setenv("PATH", dir)
}

// readFakeGHArgv returns the recorded argv strings, one per
// invocation, in call order. Returns nil when no calls were made.
func readFakeGHArgv(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	require.NoError(t, err)
	raw := strings.TrimRight(string(data), "\r\n")
	if raw == "" {
		return nil
	}
	lines := strings.Split(raw, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}
	return lines
}

func TestLoadValid(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
sync_interval = "10m"
github_token_env = "MY_TOKEN"
host = "127.0.0.1"
port = 9000

[[repos]]
owner = "apache"
name = "arrow"

[[repos]]
owner = "ibis-project"
name = "ibis"
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Repos, 2)
	assert.Equal("apache/arrow", cfg.Repos[0].FullName())
	assert.Equal("10m", cfg.SyncInterval)
	assert.Equal(9000, cfg.Port)
}

func TestLoadCasefoldsRepoOwnerAndName(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
[[repos]]
owner = "Org"
name = "Foo"
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Repos, 1)
	assert.Equal("org", cfg.Repos[0].Owner)
	assert.Equal("foo", cfg.Repos[0].Name)
}

func TestLoadRejectsDuplicateReposAfterCasefolding(t *testing.T) {
	path := writeConfig(t, `
[[repos]]
owner = "Org"
name = "Foo"

[[repos]]
owner = "org"
name = "foo"
`)

	_, err := Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), `duplicate repo "org/foo"`)
}

func TestLoadDefaults(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
[[repos]]
owner = "test"
name = "repo"
`)
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal("5m", cfg.SyncInterval)
	assert.Equal("127.0.0.1", cfg.Host)
	assert.Equal(8091, cfg.Port)
	assert.Equal("github.com", cfg.DefaultPlatformHost)
	require.Len(t, cfg.Repos, 1)
	assert.Equal("github", cfg.Repos[0].Platform)
	assert.Equal("github.com", cfg.Repos[0].PlatformHostOrDefault())
}

func TestLoadNormalizesDefaultPlatformHost(t *testing.T) {
	assert := Assert.New(t)
	cfg, cfg2 := roundTripConfigString(t, `
default_platform_host = "GHE.Example.COM"

[[repos]]
owner = "test"
name = "repo"
`)

	assert.Equal("ghe.example.com", cfg.DefaultPlatformHost)
	assert.Equal("ghe.example.com", cfg2.DefaultPlatformHost)
}

func TestLoadAppliesDefaultPlatformHostToLegacyGitHubRepos(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
default_platform_host = "ghe.example.com"
github_token_env = "GHE_TOKEN"

[[repos]]
owner = "Acme"
name = "Widgets"
`)
	t.Setenv("GHE_TOKEN", "ghe-secret")

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Repos, 1)
	assert.Equal("github", cfg.Repos[0].Platform)
	assert.Equal("ghe.example.com", cfg.Repos[0].PlatformHost)
	assert.Equal("ghe.example.com", cfg.Repos[0].PlatformHostOrDefault())
	assert.Equal(
		"ghe-secret",
		cfg.TokenForPlatformHost("github", cfg.Repos[0].PlatformHost, ""),
	)
}

func TestLoadNoRepos(t *testing.T) {
	path := writeConfig(t, `host = "127.0.0.1"`)
	cfg, err := Load(path)
	require.NoError(t, err)
	Assert.Empty(t, cfg.Repos)
}

func TestLoadInvalidSyncInterval(t *testing.T) {
	path := writeConfig(t, `
sync_interval = "not-a-duration"
[[repos]]
owner = "a"
name = "b"
`)
	_, err := Load(path)
	require.Error(t, err)
}

func TestLoadRejectsNonLoopback(t *testing.T) {
	path := writeConfig(t, `
host = "0.0.0.0"
[[repos]]
owner = "a"
name = "b"
`)
	_, err := Load(path)
	require.Error(t, err)
}

func TestLoadRepoMissingFields(t *testing.T) {
	path := writeConfig(t, `
[[repos]]
owner = "a"
`)
	_, err := Load(path)
	require.Error(t, err)
}

func TestLoadRepoNameDotGitOnly(t *testing.T) {
	path := writeConfig(t, `
[[repos]]
owner = "a"
name = ".git"
`)
	_, err := Load(path)
	require.Error(t, err)
}

func TestLoadRejectsGlobInOwner(t *testing.T) {
	path := writeConfig(t, `
[[repos]]
owner = "acme-*"
name = "widgets"
`)

	_, err := Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "glob syntax in owner")
}

func TestLoadRejectsGlobInOwnerBeforeGitHubRefNormalization(t *testing.T) {
	path := writeConfig(t, `
[[repos]]
owner = "acme-*"
name = "https://github.com/acme/widgets"
`)

	_, err := Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "glob syntax in owner")
}

func TestRepoHasNameGlob(t *testing.T) {
	assert := Assert.New(t)

	assert.False((&Repo{Owner: "acme", Name: "widgets"}).HasNameGlob())
	assert.True((&Repo{Owner: "acme", Name: "widgets-*"}).HasNameGlob())
	assert.True((&Repo{Owner: "acme", Name: "widgets-?"}).HasNameGlob())
}

func TestGitHubToken(t *testing.T) {
	t.Setenv("TEST_GH_TOKEN", "secret123")
	cfg := &Config{GitHubTokenEnv: "TEST_GH_TOKEN"}
	Assert.Equal(t, "secret123", cfg.GitHubToken())
}

func TestGitHubTokenFallsBackToGHCli(t *testing.T) {
	setFakeGHCLI(t, "gh-secret")
	t.Setenv("TEST_GH_TOKEN", "")

	cfg := &Config{GitHubTokenEnv: "TEST_GH_TOKEN"}
	Assert.Equal(t, "gh-secret", cfg.GitHubToken())
}

func TestGitHubTokenPrefersEnvVarOverGHCli(t *testing.T) {
	setFakeGHCLI(t, "gh-secret")
	t.Setenv("TEST_GH_TOKEN", "secret123")

	cfg := &Config{GitHubTokenEnv: "TEST_GH_TOKEN"}
	Assert.Equal(t, "secret123", cfg.GitHubToken())
}

func TestBasePathValidation(t *testing.T) {
	base := `
[[repos]]
owner = "a"
name = "b"
`
	tests := []struct {
		name    string
		value   string
		wantErr bool
		wantBP  string
	}{
		{"default", "", false, "/"},
		{"root", "/", false, "/"},
		{"simple", "middleman", false, "/middleman/"},
		{"with slashes", "/middleman/", false, "/middleman/"},
		{"nested", "/apps/middleman", false, "/apps/middleman/"},
		{"dot segment", "/../evil", true, ""},
		{"single dot", "/./path", true, ""},
		{"special chars", "/mid<script>", true, ""},
		{"quotes", `/mid"man`, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extra := ""
			if tt.value != "" {
				extra = `base_path = "` + tt.value + `"`
			}
			path := writeConfig(t, extra+"\n"+base)
			cfg, err := Load(path)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			Assert.Equal(t, tt.wantBP, cfg.BasePath)
		})
	}
}

func TestGitHubTokenReturnsEmptyWhenGHCliUnavailable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("TEST_GH_TOKEN", "")

	cfg := &Config{GitHubTokenEnv: "TEST_GH_TOKEN"}
	Assert.Empty(t, cfg.GitHubToken())
}

func TestMiddlemanHomeOverridesDefaultPaths(t *testing.T) {
	assert := Assert.New(t)
	t.Setenv("MIDDLEMAN_HOME", "/tmp/middleman-test")

	assert.Equal(
		filepath.FromSlash("/tmp/middleman-test/config.toml"),
		DefaultConfigPath(),
	)
	assert.Equal("/tmp/middleman-test", DefaultDataDir())
}

func TestDefaultPathsWithoutMiddlemanHome(t *testing.T) {
	assert := Assert.New(t)
	t.Setenv("MIDDLEMAN_HOME", "")
	t.Setenv("HOME", "/fakehome")

	assert.Equal(
		filepath.FromSlash("/fakehome/.config/middleman/config.toml"),
		DefaultConfigPath(),
	)
	assert.Equal(filepath.FromSlash("/fakehome/.config/middleman"), DefaultDataDir())
}

func TestDBPath(t *testing.T) {
	cfg := &Config{DataDir: "/tmp/middleman-test"}
	expected := filepath.FromSlash("/tmp/middleman-test/middleman.db")
	Assert.Equal(t, expected, cfg.DBPath())
}

func TestLoadActivityDefaults(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
[[repos]]
owner = "a"
name = "b"
`)
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal("threaded", cfg.Activity.ViewMode)
	assert.Equal("7d", cfg.Activity.TimeRange)
	assert.False(cfg.Activity.HideClosed)
	assert.False(cfg.Activity.HideBots)
	assert.False(cfg.Activity.CollapseThreads)
}

func TestLoadActivityExplicit(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
[[repos]]
owner = "a"
name = "b"

[activity]
view_mode = "threaded"
time_range = "30d"
hide_closed = true
hide_bots = true
collapse_threads = true
`)
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal("threaded", cfg.Activity.ViewMode)
	assert.Equal("30d", cfg.Activity.TimeRange)
	assert.True(cfg.Activity.HideClosed)
	assert.True(cfg.Activity.HideBots)
	assert.True(cfg.Activity.CollapseThreads)
}

func TestLoadActivityInvalidViewMode(t *testing.T) {
	path := writeConfig(t, `
[[repos]]
owner = "a"
name = "b"

[activity]
view_mode = "kanban"
`)
	_, err := Load(path)
	require.Error(t, err)
}

func TestLoadActivityInvalidTimeRange(t *testing.T) {
	path := writeConfig(t, `
[[repos]]
owner = "a"
name = "b"

[activity]
time_range = "1y"
`)
	_, err := Load(path)
	require.Error(t, err)
}

func TestLoadNormalizesRepoNames(t *testing.T) {
	tests := []struct {
		name      string
		owner     string
		repoName  string
		wantOwner string
		wantName  string
	}{
		{
			name:      "strips .git suffix",
			owner:     "apache",
			repoName:  "arrow.git",
			wantOwner: "apache",
			wantName:  "arrow",
		},
		{
			name:      "HTTPS URL in name",
			owner:     "ignored",
			repoName:  "https://github.com/apache/arrow",
			wantOwner: "apache",
			wantName:  "arrow",
		},
		{
			name:      "HTTPS URL with .git in name",
			owner:     "ignored",
			repoName:  "https://github.com/apache/arrow.git",
			wantOwner: "apache",
			wantName:  "arrow",
		},
		{
			name:      "SSH URL in name",
			owner:     "ignored",
			repoName:  "git@github.com:apache/arrow.git",
			wantOwner: "apache",
			wantName:  "arrow",
		},
		{
			name:      "SSH URL without .git in name",
			owner:     "ignored",
			repoName:  "git@github.com:apache/arrow",
			wantOwner: "apache",
			wantName:  "arrow",
		},
		{
			name:      "SSH URI-style URL",
			owner:     "ignored",
			repoName:  "ssh://git@github.com/apache/arrow.git",
			wantOwner: "apache",
			wantName:  "arrow",
		},
		{
			name:      "SSH URI-style with port",
			owner:     "ignored",
			repoName:  "ssh://git@github.com:22/apache/arrow.git",
			wantOwner: "apache",
			wantName:  "arrow",
		},
		{
			name:      "omitted platform GitLab SSH URL not parsed",
			owner:     "ignored",
			repoName:  "ssh://git@gitlab.com/apache/arrow.git",
			wantOwner: "ignored",
			wantName:  "ssh://git@gitlab.com/apache/arrow",
		},
		{
			name:      "bare github.com path in name",
			owner:     "ignored",
			repoName:  "github.com/apache/arrow",
			wantOwner: "apache",
			wantName:  "arrow",
		},
		{
			name:      "HTTPS URL in owner",
			owner:     "https://github.com/apache/arrow.git",
			repoName:  "ignored",
			wantOwner: "apache",
			wantName:  "arrow",
		},
		{
			name:      "plain owner and name unchanged",
			owner:     "apache",
			repoName:  "arrow",
			wantOwner: "apache",
			wantName:  "arrow",
		},
		{
			name:      "URL with query string",
			owner:     "ignored",
			repoName:  "https://github.com/apache/arrow?tab=readme",
			wantOwner: "apache",
			wantName:  "arrow",
		},
		{
			name:      "URL with fragment",
			owner:     "ignored",
			repoName:  "https://github.com/apache/arrow#readme",
			wantOwner: "apache",
			wantName:  "arrow",
		},
		{
			name:      "URL with trailing slash",
			owner:     "ignored",
			repoName:  "https://github.com/apache/arrow/",
			wantOwner: "apache",
			wantName:  "arrow",
		},
		{
			name:      "URL with .git and trailing slash",
			owner:     "ignored",
			repoName:  "https://github.com/apache/arrow.git/",
			wantOwner: "apache",
			wantName:  "arrow",
		},
		{
			name:      "repo literally named github.com",
			owner:     "acme",
			repoName:  "github.com",
			wantOwner: "acme",
			wantName:  "github.com",
		},
		{
			name:      "non-github HTTPS host not parsed",
			owner:     "ignored",
			repoName:  "https://notgithub.com/apache/arrow",
			wantOwner: "ignored",
			wantName:  "https://notgithub.com/apache/arrow",
		},
		{
			name:      "non-github SSH host not parsed",
			owner:     "ignored",
			repoName:  "git@notgithub.com:apache/arrow.git",
			wantOwner: "ignored",
			wantName:  "git@notgithub.com:apache/arrow",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := Assert.New(t)
			cfg := fmt.Sprintf(`
[[repos]]
owner = %q
name = %q
`, tt.owner, tt.repoName)
			path := writeConfig(t, cfg)
			got, err := Load(path)
			require.NoError(t, err)
			assert.Equal(tt.wantOwner, got.Repos[0].Owner)
			assert.Equal(tt.wantName, got.Repos[0].Name)
		})
	}
}

func TestLoadOmittedPlatformGitLabURLRemainsGitHub(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
[[repos]]
owner = "Ignored"
name = "https://gitlab.com/MyGroup/SubGroup/MyProject.git"
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Repos, 1)
	assert.Equal("github", cfg.Repos[0].Platform)
	assert.Equal("github.com", cfg.Repos[0].PlatformHostOrDefault())
	assert.Equal("ignored", cfg.Repos[0].Owner)
	assert.Equal("https://gitlab.com/mygroup/subgroup/myproject", cfg.Repos[0].Name)
}

func TestLoadRejectsMalformedGitHubRef(t *testing.T) {
	tests := []struct {
		name     string
		owner    string
		repoName string
	}{
		{
			name:     "HTTPS URL missing repo",
			owner:    "ignored",
			repoName: "https://github.com/apache/",
		},
		{
			name:     "HTTPS URL owner only",
			owner:    "ignored",
			repoName: "https://github.com/apache",
		},
		{
			name:     "SSH URL missing repo",
			owner:    "ignored",
			repoName: "git@github.com:apache",
		},
		{
			name:     "bare HTTPS prefix",
			owner:    "ignored",
			repoName: "https://github.com/",
		},
		{
			name:     "bare github.com slash",
			owner:    "ignored",
			repoName: "github.com/",
		},
		{
			name:     "bare SSH prefix",
			owner:    "ignored",
			repoName: "git@github.com:",
		},
		{
			name:     "HTTPS host only no slash",
			owner:    "ignored",
			repoName: "https://github.com",
		},
		{
			name:     "SSH URI bare host",
			owner:    "ignored",
			repoName: "ssh://git@github.com",
		},
		{
			name:     "SSH URI bare host with port",
			owner:    "ignored",
			repoName: "ssh://git@github.com:22",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := fmt.Sprintf(`
[[repos]]
owner = %q
name = %q
`, tt.owner, tt.repoName)
			path := writeConfig(t, cfg)
			_, err := Load(path)
			require.Error(t, err)
			Assert.Contains(t, err.Error(), "incomplete GitHub reference")
		})
	}
}

func TestSaveRoundTrip(t *testing.T) {
	assert := Assert.New(t)
	cfg, cfg2 := roundTripConfigString(t, `
sync_interval = "10m"
github_token_env = "MY_TOKEN"
host = "127.0.0.1"
port = 9000
base_path = "/app/"

[[repos]]
owner = "apache"
name = "arrow"

[activity]
view_mode = "threaded"
time_range = "30d"
hide_closed = true
hide_bots = true
collapse_threads = true
`)
	assert.Equal("MY_TOKEN", cfg2.GitHubTokenEnv)
	assert.Equal(cfg.SyncInterval, cfg2.SyncInterval)
	assert.Equal(cfg.Host, cfg2.Host)
	assert.Equal(cfg.Port, cfg2.Port)
	assert.Equal(cfg.BasePath, cfg2.BasePath)
	assert.Len(cfg2.Repos, len(cfg.Repos))
	assert.Equal(cfg.Repos[0].FullName(), cfg2.Repos[0].FullName())
	assert.Equal(cfg.Activity.ViewMode, cfg2.Activity.ViewMode)
	assert.Equal(cfg.Activity.TimeRange, cfg2.Activity.TimeRange)
	assert.Equal(cfg.Activity.HideClosed, cfg2.Activity.HideClosed)
	assert.Equal(cfg.Activity.HideBots, cfg2.Activity.HideBots)
	assert.Equal(cfg.Activity.CollapseThreads, cfg2.Activity.CollapseThreads)
}

func TestSavePreservesDefaults(t *testing.T) {
	assert := Assert.New(t)
	_, cfg2 := roundTripConfigString(t, `
[[repos]]
owner = "a"
name = "b"
`)
	assert.Equal("5m", cfg2.SyncInterval)
	assert.Equal("127.0.0.1", cfg2.Host)
	assert.Equal(8091, cfg2.Port)
	assert.Equal("threaded", cfg2.Activity.ViewMode)
	assert.Equal("7d", cfg2.Activity.TimeRange)
}

func TestEnsureDefaultCreatesFile(t *testing.T) {
	assert := Assert.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "config.toml")

	require.NoError(t, EnsureDefault(path))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(string(data), "sync_interval")
	assert.Contains(string(data), "github_token_env")
	assert.Contains(string(data), "[[repos]]")
}

func TestEnsureDefaultSkipsExisting(t *testing.T) {
	path := writeConfig(t, `
[[repos]]
owner = "a"
name = "b"
`)
	require.NoError(t, EnsureDefault(path))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	Assert.Contains(t, string(data), `owner = "a"`)
}

func TestEnsureDefaultIdempotent(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	require.NoError(EnsureDefault(path))
	info1, err := os.Stat(path)
	require.NoError(err)

	require.NoError(EnsureDefault(path))
	info2, err := os.Stat(path)
	require.NoError(err)

	require.Equal(info1.ModTime(), info2.ModTime())
}

func TestLoadRepoPlatformHost(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
[[repos]]
owner = "apache"
name = "arrow"
platform_host = "github.example.com"
token_env = "GHE_TOKEN"

[[repos]]
owner = "ibis-project"
name = "ibis"
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Repos, 2)
	assert.Equal("github", cfg.Repos[0].Platform)
	assert.Equal("github.example.com", cfg.Repos[0].PlatformHost)
	assert.Equal("GHE_TOKEN", cfg.Repos[0].TokenEnv)
	assert.Equal("github", cfg.Repos[1].Platform)
	assert.Empty(cfg.Repos[1].PlatformHost)
	assert.Equal("github.com", cfg.Repos[1].PlatformHostOrDefault())
	assert.Empty(cfg.Repos[1].TokenEnv)
}

func TestLoadPlatformConfigGitLabToken(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
[[platforms]]
type = "gitlab"
host = "gitlab.com"
token_env = "MIDDLEMAN_GITLAB_TOKEN"

[[repos]]
platform = "gitlab"
platform_host = "gitlab.com"
owner = "acme"
name = "widgets"
`)
	t.Setenv("MIDDLEMAN_GITLAB_TOKEN", "gitlab-secret")

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Platforms, 1)
	require.Len(t, cfg.Repos, 1)
	assert.Equal("gitlab", cfg.Platforms[0].Type)
	assert.Equal("gitlab.com", cfg.Platforms[0].Host)
	assert.Equal("MIDDLEMAN_GITLAB_TOKEN", cfg.Platforms[0].TokenEnv)
	assert.Equal("gitlab", cfg.Repos[0].Platform)
	assert.Equal("gitlab.com", cfg.Repos[0].PlatformHost)
	assert.Equal(
		"gitlab-secret",
		cfg.TokenForPlatformHost("gitlab", "gitlab.com", ""),
	)
}

func TestLoadPlatformConfigForgejoToken(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
[[platforms]]
type = "forgejo"
host = "codeberg.org"
token_env = "MIDDLEMAN_FORGEJO_TOKEN"

[[repos]]
platform = "forgejo"
platform_host = "codeberg.org"
owner = "forgejo"
name = "forgejo"
`)
	t.Setenv("MIDDLEMAN_FORGEJO_TOKEN", "forgejo-secret")

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Platforms, 1)
	require.Len(t, cfg.Repos, 1)
	assert.Equal("forgejo", cfg.Platforms[0].Type)
	assert.Equal("codeberg.org", cfg.Platforms[0].Host)
	assert.Equal("MIDDLEMAN_FORGEJO_TOKEN", cfg.Platforms[0].TokenEnv)
	assert.Equal("forgejo", cfg.Repos[0].Platform)
	assert.Equal("codeberg.org", cfg.Repos[0].PlatformHost)
	assert.Equal("forgejo-secret", cfg.TokenForPlatformHost("forgejo", "codeberg.org", ""))
}

func TestLoadForgejoDefaultHostUsesDefaultTokenEnv(t *testing.T) {
	path := writeConfig(t, `
[[repos]]
platform = "forgejo"
platform_host = "codeberg.org"
owner = "forgejo"
name = "forgejo"
`)
	t.Setenv("MIDDLEMAN_FORGEJO_TOKEN", "codeberg-secret")

	cfg, err := Load(path)
	require.NoError(t, err)
	Assert.Equal(t, "codeberg-secret", cfg.TokenForPlatformHost("forgejo", "codeberg.org", ""))
	Assert.Empty(t, cfg.TokenForPlatformHost("forgejo", "forgejo.example.com", ""))
}

func TestLoadPlatformConfigForgejoTokensAreHostScoped(t *testing.T) {
	path := writeConfig(t, `
[[platforms]]
type = "forgejo"
host = "codeberg.org"
token_env = "MIDDLEMAN_FORGEJO_TOKEN"

[[platforms]]
type = "forgejo"
host = "forgejo.example.com"
token_env = "FORGEJO_EXAMPLE_TOKEN"

[[repos]]
platform = "forgejo"
platform_host = "codeberg.org"
owner = "forgejo"
name = "forgejo"

[[repos]]
platform = "forgejo"
platform_host = "forgejo.example.com"
owner = "team"
name = "service"
`)
	t.Setenv("MIDDLEMAN_FORGEJO_TOKEN", "codeberg-secret")
	t.Setenv("FORGEJO_EXAMPLE_TOKEN", "self-hosted-secret")

	cfg, err := Load(path)
	require.NoError(t, err)
	Assert.Equal(t, "codeberg-secret", cfg.TokenForPlatformHost("forgejo", "codeberg.org", ""))
	Assert.Equal(t, "self-hosted-secret", cfg.TokenForPlatformHost("forgejo", "forgejo.example.com", ""))
}

func TestLoadParsesForgejoCodebergURL(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
[[repos]]
name = "https://codeberg.org/forgejo/forgejo.git"
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Repos, 1)
	assert.Equal("forgejo", cfg.Repos[0].Platform)
	assert.Equal("codeberg.org", cfg.Repos[0].PlatformHost)
	assert.Equal("forgejo", cfg.Repos[0].Owner)
	assert.Equal("forgejo", cfg.Repos[0].Name)
	assert.Equal("forgejo/forgejo", cfg.Repos[0].RepoPath)
}

func TestLoadPlatformConfigGiteaToken(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
[[platforms]]
type = "gitea"
host = "gitea.com"
token_env = "MIDDLEMAN_GITEA_TOKEN"

[[repos]]
platform = "gitea"
platform_host = "gitea.com"
owner = "gitea"
name = "tea"
`)
	t.Setenv("MIDDLEMAN_GITEA_TOKEN", "gitea-secret")

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Platforms, 1)
	require.Len(t, cfg.Repos, 1)
	assert.Equal("gitea", cfg.Platforms[0].Type)
	assert.Equal("gitea.com", cfg.Platforms[0].Host)
	assert.Equal("MIDDLEMAN_GITEA_TOKEN", cfg.Platforms[0].TokenEnv)
	assert.Equal("gitea", cfg.Repos[0].Platform)
	assert.Equal("gitea.com", cfg.Repos[0].PlatformHost)
	assert.Equal("gitea-secret", cfg.TokenForPlatformHost("gitea", "gitea.com", ""))
}

func TestLoadGiteaDefaultHostUsesDefaultTokenEnv(t *testing.T) {
	path := writeConfig(t, `
[[repos]]
platform = "gitea"
platform_host = "gitea.com"
owner = "gitea"
name = "tea"
`)
	t.Setenv("MIDDLEMAN_GITEA_TOKEN", "gitea-public-secret")

	cfg, err := Load(path)
	require.NoError(t, err)
	Assert.Equal(t, "gitea-public-secret", cfg.TokenForPlatformHost("gitea", "gitea.com", ""))
	Assert.Empty(t, cfg.TokenForPlatformHost("gitea", "gitea.internal.example", ""))
}

func TestLoadPlatformConfigGiteaTokensAreHostScoped(t *testing.T) {
	path := writeConfig(t, `
[[platforms]]
type = "gitea"
host = "gitea.com"
token_env = "MIDDLEMAN_GITEA_TOKEN"

[[platforms]]
type = "gitea"
host = "gitea.internal.example"
token_env = "GITEA_INTERNAL_TOKEN"

[[repos]]
platform = "gitea"
platform_host = "gitea.com"
owner = "gitea"
name = "tea"

[[repos]]
platform = "gitea"
platform_host = "gitea.internal.example"
owner = "team"
name = "service"
`)
	t.Setenv("MIDDLEMAN_GITEA_TOKEN", "gitea-public-secret")
	t.Setenv("GITEA_INTERNAL_TOKEN", "gitea-internal-secret")

	cfg, err := Load(path)
	require.NoError(t, err)
	Assert.Equal(t, "gitea-public-secret", cfg.TokenForPlatformHost("gitea", "gitea.com", ""))
	Assert.Equal(t, "gitea-internal-secret", cfg.TokenForPlatformHost("gitea", "gitea.internal.example", ""))
}

func TestLoadParsesGiteaURL(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
[[repos]]
name = "https://gitea.com/gitea/tea.git"
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Repos, 1)
	assert.Equal("gitea", cfg.Repos[0].Platform)
	assert.Equal("gitea.com", cfg.Repos[0].PlatformHost)
	assert.Equal("gitea", cfg.Repos[0].Owner)
	assert.Equal("tea", cfg.Repos[0].Name)
	assert.Equal("gitea/tea", cfg.Repos[0].RepoPath)
}

func TestLoadKeepsExistingGitHubURLInference(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
[[repos]]
name = "https://github.com/wesm/middleman.git"
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Repos, 1)
	assert.Equal("github", cfg.Repos[0].Platform)
	assert.Equal("github.com", cfg.Repos[0].PlatformHost)
	assert.Equal("wesm", cfg.Repos[0].Owner)
	assert.Equal("middleman", cfg.Repos[0].Name)
	assert.Equal("wesm/middleman", cfg.Repos[0].RepoPath)
}

func TestLoadKeepsExistingGitLabURLInference(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
[[repos]]
platform = "gitlab"
name = "https://gitlab.com/gitlab-org/gitlab.git"
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Repos, 1)
	assert.Equal("gitlab", cfg.Repos[0].Platform)
	assert.Equal("gitlab.com", cfg.Repos[0].PlatformHost)
	assert.Equal("gitlab-org", cfg.Repos[0].Owner)
	assert.Equal("gitlab", cfg.Repos[0].Name)
	assert.Equal("gitlab-org/gitlab", cfg.Repos[0].RepoPath)
}

func TestLoadRejectsDuplicatePlatformConfig(t *testing.T) {
	path := writeConfig(t, `
[[platforms]]
type = "gitlab"
host = "https://gitlab.example.com/"
token_env = "GITLAB_TOKEN"

[[platforms]]
type = "gitlab"
host = "gitlab.example.com"
token_env = "GITLAB_TOKEN"
`)

	_, err := Load(path)
	require.Error(t, err)
	Assert.Contains(t, err.Error(), `duplicate platform "gitlab/gitlab.example.com"`)
}

func TestLoadRejectsConflictingPlatformTokenEnv(t *testing.T) {
	path := writeConfig(t, `
[[platforms]]
type = "gitlab"
host = "gitlab.example.com"
token_env = "GITLAB_TOKEN_A"

[[platforms]]
type = "gitlab"
host = "https://gitlab.example.com/"
token_env = "GITLAB_TOKEN_B"
`)

	_, err := Load(path)
	require.Error(t, err)
	Assert.Contains(t, err.Error(), "conflicting token_env")
}

func TestLoadGitLabNestedNamespaceURL(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
[[repos]]
platform = "gitlab"
owner = "ignored"
name = "https://gitlab.com/My-Group/SubGroup/My-Project.git"
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Repos, 1)
	assert.Equal("gitlab", cfg.Repos[0].Platform)
	assert.Equal("gitlab.com", cfg.Repos[0].PlatformHost)
	assert.Equal("My-Group/SubGroup", cfg.Repos[0].Owner)
	assert.Equal("My-Project", cfg.Repos[0].Name)
}

func TestLoadGitLabMergeRequestURL(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
[[repos]]
platform = "gitlab"
owner = "ignored"
name = "https://gitlab.com/group/project/-/merge_requests/1"
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Repos, 1)
	assert.Equal("gitlab", cfg.Repos[0].Platform)
	assert.Equal("gitlab.com", cfg.Repos[0].PlatformHost)
	assert.Equal("group", cfg.Repos[0].Owner)
	assert.Equal("project", cfg.Repos[0].Name)
}

func TestLoadPreservesExplicitGitLabOwnerNameCase(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
[[repos]]
platform = "gitlab"
platform_host = "gitlab.com"
owner = "My-Group/SubGroup"
name = "My-Project"
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Repos, 1)
	assert.Equal("gitlab", cfg.Repos[0].Platform)
	assert.Equal("My-Group/SubGroup", cfg.Repos[0].Owner)
	assert.Equal("My-Project", cfg.Repos[0].Name)
}

func TestLoadNormalizesSelfHostedGitLabPlatformHost(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
[[platforms]]
type = "gitlab"
host = "https://gitlab.example.com/"
token_env = "GITLAB_TOKEN"

[[repos]]
platform = "gitlab"
platform_host = "https://gitlab.example.com/"
owner = "acme"
name = "widgets"
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal("gitlab.example.com", cfg.Platforms[0].Host)
	assert.Equal("gitlab.example.com", cfg.Repos[0].PlatformHost)
}

func TestLoadPreservesSelfHostedGitLabHostPort(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
[[platforms]]
type = "gitlab"
host = "https://gitlab.example.com:8443/"
token_env = "GITLAB_TOKEN"

[[repos]]
platform = "gitlab"
platform_host = "gitlab.example.com:8443"
owner = "acme"
name = "widgets"
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal("gitlab.example.com:8443", cfg.Platforms[0].Host)
	assert.Equal("gitlab.example.com:8443", cfg.Repos[0].PlatformHost)
}

func TestLoadRejectsGitLabSubpathPlatformHost(t *testing.T) {
	path := writeConfig(t, `
[[repos]]
platform = "gitlab"
platform_host = "https://example.com/gitlab"
owner = "acme"
name = "widgets"
`)

	_, err := Load(path)
	require.Error(t, err)
	Assert.Contains(t, err.Error(), "invalid_repo_ref")
}

func TestLoadRejectsUnsafePlatformHosts(t *testing.T) {
	tests := []struct {
		name string
		host string
	}{
		{"url userinfo", "https://gitlab.com@attacker.example/"},
		{"bare userinfo", "gitlab.com@attacker.example"},
		{"malformed port", "gitlab.example.com:bad"},
		{"control character", "gitlab.example.com\nattacker.example"},
		{"whitespace", "gitlab.example.com attacker.example"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfig(t, fmt.Sprintf(`
[[repos]]
platform = "gitlab"
platform_host = %q
owner = "acme"
name = "widgets"
`, tt.host))

			_, err := Load(path)
			require.Error(t, err)
			Assert.Contains(t, err.Error(), "invalid_repo_ref")
		})
	}
}

func TestLoadRejectsAmbiguousGitLabURL(t *testing.T) {
	path := writeConfig(t, `
[[repos]]
platform = "gitlab"
owner = "ignored"
name = "https://gitlab.com/acme"
`)

	_, err := Load(path)
	require.Error(t, err)
	Assert.Contains(t, err.Error(), "incomplete GitLab reference")
}

func TestRepoPlatformHostOrDefault(t *testing.T) {
	tests := []struct {
		name string
		host string
		want string
	}{
		{"empty defaults to github.com", "", "github.com"},
		{"explicit host preserved", "github.example.com", "github.example.com"},
		{"github.com explicit", "github.com", "github.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Repo{
				Owner:        "a",
				Name:         "b",
				PlatformHost: tt.host,
			}
			Assert.Equal(t, tt.want, r.PlatformHostOrDefault())
		})
	}
}

func TestRepoResolveToken(t *testing.T) {
	t.Run("token_env set and populated", func(t *testing.T) {
		t.Setenv("MY_GHE_TOKEN", "ghe-secret")
		r := Repo{Owner: "a", Name: "b", TokenEnv: "MY_GHE_TOKEN"}
		Assert.Equal(t, "ghe-secret", r.ResolveToken("global-token"))
	})

	t.Run("token_env set but empty falls back to global", func(t *testing.T) {
		t.Setenv("MY_GHE_TOKEN", "")
		r := Repo{Owner: "a", Name: "b", TokenEnv: "MY_GHE_TOKEN"}
		Assert.Equal(t, "global-token", r.ResolveToken("global-token"))
	})

	t.Run("token_env not set falls back to global", func(t *testing.T) {
		r := Repo{Owner: "a", Name: "b"}
		Assert.Equal(t, "global-token", r.ResolveToken("global-token"))
	})
}

func TestConfigResolveRepoTokenUsesPlatformToken(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
github_token_env = "GH_TOKEN"

[[platforms]]
type = "gitlab"
host = "gitlab.com"
token_env = "GITLAB_TOKEN"

[[repos]]
owner = "acme"
name = "widgets"

[[repos]]
platform = "gitlab"
platform_host = "gitlab.com"
owner = "group"
name = "project"
`)
	t.Setenv("GH_TOKEN", "github-secret")
	t.Setenv("GITLAB_TOKEN", "gitlab-secret")

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Repos, 2)
	assert.Equal("github-secret", cfg.ResolveRepoToken(cfg.Repos[0]))
	assert.Equal("gitlab-secret", cfg.ResolveRepoToken(cfg.Repos[1]))
}

func TestConfigResolveRepoTokenPrefersRepoTokenEnv(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
[[platforms]]
type = "gitlab"
host = "gitlab.com"
token_env = "GITLAB_TOKEN"

[[repos]]
platform = "gitlab"
platform_host = "gitlab.com"
owner = "group"
name = "project"
token_env = "REPO_GITLAB_TOKEN"
`)
	t.Setenv("GITLAB_TOKEN", "platform-secret")
	t.Setenv("REPO_GITLAB_TOKEN", "repo-secret")

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Repos, 1)
	assert.Equal("repo-secret", cfg.ResolveRepoToken(cfg.Repos[0]))
}

func TestValidateRejectsDuplicateOwnerName(t *testing.T) {
	path := writeConfig(t, `
[[repos]]
owner = "apache"
name = "arrow"

[[repos]]
owner = "apache"
name = "arrow"
`)
	_, err := Load(path)
	require.Error(t, err)
	Assert.Contains(t, err.Error(), "duplicate repo")
}

func TestValidateAllowsSameOwnerNameAcrossPlatformHosts(t *testing.T) {
	path := writeConfig(t, `
[[repos]]
owner = "apache"
name = "arrow"

[[repos]]
platform = "github"
platform_host = "github.example.com"
owner = "apache"
name = "arrow"

[[repos]]
platform = "gitlab"
platform_host = "gitlab.com"
owner = "apache"
name = "arrow"
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Repos, 3)
}

func TestValidateRejectsDuplicateRepoWithinSamePlatformHost(t *testing.T) {
	path := writeConfig(t, `
[[repos]]
platform = "gitlab"
platform_host = "gitlab.com"
owner = "Apache"
name = "Arrow"

[[repos]]
platform = "gitlab"
platform_host = "gitlab.com"
owner = "Apache"
name = "Arrow"
`)

	_, err := Load(path)
	require.Error(t, err)
	Assert.Contains(t, err.Error(), `duplicate repo "gitlab/gitlab.com/Apache/Arrow"`)
}

func TestValidateRejectsGitLabDuplicateRepoByCaseWithinSamePlatformHost(t *testing.T) {
	path := writeConfig(t, `
[[repos]]
platform = "gitlab"
platform_host = "gitlab.com"
owner = "Apache"
name = "Arrow"

[[repos]]
platform = "gitlab"
platform_host = "gitlab.com"
owner = "apache"
name = "arrow"
`)

	_, err := Load(path)
	require.Error(t, err)
	Assert.Contains(t, err.Error(), `duplicate repo "gitlab/gitlab.com/Apache/Arrow"`)
}

func TestLoadGitLabSSHURIWithPortDoesNotUseSSHPortAsPlatformHost(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
[[repos]]
platform = "gitlab"
owner = "ignored"
name = "ssh://git@gitlab.example.com:2222/group/project.git"
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Repos, 1)
	assert.Equal("gitlab", cfg.Repos[0].Platform)
	assert.Equal("gitlab.example.com", cfg.Repos[0].PlatformHost)
	assert.Equal("group", cfg.Repos[0].Owner)
	assert.Equal("project", cfg.Repos[0].Name)
}

func TestLoadGitLabSSHURIWithPortKeepsExplicitPlatformHost(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
[[platforms]]
type = "gitlab"
host = "gitlab.example.com:8443"
token_env = "GITLAB_TOKEN"

[[repos]]
platform = "gitlab"
platform_host = "gitlab.example.com:8443"
owner = "ignored"
name = "ssh://git@gitlab.example.com:2222/group/project.git"
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Repos, 1)
	assert.Equal("gitlab", cfg.Repos[0].Platform)
	assert.Equal("gitlab.example.com:8443", cfg.Repos[0].PlatformHost)
	assert.Equal("group", cfg.Repos[0].Owner)
	assert.Equal("project", cfg.Repos[0].Name)
}

func TestValidateRejectsConflictingTokenEnv(t *testing.T) {
	path := writeConfig(t, `
[[repos]]
owner = "org1"
name = "repo1"
platform_host = "github.example.com"
token_env = "GHE_TOKEN_A"

[[repos]]
owner = "org2"
name = "repo2"
platform_host = "github.example.com"
token_env = "GHE_TOKEN_B"
`)
	_, err := Load(path)
	require.Error(t, err)
	Assert.Contains(t, err.Error(), "conflicting token_env")
}

func TestValidateScopesTokenEnvConflictsByPlatformHost(t *testing.T) {
	path := writeConfig(t, `
[[repos]]
platform = "github"
platform_host = "example.com"
owner = "org1"
name = "repo1"
token_env = "GITHUB_TOKEN"

[[repos]]
platform = "gitlab"
platform_host = "example.com"
owner = "org2"
name = "repo2"
token_env = "GITLAB_TOKEN"

[[repos]]
platform = "gitlab"
platform_host = "gitlab.example.com"
owner = "org3"
name = "repo3"
token_env = "OTHER_GITLAB_TOKEN"
`)

	_, err := Load(path)
	require.NoError(t, err)
}

func TestSaveRoundTripPlatformHost(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	path := writeConfig(t, `
[[repos]]
owner = "apache"
name = "arrow"
platform_host = "github.example.com"
token_env = "GHE_TOKEN"

[[repos]]
owner = "ibis-project"
name = "ibis"
`)
	cfg, err := Load(path)
	require.NoError(err)

	savePath := filepath.Join(t.TempDir(), "saved.toml")
	require.NoError(cfg.Save(savePath))

	cfg2, err := Load(savePath)
	require.NoError(err)
	require.Len(cfg2.Repos, 2)
	assert.Equal("github.example.com", cfg2.Repos[0].PlatformHost)
	assert.Equal("GHE_TOKEN", cfg2.Repos[0].TokenEnv)
	assert.Empty(cfg2.Repos[1].PlatformHost)
	assert.Empty(cfg2.Repos[1].TokenEnv)
}

func TestSaveRoundTripEmptyGitHubTokenEnv(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
github_token_env = ""

[[repos]]
owner = "a"
name = "b"
`)
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Empty(cfg.GitHubTokenEnv)

	savePath := filepath.Join(t.TempDir(), "saved.toml")
	require.NoError(t, cfg.Save(savePath))

	cfg2, err := Load(savePath)
	require.NoError(t, err)
	assert.Empty(cfg2.GitHubTokenEnv)
}

func TestRoborevConfigRoundTrip(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
[[repos]]
owner = "a"
name = "b"

[roborev]
endpoint = "http://custom:9999"
`)
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal("http://custom:9999", cfg.RoborevEndpoint())

	savePath := filepath.Join(t.TempDir(), "saved.toml")
	require.NoError(t, cfg.Save(savePath))

	cfg2, err := Load(savePath)
	require.NoError(t, err)
	assert.Equal("http://custom:9999", cfg2.RoborevEndpoint())
}

func TestTerminalConfigRoundTrip(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	path := writeConfig(t, `
[[repos]]
owner = "a"
name = "b"

[terminal]
font_family = '  "Iosevka Term", monospace  '
font_size = 16
scrollback = 5000
line_height = 1.2
letter_spacing = 1
cursor_blink = false
font_ligatures = true
renderer = "ghostty-web"
`)
	cfg, err := Load(path)
	require.NoError(err)
	assert.Equal("\"Iosevka Term\", monospace", cfg.Terminal.FontFamily)
	assert.Equal(16, cfg.Terminal.FontSize)
	assert.Equal(5000, cfg.Terminal.Scrollback)
	assert.InDelta(1.2, cfg.Terminal.LineHeight, 0.001)
	assert.Equal(1, cfg.Terminal.LetterSpacing)
	require.NotNil(cfg.Terminal.CursorBlink)
	assert.False(*cfg.Terminal.CursorBlink)
	assert.True(cfg.Terminal.FontLigatures)
	assert.Equal("ghostty-web", cfg.Terminal.Renderer)

	savePath := filepath.Join(t.TempDir(), "saved.toml")
	require.NoError(cfg.Save(savePath))

	cfg2, err := Load(savePath)
	require.NoError(err)
	assert.Equal("\"Iosevka Term\", monospace", cfg2.Terminal.FontFamily)
	assert.Equal(16, cfg2.Terminal.FontSize)
	assert.Equal(5000, cfg2.Terminal.Scrollback)
	assert.InDelta(1.2, cfg2.Terminal.LineHeight, 0.001)
	assert.Equal(1, cfg2.Terminal.LetterSpacing)
	require.NotNil(cfg2.Terminal.CursorBlink)
	assert.False(*cfg2.Terminal.CursorBlink)
	assert.True(cfg2.Terminal.FontLigatures)
	assert.Equal("ghostty-web", cfg2.Terminal.Renderer)
}

func TestTerminalRendererDefaultsToXterm(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	path := writeConfig(t, `
[[repos]]
owner = "a"
name = "b"
`)
	cfg, err := Load(path)
	require.NoError(err)

	assert.Equal("xterm", cfg.Terminal.Renderer)
	assert.Equal(14, cfg.Terminal.FontSize)
	assert.Equal(1000, cfg.Terminal.Scrollback)
	assert.InDelta(1.0, cfg.Terminal.LineHeight, 0.001)
	assert.Equal(0, cfg.Terminal.LetterSpacing)
	require.NotNil(cfg.Terminal.CursorBlink)
	assert.True(*cfg.Terminal.CursorBlink)
	assert.False(cfg.Terminal.FontLigatures)
}

func TestIssueWorkspaceBranchStyleDefaultsToSlug(t *testing.T) {
	path := writeConfig(t, `
[[repos]]
owner = "a"
name = "b"
`)
	cfg, err := Load(path)
	require.NoError(t, err)

	Assert.Equal(t, IssueWorkspaceBranchStyleSlug, cfg.IssueWorkspaceBranchStyle)
	Assert.True(t, cfg.IssueWorkspaceBranchSlugEnabled())
}

func TestIssueWorkspaceBranchStyleAcceptsBare(t *testing.T) {
	path := writeConfig(t, `
issue_workspace_branch_style = "bare"

[[repos]]
owner = "a"
name = "b"
`)
	cfg, err := Load(path)
	require.NoError(t, err)

	Assert.Equal(t, IssueWorkspaceBranchStyleBare, cfg.IssueWorkspaceBranchStyle)
	Assert.False(t, cfg.IssueWorkspaceBranchSlugEnabled())
}

func TestIssueWorkspaceBranchStyleRejectsInvalidValue(t *testing.T) {
	path := writeConfig(t, `
issue_workspace_branch_style = "fancy"

[[repos]]
owner = "a"
name = "b"
`)
	_, err := Load(path)
	require.Error(t, err)
	Assert.Contains(t, err.Error(), "invalid issue_workspace_branch_style")
}

func TestIssueWorkspaceBranchStyleRoundTrip(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
issue_workspace_branch_style = "bare"

[[repos]]
owner = "a"
name = "b"
`)
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(IssueWorkspaceBranchStyleBare, cfg.IssueWorkspaceBranchStyle)

	savePath := filepath.Join(t.TempDir(), "saved.toml")
	require.NoError(t, cfg.Save(savePath))

	cfg2, err := Load(savePath)
	require.NoError(t, err)
	assert.Equal(IssueWorkspaceBranchStyleBare, cfg2.IssueWorkspaceBranchStyle)
	assert.False(cfg2.IssueWorkspaceBranchSlugEnabled())
}

func TestIssueWorkspaceBranchStyleSlugIsOmittedFromSavedConfig(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
issue_workspace_branch_style = "slug"

[[repos]]
owner = "a"
name = "b"
`)
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(IssueWorkspaceBranchStyleSlug, cfg.IssueWorkspaceBranchStyle)

	savePath := filepath.Join(t.TempDir(), "saved.toml")
	require.NoError(t, cfg.Save(savePath))

	data, err := os.ReadFile(savePath)
	require.NoError(t, err)
	// The default style should not be written back to disk; the
	// field is treated as opt-out only.
	assert.NotContains(string(data), "issue_workspace_branch_style")
}

func TestTerminalRendererRejectsInvalidValue(t *testing.T) {
	path := writeConfig(t, `
[[repos]]
owner = "a"
name = "b"

[terminal]
renderer = "vt100"
`)
	_, err := Load(path)
	require.Error(t, err)
	Assert.Contains(t, err.Error(), "invalid terminal.renderer")
}

func TestSyncBudgetPerHour(t *testing.T) {
	t.Run("default is 500 when not set", func(t *testing.T) {
		path := writeConfig(t, `
[[repos]]
owner = "a"
name = "b"
`)
		cfg, err := Load(path)
		require.NoError(t, err)
		Assert.Equal(t, 500, cfg.BudgetPerHour())
	})

	t.Run("rejects value below 50", func(t *testing.T) {
		path := writeConfig(t, `
sync_budget_per_hour = 49
[[repos]]
owner = "a"
name = "b"
`)
		_, err := Load(path)
		require.Error(t, err)
		Assert.Contains(t, err.Error(), "sync_budget_per_hour must be >= 50 or omitted")
	})

	t.Run("configured value preserved", func(t *testing.T) {
		path := writeConfig(t, `
sync_budget_per_hour = 1000
[[repos]]
owner = "a"
name = "b"
`)
		cfg, err := Load(path)
		require.NoError(t, err)
		Assert.Equal(t, 1000, cfg.BudgetPerHour())
	})

	t.Run("round-trips through Save", func(t *testing.T) {
		require := require.New(t)
		path := writeConfig(t, `
sync_budget_per_hour = 750
[[repos]]
owner = "a"
name = "b"
`)
		cfg, err := Load(path)
		require.NoError(err)

		savePath := filepath.Join(t.TempDir(), "saved.toml")
		require.NoError(cfg.Save(savePath))

		cfg2, err := Load(savePath)
		require.NoError(err)
		Assert.Equal(t, 750, cfg2.BudgetPerHour())
	})
}

func TestSSEBufferSize(t *testing.T) {
	t.Run("default is 256 when unset", func(t *testing.T) {
		path := writeConfig(t, `
[[repos]]
owner = "a"
name = "b"
`)
		cfg, err := Load(path)
		require.NoError(t, err)
		Assert.Equal(t, 256, cfg.SSEBufferSize)
		Assert.Equal(t, 256, cfg.SSEBufferSizeOrDefault())
	})

	t.Run("nil receiver returns default", func(t *testing.T) {
		var cfg *Config
		Assert.Equal(t, 256, cfg.SSEBufferSizeOrDefault())
	})

	t.Run("rejects below minimum", func(t *testing.T) {
		path := writeConfig(t, `
sse_buffer_size = 8
[[repos]]
owner = "a"
name = "b"
`)
		_, err := Load(path)
		require.Error(t, err)
		Assert.Contains(t, err.Error(), "sse_buffer_size must be between 16 and 16384")
	})

	t.Run("rejects above maximum", func(t *testing.T) {
		path := writeConfig(t, `
sse_buffer_size = 20000
[[repos]]
owner = "a"
name = "b"
`)
		_, err := Load(path)
		require.Error(t, err)
		Assert.Contains(t, err.Error(), "sse_buffer_size must be between 16 and 16384")
	})

	t.Run("accepts valid value in range", func(t *testing.T) {
		path := writeConfig(t, `
sse_buffer_size = 1024
[[repos]]
owner = "a"
name = "b"
`)
		cfg, err := Load(path)
		require.NoError(t, err)
		Assert.Equal(t, 1024, cfg.SSEBufferSize)
		Assert.Equal(t, 1024, cfg.SSEBufferSizeOrDefault())
	})

	t.Run("accepts boundary minimum 16", func(t *testing.T) {
		path := writeConfig(t, `
sse_buffer_size = 16
[[repos]]
owner = "a"
name = "b"
`)
		cfg, err := Load(path)
		require.NoError(t, err)
		Assert.Equal(t, 16, cfg.SSEBufferSize)
	})

	t.Run("accepts boundary maximum 16384", func(t *testing.T) {
		path := writeConfig(t, `
sse_buffer_size = 16384
[[repos]]
owner = "a"
name = "b"
`)
		cfg, err := Load(path)
		require.NoError(t, err)
		Assert.Equal(t, 16384, cfg.SSEBufferSize)
	})

	t.Run("round-trips through Save", func(t *testing.T) {
		require := require.New(t)
		path := writeConfig(t, `
sse_buffer_size = 1024
[[repos]]
owner = "a"
name = "b"
`)
		cfg, err := Load(path)
		require.NoError(err)

		savePath := filepath.Join(t.TempDir(), "saved.toml")
		require.NoError(cfg.Save(savePath))

		cfg2, err := Load(savePath)
		require.NoError(err)
		Assert.Equal(t, 1024, cfg2.SSEBufferSize)
	})

	t.Run("default value is omitted from Save output", func(t *testing.T) {
		require := require.New(t)
		path := writeConfig(t, `
[[repos]]
owner = "a"
name = "b"
`)
		cfg, err := Load(path)
		require.NoError(err)

		savePath := filepath.Join(t.TempDir(), "saved.toml")
		require.NoError(cfg.Save(savePath))

		// Reload should still produce the default.
		cfg2, err := Load(savePath)
		require.NoError(err)
		Assert.Equal(t, 256, cfg2.SSEBufferSize)
	})
}

func TestRoborevEndpointDefault(t *testing.T) {
	cfg := &Config{}
	Assert.Equal(
		t, "http://127.0.0.1:7373", cfg.RoborevEndpoint(),
	)
}

func TestLoadTmuxCommand(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
[tmux]
command = ["systemd-run", "--user", "--scope", "tmux"]
`)
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(
		[]string{"systemd-run", "--user", "--scope", "tmux"},
		cfg.Tmux.Command,
	)
}

func TestLoadTmuxCommandOmitted(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, ``)
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Empty(cfg.Tmux.Command)
	assert.Equal([]string{"tmux"}, cfg.TmuxCommand())
	assert.True(cfg.TmuxAgentSessionsEnabled())
}

func TestLoadTmuxCommandEmptyArray(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
[tmux]
command = []
`)
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal([]string{"tmux"}, cfg.TmuxCommand())
}

func TestLoadTmuxAgentSessionsDisabled(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
[tmux]
agent_sessions = false
`)
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.False(cfg.TmuxAgentSessionsEnabled())
}

func TestSavePreservesTmuxAgentSessionsDisabled(t *testing.T) {
	assert := Assert.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	disabled := false

	cfg := &Config{
		SyncInterval:   "5m",
		GitHubTokenEnv: "MIDDLEMAN_GITHUB_TOKEN",
		Host:           "127.0.0.1",
		Port:           8091,
		DataDir:        dir,
		Activity:       Activity{ViewMode: "threaded", TimeRange: "7d"},
		Tmux: Tmux{
			AgentSessions: &disabled,
		},
	}
	require.NoError(t, cfg.Save(path))

	reloaded, err := Load(path)
	require.NoError(t, err)
	assert.False(reloaded.TmuxAgentSessionsEnabled())
}

func TestTmuxCommandDefensiveCopy(t *testing.T) {
	assert := Assert.New(t)
	cfg := &Config{Tmux: Tmux{
		Command: []string{"tmux"},
	}}
	first := cfg.TmuxCommand()
	first[0] = "hacked"
	second := cfg.TmuxCommand()
	assert.Equal([]string{"tmux"}, second)
}

func TestTmuxCommandNilReceiver(t *testing.T) {
	assert := Assert.New(t)
	var cfg *Config
	assert.Equal([]string{"tmux"}, cfg.TmuxCommand())
}

func TestLoadTmuxCommandRejectsEmptyFirstElement(t *testing.T) {
	path := writeConfig(t, `
[tmux]
command = ["", "extra"]
`)
	_, err := Load(path)
	require.Error(t, err)
	require.Contains(
		t, err.Error(),
		`config: invalid tmux.command`,
	)
}

// TestLoadTmuxCommandRejectsWhitespaceFirstElement covers the
// whitespace-only case: "   " would sneak past a plain == "" check
// and exec("   ") fails with a confusing shell-level error rather
// than the config-load validation message operators actually want.
func TestLoadTmuxCommandRejectsWhitespaceFirstElement(t *testing.T) {
	path := writeConfig(t, `
[tmux]
command = ["   ", "extra"]
`)
	_, err := Load(path)
	require.Error(t, err)
	require.Contains(
		t, err.Error(),
		`config: invalid tmux.command`,
	)
}

func TestLoadShellCommand(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
[shell]
command = ["systemd-run", "--user", "--scope", "zsh"]
`)
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(
		[]string{"systemd-run", "--user", "--scope", "zsh"},
		cfg.Shell.Command,
	)
	assert.Equal(
		[]string{"systemd-run", "--user", "--scope", "zsh"},
		cfg.ShellCommand(),
	)
}

func TestLoadShellCommandOmitted(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, ``)
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Empty(cfg.Shell.Command)
	// Unset returns nil, signalling the runtime to fall back to $SHELL.
	assert.Nil(cfg.ShellCommand())
}

func TestLoadShellCommandEmptyArray(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
[shell]
command = []
`)
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Nil(cfg.ShellCommand())
}

func TestShellCommandDefensiveCopy(t *testing.T) {
	assert := Assert.New(t)
	cfg := &Config{Shell: Shell{Command: []string{"zsh"}}}
	first := cfg.ShellCommand()
	first[0] = "hacked"
	second := cfg.ShellCommand()
	assert.Equal([]string{"zsh"}, second)
}

func TestShellCommandNilReceiver(t *testing.T) {
	assert := Assert.New(t)
	var cfg *Config
	assert.Nil(cfg.ShellCommand())
}

func TestLoadShellCommandRejectsEmptyFirstElement(t *testing.T) {
	path := writeConfig(t, `
[shell]
command = ["", "zsh"]
`)
	_, err := Load(path)
	require.Error(t, err)
	require.Contains(
		t, err.Error(),
		`config: invalid shell.command`,
	)
}

// Whitespace-only first element sneaks past a plain == "" check and
// exec("   ") fails with a confusing shell-level error rather than
// the config-load validation message operators actually want.
func TestLoadShellCommandRejectsWhitespaceFirstElement(t *testing.T) {
	path := writeConfig(t, `
[shell]
command = ["   ", "zsh"]
`)
	_, err := Load(path)
	require.Error(t, err)
	require.Contains(
		t, err.Error(),
		`config: invalid shell.command`,
	)
}

func TestSavePreservesShellCommand(t *testing.T) {
	assert := Assert.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	cfg := &Config{
		SyncInterval:   "5m",
		GitHubTokenEnv: "MIDDLEMAN_GITHUB_TOKEN",
		Host:           "127.0.0.1",
		Port:           8091,
		DataDir:        dir,
		Activity:       Activity{ViewMode: "threaded", TimeRange: "7d"},
		Shell: Shell{
			Command: []string{"systemd-run", "--user", "zsh"},
		},
	}
	require.NoError(t, cfg.Save(path))

	reloaded, err := Load(path)
	require.NoError(t, err)
	assert.Equal(
		[]string{"systemd-run", "--user", "zsh"},
		reloaded.Shell.Command,
	)
}

func TestSavePreservesTmuxCommand(t *testing.T) {
	assert := Assert.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	cfg := &Config{
		SyncInterval:   "5m",
		GitHubTokenEnv: "MIDDLEMAN_GITHUB_TOKEN",
		Host:           "127.0.0.1",
		Port:           8091,
		DataDir:        dir,
		Activity:       Activity{ViewMode: "threaded", TimeRange: "7d"},
		Tmux: Tmux{
			Command: []string{"systemd-run", "--user", "--scope", "tmux"},
		},
	}
	require.NoError(t, cfg.Save(path))

	reloaded, err := Load(path)
	require.NoError(t, err)
	assert.Equal(
		[]string{"systemd-run", "--user", "--scope", "tmux"},
		reloaded.Tmux.Command,
	)
}

func TestLoadAgents(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
[[agents]]
key = "codex"
label = "Codex"
command = ["codex", "--full-auto"]

[[agents]]
key = "claude"
label = "Claude"
command = ["claude"]
enabled = false
`)
	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Agents, 2)
	assert.Equal("codex", cfg.Agents[0].Key)
	assert.Equal("Codex", cfg.Agents[0].Label)
	assert.Equal(
		[]string{"codex", "--full-auto"},
		cfg.Agents[0].Command,
	)
	assert.True(cfg.Agents[0].EnabledOrDefault())
	assert.False(cfg.Agents[1].EnabledOrDefault())
}

func TestLoadAgentDefaultsLabelAndNormalizesKey(t *testing.T) {
	assert := Assert.New(t)
	path := writeConfig(t, `
[[agents]]
key = "  Codex  "
command = ["codex"]
`)
	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Agents, 1)
	assert.Equal("codex", cfg.Agents[0].Key)
	assert.Equal("codex", cfg.Agents[0].Label)
}

func TestLoadAgentRejectsMissingKey(t *testing.T) {
	path := writeConfig(t, `
[[agents]]
label = "Codex"
command = ["codex"]
`)
	_, err := Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "config: agents[0]: key")
}

func TestLoadAgentRejectsEnabledMissingCommand(t *testing.T) {
	path := writeConfig(t, `
[[agents]]
key = "codex"
`)
	_, err := Load(path)
	require.Error(t, err)
	require.Contains(
		t, err.Error(),
		"config: agents[0]: command",
	)
}

func TestLoadAgentAllowsDisabledMissingCommand(t *testing.T) {
	path := writeConfig(t, `
[[agents]]
key = "codex"
enabled = false
`)
	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Agents, 1)
	Assert.False(t, cfg.Agents[0].EnabledOrDefault())
}

func TestLoadAgentRejectsEmptyCommandFirstElement(t *testing.T) {
	path := writeConfig(t, `
[[agents]]
key = "codex"
command = ["   ", "extra"]
`)
	_, err := Load(path)
	require.Error(t, err)
	require.Contains(
		t, err.Error(),
		"config: agents[0]: command first element must be non-empty",
	)
}

func TestLoadAgentRejectsDuplicateKeys(t *testing.T) {
	path := writeConfig(t, `
[[agents]]
key = "codex"
command = ["codex"]

[[agents]]
key = " CODEX "
command = ["codex-custom"]
`)
	_, err := Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), `config: duplicate agent "codex"`)
}

func TestLoadAgentRejectsReservedSystemKeys(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{name: "tmux", key: "tmux"},
		{name: "plain shell", key: " plain_shell "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfig(t, fmt.Sprintf(`
[[agents]]
key = %q
command = ["codex"]
`, tt.key))

			_, err := Load(path)

			require.Error(t, err)
			require.Contains(
				t, err.Error(),
				"reserved system launch target",
			)
		})
	}
}

func TestSavePreservesAgents(t *testing.T) {
	assert := Assert.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	disabled := false

	cfg := &Config{
		SyncInterval:   "5m",
		GitHubTokenEnv: "MIDDLEMAN_GITHUB_TOKEN",
		Host:           "127.0.0.1",
		Port:           8091,
		DataDir:        dir,
		Activity:       Activity{ViewMode: "threaded", TimeRange: "7d"},
		Agents: []Agent{{
			Key:     "codex",
			Label:   "Codex",
			Command: []string{"codex", "--full-auto"},
		}, {
			Key:     "claude",
			Label:   "Claude",
			Enabled: &disabled,
		}},
	}
	require.NoError(t, cfg.Save(path))

	reloaded, err := Load(path)
	require.NoError(t, err)
	require.Len(t, reloaded.Agents, 2)
	assert.Equal("codex", reloaded.Agents[0].Key)
	assert.Equal(
		[]string{"codex", "--full-auto"},
		reloaded.Agents[0].Command,
	)
	assert.False(reloaded.Agents[1].EnabledOrDefault())
}

func TestTokenEnvNamesIncludesGlobalPlatformAndPerRepo(t *testing.T) {
	var nilCfg *Config
	require.Nil(t, nilCfg.TokenEnvNames())

	cfg := &Config{
		GitHubTokenEnv: "WORK_GH_BOT_TOKEN",
		Platforms: []PlatformConfig{
			{Type: "forgejo", Host: "codeberg.org", TokenEnv: "MIDDLEMAN_FORGEJO_TOKEN"},
			{Type: "forgejo", Host: "forgejo.example.com", TokenEnv: "FORGEJO_EXAMPLE_TOKEN"},
			{Type: "gitea", Host: "gitea.internal.example", TokenEnv: "GITEA_INTERNAL_TOKEN"},
		},
		Repos: []Repo{
			{Owner: "acme", Name: "widget", TokenEnv: "ACME_TOKEN"},
			{Owner: "other", Name: "thing"},
			{Owner: "third", Name: "x", TokenEnv: "THIRD_TOKEN"},
		},
	}
	Assert.Equal(
		t,
		[]string{
			"WORK_GH_BOT_TOKEN",
			"MIDDLEMAN_FORGEJO_TOKEN",
			"FORGEJO_EXAMPLE_TOKEN",
			"GITEA_INTERNAL_TOKEN",
			"ACME_TOKEN",
			"THIRD_TOKEN",
		},
		cfg.TokenEnvNames(),
	)
}

func TestTokenEnvNamesIncludesImplicitPublicForgeTokenEnvs(t *testing.T) {
	cfg := &Config{
		GitHubTokenEnv: "WORK_GH_BOT_TOKEN",
		Repos: []Repo{
			{
				Platform:     "forgejo",
				PlatformHost: "codeberg.org",
				Owner:        "forgejo",
				Name:         "forgejo",
			},
			{
				Platform:     "gitea",
				PlatformHost: "gitea.com",
				Owner:        "gitea",
				Name:         "tea",
			},
		},
	}

	Assert.Equal(
		t,
		[]string{
			"WORK_GH_BOT_TOKEN",
			"MIDDLEMAN_FORGEJO_TOKEN",
			"MIDDLEMAN_GITEA_TOKEN",
		},
		cfg.TokenEnvNames(),
	)
}

func TestTokenEnvNamesIncludesFallbackProviderDefaultsForRepoTokenEnv(t *testing.T) {
	cfg := &Config{
		GitHubTokenEnv: "WORK_GH_BOT_TOKEN",
		Repos: []Repo{
			{
				Platform:     "forgejo",
				PlatformHost: "codeberg.org",
				Owner:        "forgejo",
				Name:         "forgejo",
				TokenEnv:     "REPO_FORGEJO_TOKEN",
			},
			{
				Platform:     "gitea",
				PlatformHost: "gitea.com",
				Owner:        "gitea",
				Name:         "tea",
				TokenEnv:     "REPO_GITEA_TOKEN",
			},
		},
	}

	Assert.Equal(
		t,
		[]string{
			"WORK_GH_BOT_TOKEN",
			"MIDDLEMAN_FORGEJO_TOKEN",
			"MIDDLEMAN_GITEA_TOKEN",
			"REPO_FORGEJO_TOKEN",
			"REPO_GITEA_TOKEN",
		},
		cfg.TokenEnvNames(),
	)
}

func TestGhAuthTokenForHostPassesHostnameFlag(t *testing.T) {
	argvPath := setFakeGHCLIScript(t, fakeGHCLIOptions{
		Stdout: "gh-secret-github",
	})

	got := ghAuthTokenForHost("github.com")
	Assert.Equal(t, "gh-secret-github", got)

	argv := readFakeGHArgv(t, argvPath)
	require.Len(t, argv, 1)
	Assert.Equal(t, "auth token --hostname github.com", argv[0])
}

func TestGhAuthTokenForHostRetriesBareWhenOldGHRejectsHostnameFlag(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	argvPath := setFakeGHCLIWithScript(
		t, fakeGHCLIRejectHostnameThenBareScript(),
	)

	got := ghAuthTokenForHost("github.com")
	assert.Equal("gh-secret-bare", got)

	argv := readFakeGHArgv(t, argvPath)
	require.Len(argv, 2)
	assert.Equal("auth token --hostname github.com", argv[0])
	assert.Equal("auth token", argv[1])
}

func TestGhAuthTokenForHostDoesNotRetryBareOnAuthFailure(t *testing.T) {
	argvPath := setFakeGHCLIScript(t, fakeGHCLIOptions{
		Stderr:   "no oauth token",
		ExitCode: 1,
	})

	got := ghAuthTokenForHost("github.com")
	Assert.Empty(t, got)

	argv := readFakeGHArgv(t, argvPath)
	require.Len(t, argv, 1, "should not retry bare on non-flag-rejection failure")
	Assert.Equal(t, "auth token --hostname github.com", argv[0])
}

func TestGhAuthTokenForHostDoesNotRetryBareOnGHEFlagRejection(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	argvPath := setFakeGHCLIWithScript(t, fakeGHCLIRejectHostnameScript())

	got := ghAuthTokenForHost("ghe.example.com")
	assert.Empty(got)

	argv := readFakeGHArgv(t, argvPath)
	require.Len(argv, 1, "non-github.com host must not retry bare")
	assert.Equal("auth token --hostname ghe.example.com", argv[0])
}

func TestGhAuthTokenForHostReturnsEmptyWhenBinaryMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	Assert.Empty(t, ghAuthTokenForHost("github.com"))
}

func TestGhAuthTokenForHostTimesOut(t *testing.T) {
	// Fake gh sleeps longer than the timeout, so the helper must
	// return "" once the context expires.
	setFakeGHCLIScript(t, fakeGHCLIOptions{
		Stdout:       "never-reached",
		SleepSeconds: 10,
	})

	start := time.Now()
	got := ghAuthTokenForHost("github.com")
	elapsed := time.Since(start)

	Assert.Empty(t, got)
	Assert.Less(
		t, elapsed, ghAuthExecTimeout+2*time.Second,
		"helper should return shortly after timeout, took %s", elapsed,
	)
}

func TestTokenForPlatformHostUsesGHWithHostnameForGHE(t *testing.T) {
	argvPath := setFakeGHCLIScript(t, fakeGHCLIOptions{
		Stdout: "ghe-secret",
	})
	t.Setenv("MIDDLEMAN_GITHUB_TOKEN", "")

	cfg := &Config{GitHubTokenEnv: "MIDDLEMAN_GITHUB_TOKEN"}
	got := cfg.TokenForPlatformHost("github", "ghe.example.com", "")
	Assert.Equal(t, "ghe-secret", got)

	argv := readFakeGHArgv(t, argvPath)
	require.Len(t, argv, 1)
	Assert.Equal(t, "auth token --hostname ghe.example.com", argv[0])
}

func TestTokenForPlatformHostPrefersEnvOverGHForGHE(t *testing.T) {
	argvPath := setFakeGHCLIScript(t, fakeGHCLIOptions{
		Stdout: "ghe-from-gh",
	})
	t.Setenv("MIDDLEMAN_GITHUB_TOKEN", "ghe-from-env")

	cfg := &Config{GitHubTokenEnv: "MIDDLEMAN_GITHUB_TOKEN"}
	got := cfg.TokenForPlatformHost("github", "ghe.example.com", "")
	Assert.Equal(t, "ghe-from-env", got)

	Assert.Empty(t, readFakeGHArgv(t, argvPath), "env var should short-circuit gh")
}

func TestTokenForPlatformHostPrefersPlatformsEntryOverGHForGHE(t *testing.T) {
	argvPath := setFakeGHCLIScript(t, fakeGHCLIOptions{
		Stdout: "ghe-from-gh",
	})
	t.Setenv("MIDDLEMAN_GITHUB_TOKEN", "")
	t.Setenv("PLATFORMS_GHE_TOKEN", "ghe-from-platforms")

	cfg := &Config{
		GitHubTokenEnv: "MIDDLEMAN_GITHUB_TOKEN",
		Platforms: []PlatformConfig{
			{Type: "github", Host: "ghe.example.com", TokenEnv: "PLATFORMS_GHE_TOKEN"},
		},
	}
	got := cfg.TokenForPlatformHost("github", "ghe.example.com", "")
	Assert.Equal(t, "ghe-from-platforms", got)

	Assert.Empty(t, readFakeGHArgv(t, argvPath), "[[platforms]] should short-circuit gh")
}

func TestTokenForPlatformHostPrefersRepoTokenEnvOverGHForGHE(t *testing.T) {
	argvPath := setFakeGHCLIScript(t, fakeGHCLIOptions{
		Stdout: "ghe-from-gh",
	})
	t.Setenv("MIDDLEMAN_GITHUB_TOKEN", "")
	t.Setenv("REPO_GHE_TOKEN", "ghe-from-repo")

	cfg := &Config{GitHubTokenEnv: "MIDDLEMAN_GITHUB_TOKEN"}
	got := cfg.TokenForPlatformHost("github", "ghe.example.com", "REPO_GHE_TOKEN")
	Assert.Equal(t, "ghe-from-repo", got)

	Assert.Empty(t, readFakeGHArgv(t, argvPath), "repo token_env should short-circuit gh")
}

func TestGitHubTokenInvokesGHWithGithubComHostname(t *testing.T) {
	argvPath := setFakeGHCLIScript(t, fakeGHCLIOptions{
		Stdout: "default-host-secret",
	})
	t.Setenv("MIDDLEMAN_GITHUB_TOKEN", "")

	cfg := &Config{GitHubTokenEnv: "MIDDLEMAN_GITHUB_TOKEN"}
	got := cfg.GitHubToken()
	Assert.Equal(t, "default-host-secret", got)

	argv := readFakeGHArgv(t, argvPath)
	require.Len(t, argv, 1)
	Assert.Equal(t, "auth token --hostname github.com", argv[0])
}
