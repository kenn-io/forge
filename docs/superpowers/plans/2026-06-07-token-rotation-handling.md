# Token Rotation Handling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let Middleman pick up rotated provider tokens without a daemon restart, with token-file support and no token leakage through logs, errors, telemetry, or persisted state.

**Architecture:** Add provider-neutral token source descriptors to config, then build managed runtime token sources that read env vars and token files lazily and can update their descriptor on config reload. Provider clients and GitHub GraphQL fetchers hold `(platform, host)` sources instead of startup token strings, and auth transports read the current token per operation with a single 401 refresh retry. Clone fetches read from sources at operation time too, but git credentials are selected by URL host, so startup rejects same-host provider conflicts before passing a host-keyed source map to the clone manager.

**Tech Stack:** Go, TOML config, `net/http` RoundTripper wrappers, provider SDK HTTP-client hooks, testify, shuffled `go test`, existing Huma/OpenAPI generation only if schemas change.

---

## Implementation Status

As of 2026-06-08, this branch has implemented the plan. The task checklists
below preserve the original execution detail, while this status ledger records
what shipped and what changed during implementation:

- Task 1 complete: config supports `token_file`, normalized paths,
  provider-host token descriptors, source conflict validation, and complete
  token env stripping metadata.
- Task 2 complete: managed env/file/GitHub CLI token sources read lazily, file
  and env sources are not cached, GitHub CLI cache entries can be invalidated,
  and redaction now includes a bounded process-local recent-token registry so
  opaque token values long enough to avoid ordinary-word false positives are
  scrubbed after any `ManagedSource` returns them, with duplicate token returns
  refreshing registry recency.
- Task 3 complete: startup builds provider-host source descriptors, fails
  missing-token checks without printing values, and threads sources through the
  app.
- Task 4 complete: provider REST/GraphQL transports read the current source per
  request and retry once after 401 where supported.
- Task 5 complete: clone credentials are read from host-keyed runtime sources
  after same-host descriptor validation.
- Task 6 complete: config reload updates token-source descriptors and runtime
  env sanitization without requiring restart for existing provider hosts.
- Task 7 complete in code and docs: the e2e test is
  `TestTokenFileRotationE2EConfigStartupAndHTTPSync`, and the README/invariant
  docs now name the exact public-host defaults. No OpenAPI artifacts were
  regenerated because no Huma schema changed.

The task-level commit commands below were superseded by the branch cleanup into
logical design and implementation commits plus the review-fix commit.

---

## File Structure

- `internal/tokenauth/descriptor.go` - new provider-neutral token source keys, ordered candidates, safe display strings, and descriptor comparison.
- `internal/tokenauth/source.go` - new managed token source implementation for env, token files, GitHub CLI fallback, cache invalidation, and source-set updates.
- `internal/tokenauth/transport.go` - new auth and one-shot-401 retry transports used by REST, GraphQL, GitLab, Forgejo, and Gitea clients.
- `internal/tokenauth/redact.go` - redaction helpers for active opaque tokens, token-shaped strings, and token-bearing URL/userinfo strings.
- `internal/tokenauth/*_test.go` - low-level tests for lazy reads, file rotation, CLI cache invalidation, retry behavior, and redaction.
- `internal/config/config.go` - add `token_file`, normalize token-file paths at load time, expose token source descriptors, extend conflict validation, and keep `TokenEnvNames` complete.
- `internal/config/config_test.go` - config parsing, precedence, path normalization, fallback, conflict, save round-trip, and sanitizer env-name tests.
- `cmd/middleman/provider_startup.go` - collect managed token sources instead of token strings, fail fast without printing token values, and pass sources into provider factories.
- `cmd/middleman/main.go` - thread the token source set into provider startup, clone manager, GraphQL fetchers, and server config reload.
- `cmd/middleman/*_test.go` - update startup tests from token strings to managed sources and add token-file rotation coverage.
- `internal/github/client.go` - change GitHub REST client construction to use a token source and retry once after HTTP 401.
- `internal/github/graphql.go` - change GraphQL fetcher construction to use the same source/retry model.
- `internal/github/*_test.go` - tests proving the second REST/GraphQL request uses the rotated token, 401 retries once, and 403 does not retry.
- `internal/platform/gitlab/client.go` - inject `Private-Token` from a source per request.
- `internal/platform/forgejo/client.go` and `internal/platform/gitea/client.go` - inject `Authorization: token <token>` from a source per request.
- `internal/platform/*/*_test.go` - provider tests for rotated second request and sanitized auth failures.
- `internal/gitclone/clone.go` - replace host-token strings with host-keyed token sources after startup conflict validation and fetch credentials at operation time.
- `internal/gitclone/*_test.go` - clone auth tests for operation-time rotation, token-source failures, and stderr redaction.
- `internal/server/server.go` and `internal/server/config_reload.go` - store the token source set, hot-update source descriptors on config reload, and stop marking existing-host source changes as restart-required.
- `internal/workspace/localruntime/manager.go` - add a method to update stripped env var names for future launched sessions.
- `internal/server/config_reload_test.go` and `internal/server/settings_test.go` - reload tests for source changes without restart and runtime sanitizer updates.
- `internal/server/e2etest/token_rotation_test.go` - full-stack HTTP API + SQLite token-file rotation test.
- `README.md` and `context/platform-sync-invariants.md` - document `token_file`, precedence, rotation behavior, and provider-host credential boundaries.

---

## Task 1: Config Token Source Descriptors

**Files:**
- Create: `internal/tokenauth/descriptor.go`
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

This task adds the config vocabulary and source resolution model, but does not yet change provider clients.

- [ ] **Step 1: Write failing config tests**

Add these tests near the existing token-env tests in `internal/config/config_test.go`:

```go
func TestLoadTokenFilePathsAreNormalized(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	require.NoError(t, os.MkdirAll(home, 0o755))
	t.Setenv("HOME", home)
	cfgPath := filepath.Join(dir, "config", "config.toml")
	require.NoError(t, os.MkdirAll(filepath.Dir(cfgPath), 0o755))
	require.NoError(t, os.WriteFile(cfgPath, []byte(`
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"

[[platforms]]
type = "gitlab"
host = "gitlab.com"
token_file = "tokens/gitlab"

[[repos]]
owner = "acme"
name = "widget"
platform = "github"
token_file = "~/tokens/github"
`), 0o600))

	cfg, err := Load(cfgPath)
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(filepath.Dir(cfgPath), "tokens", "gitlab"), cfg.Platforms[0].TokenFile)
	assert.Equal(t, filepath.Join(home, "tokens", "github"), cfg.Repos[0].TokenFile)
}

func TestConfigTokenSourceDescriptorPrecedence(t *testing.T) {
	cfg := &Config{
		GitHubTokenEnv: "MIDDLEMAN_GITHUB_TOKEN",
		Platforms: []PlatformConfig{{
			Type: "gitlab", Host: "gitlab.com", TokenFile: "/platform/file", TokenEnv: "PLATFORM_TOKEN",
		}},
	}

	desc := cfg.TokenSourceForPlatformHost("gitlab", "gitlab.com", "REPO_TOKEN", "/repo/file")

	require.Len(t, desc.Candidates, 4)
	assert.Equal(t, tokenauth.SourceKindFile, desc.Candidates[0].Kind)
	assert.Equal(t, "/repo/file", desc.Candidates[0].FilePath)
	assert.Equal(t, tokenauth.SourceKindEnv, desc.Candidates[1].Kind)
	assert.Equal(t, "REPO_TOKEN", desc.Candidates[1].EnvName)
	assert.Equal(t, tokenauth.SourceKindFile, desc.Candidates[2].Kind)
	assert.Equal(t, "/platform/file", desc.Candidates[2].FilePath)
	assert.Equal(t, tokenauth.SourceKindEnv, desc.Candidates[3].Kind)
	assert.Equal(t, "PLATFORM_TOKEN", desc.Candidates[3].EnvName)
}

func TestValidateRejectsConflictingTokenSources(t *testing.T) {
	cfg := &Config{
		GitHubTokenEnv: "MIDDLEMAN_GITHUB_TOKEN",
		Repos: []Repo{
			{Owner: "acme", Name: "one", Platform: "github", PlatformHost: "ghe.example.com", TokenFile: "/tokens/a"},
			{Owner: "acme", Name: "two", Platform: "github", PlatformHost: "ghe.example.com", TokenFile: "/tokens/b"},
		},
	}

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "conflicting token source")
	assert.NotContains(t, err.Error(), "ghp_")
}

func TestSaveRoundTripTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg := &Config{
		SyncInterval:        defaultSyncInterval,
		GitHubTokenEnv:      defaultGitHubTokenEnv,
		DefaultPlatformHost: defaultPlatformHost,
		Host:                defaultHost,
		Port:                defaultPort,
		Platforms: []PlatformConfig{{
			Type: "gitlab", Host: "gitlab.com", TokenFile: "/tokens/gitlab",
		}},
		Repos: []Repo{{
			Owner: "acme", Name: "widget", Platform: "gitlab", PlatformHost: "gitlab.com", TokenFile: "/tokens/repo",
		}},
	}

	require.NoError(t, cfg.Save(path))
	loaded, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "/tokens/gitlab", loaded.Platforms[0].TokenFile)
	assert.Equal(t, "/tokens/repo", loaded.Repos[0].TokenFile)
}
```

Run: `go test ./internal/config -run 'TestLoadTokenFilePathsAreNormalized|TestConfigTokenSourceDescriptorPrecedence|TestValidateRejectsConflictingTokenSources|TestSaveRoundTripTokenFile' -shuffle=on`
Expected: FAIL because `tokenauth`, `TokenFile`, and descriptor methods do not exist.

- [ ] **Step 2: Add descriptor types**

Create `internal/tokenauth/descriptor.go`:

```go
package tokenauth

import (
	"fmt"
	"slices"
)

type SourceKind string

const (
	SourceKindEnv       SourceKind = "env"
	SourceKindFile      SourceKind = "file"
	SourceKindGitHubCLI SourceKind = "github_cli"
)

type Key struct {
	Platform string
	Host     string
}

func (k Key) String() string {
	return k.Platform + "\x00" + k.Host
}

type Candidate struct {
	Kind     SourceKind
	EnvName  string
	FilePath string
	Host     string
}

func (c Candidate) SafeString() string {
	switch c.Kind {
	case SourceKindEnv:
		return fmt.Sprintf("env:%s", c.EnvName)
	case SourceKindFile:
		return fmt.Sprintf("file:%s", c.FilePath)
	case SourceKindGitHubCLI:
		return fmt.Sprintf("github_cli:%s", c.Host)
	default:
		return string(c.Kind)
	}
}

type Descriptor struct {
	Key        Key
	Candidates []Candidate
}

func (d Descriptor) EqualSource(other Descriptor) bool {
	return d.Key == other.Key && slices.Equal(d.Candidates, other.Candidates)
}

func (d Descriptor) SafeString() string {
	if len(d.Candidates) == 0 {
		return "none"
	}
	out := d.Candidates[0].SafeString()
	for _, c := range d.Candidates[1:] {
		out += " -> " + c.SafeString()
	}
	return out
}
```

- [ ] **Step 3: Add `token_file` config fields and path normalization**

In `internal/config/config.go`, import `go.kenn.io/middleman/internal/tokenauth`.

Add fields:

```go
type Repo struct {
	Owner        string `toml:"owner" json:"owner"`
	Name         string `toml:"name" json:"name"`
	RepoPath     string `toml:"repo_path,omitempty" json:"repo_path,omitempty"`
	Platform     string `toml:"platform,omitempty" json:"platform,omitempty"`
	PlatformHost string `toml:"platform_host,omitempty" json:"platform_host,omitempty"`
	TokenEnv     string `toml:"token_env,omitempty" json:"token_env,omitempty"`
	TokenFile    string `toml:"token_file,omitempty" json:"token_file,omitempty"`
}

type PlatformConfig struct {
	Type      string `toml:"type" json:"type"`
	Host      string `toml:"host" json:"host"`
	TokenEnv  string `toml:"token_env,omitempty" json:"token_env,omitempty"`
	TokenFile string `toml:"token_file,omitempty" json:"token_file,omitempty"`
}
```

After TOML unmarshal and before `Validate()` in `Load`, add:

```go
cfg.normalizeTokenFilePaths(filepath.Dir(path))
```

Add helpers:

```go
func (c *Config) normalizeTokenFilePaths(configDir string) {
	for i := range c.Platforms {
		c.Platforms[i].TokenFile = normalizeTokenFilePath(configDir, c.Platforms[i].TokenFile)
	}
	for i := range c.Repos {
		c.Repos[i].TokenFile = normalizeTokenFilePath(configDir, c.Repos[i].TokenFile)
	}
}

func normalizeTokenFilePath(configDir, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if raw == "~" {
		return homeDir()
	}
	if strings.HasPrefix(raw, "~/") {
		return filepath.Join(homeDir(), strings.TrimPrefix(raw, "~/"))
	}
	if filepath.IsAbs(raw) {
		return filepath.Clean(raw)
	}
	return filepath.Clean(filepath.Join(configDir, raw))
}
```

Also trim `TokenFile` beside `TokenEnv` inside `Validate()` for manually constructed configs.

- [ ] **Step 4: Add descriptor resolution and conflict validation**

In `internal/config/config.go`, add:

```go
func (c *Config) ResolveRepoTokenSource(r Repo) tokenauth.Descriptor {
	if c == nil {
		return tokenauth.Descriptor{}
	}
	return c.TokenSourceForPlatformHost(
		r.PlatformOrDefault(), r.PlatformHostOrDefault(), r.TokenEnv, r.TokenFile,
	)
}

func (c *Config) TokenSourceForPlatformHost(
	platform, host, repoTokenEnv, repoTokenFile string,
) tokenauth.Descriptor {
	if c == nil {
		return tokenauth.Descriptor{}
	}
	p, err := normalizePlatform(platform)
	if err != nil {
		return tokenauth.Descriptor{}
	}
	h, err := normalizePlatformHost(p, host)
	if err != nil {
		return tokenauth.Descriptor{}
	}
	desc := tokenauth.Descriptor{Key: tokenauth.Key{Platform: p, Host: h}}
	if repoTokenFile != "" {
		desc.Candidates = append(desc.Candidates, tokenauth.Candidate{Kind: tokenauth.SourceKindFile, FilePath: repoTokenFile})
	}
	if repoTokenEnv != "" {
		desc.Candidates = append(desc.Candidates, tokenauth.Candidate{Kind: tokenauth.SourceKindEnv, EnvName: repoTokenEnv})
	}
	for _, pc := range c.Platforms {
		if pc.Type == p && pc.Host == h {
			if pc.TokenFile != "" {
				desc.Candidates = append(desc.Candidates, tokenauth.Candidate{Kind: tokenauth.SourceKindFile, FilePath: pc.TokenFile})
			}
			if pc.TokenEnv != "" {
				desc.Candidates = append(desc.Candidates, tokenauth.Candidate{Kind: tokenauth.SourceKindEnv, EnvName: pc.TokenEnv})
			}
			break
		}
	}
	if defaultTokenEnv, ok := defaultTokenEnvForPlatformHost(p, h); ok {
		desc.Candidates = append(desc.Candidates, tokenauth.Candidate{Kind: tokenauth.SourceKindEnv, EnvName: defaultTokenEnv})
	}
	if p == defaultPlatform {
		desc.Candidates = append(desc.Candidates, tokenauth.Candidate{Kind: tokenauth.SourceKindEnv, EnvName: c.GitHubTokenEnv})
		desc.Candidates = append(desc.Candidates, tokenauth.Candidate{Kind: tokenauth.SourceKindGitHubCLI, Host: h})
	}
	return desc
}
```

Replace the host token conflict map in `Validate()` with descriptor comparison:

```go
hostToken := make(map[string]tokenauth.Descriptor, len(c.Repos))
for _, r := range c.Repos {
	key := r.PlatformOrDefault() + "\x00" + r.PlatformHostOrDefault()
	effective := c.ResolveRepoTokenSource(r)
	if prev, ok := hostToken[key]; ok {
		if !prev.EqualSource(effective) {
			return fmt.Errorf(
				"config: conflicting token source for %s host %q: %s vs %s",
				r.PlatformOrDefault(), r.PlatformHostOrDefault(), prev.SafeString(), effective.SafeString(),
			)
		}
	} else {
		hostToken[key] = effective
	}
}
```

- [ ] **Step 5: Keep token env stripping complete**

Update `TokenEnvNames()` so it walks every descriptor candidate and includes every `SourceKindEnv`, even when a higher-precedence `token_file` exists:

```go
for _, r := range c.Repos {
	for _, candidate := range c.ResolveRepoTokenSource(r).Candidates {
		if candidate.Kind == tokenauth.SourceKindEnv {
			names = appendTokenEnvName(names, candidate.EnvName)
		}
	}
}
```

Keep the existing platform-level loop if it still contributes env names for configured-but-unreferenced hosts.

- [ ] **Step 6: Run focused config tests**

Run: `go test ./internal/config -run 'TestLoadTokenFilePathsAreNormalized|TestConfigTokenSourceDescriptorPrecedence|TestValidateRejectsConflictingTokenSources|TestSaveRoundTripTokenFile|TestTokenEnvNames' -shuffle=on`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/tokenauth/descriptor.go internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): describe refreshable token sources"
```

---

## Task 2: Managed Token Sources And Redaction

**Files:**
- Create: `internal/tokenauth/source.go`
- Create: `internal/tokenauth/redact.go`
- Test: `internal/tokenauth/source_test.go`
- Test: `internal/tokenauth/redact_test.go`

This task implements lazy token reads and sanitization in isolation.

- [ ] **Step 1: Write failing token source tests**

Create `internal/tokenauth/source_test.go`:

```go
package tokenauth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManagedSourceReadsTokenFileEachCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(path, []byte("first\n"), 0o600))
	src := NewManagedSource(Descriptor{
		Key: Key{Platform: "github", Host: "github.com"},
		Candidates: []Candidate{{Kind: SourceKindFile, FilePath: path}},
	}, Options{})

	got, err := src.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "first", got)

	require.NoError(t, os.WriteFile(path, []byte("second\n"), 0o600))
	got, err = src.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "second", got)
}

func TestManagedSourceFallsThroughEmptyFileAndEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(path, []byte("\n"), 0o600))
	t.Setenv("PRIMARY_TOKEN", "")
	t.Setenv("SECONDARY_TOKEN", "from-env")
	src := NewManagedSource(Descriptor{
		Key: Key{Platform: "gitlab", Host: "gitlab.com"},
		Candidates: []Candidate{
			{Kind: SourceKindFile, FilePath: path},
			{Kind: SourceKindEnv, EnvName: "PRIMARY_TOKEN"},
			{Kind: SourceKindEnv, EnvName: "SECONDARY_TOKEN"},
		},
	}, Options{})

	got, err := src.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "from-env", got)
}

func TestManagedSourceUnreadableFileDoesNotExposeToken(t *testing.T) {
	src := NewManagedSource(Descriptor{
		Key: Key{Platform: "github", Host: "github.com"},
		Candidates: []Candidate{{Kind: SourceKindFile, FilePath: filepath.Join(t.TempDir(), "missing")}},
	}, Options{})

	_, err := src.Token(context.Background())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "ghp_sentinel_secret")
	assert.Contains(t, err.Error(), "token file")
}

func TestManagedSourceGitHubCLIInvalidatesCache(t *testing.T) {
	calls := 0
	runner := func(context.Context, string) (string, error) {
		calls++
		return []string{"first", "second"}[calls-1], nil
	}
	src := NewManagedSource(Descriptor{
		Key: Key{Platform: "github", Host: "github.com"},
		Candidates: []Candidate{{Kind: SourceKindGitHubCLI, Host: "github.com"}},
	}, Options{GitHubCLI: runner})

	first, err := src.Token(context.Background())
	require.NoError(t, err)
	second, err := src.Token(context.Background())
	require.NoError(t, err)
	src.Invalidate()
	third, err := src.Token(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "first", first)
	assert.Equal(t, "first", second)
	assert.Equal(t, "second", third)
	assert.Equal(t, 2, calls)
}

func TestManagedSourceUpdateChangesDescriptor(t *testing.T) {
	t.Setenv("OLD_TOKEN", "old")
	t.Setenv("NEW_TOKEN", "new")
	src := NewManagedSource(Descriptor{
		Key: Key{Platform: "github", Host: "github.com"},
		Candidates: []Candidate{{Kind: SourceKindEnv, EnvName: "OLD_TOKEN"}},
	}, Options{})

	src.Update(Descriptor{
		Key: Key{Platform: "github", Host: "github.com"},
		Candidates: []Candidate{{Kind: SourceKindEnv, EnvName: "NEW_TOKEN"}},
	})

	got, err := src.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "new", got)
}

func TestSourceSetUpsertReusesExistingSource(t *testing.T) {
	set := NewSourceSet(Options{})
	key := Key{Platform: "github", Host: "github.com"}
	first := set.Upsert(Descriptor{Key: key, Candidates: []Candidate{{Kind: SourceKindEnv, EnvName: "OLD_TOKEN"}}})
	second := set.Upsert(Descriptor{Key: key, Candidates: []Candidate{{Kind: SourceKindEnv, EnvName: "NEW_TOKEN"}}})

	assert.Same(t, first, second)
	assert.Equal(t, "env:NEW_TOKEN", second.Descriptor().SafeString())
}

func TestMissingTokenErrorIsDetectable(t *testing.T) {
	src := NewManagedSource(Descriptor{Key: Key{Platform: "gitlab", Host: "gitlab.com"}}, Options{})
	_, err := src.Token(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrMissingToken))
	assert.NotContains(t, err.Error(), "secret")
}
```

Create `internal/tokenauth/redact_test.go`:

```go
package tokenauth

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRedactKnownSecrets(t *testing.T) {
	got := RedactKnownSecrets("Authorization: Bearer plain-provider-secret", "plain-provider-secret")
	assert.Equal(t, "Authorization: Bearer [REDACTED]", got)
}

func TestRedactTokenBearingURL(t *testing.T) {
	got := RedactKnownSecrets("https://x-access-token:ghp_sentinel_secret@github.com/acme/repo.git", "ghp_sentinel_secret")
	assert.NotContains(t, got, "ghp_sentinel_secret")
	assert.Contains(t, got, "[REDACTED]")
}

func TestRedactError(t *testing.T) {
	err := RedactError(errors.New("token ghp_sentinel_secret failed"), "ghp_sentinel_secret")
	assert.EqualError(t, err, "token [REDACTED] failed")
}

func TestRedactErrorRedactsTokenLikeStringsWithoutExplicitSecret(t *testing.T) {
	err := RedactError(errors.New("git stderr contained ghp_sentinel_secret"))
	assert.EqualError(t, err, "git stderr contained [REDACTED]")
}
```

Run: `go test ./internal/tokenauth -shuffle=on`
Expected: FAIL because the package implementation does not exist.

- [ ] **Step 2: Implement managed sources**

Create `internal/tokenauth/source.go` with these exported APIs:

```go
package tokenauth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

var ErrMissingToken = errors.New("missing provider token")

type GitHubCLIRunner func(context.Context, string) (string, error)

type Options struct {
	GitHubCLI GitHubCLIRunner
}

type Source interface {
	Token(context.Context) (string, error)
	Invalidate()
	Descriptor() Descriptor
}

type ManagedSource struct {
	mu       sync.Mutex
	desc     Descriptor
	options  Options
	ghToken  string
	ghCached bool
}

func NewManagedSource(desc Descriptor, options Options) *ManagedSource {
	return &ManagedSource{desc: desc, options: options}
}

func (s *ManagedSource) Descriptor() Descriptor {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneDescriptor(s.desc)
}

func (s *ManagedSource) Update(desc Descriptor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.desc.EqualSource(desc) {
		s.ghToken = ""
		s.ghCached = false
	}
	s.desc = cloneDescriptor(desc)
}

func (s *ManagedSource) Invalidate() {
	s.mu.Lock()
	s.ghToken = ""
	s.ghCached = false
	s.mu.Unlock()
}

func (s *ManagedSource) Token(ctx context.Context) (string, error) {
	desc := s.Descriptor()
	if len(desc.Candidates) == 0 {
		return "", missingTokenError(desc)
	}
	for _, candidate := range desc.Candidates {
		token, used, err := s.tokenFromCandidate(ctx, candidate)
		if err != nil {
			return "", err
		}
		if used && token != "" {
			RegisterKnownSecret(token)
			return token, nil
		}
	}
	return "", missingTokenError(desc)
}

func (s *ManagedSource) tokenFromCandidate(ctx context.Context, candidate Candidate) (string, bool, error) {
	switch candidate.Kind {
	case SourceKindEnv:
		return strings.TrimSpace(os.Getenv(candidate.EnvName)), true, nil
	case SourceKindFile:
		data, err := os.ReadFile(candidate.FilePath)
		if err != nil {
			return "", false, fmt.Errorf("read token file %s: %w", candidate.FilePath, err)
		}
		return strings.TrimSpace(string(data)), true, nil
	case SourceKindGitHubCLI:
		return s.githubCLIToken(ctx, candidate.Host)
	default:
		return "", false, nil
	}
}

func (s *ManagedSource) githubCLIToken(ctx context.Context, host string) (string, bool, error) {
	s.mu.Lock()
	if s.ghCached {
		token := s.ghToken
		s.mu.Unlock()
		return token, true, nil
	}
	runner := s.options.GitHubCLI
	s.mu.Unlock()
	if runner == nil {
		return "", true, nil
	}
	token, err := runner(ctx, host)
	if err != nil {
		return "", false, fmt.Errorf("github cli token for %s: %w", host, err)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", true, nil
	}
	s.mu.Lock()
	s.ghToken = token
	s.ghCached = true
	s.mu.Unlock()
	return token, true, nil
}

func missingTokenError(desc Descriptor) error {
	return fmt.Errorf("%w for %s host %s via %s", ErrMissingToken, desc.Key.Platform, desc.Key.Host, desc.SafeString())
}
```

Add `SourceSet` to the same file:

```go
type SourceSet struct {
	mu      sync.Mutex
	options Options
	sources map[Key]*ManagedSource
}

func NewSourceSet(options Options) *SourceSet {
	return &SourceSet{options: options, sources: make(map[Key]*ManagedSource)}
}

func (s *SourceSet) Upsert(desc Descriptor) *ManagedSource {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.sources[desc.Key]; ok {
		existing.Update(desc)
		return existing
	}
	src := NewManagedSource(desc, s.options)
	s.sources[desc.Key] = src
	return src
}

func (s *SourceSet) Get(key Key) (*ManagedSource, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	src, ok := s.sources[key]
	return src, ok
}

func (s *SourceSet) Keys() []Key {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]Key, 0, len(s.sources))
	for key := range s.sources {
		keys = append(keys, key)
	}
	return keys
}

func cloneDescriptor(desc Descriptor) Descriptor {
	desc.Candidates = append([]Candidate(nil), desc.Candidates...)
	return desc
}
```

- [ ] **Step 3: Implement redaction helpers**

Create `internal/tokenauth/redact.go`:

```go
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
```

- [ ] **Step 4: Run tokenauth tests**

Run: `go test ./internal/tokenauth -shuffle=on`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tokenauth/source.go internal/tokenauth/redact.go internal/tokenauth/source_test.go internal/tokenauth/redact_test.go
git commit -m "feat(auth): add managed provider token sources"
```

---

## Task 3: Startup Uses Managed Sources

**Files:**
- Modify: `cmd/middleman/provider_startup.go`
- Modify: `cmd/middleman/main.go`
- Test: `cmd/middleman/startup_token_e2e_test.go`
- Test: `cmd/middleman/main_test.go`

This task replaces startup token strings with source objects and keeps provider construction behavior otherwise unchanged.

This is an intermediate, non-shippable state. Do not stop after this task:
provider clients, GraphQL fetchers, clone fetches, and config reload still need
Tasks 4 through 6 before a running service can use rotated tokens everywhere.

- [ ] **Step 1: Write failing startup tests**

In `cmd/middleman/startup_token_e2e_test.go`, add:

```go
func TestCollectProviderTokenSourcesReadsRotatedTokenFile(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenPath, []byte("first\n"), 0o600))
	cfg := &config.Config{
		GitHubTokenEnv: "MIDDLEMAN_GITHUB_TOKEN",
		Repos: []config.Repo{{
			Owner: "acme", Name: "widget", Platform: "github", PlatformHost: "github.com", TokenFile: tokenPath,
		}},
	}
	require.NoError(t, cfg.Validate())

	set := tokenauth.NewSourceSet(tokenauth.Options{})
	sources, err := collectProviderTokenSources(t.Context(), cfg, set)
	require.NoError(t, err)
	src := sources[providerHostKey("github", "github.com")]

	first, err := src.Token(t.Context())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(tokenPath, []byte("second\n"), 0o600))
	second, err := src.Token(t.Context())
	require.NoError(t, err)

	assert.Equal(t, "first", first)
	assert.Equal(t, "second", second)
}
```

Update `cmd/middleman/main_test.go` assertions so `startup.cloneSources` is keyed by `tokenauth.Key{Platform: "...", Host: "..."}` and returns a source whose token can be read in the test context.

Run: `go test ./cmd/middleman -run 'TestCollectProviderTokenSourcesReadsRotatedTokenFile|TestBuildProviderStartup' -shuffle=on`
Expected: FAIL because startup still uses token strings.

- [ ] **Step 2: Replace startup token maps**

In `cmd/middleman/provider_startup.go`, change these types:

```go
type providerFactoryInput struct {
	host        string
	token       string
	tokenSource tokenauth.Source
	rateTracker *github.RateTracker
	budget      *github.SyncBudget
}

type providerFactoryOutput struct {
	githubClient github.Client
	provider     platform.Provider
	githubToken  string
	githubSource tokenauth.Source
}

type providerStartup struct {
	registry     *platform.Registry
	rateTrackers map[string]*github.RateTracker
	budgets      map[string]*github.SyncBudget
	cloneTokens  map[string]string
	cloneSources map[tokenauth.Key]tokenauth.Source
	fetchers     map[string]*github.GraphQLFetcher
}
```

Add imports for `context` and `go.kenn.io/middleman/internal/tokenauth`.

- [ ] **Step 3: Collect source descriptors and fail fast without printing tokens**

Export a context-aware GitHub CLI runner from `internal/config/config.go` so runtime sources can invalidate and re-read CLI credentials:

```go
func GitHubCLITokenForHost(ctx context.Context, host string) (string, error) {
	out, stderr, err := runGHAuthToken(ctx, "--hostname", host)
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	if host == platformpkg.DefaultGitHubHost && isUnsupportedHostnameFlag(err, stderr) {
		out, _, err = runGHAuthToken(ctx)
		if err == nil {
			return strings.TrimSpace(string(out)), nil
		}
	}
	return "", nil
}
```

Keep `ghAuthTokenForHost(host string)` as the old string-returning wrapper:

```go
func ghAuthTokenForHost(host string) string {
	ctx, cancel := context.WithTimeout(context.Background(), ghAuthExecTimeout)
	defer cancel()
	token, _ := GitHubCLITokenForHost(ctx, host)
	return token
}
```

Then replace `collectProviderTokens` with:

```go
func collectProviderTokenSources(
	ctx context.Context,
	cfg *config.Config,
	set *tokenauth.SourceSet,
) (map[string]tokenauth.Source, error) {
	providerSources := make(map[string]tokenauth.Source, len(cfg.Repos)+len(cfg.Platforms)+1)
	addRequired := func(desc tokenauth.Descriptor, label string) error {
		key := providerHostKey(desc.Key.Platform, desc.Key.Host)
		if _, seen := providerSources[key]; seen {
			return nil
		}
		src := set.Upsert(desc)
		if _, err := src.Token(ctx); err != nil {
			return fmt.Errorf("no token for %s via %s: %w", label, desc.SafeString(), err)
		}
		providerSources[key] = src
		return nil
	}
	addOptional := func(desc tokenauth.Descriptor, label string) error {
		key := providerHostKey(desc.Key.Platform, desc.Key.Host)
		if _, seen := providerSources[key]; seen {
			return nil
		}
		src := set.Upsert(desc)
		if _, err := src.Token(ctx); err != nil {
			if errors.Is(err, tokenauth.ErrMissingToken) {
				return nil
			}
			return fmt.Errorf("read optional token for %s via %s: %w", label, desc.SafeString(), err)
		}
		providerSources[key] = src
		return nil
	}
	for _, r := range cfg.Repos {
		desc := cfg.ResolveRepoTokenSource(r)
		if err := addRequired(desc, fmt.Sprintf("%s host %s (repo %s/%s)", desc.Key.Platform, desc.Key.Host, r.Owner, r.Name)); err != nil {
			return nil, err
		}
	}
	for _, p := range cfg.Platforms {
		desc := cfg.TokenSourceForPlatformHost(p.Type, p.Host, "", "")
		if len(desc.Candidates) > 0 {
			if err := addOptional(desc, fmt.Sprintf("%s host %s", desc.Key.Platform, desc.Key.Host)); err != nil {
				return nil, err
			}
		}
	}
	defaultDesc := cfg.TokenSourceForPlatformHost(string(platform.KindGitHub), platform.DefaultGitHubHost, "", "")
	if len(defaultDesc.Candidates) > 0 {
		if err := addOptional(defaultDesc, "github host github.com"); err != nil {
			return nil, err
		}
	}
	if err := validateProviderHostKeys(providerSources); err != nil {
		return nil, err
	}
	return providerSources, nil
}
```

Import `errors` in `cmd/middleman/provider_startup.go`. Repo descriptors are required because configured repos need authenticated sync; platform-only descriptors are optional so a configured-but-unreferenced host does not fail startup solely because its token is absent.

Update `validateProviderHostKeys` generics or add a source-map-specific validator so it still validates keys without depending on map value type.

- [ ] **Step 4: Thread sources through provider startup without changing provider constructors yet**

Change `buildProviderStartup` to accept `map[string]tokenauth.Source`. When iterating, read one startup token for the still-static provider constructors:

```go
token, err := tokenSource.Token(context.Background())
if err != nil {
	return providerStartup{}, fmt.Errorf(
		"read token for %s host %s via %s: %w",
		platformName, host, tokenSource.Descriptor().SafeString(), err,
	)
}
```

Pass both `token` and `tokenSource` to the factory. Keep the existing default factories on their current string-token constructors in this task:

```go
built, err := factory(providerFactoryInput{
	host:        host,
	token:       token,
	tokenSource: tokenSource,
	rateTracker: startup.rateTrackers[rateKey],
	budget:      startup.budgets[rateKey],
})
```

Store both clone forms for now:

```go
startup.cloneTokens[host] = token
startup.cloneSources[tokenauth.Key{Platform: platformName, Host: host}] = tokenSource
```

Keep `github.NewGraphQLFetcher(token, host, ...)` for this task. Task 4 switches provider and GraphQL constructors to use `tokenSource`; Task 5 switches clone fetches to `cloneSources`.

- [ ] **Step 5: Update main wiring**

In `cmd/middleman/main.go`, replace startup collection with:

```go
tokenSources := tokenauth.NewSourceSet(tokenauth.Options{
	GitHubCLI: config.GitHubCLITokenForHost,
})
providerSources, err := collectProviderTokenSources(context.Background(), cfg, tokenSources)
```

Keep `gitclone.New` on `startup.cloneTokens` only for this intermediate task because the clone manager changes in Task 5. `TokenSources` is added to `server.ServerOptions` in Task 6.

- [ ] **Step 6: Commit**

```bash
git add cmd/middleman/provider_startup.go cmd/middleman/main.go cmd/middleman/startup_token_e2e_test.go cmd/middleman/main_test.go
git commit -m "feat(startup): wire providers to refreshable token sources"
```

---

## Task 4: Dynamic Provider Auth Transports

**Files:**
- Create: `internal/tokenauth/transport.go`
- Modify: `internal/github/client.go`
- Modify: `internal/github/graphql.go`
- Modify: `internal/platform/gitlab/client.go`
- Modify: `internal/platform/forgejo/client.go`
- Modify: `internal/platform/gitea/client.go`
- Test: `internal/tokenauth/transport_test.go`
- Test: `internal/github/client_test.go`
- Test: `internal/github/graphql_test.go`
- Test: provider client tests under `internal/platform/*`

This task makes API clients read tokens immediately before each HTTP request.

- [ ] **Step 1: Write failing transport tests**

Create `internal/tokenauth/transport_test.go`:

```go
package tokenauth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sequenceSource struct {
	tokens      []string
	invalidated int
}

func (s *sequenceSource) Token(context.Context) (string, error) {
	token := s.tokens[0]
	if len(s.tokens) > 1 {
		s.tokens = s.tokens[1:]
	}
	return token, nil
}

func (s *sequenceSource) Invalidate() { s.invalidated++ }
func (s *sequenceSource) Descriptor() Descriptor {
	return Descriptor{Key: Key{Platform: "github", Host: "github.com"}}
}

func TestAuthTransportReadsTokenEachRequest(t *testing.T) {
	src := &sequenceSource{tokens: []string{"first", "second"}}
	var auth []string
	rt := AuthTransport{
		Source: src,
		Base: RoundTripFunc(func(req *http.Request) (*http.Response, error) {
			auth = append(auth, req.Header.Get("Authorization"))
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header), Request: req}, nil
		}),
		SetHeader: BearerAuthHeader,
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://api.example.test", nil)
	require.NoError(t, err)
	_, err = rt.RoundTrip(req)
	require.NoError(t, err)
	_, err = rt.RoundTrip(req)
	require.NoError(t, err)

	assert.Equal(t, []string{"Bearer first", "Bearer second"}, auth)
}

func TestRetryOnUnauthorizedInvalidatesAndRetriesOnce(t *testing.T) {
	src := &sequenceSource{tokens: []string{"old", "new"}}
	var auth []string
	calls := 0
	rt := AuthTransport{
		Source: src,
		Base: RoundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			auth = append(auth, req.Header.Get("Authorization"))
			status := http.StatusUnauthorized
			if calls == 2 {
				status = http.StatusOK
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header), Request: req}, nil
		}),
		SetHeader:           BearerAuthHeader,
		RetryOnUnauthorized: true,
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://api.example.test", nil)
	require.NoError(t, err)
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 1, src.invalidated)
	assert.Equal(t, []string{"Bearer old", "Bearer new"}, auth)
}

func TestRetryOnUnauthorizedDoesNotRetryForbidden(t *testing.T) {
	src := &sequenceSource{tokens: []string{"old", "new"}}
	calls := 0
	rt := AuthTransport{
		Source: src,
		Base: RoundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header), Request: req}, nil
		}),
		SetHeader:           BearerAuthHeader,
		RetryOnUnauthorized: true,
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://api.example.test", nil)
	require.NoError(t, err)
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Equal(t, 1, calls)
	assert.Equal(t, 0, src.invalidated)
}

func TestRetryOnUnauthorizedReplaysGetBody(t *testing.T) {
	src := &sequenceSource{tokens: []string{"old", "new"}}
	var bodies []string
	calls := 0
	rt := AuthTransport{
		Source: src,
		Base: RoundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			bodies = append(bodies, string(body))
			status := http.StatusUnauthorized
			if calls == 2 {
				status = http.StatusOK
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header), Request: req}, nil
		}),
		SetHeader:           BearerAuthHeader,
		RetryOnUnauthorized: true,
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://api.example.test", strings.NewReader("payload"))
	require.NoError(t, err)
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, []string{"payload", "payload"}, bodies)
	assert.Equal(t, 1, src.invalidated)
}

func TestRetryOnUnauthorizedDoesNotRetryUnrewindableBody(t *testing.T) {
	src := &sequenceSource{tokens: []string{"old", "new"}}
	calls := 0
	rt := AuthTransport{
		Source: src,
		Base: RoundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header), Request: req}, nil
		}),
		SetHeader:           BearerAuthHeader,
		RetryOnUnauthorized: true,
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://api.example.test", io.NopCloser(strings.NewReader("payload")))
	require.NoError(t, err)
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, 1, calls)
	assert.Equal(t, 0, src.invalidated)
}
```

Run: `go test ./internal/tokenauth -run 'TestAuthTransport|TestRetryOnUnauthorized' -shuffle=on`
Expected: FAIL because `AuthTransport` does not exist.

- [ ] **Step 2: Implement auth transport**

Create `internal/tokenauth/transport.go`:

```go
package tokenauth

import (
	"fmt"
	"io"
	"net/http"
)

type RoundTripFunc func(*http.Request) (*http.Response, error)

func (f RoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type HeaderSetter func(*http.Request, string)

func BearerAuthHeader(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
}

func TokenAuthHeader(req *http.Request, token string) {
	req.Header.Set("Authorization", "token "+token)
}

func PrivateTokenHeader(req *http.Request, token string) {
	req.Header.Set("Private-Token", token)
}

type AuthTransport struct {
	Source              Source
	Base                http.RoundTripper
	SetHeader           HeaderSetter
	RetryOnUnauthorized bool
}

func (t AuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	first, err := t.authorizedRequest(req)
	if err != nil {
		return nil, err
	}
	resp, err := base.RoundTrip(first)
	if err != nil || !t.RetryOnUnauthorized || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	retry, ok := cloneForRetry(req)
	if !ok {
		return resp, err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	t.Source.Invalidate()
	second, err := t.authorizedRequest(retry)
	if err != nil {
		return nil, err
	}
	return base.RoundTrip(second)
}

func (t AuthTransport) authorizedRequest(req *http.Request) (*http.Request, error) {
	if t.Source == nil {
		return nil, fmt.Errorf("%w: nil token source", ErrMissingToken)
	}
	token, err := t.Source.Token(req.Context())
	if err != nil {
		return nil, err
	}
	clone := req.Clone(req.Context())
	if req.Body != nil && req.Body != http.NoBody {
		clone.Body = req.Body
	}
	t.SetHeader(clone, token)
	return clone, nil
}

func cloneForRetry(req *http.Request) (*http.Request, bool) {
	clone := req.Clone(req.Context())
	if req.Body == nil || req.Body == http.NoBody {
		return clone, true
	}
	if req.GetBody == nil {
		return nil, false
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, false
	}
	clone.Body = body
	return clone, true
}

```

- [ ] **Step 3: Update GitHub REST and GraphQL constructors**

In `internal/github/client.go`, change `NewClient` to accept `tokenauth.Source` instead of `string`. Update `cmd/middleman/provider_startup.go` so default factories pass `input.tokenSource` and remove the now-unused `token` string from provider factory input/output after all providers compile. Build the HTTP client so auth is read per request:

```go
authRT := tokenauth.AuthTransport{
	Source:              source,
	Base:                http.DefaultTransport,
	SetHeader:           tokenauth.BearerAuthHeader,
	RetryOnUnauthorized: true,
}
et := &etagTransport{base: authRT}
var transport http.RoundTripper = et
if budget != nil {
	transport = &budgetTransport{base: transport, budget: budget}
}
httpClient := &http.Client{Transport: wrapPublicGitHubAPIGuard(transport)}
```

Keep the existing REST order of `AuthTransport -> etagTransport -> budgetTransport -> wrapPublicGitHubAPIGuard` from the caller's perspective: auth remains the innermost HTTP layer replacing the old OAuth2 transport, ETag still wraps auth, budget still wraps ETag, and the public GitHub guard remains outermost. Update tests to use `tokenauth.NewManagedSource` with env or file candidates rather than string tokens.

In `internal/github/graphql.go`, make `NewGraphQLFetcher` accept `tokenauth.Source` and use the same `AuthTransport` for the `githubv4` HTTP client. Preserve the existing GraphQL order by replacing the old OAuth2 transport with auth, then wrapping it with `graphqlRateTransport`, `budgetTransport`, and `wrapPublicGitHubAPIGuard` in that order.

- [ ] **Step 4: Update GitLab, Forgejo, and Gitea clients**

Change each provider `NewClient` constructor to accept `tokenauth.Source`.

For GitLab, pass an empty SDK token and inject auth with:

```go
baseTransport := http.DefaultTransport
if opts.rateTracker != nil {
	baseTransport = &rateTrackingTransport{
		base:        baseTransport,
		rateTracker: opts.rateTracker,
	}
}
authRT := tokenauth.AuthTransport{
	Source:              source,
	Base:                baseTransport,
	SetHeader:           tokenauth.PrivateTokenHeader,
	RetryOnUnauthorized: true,
}
client, err := gitlab.NewClient("", gitlab.WithBaseURL(baseURL), gitlab.WithHTTPClient(&http.Client{Transport: authRT}))
```

For Forgejo and Gitea, stop using the SDK static token setter and wrap the HTTP client with:

```go
httpTransport := http.DefaultTransport
if opts.rateTracker != nil {
	httpTransport = &rateTrackingTransport{
		base:        httpTransport,
		rateTracker: opts.rateTracker,
	}
}
mergeability := gitealike.NewMergeableCache()
httpTransport = &gitealike.MergeableCaptureTransport{
	Base:  httpTransport,
	Cache: mergeability,
}
authRT := tokenauth.AuthTransport{
	Source:              source,
	Base:                httpTransport,
	SetHeader:           tokenauth.TokenAuthHeader,
	RetryOnUnauthorized: true,
}
```

The Gitea-like SDK HTTP client should use `authRT` as its transport while keeping the mergeability capture and rate tracking transports underneath it. This preserves the current mergeability cache and rate observation behavior while moving token injection out of the SDK static token option.

- [ ] **Step 5: Add focused provider transport tests**

Add low-level `internal/tokenauth` tests for header injection, per-request token
reads, 401 invalidation/retry, 403 non-retry, and body replay. These prove the
shared behavior once.

- [ ] **Step 6: Add provider constructor integration tests**

For each provider package, add a small test server that records auth headers for
two representative SDK/API calls. Rotate the backing token file between calls
and assert the second call carries the second token. Keep provider fixtures
minimal and avoid duplicating every shared retry case that `internal/tokenauth`
already covers.

Add GitHub REST and GraphQL 403 cases because their constructor layering is
provider-specific and must prove the source was not invalidated and only one
HTTP call occurred. Use sentinel values such as `ghp_sentinel_old` and
`ghp_sentinel_new`; captured logs and returned errors must not contain either
sentinel.

- [ ] **Step 7: Run provider auth tests**

Run: `go test ./internal/tokenauth ./internal/github ./internal/platform/gitlab ./internal/platform/forgejo ./internal/platform/gitea -run 'Token|Auth|Unauthorized|Forbidden|GraphQLFetcher' -shuffle=on`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/tokenauth/transport.go internal/tokenauth/transport_test.go internal/github internal/platform cmd/middleman
git commit -m "feat(auth): refresh provider tokens per request"
```

---

## Task 5: Clone Fetches Use Host-Keyed Runtime Sources

**Files:**
- Modify: `internal/gitclone/clone.go`
- Modify: `cmd/middleman/provider_startup.go`
- Modify: `cmd/middleman/main.go`
- Test: `internal/gitclone/auth_test.go`
- Test: `cmd/middleman/main_test.go`

This task makes clone/fetch auth read a token source at each git operation.
Git credential helpers select credentials by URL host, not by Middleman's
provider kind, so startup must first reject same-host provider entries unless
their effective token-source descriptors are identical.

- [ ] **Step 1: Write failing clone auth tests**

In `internal/gitclone/auth_test.go`, add tests proving:

- `gitRunner` reads the current in-memory source token for every command.
- A `token_file` source is re-read for every command so atomic replacement is
  visible to the next fetch.
- A 401-like git auth failure invalidates the source and retries once.
- Git stderr and the wrapped git process error never expose credential-bearing
  URLs or token-like strings.

Run: `go test ./internal/gitclone -run 'TestGitRunnerReadsToken(FileSource|Source)ForEachCommand|TestGitRetriesAuthFailureAfterInvalidatingTokenSource|TestGitWithInputRedactsTokenFromGitStderr' -shuffle=on`
Expected: FAIL until the clone manager stores sources and redacts git errors.

- [ ] **Step 2: Read host-keyed sources immediately before git execution**

In `internal/gitclone/clone.go`, change the clone manager from static
host-token strings to `map[string]tokenauth.Source`, keeping
`ClonePath(host, owner, name)` and all public clone/sync method signatures
host-based so the existing on-disk clone layout remains stable:

```go
type Manager struct {
	baseDir      string
	tokenSources map[string]tokenauth.Source
	ensureSF     singleflight.Group
}

func New(baseDir string, tokenSources map[string]tokenauth.Source) *Manager {
	return &Manager{baseDir: baseDir, tokenSources: tokenSources}
}
```

`gitRunner(ctx, host)` should look up `m.tokenSources[host]`, call
`source.Token(ctx)` immediately before building the runner, and install Basic
auth only when the returned token is non-empty. Token-source errors should be
returned with `tokenauth.RedactError`.

`gitWithInput` should build git errors through a helper that redacts both raw
stderr and the wrapped subprocess error:

```go
func wrapGitError(err error, stderr []byte) error {
	msg := tokenauth.RedactKnownSecrets(string(stderr))
	if isNotFoundError(msg) {
		return fmt.Errorf("%w: %s", ErrNotFound, msg)
	}
	return fmt.Errorf("%w: %s", tokenauth.RedactError(err), msg)
}
```

- [ ] **Step 3: Validate same-host source descriptors before clone auth**

In `cmd/middleman/provider_startup.go` and `cmd/middleman/main.go`, validate
the provider source map before building clone auth:

- `providerHostKey(platform, host)` remains the provider/API identity key.
- `validateProviderHostKeys` groups by hostname and compares
  `tokenauth.Source.Descriptor().SafeString()` for source values.
- If two provider kinds share a hostname with different source descriptors,
  startup returns a clear error telling the user to use identical tokens or
  separate hosts.
- After validation, `providerStartup.cloneAuth` may be `map[string]tokenauth.Source`
  because each hostname has exactly one allowed clone credential source.

Add source-level validation tests in `cmd/middleman/main_test.go` for rejecting
different descriptors on the same hostname and allowing identical descriptors.

- [ ] **Step 4: Run clone and startup tests**

Run: `go test ./internal/gitclone ./cmd/middleman -run 'TestGitRunnerReadsToken(FileSource|Source)ForEachCommand|TestGitRetriesAuthFailureAfterInvalidatingTokenSource|TestGitWithInputRedactsTokenFromGitStderr|TestValidateProviderHostKeys' -shuffle=on`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/gitclone cmd/middleman
git commit -m "feat(clone): refresh clone credentials per operation"
```

---

## Task 6: Config Reload Updates Token Sources And Sanitizer

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/server/config_reload.go`
- Modify: `internal/workspace/localruntime/manager.go`
- Test: `internal/server/config_reload_test.go`
- Test: `internal/workspace/localruntime/manager_test.go`

This task makes source metadata changes hot-reloadable for already configured provider hosts.

- [ ] **Step 1: Write failing reload tests**

In `internal/server/config_reload_test.go`, add:

```go
func TestConfigReloadTokenSourceChangeForExistingHostDoesNotRequireRestart(t *testing.T) {
	cfgPath, cfg := writeReloadTestConfig(t, `
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"

[[repos]]
owner = "acme"
name = "widget"
platform = "github"
platform_host = "github.com"
token_env = "OLD_TOKEN"
`)
	t.Setenv("OLD_TOKEN", "old")
	t.Setenv("NEW_TOKEN", "new")
	sourceSet := tokenauth.NewSourceSet(tokenauth.Options{})
	oldDesc := cfg.ResolveRepoTokenSource(cfg.Repos[0])
	src := sourceSet.Upsert(oldDesc)
	srv := newReloadTestServer(t, cfg, cfgPath, server.ServerOptions{TokenSources: sourceSet})

	require.NoError(t, os.WriteFile(cfgPath, []byte(`
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"

[[repos]]
owner = "acme"
name = "widget"
platform = "github"
platform_host = "github.com"
token_env = "NEW_TOKEN"
`), 0o600))

	event := srv.applyConfigChange(t.Context())
	require.True(t, event.Valid)
	assert.False(t, event.RestartRequired)
	got, err := src.Token(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "new", got)
}
```

In `internal/workspace/localruntime/manager_test.go`, add:

```go
func TestManagerUpdateStripEnvVarsAffectsFutureLaunches(t *testing.T) {
	backend := &fakePtyOwnerRuntime{}
	mgr := NewManager(Options{
		Targets: []LaunchTarget{{Key: "agent", Label: "Agent", Kind: LaunchTargetAgent, Command: []string{"true"}}},
		StripEnvVars: []string{"OLD_TOKEN"},
		PtyOwnerRuntime: backend,
	})
	mgr.UpdateStripEnvVars([]string{"NEW_TOKEN", "NEW_TOKEN"})

	_, err := mgr.Start(context.Background(), StartRequest{
		WorkspaceID: "ws", TargetKey: "agent", CWD: t.TempDir(),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"NEW_TOKEN"}, backend.startedStripEnvVars)
}
```

Run: `go test ./internal/server ./internal/workspace/localruntime -run 'TestConfigReloadTokenSourceChangeForExistingHostDoesNotRequireRestart|TestManagerUpdateStripEnvVarsAffectsFutureLaunches' -shuffle=on`
Expected: FAIL because server reload and runtime sanitizer do not update these fields.

- [ ] **Step 2: Add runtime sanitizer update**

In `internal/workspace/localruntime/manager.go`, add:

```go
func (m *Manager) UpdateStripEnvVars(names []string) {
	m.mu.Lock()
	m.stripEnvVars = dedupeStrings(names)
	m.mu.Unlock()
}
```

In `internal/server/settings_handlers.go`, update `refreshRuntimeTargetsLocked()`:

```go
s.runtime.UpdateTargets(localruntime.ResolveLaunchTargets(
	s.cfg.Agents, tmuxCmd, nil,
))
s.runtime.UpdateStripEnvVars(s.cfg.TokenEnvNames())
```

- [ ] **Step 3: Store source set on the server**

Add to `ServerOptions`:

```go
TokenSources *tokenauth.SourceSet
```

Add `tokenSources *tokenauth.SourceSet` to `Server`, initialize it in `newServer`, and import `go.kenn.io/middleman/internal/tokenauth`.

- [ ] **Step 4: Make startup snapshot compare provider host identities, not source names**

In `internal/server/config_reload.go`, replace `GitHubTokenEnv`, full `Platforms`, and `TokenEnvNames` in `startupConfigSnapshot` with:

```go
ProviderHosts []tokenauth.Key
```

Build it from normalized `cfg.Platforms` plus repo provider hosts known at startup:

```go
func startupProviderHosts(cfg *config.Config) []tokenauth.Key {
	seen := map[tokenauth.Key]struct{}{}
	var out []tokenauth.Key
	add := func(platform, host string) {
		key := tokenauth.Key{Platform: platform, Host: host}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	for _, p := range cfg.Platforms {
		add(p.Type, p.Host)
	}
	for _, r := range cfg.Repos {
		add(r.PlatformOrDefault(), r.PlatformHostOrDefault())
	}
	slices.SortFunc(out, func(a, b tokenauth.Key) int {
		if a.Platform != b.Platform {
			return strings.Compare(a.Platform, b.Platform)
		}
		return strings.Compare(a.Host, b.Host)
	})
	return out
}
```

Keep listener, sync interval, data dir, tmux, shell, and other startup-bound fields unchanged.

- [ ] **Step 5: Hot-update token source descriptors during reload**

In `applyConfigChange`, after `newCfg` loads and before resolving repos, update known sources:

```go
if s.tokenSources != nil {
	updated := map[tokenauth.Key]struct{}{}
	updateIfKnown := func(desc tokenauth.Descriptor) {
		if _, ok := s.tokenSources.Get(desc.Key); !ok {
			return
		}
		s.tokenSources.Upsert(desc)
		updated[desc.Key] = struct{}{}
	}
	for _, repo := range newCfg.Repos {
		desc := newCfg.ResolveRepoTokenSource(repo)
		updateIfKnown(desc)
	}
	for _, p := range newCfg.Platforms {
		desc := newCfg.TokenSourceForPlatformHost(p.Type, p.Host, "", "")
		if _, ok := updated[desc.Key]; !ok {
			updateIfKnown(desc)
		}
	}
}
```

Repo descriptors win over platform descriptors for the same provider host because repo descriptors carry repo-level `token_file`/`token_env` overrides and their fallback candidates. Unknown hosts are still handled by `resolveReposForReload` and mark `restart_required`.

- [ ] **Step 6: Run reload tests**

Run: `go test ./internal/server ./internal/workspace/localruntime -run 'ConfigReload|StripEnvVars|UpdateSettings' -shuffle=on`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/server internal/workspace/localruntime cmd/middleman/main.go
git commit -m "feat(server): hot-reload token source metadata"
```

---

## Task 7: End-To-End Coverage And Docs

**Files:**
- Create: `internal/server/e2etest/token_rotation_test.go`
- Modify: `README.md`
- Modify: `context/platform-sync-invariants.md`
- Optional if OpenAPI changes: `internal/apiclient/generated/client.gen.go`, frontend generated API schema files
- Test: `internal/server/e2etest/token_rotation_test.go`, affected package tests, and full short test target

This task adds the full-stack token-rotation test required for this user-visible auth/data-flow change, documents the new config, and runs the final checks.

- [x] **Step 1: Add a full-stack token-file rotation e2e test**

Create `internal/server/e2etest/token_rotation_test.go`. The implemented test
is named `TestTokenFileRotationE2EConfigStartupAndHTTPSync` and keeps the same
required shape: real config load, real SQLite DB, real Middleman HTTP API, real
syncer path, token file rotation via atomic rename, and auth-header assertions
before and after rotation.

```go
package e2etest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/config"
	ghclient "go.kenn.io/middleman/internal/github"
	gitlabclient "go.kenn.io/middleman/internal/platform/gitlab"
	"go.kenn.io/middleman/internal/server"
	"go.kenn.io/middleman/internal/testutil/dbtest"
	"go.kenn.io/middleman/internal/tokenauth"
)

func TestTokenFileRotationE2EConfigStartupAndHTTPSync(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "gitlab.token")
	require.NoError(os.WriteFile(tokenPath, []byte("token-secret-first\n"), 0o600))

	authHeaders := make(chan string, 8)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeaders <- r.Header.Get("Private-Token")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.EscapedPath(), "/projects/acme%2Fwidget/merge_requests"):
			_, _ = w.Write([]byte(`[]`))
		case strings.Contains(r.URL.EscapedPath(), "/projects/acme%2Fwidget"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 101, "path_with_namespace": "acme/widget", "name": "widget",
				"web_url": "https://gitlab.test/acme/widget",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()

	cfgPath := filepath.Join(dir, "config.toml")
	require.NoError(os.WriteFile(cfgPath, []byte(`
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[platforms]]
type = "gitlab"
host = "gitlab.test"
token_file = "`+tokenPath+`"

[[repos]]
owner = "acme"
name = "widget"
platform = "gitlab"
platform_host = "gitlab.test"
`), 0o600))
	cfg, err := config.Load(cfgPath)
	require.NoError(err)

	sourceSet := tokenauth.NewSourceSet(tokenauth.Options{})
	source := sourceSet.Upsert(cfg.TokenSourceForPlatformHost("gitlab", "gitlab.test", "", ""))
	providerClient, err := gitlabclient.NewClient(
		"gitlab.test", source,
		gitlabclient.WithBaseURLForTesting(provider.URL+"/api/v4"),
	)
	require.NoError(err)

	database := dbtest.Open(t)
	registry, err := ghclient.NewProviderRegistry(nil, providerClient)
	require.NoError(err)
	_, resolved, err := ghclient.ResolveConfiguredRepoWithRegistry(t.Context(), registry, cfg.Repos[0])
	require.NoError(err)
	drainAuthHeaders(authHeaders)
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, resolved, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)

	middleman := server.NewWithConfig(
		database, syncer, nil, nil, cfg, cfgPath,
		server.ServerOptions{TokenSources: sourceSet},
	)
	ts := httptest.NewServer(middleman)
	defer ts.Close()

	resp := doServerJSON(t, ts.Client(), http.MethodPost, ts.URL+"/api/v1/sync", nil)
	resp.Body.Close()
	require.Equal(http.StatusOK, resp.StatusCode)
	assert.Equal("token-secret-first", readNextAuthHeader(t, authHeaders))
	drainAuthHeaders(authHeaders)

	tmp := tokenPath + ".tmp"
	require.NoError(os.WriteFile(tmp, []byte("token-secret-second\n"), 0o600))
	require.NoError(os.Rename(tmp, tokenPath))

	resp = doServerJSON(t, ts.Client(), http.MethodPost, ts.URL+"/api/v1/sync", nil)
	resp.Body.Close()
	require.Equal(http.StatusOK, resp.StatusCode)
	assert.Equal("token-secret-second", readNextAuthHeader(t, authHeaders))
}

func readNextAuthHeader(t *testing.T, headers <-chan string) string {
	t.Helper()
	select {
	case header := <-headers:
		return header
	case <-time.After(2 * time.Second):
		require.FailNow(t, "timed out waiting for provider auth header")
		return ""
	}
}

func drainAuthHeaders(headers <-chan string) {
	for {
		select {
		case <-headers:
		default:
			return
		}
	}
}
```

The test must keep this shape: real config load, real SQLite DB, real Middleman HTTP API, real syncer path, token file rotation via atomic rename, and auth-header assertion before and after rotation. If the final GitLab parser requires additional fields, add only the missing fields shown by the failing parser error and keep the provider fixture minimal.

- [x] **Step 2: Run the new e2e test**

Run: `go test ./internal/server/e2etest -run TestTokenFileRotationE2EConfigStartupAndHTTPSync -shuffle=on`
Expected: PASS.

- [x] **Step 3: Update README token docs**

In `README.md`, update the token sections to say:

```markdown
Tokens can come from `token_file`, `token_env`, exact public-host defaults, or the GitHub CLI fallback. Use `token_file` when you need rotation without restarting Middleman: write the new token to a temporary file, then atomically rename it over the configured path. Middleman reads token files on demand and trims surrounding whitespace.

Public-host defaults are: GitHub `github.com` uses `github_token_env`, defaulting to `MIDDLEMAN_GITHUB_TOKEN`, then GitHub CLI fallback; GitLab `gitlab.com` has no implicit default env var; Forgejo `codeberg.org` uses `MIDDLEMAN_FORGEJO_TOKEN`; and Gitea `gitea.com` uses `MIDDLEMAN_GITEA_TOKEN`.

For a repo or platform entry, `token_file` is checked before `token_env`. Empty token files and empty env vars are treated as absent so the next configured fallback can still supply a token.
```

Add an example:

```toml
[[platforms]]
type = "gitlab"
host = "gitlab.com"
token_file = "~/.config/middleman/tokens/gitlab.com"
```

- [x] **Step 4: Update provider invariant docs**

In `context/platform-sync-invariants.md`, replace the token boundary paragraph with:

```markdown
Provider tokens are scoped by `(platform, platform_host)`. Token lookup checks repo `token_file`, repo `token_env`, platform `token_file`, platform `token_env`, then exact public-host defaults. GitHub `github.com` uses `github_token_env`, defaulting to `MIDDLEMAN_GITHUB_TOKEN`, then GitHub CLI fallback; GitLab `gitlab.com` has no implicit default env var; Forgejo `codeberg.org` uses `MIDDLEMAN_FORGEJO_TOKEN`; and Gitea `gitea.com` uses `MIDDLEMAN_GITEA_TOKEN`. Token files are read lazily so atomic file replacement rotates credentials without rebuilding provider clients. Provider token caches and auth transports must stay keyed by `(platform, platform_host)`. Git clone credentials are selected by URL host, so startup rejects same-host provider entries unless their effective clone token-source descriptors match before passing a host-keyed source map to the clone manager.
```

- [x] **Step 5: Regenerate API artifacts only if Huma schemas changed**

If adding `token_file` changed the OpenAPI output, run:

`make api-generate`

Expected: generated Go and frontend API schema files update cleanly. If no API schema changes are detected, do not run or commit generated artifacts. The generated paths are `frontend/openapi/openapi.yaml`, `packages/ui/src/api/generated/schema.ts`, `packages/ui/src/api/generated/client.ts`, and `internal/apiclient/generated/client.gen.go`.

Completed status: no Huma schema changed, so generated artifacts were not
regenerated.

- [x] **Step 6: Run focused verification**

Original planned command: `go test ./internal/tokenauth ./internal/config ./cmd/middleman ./internal/github ./internal/platform/gitlab ./internal/platform/forgejo ./internal/platform/gitea ./internal/gitclone ./internal/server ./internal/server/e2etest ./internal/workspace/localruntime -shuffle=on`
Expected: PASS.

Completed with the affected package set after review-fix changes:
`go test . ./internal/tokenauth ./internal/config ./cmd/middleman ./internal/github ./internal/platform/gitlab ./internal/platform/forgejo ./internal/platform/gitea ./internal/gitclone ./internal/server ./internal/server/e2etest -short -shuffle=on`
passed. The adjacent localruntime sanitizer checks
`TestManagerLaunchPassesStripEnvVarsToPtyOwner` and
`TestManagerUpdateStripEnvVarsAffectsFutureLaunches` also passed.

- [x] **Step 7: Run project short test target**

Run: `make test-short`
Original expected result: PASS.

Observed during review-fix verification: the target was run and failed only in
`internal/workspace/localruntime`
`TestTmuxLauncherCopiesClientEnvWithoutGlobalUpdateEnvironment`, which timed
out waiting for a tmux output file. Tokenauth, config, server, server e2e,
provider, clone, and root packages passed.

- [x] **Step 8: Inspect logs/errors for sentinel leakage**

Run: `rg -n "ghp_sentinel|glpat_sentinel|plain-provider-secret|token-secret|Authorization: Bearer|Private-Token|x-access-token:[^@]+@" internal cmd docs`
Expected: only tests and docs references to sentinel values appear; no production log messages or error strings contain token values or auth headers.

Observed during review-fix verification: the production scan found only the
README placeholder token, the provider transport header assignment, and older
plan prose about auth-header test expectations. No production log or error
message prints token values.

- [x] **Step 9: Commit**

```bash
git add internal/server/e2etest/token_rotation_test.go README.md context/platform-sync-invariants.md
git commit -m "test: cover refreshable provider tokens end to end"
```

If `make api-generate` produced schema changes, include the generated files in
the same commit after inspecting that they contain only the expected
`token_file` schema updates.

Completed by the final review-fix commit for roborev job 5865.

---

## Self-Review Notes

- Spec coverage: config `token_file`, precedence, path normalization, provider-host conflict checks, token source lazy reads, GitHub CLI cache invalidation, dynamic provider transports, clone auth, config reload, runtime sanitizer updates, redaction tests, full-stack e2e coverage, docs, and final verification are all mapped to tasks above.
- Placeholder scan: no deferred-work markers or vague error-handling steps remain. Each task has concrete files, code snippets, commands, and expected outcomes.
- Type consistency: `tokenauth.Key`, `tokenauth.Descriptor`, `tokenauth.Candidate`, `tokenauth.Source`, `tokenauth.ManagedSource`, and `tokenauth.SourceSet` are introduced before later tasks use them. Provider/API auth and config reload use provider-host keys; clone auth uses a host-keyed source map only after startup validates that same-host provider descriptors match.
