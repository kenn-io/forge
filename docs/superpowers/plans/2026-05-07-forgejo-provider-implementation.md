# Forgejo And Gitea Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Forgejo and Gitea as sibling Middleman providers, alongside the existing GitHub and GitLab providers, with read sync, repo import, provider-aware routing, and a documented post-MVP path to GitHub-parity mutations.

**Architecture:** Reuse the existing `internal/platform` capability model. GitHub remains the compatibility baseline for user-visible PR behavior, GitLab remains the template for provider-neutral startup, registry, pagination, and persistence wiring. Forgejo and Gitea share a substantial `internal/platform/gitealike` implementation for provider methods, pagination, DTO normalization, rate/error mapping, and capability defaults; `internal/platform/forgejo` and `internal/platform/gitea` are thin SDK adapters plus true divergences such as Forgejo Actions.

**Tech Stack:** Go, SQLite, Huma/OpenAPI, `codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3` v3.0.0, `code.gitea.io/sdk/gitea` v0.25.0 or current pinned release, Svelte 5, Bun, Forgejo and Gitea container e2e fixtures, provider-aware Go API tests with real SQLite.

---

## Research Basis

- Forgejo documents Gitea API compatibility through its version scheme. Forgejo versions include `+gitea-<version>` metadata, and the `/api/v1/version` endpoint can be used to inspect the compatible Gitea version. Source: https://forgejo.org/docs/latest/user/versions/
- Forgejo API authentication accepts OAuth2 bearer tokens and token query parameters. Middleman should use an access token from an environment variable and send it through the SDK token option, not via query strings. Source: https://forgejo.org/docs/latest/user/api-usage/
- Forgejo scoped tokens require explicit scopes. Read-only sync needs `read:repository` for repositories, pull requests, releases, tags, files, and statuses, plus `read:issue` for issues, labels, milestones, and issue or PR comments under issue routes. Notification import would additionally need `read:notification`. Comment, issue, review, merge, or state mutations need the corresponding write scopes. Source: https://forgejo.org/docs/latest/user/token-scope/
- Forgejo provides official container images at `codeberg.org/forgejo/forgejo:<major>` and documents Docker Compose installation. Use those images for optional local e2e coverage. Source: https://forgejo.org/docs/latest/admin/installation/docker/
- The Forgejo SDK choice is `codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3`. `go list -m -json codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3@latest` reports `v3.0.0`, published March 5, 2026. It exposes `NewClient(url, options...)`, `SetToken`, `SetHTTPClient`, `SetUserAgent`, repository, pull, issue/comment, release/tag, status, and Forgejo-specific Actions APIs such as `ListRepoActionRuns`, `ListRepoActionTasks`, `ListRepoActionJobs`, `ListRepoActionVariables`, and `DispatchRepoWorkflow`.
- The Gitea SDK choice is `code.gitea.io/sdk/gitea`, published as `v0.25.0` on May 5, 2026. It exposes `NewClient(url, options...)`, `SetToken`, `SetHTTPClient`, `SetUserAgent`, repository APIs such as `GetRepo`, pull APIs such as `ListRepoPullRequests`, issue/comment APIs such as `ListRepoIssues` and `ListRepoIssueComments`, release/tag APIs, status APIs, and `Response.NextPage` pagination. Source: https://pkg.go.dev/code.gitea.io/sdk/gitea
- Gitea Docker docs currently show `docker.gitea.com/gitea:1.26.1` as a specific image example and document scoped API tokens with `read:repository`, `read:issue`, `read:notification`, and related scopes. Source: https://docs.gitea.com/installation/install-with-docker and https://docs.gitea.com/development/api-usage
- `go list -m -versions code.gitea.io/sdk/gitea` currently reports versions through `v0.25.0`; pin the exact release in `go.mod` when implementation starts.

## Core Decisions

- Provider kinds: add `platform.KindForgejo` with string value `"forgejo"` and `platform.KindGitea` with string value `"gitea"`.
- Default hosts: use `codeberg.org` for Forgejo and `gitea.com` for Gitea. Self-hosted instances remain first-class through `platform_host`.
- Metadata: `AllowNestedOwner=false` for both Forgejo and Gitea; both use `owner/repo` identities, unlike GitLab nested namespaces.
- Case handling: do not lower-case configured Forgejo or Gitea owner/name during config normalization. Preserve API-returned canonical owner, repo name, and full name in persisted display fields. Existing DB lookup keys may remain case-folded where already provider-neutral, but provider normalization should not rewrite display values.
- Token envs: introduce `MIDDLEMAN_FORGEJO_TOKEN` and `MIDDLEMAN_GITEA_TOKEN` as documented public-host defaults, but make token resolution host-specific. Federated and self-hosted Forgejo/Gitea deployments must be configured with one `[[platforms]]` entry per `(type, host)` pair and may use a different `token_env` per hostname. Do not share a Forgejo token across `codeberg.org` and a self-hosted Forgejo instance unless the user explicitly points both hosts at the same env var. Do not add a `gh auth token` or `tea` fallback for either provider.
- SDK split: use `codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3` for Forgejo and `code.gitea.io/sdk/gitea` for Gitea. Do not route Forgejo through the Gitea SDK; the overlap is real, but Forgejo-specific Actions APIs and future divergence should be visible in code.
- Shared code: make `internal/platform/gitealike` the default home for shared behavior. It should own the common provider implementation, shared DTO structs, pagination loops, read-method orchestration, repo import fallback, state/status normalization, dedupe keys, rate-limit parsing, error mapping, and default read capabilities. Provider packages should adapt SDK structs into `gitealike` DTOs or implement a narrow transport interface; they should not duplicate method loops unless an endpoint truly diverges.
- Adapter boundary: do not put either SDK's concrete structs into `gitealike`. Instead, each provider package maps SDK records into shared DTOs like `RepositoryDTO`, `PullRequestDTO`, `IssueDTO`, `CommentDTO`, `ReleaseDTO`, `TagDTO`, `StatusDTO`, and optionally `ActionRunDTO`. This keeps common behavior highly reused without letting one SDK leak into the other provider.
- API base URL: construct each SDK client with `https://<platform_host>` for normal use, plus `WithBaseURLForTesting` for container and `httptest` coverage. Both SDKs append API routes internally.
- First milestone capabilities: read repositories, open pull requests as `platform.MergeRequest`, open issues, comments/timeline where available, releases, tags, and commit statuses for both providers. Keep comment mutation, issue mutation, state mutation, review mutation, and merge mutation disabled in the MVP; Task 7 enables them later only after tests prove the SDK methods work against that provider.
- MVP acceptance boundary: the first releasable Forgejo/Gitea implementation is read-only plus repo import and settings UI. Tasks 1 through 6 plus Task 8 define the MVP. Task 7 is an explicit post-MVP parity task unless the implementer intentionally chooses to keep working before merge and proves each mutation with provider tests and optional container fixtures. No Forgejo or Gitea mutation capability should be true in the MVP.
- CI mapping: start with commit statuses through `ListStatuses(owner, repo, sha, opts)` for both providers. Add Forgejo Actions read support through the Forgejo SDK as a separate capability-backed enhancement; add Gitea Actions only if the Gitea SDK and live fixture prove equivalent behavior.
- Repo import: support exact owner/org repo listing through `ListUserRepos` or `ListOrgRepos` style SDK methods, with a user-first then org fallback similar to GitLab group/user fallback. Do not support nested group glob semantics for Forgejo or Gitea.
- Local e2e: add optional Forgejo and Gitea fixtures parallel to `scripts/e2e/gitlab`, using SQLite inside each container for speed and bootstrap scripts that seed a user/org, repository, branch, pull request, issue, label, comments, tag, release, and commit status. Forgejo's fixture should additionally seed or query Actions data when testing Forgejo-only Actions support.

## GitHub Provider Feature Parity Target

GitHub is the user-visible behavior baseline for these providers. Forgejo and Gitea should expose the same Middleman capability whenever the provider SDK and a real or `httptest` fixture prove the operation works. A missing or incompatible endpoint is acceptable only when the provider reports the capability as false and the server/UI tests prove the action is hidden or returns `unsupported_capability`.

Rows marked `Target yes` for mutations are post-MVP parity targets, not MVP acceptance requirements.

| GitHub provider feature | Forgejo target | Gitea target | Implementation spec |
| --- | --- | --- | --- |
| `ReadRepositories` | Yes | Yes | Shared `gitealike.Provider.GetRepository` and `ListRepositories`; SDK adapters cover user-first then org fallback. |
| `ReadMergeRequests` | Yes | Yes | Shared pull request listing/get/detail normalization into `platform.MergeRequest`, including draft/WIP detection only after SDK field behavior is proven. |
| `ReadIssues` | Yes | Yes | Shared issue listing/get normalization, with tests that exclude pull requests from issue lists when the API returns mixed issue records. |
| `ReadComments` | Yes | Yes | Shared issue-comment, pull-review, and pull-commit event import where endpoints exist. Any missing GitHub timeline event kinds must be documented in tests and normalized to the closest stable Middleman event type instead of silently inventing data. |
| `ReadReleases` | Yes | Yes | Shared release and tag readers with SDK pagination and normalization. |
| `ReadCI` | Yes | Yes | Both providers start with commit statuses. Forgejo may merge Actions runs into CI checks through Forgejo SDK Actions APIs. Gitea remains status-only until the Gitea SDK and fixture prove equivalent Actions data. |
| `CommentMutation` / `platform.CommentMutator` | Target yes | Target yes | Use shared comment mutation over issue-comment APIs for PR and issue comments. Capability is true only after create/edit comment tests pass for that provider. |
| `StateMutation` / `platform.StateMutator` | Target yes | Target yes | Use shared close/reopen orchestration over issue and pull request edit endpoints. Capability is true only after both issue and PR state tests pass. |
| `MergeMutation` / `platform.MergeMutator` | Target yes | Target yes | Use provider merge-pull-request endpoints and map Middleman merge methods to provider method values in tested tables. |
| `ReviewMutation` / `platform.ReviewMutator` | Target yes | Target yes | Enable approval review only after the SDK and fixture prove the provider accepts approval reviews and returns data that can normalize to `MergeRequestEvent`. |
| `IssueMutation` / `platform.IssueMutator` | Target yes | Target yes | Use shared create issue orchestration and tests for title/body creation. |
| `MergeRequestContentMutator` | Target yes | Target yes | There is no separate `platform.Capabilities` bool today; server PR title/body routes are gated by `state_mutation` and then require this interface from the registry. Tests must cover both the capability flag and the interface lookup. |
| `WorkflowApproval` / `platform.WorkflowApprovalMutator` | Research before enabling | Research before enabling | GitHub has a first-class workflow approval path. Forgejo Actions exposes run/task/job data, but approval mutation must remain false until an approval endpoint is proven. Gitea must also remain false until an equivalent endpoint is proven. |
| `ReadyForReview` / `platform.ReadyForReviewMutator` | Research before enabling | Research before enabling | GitHub has a first-class ready-for-review mutation. Forgejo/Gitea should only set this true if a stable draft-to-ready endpoint or SDK field exists; title-prefix edits are not enough unless the provider documents them as the supported mechanism and tests prove behavior. |

Capability tests must compare this table against `Capabilities()` for Forgejo and Gitea. For every true mutation capability, tests must also assert the provider implements the corresponding `platform.*Mutator` interface. For every false capability, server API tests must assert `unsupported_capability` with the provider kind and host.

## File Structure

- Modify `internal/platform/types.go`: add `KindForgejo`, `KindGitea`, `DefaultForgejoHost`, and `DefaultGiteaHost`.
- Modify `internal/platform/metadata.go`: add Forgejo and Gitea metadata, kind aliases, and tests.
- Create `internal/platform/gitealike/types.go`: SDK-free DTOs and transport interfaces shared by Forgejo and Gitea.
- Create `internal/platform/gitealike/provider.go`: common `platform.Provider`, `RepositoryReader`, `MergeRequestReader`, `IssueReader`, `ReleaseReader`, `TagReader`, and `CIReader` implementation over the shared transport interface.
- Create `internal/platform/gitealike/normalize.go`: convert shared DTOs into `platform.Repository`, `platform.MergeRequest`, `platform.Issue`, events, releases, tags, and CI checks.
- Create `internal/platform/gitealike/client.go`: common foreground timeout, pagination, rate-limit parsing, error mapping, owner/repo validation, dedupe keys, and repo import fallback helpers.
- Create `internal/platform/gitealike/*_test.go`: unit coverage for shared provider behavior, normalization, pagination, capability defaults, GitHub-parity expectations, and error/rate mapping.
- Create `internal/platform/forgejo/client.go`: Forgejo SDK client construction, options, auth, and a transport adapter that satisfies `gitealike.Transport`; include Forgejo Actions helpers for the Forgejo-only extension points.
- Create `internal/platform/forgejo/convert.go`: map `codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3` repository, pull request, issue, comment, review, release, tag, status, and action run types into `gitealike` DTOs.
- Create `internal/platform/forgejo/client_test.go`: `httptest` coverage for auth header shape, base URL, SDK-to-transport behavior, and Forgejo Actions divergence.
- Create `internal/platform/forgejo/convert_test.go`: deterministic unit coverage for Forgejo SDK struct conversion into shared DTOs, including action runs.
- Create `internal/platform/gitea/client.go`: Gitea SDK client construction, options, auth, and a transport adapter that satisfies `gitealike.Transport`.
- Create `internal/platform/gitea/convert.go`: map `code.gitea.io/sdk/gitea` repository, pull request, issue, comment, review, release, tag, and status types into `gitealike` DTOs.
- Create `internal/platform/gitea/client_test.go`: `httptest` coverage for auth header shape, base URL, and SDK-to-transport behavior.
- Create `internal/platform/gitea/convert_test.go`: deterministic unit coverage for Gitea SDK struct conversion into shared DTOs.
- Modify `cmd/middleman/provider_startup.go`: register the Forgejo and Gitea factories and create rate trackers and clone tokens using the existing provider host key model.
- Modify `internal/config/config.go` and `internal/config/config_test.go`: add Forgejo and Gitea defaults, URL parsing, token env behavior, duplicate detection, host validation, and config examples.
- Modify `internal/server/repo_import_handlers.go`, `internal/server/settings_test.go`, and any generated route tests only where provider lists or defaults are hard-coded.
- Modify `frontend/src/lib/components/settings/repoImportProviders.ts` and tests: expose Forgejo and Gitea in the import modal with default hosts `codeberg.org` and `gitea.com`.
- Modify `README.md` and `config.example.toml`: document Forgejo and Gitea config, token scopes, and supported capabilities.
- Create `scripts/e2e/forgejo/docker-compose.yml`: optional Forgejo service bound to loopback.
- Create `scripts/e2e/forgejo/wait.sh`: readiness probe for the Forgejo UI and `/api/v1/version`.
- Create `scripts/e2e/forgejo/bootstrap.sh`: idempotent fixture seeding and manifest output.
- Create `scripts/e2e/forgejo/README.md`: usage, image tag policy, cleanup, and resource notes.
- Create `internal/server/forgejo_container_e2e_test.go`: gated optional full-stack Forgejo test with real SQLite.
- Create `scripts/e2e/gitea/docker-compose.yml`: optional Gitea service bound to loopback.
- Create `scripts/e2e/gitea/wait.sh`: readiness probe for the Gitea UI and `/api/v1/version`.
- Create `scripts/e2e/gitea/bootstrap.sh`: idempotent fixture seeding and manifest output.
- Create `scripts/e2e/gitea/README.md`: usage, image tag policy, cleanup, and resource notes.
- Create `internal/server/gitea_container_e2e_test.go`: gated optional full-stack Gitea test with real SQLite.
- Regenerate generated API artifacts with `make api-generate` only if Huma route schemas change. Adding a provider option alone should not require route shape changes.

## Task 1: Provider Metadata And Config Defaults

**Files:**
- Modify: `internal/platform/types.go`
- Modify: `internal/platform/metadata.go`
- Modify: `internal/platform/metadata_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write failing metadata tests**

Add assertions to `TestProviderMetadataForBuiltIns`:

```go
forgejo, ok := MetadataFor(KindForgejo)
require.True(t, ok)
assert.Equal(KindForgejo, forgejo.Kind)
assert.Equal("Forgejo", forgejo.Label)
assert.Equal(DefaultForgejoHost, forgejo.DefaultHost)
assert.False(forgejo.AllowNestedOwner)
assert.False(forgejo.LowercaseRepoNames)

gitea, ok := MetadataFor(KindGitea)
require.True(t, ok)
assert.Equal(KindGitea, gitea.Kind)
assert.Equal("Gitea", gitea.Label)
assert.Equal(DefaultGiteaHost, gitea.DefaultHost)
assert.False(gitea.AllowNestedOwner)
assert.False(gitea.LowercaseRepoNames)
```

Add assertions to `TestNormalizeKindAllowsFutureProviderKinds`:

```go
fj, err := NormalizeKind("fj")
require.NoError(t, err)
assert.Equal(KindForgejo, fj)

forgejo, err := NormalizeKind("Forgejo")
require.NoError(t, err)
assert.Equal(KindForgejo, forgejo)

tea, err := NormalizeKind("tea")
require.NoError(t, err)
assert.Equal(KindGitea, tea)

gitea, err := NormalizeKind("Gitea")
require.NoError(t, err)
assert.Equal(KindGitea, gitea)
```

- [ ] **Step 2: Run metadata tests and confirm failure**

Run:

```bash
go test ./internal/platform -run 'TestProviderMetadataForBuiltIns|TestNormalizeKindAllowsFutureProviderKinds' -shuffle=on
```

Expected: fails because `KindForgejo`, `KindGitea`, `DefaultForgejoHost`, and `DefaultGiteaHost` are undefined.

- [ ] **Step 3: Implement metadata**

In `internal/platform/types.go`, add:

```go
KindForgejo Kind = "forgejo"
KindGitea   Kind = "gitea"
```

and:

```go
DefaultForgejoHost = "codeberg.org"
DefaultGiteaHost   = "gitea.com"
```

In `internal/platform/metadata.go`, add:

```go
KindForgejo: {
	Kind:             KindForgejo,
	Label:            "Forgejo",
	DefaultHost:      DefaultForgejoHost,
	AllowNestedOwner: false,
},
KindGitea: {
	Kind:             KindGitea,
	Label:            "Gitea",
	DefaultHost:      DefaultGiteaHost,
	AllowNestedOwner: false,
},
```

and extend `NormalizeKind`:

```go
case "fj":
	return KindForgejo, nil
case "tea":
	return KindGitea, nil
```

- [ ] **Step 4: Write failing config tests**

Add tests to `internal/config/config_test.go`:

```go
func TestLoadPlatformConfigForgejoToken(t *testing.T) {
	cfg := loadConfigFromString(t, `
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

	assert.Equal(t, "forgejo", cfg.Platforms[0].Type)
	assert.Equal(t, "codeberg.org", cfg.Platforms[0].Host)
	assert.Equal(t, "forgejo", cfg.Repos[0].Platform)
	assert.Equal(t, "codeberg.org", cfg.Repos[0].PlatformHost)
	assert.Equal(t, "forgejo-secret", cfg.TokenForPlatformHost("forgejo", "codeberg.org", ""))
}
```

```go
func TestLoadForgejoDefaultHostUsesDefaultTokenEnv(t *testing.T) {
	cfg := loadConfigFromString(t, `
[[repos]]
platform = "forgejo"
platform_host = "codeberg.org"
owner = "forgejo"
name = "forgejo"
`)
	t.Setenv("MIDDLEMAN_FORGEJO_TOKEN", "codeberg-secret")

	assert.Equal(t, "codeberg-secret", cfg.TokenForPlatformHost("forgejo", "codeberg.org", ""))
	assert.Empty(t, cfg.TokenForPlatformHost("forgejo", "forgejo.example.com", ""))
}
```

```go
func TestLoadPlatformConfigForgejoTokensAreHostScoped(t *testing.T) {
	cfg := loadConfigFromString(t, `
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

	assert.Equal(t, "codeberg-secret", cfg.TokenForPlatformHost("forgejo", "codeberg.org", ""))
	assert.Equal(t, "self-hosted-secret", cfg.TokenForPlatformHost("forgejo", "forgejo.example.com", ""))
}
```

```go
func TestLoadParsesForgejoCodebergURL(t *testing.T) {
	cfg := loadConfigFromString(t, `
[[repos]]
platform = "forgejo"
name = "https://codeberg.org/forgejo/forgejo.git"
`)

	require.Len(t, cfg.Repos, 1)
	assert.Equal(t, "forgejo", cfg.Repos[0].Platform)
	assert.Equal(t, "codeberg.org", cfg.Repos[0].PlatformHost)
	assert.Equal(t, "forgejo", cfg.Repos[0].Owner)
	assert.Equal(t, "forgejo", cfg.Repos[0].Name)
	assert.Equal(t, "forgejo/forgejo", cfg.Repos[0].RepoPath)
}
```

```go
func TestLoadPlatformConfigGiteaToken(t *testing.T) {
	cfg := loadConfigFromString(t, `
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

	assert.Equal(t, "gitea", cfg.Platforms[0].Type)
	assert.Equal(t, "gitea.com", cfg.Platforms[0].Host)
	assert.Equal(t, "gitea", cfg.Repos[0].Platform)
	assert.Equal(t, "gitea.com", cfg.Repos[0].PlatformHost)
	assert.Equal(t, "gitea-secret", cfg.TokenForPlatformHost("gitea", "gitea.com", ""))
}
```

```go
func TestLoadGiteaDefaultHostUsesDefaultTokenEnv(t *testing.T) {
	cfg := loadConfigFromString(t, `
[[repos]]
platform = "gitea"
platform_host = "gitea.com"
owner = "gitea"
name = "tea"
`)
	t.Setenv("MIDDLEMAN_GITEA_TOKEN", "gitea-public-secret")

	assert.Equal(t, "gitea-public-secret", cfg.TokenForPlatformHost("gitea", "gitea.com", ""))
	assert.Empty(t, cfg.TokenForPlatformHost("gitea", "gitea.internal.example", ""))
}
```

```go
func TestLoadPlatformConfigGiteaTokensAreHostScoped(t *testing.T) {
	cfg := loadConfigFromString(t, `
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

	assert.Equal(t, "gitea-public-secret", cfg.TokenForPlatformHost("gitea", "gitea.com", ""))
	assert.Equal(t, "gitea-internal-secret", cfg.TokenForPlatformHost("gitea", "gitea.internal.example", ""))
}
```

```go
func TestLoadParsesGiteaURL(t *testing.T) {
	cfg := loadConfigFromString(t, `
[[repos]]
platform = "gitea"
name = "https://gitea.com/gitea/tea.git"
`)

	require.Len(t, cfg.Repos, 1)
	assert.Equal(t, "gitea", cfg.Repos[0].Platform)
	assert.Equal(t, "gitea.com", cfg.Repos[0].PlatformHost)
	assert.Equal(t, "gitea", cfg.Repos[0].Owner)
	assert.Equal(t, "tea", cfg.Repos[0].Name)
	assert.Equal(t, "gitea/tea", cfg.Repos[0].RepoPath)
}
```

Add regression tests that existing GitHub and GitLab config parsing is unchanged:

```go
func TestLoadKeepsExistingGitHubURLInference(t *testing.T) {
	cfg := loadConfigFromString(t, `
[[repos]]
name = "https://github.com/wesm/middleman.git"
`)

	require.Len(t, cfg.Repos, 1)
	assert.Equal(t, "github", cfg.Repos[0].Platform)
	assert.Equal(t, "github.com", cfg.Repos[0].PlatformHost)
	assert.Equal(t, "wesm", cfg.Repos[0].Owner)
	assert.Equal(t, "middleman", cfg.Repos[0].Name)
	assert.Equal(t, "wesm/middleman", cfg.Repos[0].RepoPath)
}
```

```go
func TestLoadKeepsExistingGitLabURLInference(t *testing.T) {
	cfg := loadConfigFromString(t, `
[[repos]]
name = "https://gitlab.com/gitlab-org/gitlab.git"
`)

	require.Len(t, cfg.Repos, 1)
	assert.Equal(t, "gitlab", cfg.Repos[0].Platform)
	assert.Equal(t, "gitlab.com", cfg.Repos[0].PlatformHost)
	assert.Equal(t, "gitlab-org", cfg.Repos[0].Owner)
	assert.Equal(t, "gitlab", cfg.Repos[0].Name)
	assert.Equal(t, "gitlab-org/gitlab", cfg.Repos[0].RepoPath)
}
```

Update `TestTokenEnvNamesIncludesGlobalAndPerRepo` so platform-scoped token env vars are stripped from launched agent environments too:

```go
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
	assert.Equal(t, []string{
		"WORK_GH_BOT_TOKEN",
		"MIDDLEMAN_FORGEJO_TOKEN",
		"FORGEJO_EXAMPLE_TOKEN",
		"GITEA_INTERNAL_TOKEN",
		"ACME_TOKEN",
		"THIRD_TOKEN",
	}, cfg.TokenEnvNames())
}
```

- [ ] **Step 5: Run config tests and confirm failure**

Run:

```bash
go test ./internal/config -run 'TestLoadPlatformConfigForgejoToken|TestLoadForgejoDefaultHostUsesDefaultTokenEnv|TestLoadPlatformConfigForgejoTokensAreHostScoped|TestLoadParsesForgejoCodebergURL|TestLoadPlatformConfigGiteaToken|TestLoadGiteaDefaultHostUsesDefaultTokenEnv|TestLoadPlatformConfigGiteaTokensAreHostScoped|TestLoadParsesGiteaURL|TestLoadKeepsExistingGitHubURLInference|TestLoadKeepsExistingGitLabURLInference|TestTokenEnvNamesIncludesGlobalPlatformAndPerRepo' -shuffle=on
```

Expected: fails until Forgejo and Gitea metadata and URL parsing are wired.

- [ ] **Step 6: Implement config support**

Update `Repo.normalize` so parsed Forgejo and Gitea URLs set `RepoPath = owner + "/" + name`, just like GitHub. Extend URL host inference so `codeberg.org` maps to Forgejo and `gitea.com` maps to Gitea when `platform` is omitted or explicitly set. Keep `TokenForPlatformHost` keyed by `(platform, host)`: `MIDDLEMAN_FORGEJO_TOKEN` should resolve only for `forgejo/codeberg.org`, `MIDDLEMAN_GITEA_TOKEN` should resolve only for `gitea/gitea.com`, and additional federated hosts should resolve only through matching `[[platforms]]` entries or repo-level `token_env` overrides. Update `TokenEnvNames` to include `Platforms[].TokenEnv` so host-scoped Forgejo/Gitea tokens are stripped from launched agent environments alongside GitHub and repo-level tokens.

- [ ] **Step 7: Run tests**

Run:

```bash
go test ./internal/platform ./internal/config -shuffle=on
```

Expected: pass.

- [ ] **Step 8: Commit**

```bash
git add internal/platform/types.go internal/platform/metadata.go internal/platform/metadata_test.go internal/config/config.go internal/config/config_test.go
git commit -m "feat: recognize Forgejo and Gitea provider configuration" -m "Adds Forgejo and Gitea metadata and config parsing so Codeberg, Gitea.com, and self-hosted repos can be represented before provider sync is implemented."
```

## Task 2: Forgejo And Gitea SDK Client Skeletons And Auth

**Files:**
- Create: `internal/platform/forgejo/client.go`
- Create: `internal/platform/forgejo/client_test.go`
- Create: `internal/platform/gitea/client.go`
- Create: `internal/platform/gitea/client_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] **Step 1: Add SDK dependencies**

Run:

```bash
go get codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3@v3.0.0
go get code.gitea.io/sdk/gitea@v0.25.0
```

Expected: `go.mod` and `go.sum` include both `codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3` and `code.gitea.io/sdk/gitea`.

- [ ] **Step 2: Write failing client construction and auth tests**

Create `internal/platform/forgejo/client_test.go` with a test that starts `httptest.Server`, constructs:

```go
client, err := NewClient(
	"codeberg.test",
	"forgejo-token",
	WithBaseURLForTesting(server.URL),
)
```

and calls the package-private SDK transport method that fetches `owner/repo` directly through the Forgejo SDK. Do not call `GetRepository` from `platform.RepositoryReader` in Task 2; the shared `gitealike.Provider` does not exist until Task 3. The handler must assert the request path is `/api/v1/repos/owner/repo` and that the token is sent through an authorization header accepted by the Forgejo SDK. Accept either `Authorization: token forgejo-token` or `Authorization: Bearer forgejo-token` depending on the SDK output; lock the observed header into the test after the first failure.

Create `internal/platform/gitea/client_test.go` with the same test shape:

```go
client, err := NewClient(
	"gitea.test",
	"gitea-token",
	WithBaseURLForTesting(server.URL),
)
```

The Gitea test should call the package-private SDK transport method that fetches `owner/repo` directly through the Gitea SDK. The handler must also assert `/api/v1/repos/owner/repo` and lock down the Gitea SDK's observed auth header.

- [ ] **Step 3: Run the failing test**

Run:

```bash
go test ./internal/platform/forgejo ./internal/platform/gitea -run TestClientLooksUpRepositoryAndSendsToken -shuffle=on
```

Expected: packages do not exist.

- [ ] **Step 4: Implement `client.go` skeletons**

In `internal/platform/forgejo/client.go`, implement:

```go
package forgejo

type ClientOption func(*clientOptions)

type clientOptions struct {
	baseURL           string
	foregroundTimeout time.Duration
	rateTracker       *ratelimit.RateTracker
}

type Client struct {
	host              string
	baseURL           string
	transport         *transport
	api               *forgejosdk.Client
	foregroundTimeout time.Duration
}
```

`NewClient(host, token string, options ...ClientOption) (*Client, error)` should default `baseURL` to `https://` plus the normalized host, use an SDK alias such as `forgejosdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"`, call `forgejosdk.NewClient(baseURL, forgejosdk.SetToken(token), forgejosdk.SetUserAgent("middleman"))`, and attach `forgejosdk.SetHTTPClient` when rate tracking is configured. If version probing makes `httptest` setup awkward, use `forgejosdk.SetForgejoVersion("13.0.0+gitea-1.26.0")` or the SDK's exact version option in tests only. Wrap the SDK in a private `transport` type with only the minimal method used by the auth test, such as `getRepositoryRaw(ctx, owner, repo string)`.

In `internal/platform/gitea/client.go`, mirror the same structure with `package gitea` and an SDK alias such as `giteasdk "code.gitea.io/sdk/gitea"`. Use `giteasdk.NewClient(baseURL, giteasdk.SetToken(token), giteasdk.SetUserAgent("middleman"))` and `giteasdk.SetHTTPClient` for rate tracking. Wrap the SDK in a private `transport` type with only the minimal method used by the auth test, such as `getRepositoryRaw(ctx, owner, repo string)`.

- [ ] **Step 5: Implement provider identity with no read delegation yet**

Implement for Forgejo:

```go
func (c *Client) Platform() platform.Kind { return platform.KindForgejo }
func (c *Client) Host() string { return c.host }
func (c *Client) Capabilities() platform.Capabilities { return platform.Capabilities{} }
```

Implement the same pattern for Gitea:

```go
func (c *Client) Platform() platform.Kind { return platform.KindGitea }
func (c *Client) Host() string { return c.host }
func (c *Client) Capabilities() platform.Capabilities { return platform.Capabilities{} }
```

Task 3 replaces the empty capability set with shared `gitealike.Provider` delegation and adds forwarding methods for every shared read interface. Task 2 intentionally avoids temporary read-provider code.

- [ ] **Step 6: Run tests**

Run:

```bash
go test ./internal/platform/forgejo ./internal/platform/gitea -shuffle=on
```

Expected: pass for construction and capability tests.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/platform/forgejo/client.go internal/platform/forgejo/client_test.go internal/platform/gitea/client.go internal/platform/gitea/client_test.go
git commit -m "feat: add Forgejo and Gitea SDK client skeletons" -m "Introduces separate SDK-backed Forgejo and Gitea clients with host-scoped auth and rate-tracking hooks before shared read behavior is added."
```

## Task 3: Shared Gitea-Like Core And SDK Conversion

**Files:**
- Create: `internal/platform/gitealike/types.go`
- Create: `internal/platform/gitealike/provider.go`
- Create: `internal/platform/gitealike/normalize.go`
- Create: `internal/platform/gitealike/client.go`
- Create: `internal/platform/gitealike/provider_test.go`
- Create: `internal/platform/gitealike/normalize_test.go`
- Create: `internal/platform/gitealike/client_test.go`
- Create: `internal/platform/forgejo/convert.go`
- Create: `internal/platform/forgejo/convert_test.go`
- Create: `internal/platform/gitea/convert.go`
- Create: `internal/platform/gitea/convert_test.go`

- [ ] **Step 1: Write normalization tests**

In `internal/platform/gitealike/normalize_test.go`, cover the shared DTO fields once and verify provider-neutral outputs:

- `Repository.ID`, `Repository.Owner.UserName`, `Repository.Name`, `Repository.FullName`, `Repository.HTMLURL`, `Repository.CloneURL`, `Repository.DefaultBranch`, `Repository.Private`, `Repository.Archived`, `Repository.Description`, `Repository.Created`, `Repository.Updated`
- `PullRequest.ID`, `PullRequest.Index`, `PullRequest.HTMLURL`, `PullRequest.Title`, `PullRequest.User.UserName`, `PullRequest.State`, `PullRequest.IsLocked`, `PullRequest.Body`, `PullRequest.Head.Ref`, `PullRequest.Head.Sha`, `PullRequest.Base.Ref`, `PullRequest.Base.Sha`, `PullRequest.Labels`, `PullRequest.Created`, `PullRequest.Updated`, `PullRequest.Merged`, `PullRequest.MergedAt`, `PullRequest.Closed`
- `Issue.ID`, `Issue.Index`, `Issue.HTMLURL`, `Issue.Title`, `Issue.User.UserName`, `Issue.State`, `Issue.Body`, `Issue.Comments`, `Issue.Labels`, `Issue.Created`, `Issue.Updated`, `Issue.Closed`
- `Comment.ID`, `Comment.User.UserName`, `Comment.Body`, `Comment.Created`
- `Release.ID`, `Release.TagName`, `Release.Title`, `Release.HTMLURL`, `Release.Target`, `Release.Prerelease`, `Release.PublishedAt`, `Release.CreatedAt`
- `Tag.Name`, `Tag.Commit.SHA`
- `Status.ID`, `Status.Context`, `Status.State`, `Status.TargetURL`, `Status.Description`, `Status.Created`, `Status.Updated`

In `internal/platform/forgejo/convert_test.go`, cover conversion from Forgejo SDK structs into the shared DTOs, including `ActionRun.ID`, `ActionRun.WorkflowID`, `ActionRun.Title`, `ActionRun.Status`, `ActionRun.CommitSHA`, `ActionRun.HTMLURL`, `ActionRun.Started`, `ActionRun.Stopped`, and `ActionRun.NeedApproval`.

In `internal/platform/gitea/convert_test.go`, cover conversion from Gitea SDK structs into the same shared DTOs. The expected DTO values should match the Forgejo conversion tests for overlapping fields.

- [ ] **Step 2: Run the failing tests**

Run:

```bash
go test ./internal/platform/gitealike ./internal/platform/forgejo ./internal/platform/gitea -run 'TestNormalize|TestConvert|Test.*Provider|Test.*Helper' -shuffle=on
```

Expected: fails because shared DTOs, normalizers, provider, and SDK converters are missing.

- [ ] **Step 3: Implement shared DTOs, transport, and helpers**

In `internal/platform/gitealike/types.go` and `client.go`, implement SDK-free DTOs and transport interfaces:

```go
package gitealike

type Transport interface {
	GetRepository(ctx context.Context, owner, repo string) (RepositoryDTO, error)
	ListUserRepositories(ctx context.Context, owner string, opts PageOptions) ([]RepositoryDTO, Page, error)
	ListOrgRepositories(ctx context.Context, owner string, opts PageOptions) ([]RepositoryDTO, Page, error)
	ListOpenPullRequests(ctx context.Context, ref platform.RepoRef, opts PageOptions) ([]PullRequestDTO, Page, error)
	GetPullRequest(ctx context.Context, ref platform.RepoRef, number int) (PullRequestDTO, error)
	ListPullRequestComments(ctx context.Context, ref platform.RepoRef, number int, opts PageOptions) ([]CommentDTO, Page, error)
	ListPullRequestReviews(ctx context.Context, ref platform.RepoRef, number int, opts PageOptions) ([]ReviewDTO, Page, error)
	ListPullRequestCommits(ctx context.Context, ref platform.RepoRef, number int, opts PageOptions) ([]CommitDTO, Page, error)
	ListOpenIssues(ctx context.Context, ref platform.RepoRef, opts PageOptions) ([]IssueDTO, Page, error)
	GetIssue(ctx context.Context, ref platform.RepoRef, number int) (IssueDTO, error)
	ListIssueComments(ctx context.Context, ref platform.RepoRef, number int, opts PageOptions) ([]CommentDTO, Page, error)
	ListReleases(ctx context.Context, ref platform.RepoRef, opts PageOptions) ([]ReleaseDTO, Page, error)
	ListTags(ctx context.Context, ref platform.RepoRef, opts PageOptions) ([]TagDTO, Page, error)
	ListStatuses(ctx context.Context, ref platform.RepoRef, sha string, opts PageOptions) ([]StatusDTO, Page, error)
}

type ActionsTransport interface {
	ListActionRuns(ctx context.Context, ref platform.RepoRef, sha string, opts PageOptions) ([]ActionRunDTO, Page, error)
}

func NormalizeState(state string) string
func OwnerRepoPath(owner, name string) string
func NoteDedupeKey(kind platform.Kind, host string, repoPath string, parentKind string, number int, eventKind string, externalID string) string
func NormalizeCommitStatus(state string) (status string, conclusion string)
func NextPage(next int) int
```

Define DTO structs for repository, pull request, issue, comment, review, commit, release, tag, status, and action run fields listed in Step 1. Keep this package free of imports from either SDK.

- [ ] **Step 4: Implement the shared provider and normalizers**

Implement in `internal/platform/gitealike/provider.go`:

```go
type Provider struct {
	kind      platform.Kind
	host      string
	transport Transport
	options   Options
}

type Options struct {
	ReadActions bool
}

func NewProvider(kind platform.Kind, host string, transport Transport, options Options) *Provider
```

`Provider` should implement all shared read interfaces by calling the transport, paginating centrally, normalizing DTOs centrally, and returning typed platform errors centrally.

Implement in `internal/platform/gitealike/normalize.go`:

```go
func NormalizeRepository(kind platform.Kind, host string, repo RepositoryDTO) (platform.Repository, error)
func NormalizePullRequest(repo platform.RepoRef, pr PullRequestDTO) platform.MergeRequest
func NormalizeIssue(repo platform.RepoRef, issue IssueDTO) platform.Issue
func NormalizeIssueComments(kind platform.Kind, repo platform.RepoRef, number int, comments []CommentDTO) []platform.IssueEvent
func NormalizeMergeRequestEvents(kind platform.Kind, repo platform.RepoRef, number int, comments []CommentDTO, reviews []ReviewDTO, commits []CommitDTO) []platform.MergeRequestEvent
func NormalizeRelease(repo platform.RepoRef, release ReleaseDTO) platform.Release
func NormalizeTag(repo platform.RepoRef, tag TagDTO) platform.Tag
func NormalizeStatuses(repo platform.RepoRef, statuses []StatusDTO, actionRuns []ActionRunDTO) []platform.CICheck
```

Use `platform.KindForgejo` or `platform.KindGitea`, `host`, `owner/name`, and `owner/name` as `RepoPath`. Use `strconv.FormatInt(id, 10)` for `PlatformExternalID` when the API only provides numeric IDs. Convert `open` and `closed` states directly; map merged pull requests to `"merged"` when the SDK exposes merged state or merged timestamps.

- [ ] **Step 5: Implement thin SDK converters**

Implement in `internal/platform/forgejo/convert.go`:

```go
func convertRepository(repo *forgejosdk.Repository) (gitealike.RepositoryDTO, error)
func convertPullRequest(pr *forgejosdk.PullRequest) gitealike.PullRequestDTO
func convertIssue(issue *forgejosdk.Issue) gitealike.IssueDTO
func convertComment(comment *forgejosdk.Comment) gitealike.CommentDTO
func convertRelease(release *forgejosdk.Release) gitealike.ReleaseDTO
func convertTag(tag *forgejosdk.Tag) gitealike.TagDTO
func convertStatus(status *forgejosdk.Status) gitealike.StatusDTO
func convertActionRun(run *forgejosdk.ActionRun) gitealike.ActionRunDTO
```

Implement in `internal/platform/gitea/convert.go` the same converter names with `giteasdk` concrete types, excluding `convertActionRun` unless Gitea Actions support is proven.

- [ ] **Step 6: Wire provider delegation and read capabilities**

Update `internal/platform/forgejo/client.go` so `Client` owns the shared provider:

```go
type Client struct {
	host              string
	baseURL           string
	provider          *gitealike.Provider
	transport         *transport
	api               *forgejosdk.Client
	foregroundTimeout time.Duration
}
```

In `NewClient`, pass the private transport to:

```go
gitealike.NewProvider(platform.KindForgejo, host, transport, gitealike.Options{ReadActions: true})
```

and delegate capabilities and read methods:

```go
func (c *Client) Capabilities() platform.Capabilities { return c.provider.Capabilities() }
func (c *Client) GetRepository(ctx context.Context, ref platform.RepoRef) (platform.Repository, error) {
	return c.provider.GetRepository(ctx, ref)
}
```

Update `internal/platform/gitea/client.go` in the same way, using:

```go
gitealike.NewProvider(platform.KindGitea, host, transport, gitealike.Options{})
```

Add forwarding methods for every shared read interface as each interface is enabled. Leave mutation capabilities false until Task 7. This keeps both packages thin and forces shared behavior through `gitealike`.

- [ ] **Step 7: Run tests**

Run:

```bash
go test ./internal/platform/gitealike ./internal/platform/forgejo ./internal/platform/gitea -shuffle=on
```

Expected: pass.

- [ ] **Step 8: Commit**

```bash
git add internal/platform/gitealike internal/platform/forgejo/convert.go internal/platform/forgejo/convert_test.go internal/platform/gitea/convert.go internal/platform/gitea/convert_test.go
git commit -m "feat: share Forgejo and Gitea provider core" -m "Adds a shared gitea-like provider core and keeps Forgejo and Gitea packages focused on SDK conversion and true endpoint divergence."
```

## Task 4: Forgejo And Gitea Read APIs, Pagination, And Error Mapping

**Files:**
- Modify: `internal/platform/forgejo/client.go`
- Modify: `internal/platform/forgejo/client_test.go`
- Modify: `internal/platform/gitea/client.go`
- Modify: `internal/platform/gitea/client_test.go`

- [ ] **Step 1: Write failing read API tests**

Add most read API tests in `internal/platform/gitealike/provider_test.go` against a fake `Transport` so pagination and behavior are proven once:

- `GetRepository` calls `GetRepo(owner, repo)`.
- `ListRepositories` tries user repositories first and organization repositories second, returning only repos matching the requested owner.
- `ListOpenMergeRequests` calls `ListRepoPullRequests` with open state and paginates until `Response.NextPage == 0`.
- `GetMergeRequest` calls `GetPullRequest`.
- `ListMergeRequestEvents` combines `ListRepoIssueComments`, `ListPullReviews`, and `ListPullRequestCommits` where available.
- `ListOpenIssues` calls `ListRepoIssues` with open state and excludes records that are pull requests if the SDK marks them as pull requests.
- `GetIssue` calls `GetIssue`.
- `ListIssueEvents` calls issue comments and maps them to issue events.
- `ListReleases`, `ListTags`, and `ListCIChecks` paginate.
- Forgejo `ListCIChecks` optionally merges commit statuses with Forgejo Actions runs when the Forgejo SDK endpoint is available.
- HTTP 404 maps to `platform.ErrRepoNotFound` or a typed provider error equivalent used elsewhere.
- HTTP 401 and 403 map to a typed provider auth/scope error that preserves provider kind, host, status code, and a message suitable for sync failure display. Insufficient token scopes must not be collapsed into generic repo-not-found or pagination errors.

Add smaller provider-package `httptest` cases only for SDK-specific request shape:

- Forgejo transport sends the expected paths, query parameters, pagination values, and token header through the Forgejo SDK.
- Gitea transport sends the expected paths, query parameters, pagination values, and token header through the Gitea SDK.
- Forgejo transport implements `gitealike.ActionsTransport`; Gitea transport does not unless a proven Gitea Actions endpoint is added.
- Forgejo and Gitea transports expose enough response metadata for `gitealike` to distinguish 401/403 insufficient-scope failures from missing resources.

- [ ] **Step 2: Run focused failing tests**

Run:

```bash
go test ./internal/platform/forgejo ./internal/platform/gitea -run 'TestClient' -shuffle=on
```

Expected: fails for unimplemented shared provider and transport methods.

- [ ] **Step 3: Implement pagination helpers**

Add SDK-specific response adapters in the provider packages and keep the pagination loop and auth/scope error mapping in `gitealike.Provider`.

```go
const defaultPageSize = 100

func nextForgejoPage(resp *forgejosdk.Response) int {
	if resp == nil {
		return 0
	}
	return resp.NextPage
}
```

```go
func nextGiteaPage(resp *giteasdk.Response) int {
	if resp == nil {
		return 0
	}
	return resp.NextPage
}
```

Every transport list call should accept `gitealike.PageOptions` and translate it to the relevant SDK's `ListOptions{Page: 1, PageSize: defaultPageSize}` or current equivalent. Update the exact option field names after `go test` compiles against the pinned SDK versions. Error adapters should classify 401 and 403 responses as the typed provider auth/scope error used by sync logs.

- [ ] **Step 4: Implement read methods**

Implement all read interfaces from `internal/platform/client.go` once on `gitealike.Provider`. The Forgejo and Gitea `Client` types should forward those methods to the shared provider. Keep unsupported optional data as empty slices, not errors, when an SDK or server returns 404 for a feature that does not exist on a repository.

- [ ] **Step 5: Run provider tests**

Run:

```bash
go test ./internal/platform/gitealike ./internal/platform/forgejo ./internal/platform/gitea -shuffle=on
```

Expected: pass.

- [ ] **Step 6: Commit**

```bash
git add internal/platform/gitealike internal/platform/forgejo/client.go internal/platform/forgejo/client_test.go internal/platform/gitea/client.go internal/platform/gitea/client_test.go
git commit -m "feat: read Forgejo and Gitea through shared provider core" -m "Completes shared read-side provider behavior with thin Forgejo and Gitea SDK transports for pagination, issue/comment sync, releases, tags, and CI status normalization."
```

## Task 5: Startup, Registry, Settings, And UI Provider List

**Files:**
- Modify: `cmd/middleman/provider_startup.go`
- Modify: `cmd/middleman/main_test.go`
- Modify: `internal/server/settings_test.go`
- Modify: `frontend/src/lib/components/settings/repoImportProviders.ts`
- Modify: `frontend/src/lib/components/settings/repoImportSelection.test.ts`
- Modify: `frontend/src/lib/components/settings/RepoImportModal.test.ts`
- Modify: `frontend/tests/e2e-full/settings-globs.spec.ts`

- [ ] **Step 1: Write startup factory test**

Extend `TestBuildProviderStartupUsesRegisteredFactoryForFutureProvider` or add:

```go
func TestBuildProviderStartupRegistersForgejoAndGiteaFactories(t *testing.T) {
	// Configure Forgejo and Gitea platforms with token env vars and assert
	// the registry has provider capabilities under (forgejo, codeberg.org)
	// and (gitea, gitea.com).
}
```

Use a fake factory if the test should not instantiate a real SDK client.

Add a host-scoped token startup test:

```go
func TestCollectProviderTokensScopesForgeTokensByHost(t *testing.T) {
	cfg := config.Config{
		Host: "127.0.0.1",
		Port: 8091,
		Platforms: []config.PlatformConfig{
			{Type: "forgejo", Host: "codeberg.org", TokenEnv: "MIDDLEMAN_FORGEJO_TOKEN"},
			{Type: "forgejo", Host: "forgejo.example.com", TokenEnv: "FORGEJO_EXAMPLE_TOKEN"},
			{Type: "gitea", Host: "gitea.com", TokenEnv: "MIDDLEMAN_GITEA_TOKEN"},
			{Type: "gitea", Host: "gitea.internal.example", TokenEnv: "GITEA_INTERNAL_TOKEN"},
		},
		Repos: []config.Repo{
			{Platform: "forgejo", PlatformHost: "codeberg.org", Owner: "forgejo", Name: "forgejo"},
			{Platform: "forgejo", PlatformHost: "forgejo.example.com", Owner: "team", Name: "service"},
			{Platform: "gitea", PlatformHost: "gitea.com", Owner: "gitea", Name: "tea"},
			{Platform: "gitea", PlatformHost: "gitea.internal.example", Owner: "team", Name: "service"},
		},
	}
	t.Setenv("MIDDLEMAN_FORGEJO_TOKEN", "codeberg-token")
	t.Setenv("FORGEJO_EXAMPLE_TOKEN", "forgejo-example-token")
	t.Setenv("MIDDLEMAN_GITEA_TOKEN", "gitea-token")
	t.Setenv("GITEA_INTERNAL_TOKEN", "gitea-internal-token")

	providerTokens, err := collectProviderTokens(&cfg)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		providerHostKey("forgejo", "codeberg.org"):         "codeberg-token",
		providerHostKey("forgejo", "forgejo.example.com"):  "forgejo-example-token",
		providerHostKey("gitea", "gitea.com"):              "gitea-token",
		providerHostKey("gitea", "gitea.internal.example"): "gitea-internal-token",
	}, providerTokens)
}
```

- [ ] **Step 2: Run focused startup tests**

Run:

```bash
go test ./cmd/middleman -run 'TestBuildProviderStartup|TestResolveStartupRepos' -shuffle=on
```

Expected: fails until both factories are registered.

- [ ] **Step 3: Wire default factory**

Import `github.com/wesm/middleman/internal/platform/forgejo` as `forgejoclient` and add a factory under `platform.KindForgejo` that calls:

```go
forgejoclient.NewClient(
	input.host,
	input.token,
	forgejoclient.WithRateTracker(input.rateTracker),
)
```

Import `github.com/wesm/middleman/internal/platform/gitea` as `giteaclient` and add a factory under `platform.KindGitea` that calls:

```go
giteaclient.NewClient(
	input.host,
	input.token,
	giteaclient.WithRateTracker(input.rateTracker),
)
```

- [ ] **Step 4: Add UI import provider**

Add to `repoImportProviders`:

```ts
{
  id: "forgejo",
  label: "Forgejo",
  defaultHost: "codeberg.org",
  allowNestedOwner: false,
  ownerPatternPlaceholder: "owner/pattern",
},
{
  id: "gitea",
  label: "Gitea",
  defaultHost: "gitea.com",
  allowNestedOwner: false,
  ownerPatternPlaceholder: "owner/pattern",
}
```

- [ ] **Step 5: Run tests**

Run:

```bash
go test ./cmd/middleman ./internal/server -run 'TestBuildProviderStartup|TestHandlePreviewRepos|TestHandleBulkAddRepos' -shuffle=on
bun test frontend/src/lib/components/settings/repoImportSelection.test.ts frontend/src/lib/components/settings/RepoImportModal.test.ts
bun --cwd frontend run test:e2e -- tests/e2e-full/settings-globs.spec.ts
```

Expected: pass. Extend `settings-globs.spec.ts` before running it so the browser test covers selecting Forgejo and Gitea, default host population, and owner pattern behavior.

- [ ] **Step 6: Commit**

```bash
git add cmd/middleman/provider_startup.go cmd/middleman/main_test.go internal/server/settings_test.go frontend/src/lib/components/settings/repoImportProviders.ts frontend/src/lib/components/settings/repoImportSelection.test.ts frontend/src/lib/components/settings/RepoImportModal.test.ts frontend/tests/e2e-full/settings-globs.spec.ts
git commit -m "feat: wire Forgejo and Gitea provider startup" -m "Registers Forgejo and Gitea with the provider registry and exposes both providers in repository import settings."
```

## Task 6: Optional Forgejo And Gitea Container E2E Fixtures

**Files:**
- Create: `scripts/e2e/forgejo/docker-compose.yml`
- Create: `scripts/e2e/forgejo/wait.sh`
- Create: `scripts/e2e/forgejo/bootstrap.sh`
- Create: `scripts/e2e/forgejo/README.md`
- Create: `internal/server/forgejo_container_e2e_test.go`
- Create: `scripts/e2e/gitea/docker-compose.yml`
- Create: `scripts/e2e/gitea/wait.sh`
- Create: `scripts/e2e/gitea/bootstrap.sh`
- Create: `scripts/e2e/gitea/README.md`
- Create: `internal/server/gitea_container_e2e_test.go`

- [ ] **Step 1: Write gated e2e test shell**

Create `internal/server/forgejo_container_e2e_test.go` with:

```go
if os.Getenv("MIDDLEMAN_FORGEJO_CONTAINER_TESTS") != "1" {
	t.Skip("set MIDDLEMAN_FORGEJO_CONTAINER_TESTS=1 to run Forgejo container e2e")
}
```

The test should start the compose stack, run the bootstrap script, load the manifest, instantiate `platform/forgejo.NewClient` with the manifest `base_url`, sync one configured repo through Middleman, and assert one pull request, one issue, one release, one tag, and one CI check are persisted. Do not pass manifest `api_url` to `NewClient`; the SDK appends `/api/v1` internally.

Create `internal/server/gitea_container_e2e_test.go` with:

```go
if os.Getenv("MIDDLEMAN_GITEA_CONTAINER_TESTS") != "1" {
	t.Skip("set MIDDLEMAN_GITEA_CONTAINER_TESTS=1 to run Gitea container e2e")
}
```

The Gitea test should start the compose stack, run the bootstrap script, load the manifest, instantiate `platform/gitea.NewClient` with the manifest `base_url`, sync one configured repo through Middleman, and assert one pull request, one issue, one release, one tag, and one CI check are persisted. Do not pass manifest `api_url` to `NewClient`; the SDK appends `/api/v1` internally.

- [ ] **Step 2: Add compose fixture**

Use for Forgejo:

```yaml
services:
  forgejo:
    image: ${MIDDLEMAN_FORGEJO_IMAGE:-codeberg.org/forgejo/forgejo:13}
    ports:
      - "127.0.0.1:${FORGEJO_HTTP_PORT:-13000}:3000"
    environment:
      USER_UID: "1000"
      USER_GID: "1000"
      FORGEJO__server__ROOT_URL: "http://localhost:3000/"
      FORGEJO__security__INSTALL_LOCK: "true"
      FORGEJO__service__DISABLE_REGISTRATION: "true"
      FORGEJO__database__DB_TYPE: "sqlite3"
    volumes:
      - forgejo-data:/data

volumes:
  forgejo-data:
```

Adjust env names if the image requires app.ini injection for the pinned version.

Use for Gitea:

```yaml
services:
  gitea:
    image: ${MIDDLEMAN_GITEA_IMAGE:-docker.gitea.com/gitea:1.26.1}
    ports:
      - "127.0.0.1:${GITEA_HTTP_PORT:-13001}:3000"
    environment:
      USER_UID: "1000"
      USER_GID: "1000"
      GITEA__server__ROOT_URL: "http://localhost:3000/"
      GITEA__security__INSTALL_LOCK: "true"
      GITEA__service__DISABLE_REGISTRATION: "true"
      GITEA__database__DB_TYPE: "sqlite3"
    volumes:
      - gitea-data:/data

volumes:
  gitea-data:
```

- [ ] **Step 3: Add bootstrap**

Each `bootstrap.sh` should create an admin token or use a pre-seeded admin account, then idempotently create:

- owner `middleman-fixture`
- repo `project-special`
- branch `feature/forgejo`
- file commit on feature branch
- pull request from feature branch to main
- issue with label
- comments on both PR and issue
- tag `v1.0.0`
- release for `v1.0.0`
- commit status for the feature SHA

Write a manifest JSON with `base_url`, `api_url`, `token`, `owner`, `repo`, `repo_path`, `pull_number`, `issue_number`, and `head_sha`.

Use manifest `base_url` for SDK-backed Middleman clients and reserve manifest `api_url` for bootstrap scripts or raw setup calls only.

The Forgejo bootstrap should also create or query enough Actions state to verify `ListRepoActionRuns` when Actions are enabled in the container. The Gitea bootstrap should not assert Forgejo-only Actions behavior.

- [ ] **Step 4: Run optional e2e locally**

Run:

```bash
MIDDLEMAN_FORGEJO_CONTAINER_TESTS=1 go test ./internal/server -run TestForgejoContainerSync -shuffle=on
MIDDLEMAN_GITEA_CONTAINER_TESTS=1 go test ./internal/server -run TestGiteaContainerSync -shuffle=on
```

Expected: pass on machines with Docker available. The default test suite must still skip this test.

- [ ] **Step 5: Commit**

```bash
git add scripts/e2e/forgejo scripts/e2e/gitea internal/server/forgejo_container_e2e_test.go internal/server/gitea_container_e2e_test.go
git commit -m "test: cover Forgejo and Gitea sync against real instances" -m "Adds optional Forgejo and Gitea container fixtures so provider API compatibility can be validated outside the default fast suite."
```

## Task 7: GitHub-Parity Mutations And Capability Gating

**Files:**
- Modify: `internal/platform/gitealike/provider.go`
- Modify: `internal/platform/gitealike/provider_test.go`
- Modify: `internal/platform/forgejo/client.go`
- Modify: `internal/platform/forgejo/client_test.go`
- Modify: `internal/platform/gitea/client.go`
- Modify: `internal/platform/gitea/client_test.go`
- Modify: `internal/server/api_test.go`
- Modify: `frontend/tests/e2e-full/provider-capabilities.spec.ts`

- [ ] **Step 1: Prove supported mutation endpoints**

Add `httptest` coverage for SDK calls equivalent to:

- `CreateIssueComment` for PR comments and issue comments.
- `EditIssueComment` for editable comments.
- `EditIssue` for close/reopen issue state.
- `EditPullRequest` for close/reopen pull request state and title/body edits.
- `CreateIssue` for issue creation.
- `CreatePullReview` and submit review for approval if the provider accepts approval reviews.
- `MergePullRequest` for merge support.
- Workflow approval endpoints only if the provider exposes a stable approval mutation.
- Ready-for-review endpoints only if the provider exposes a stable draft-to-ready mutation.
- Forgejo-only: `ListRepoActionRuns` and related action endpoints for action-derived CI and future workflow handling.

- [ ] **Step 2: Implement only proven mutators**

Put shared mutation orchestration in `gitealike.Provider` behind optional transport interfaces:

- `CommentTransport` for `platform.CommentMutator`
- `StateTransport` for `platform.StateMutator`
- `MergeTransport` for `platform.MergeMutator`
- `ReviewTransport` for `platform.ReviewMutator`
- `IssueMutationTransport` for `platform.IssueMutator`
- `MergeRequestContentTransport` for `platform.MergeRequestContentMutator`
- `WorkflowApprovalTransport` for `platform.WorkflowApprovalMutator`
- `ReadyForReviewTransport` for `platform.ReadyForReviewMutator`

Set capability flags only for methods implemented and tested against that provider's `httptest` suite and optional container fixture. If approval or ready-for-review does not map cleanly, leave `WorkflowApproval` or `ReadyForReview` false. Do not copy Forgejo Actions capability flags onto Gitea unless the Gitea SDK and fixture prove equivalent behavior.

- [ ] **Step 3: Add provider parity tests**

Add shared capability tests in `internal/platform/gitealike/provider_test.go`:

- Assert Forgejo and Gitea read capabilities match the GitHub baseline for repositories, merge requests, issues, comments, releases, and CI.
- Assert each true mutation capability has the corresponding `platform.*Mutator` interface implemented by the provider.
- Assert `MergeRequestContentMutator` is present whenever the provider supports PR title/body edits, even though there is no separate capability bool.
- Assert `WorkflowApproval` and `ReadyForReview` stay false until the concrete provider implements the matching registry interface and passes endpoint tests.

- [ ] **Step 4: Add server capability tests**

Extend provider capability tests to assert Forgejo-supported and Gitea-supported actions are visible and unsupported actions remain hidden or return typed capability errors.

- [ ] **Step 5: Run tests**

Run:

```bash
go test ./internal/platform/gitealike ./internal/platform/forgejo ./internal/platform/gitea ./internal/server -run 'Test.*Forgejo|Test.*Gitea|Test.*Capability|Test.*Parity|Test.*Comment|Test.*Merge' -shuffle=on
bun test frontend/tests/e2e-full/provider-capabilities.spec.ts
```

Expected: pass. If the Playwright command needs the dev server, use the repo's existing e2e-full invocation instead of a standalone file run.

- [ ] **Step 6: Commit**

```bash
git add internal/platform/gitealike/provider.go internal/platform/gitealike/provider_test.go internal/platform/forgejo/client.go internal/platform/forgejo/client_test.go internal/platform/gitea/client.go internal/platform/gitea/client_test.go internal/server/api_test.go frontend/tests/e2e-full/provider-capabilities.spec.ts
git commit -m "feat: enable supported Forgejo and Gitea actions" -m "Adds provider mutations only for API operations validated by provider tests and keeps unsupported actions behind typed capability errors."
```

## Task 8: Documentation And Final Verification

**Files:**
- Modify: `README.md`
- Modify: `config.example.toml`
- Modify: `context/platform-sync-invariants.md`
- Modify: `frontend/openapi/openapi.yaml` only if generated API schemas changed
- Modify: `internal/apiclient/generated/client.gen.go` only if generated API schemas changed
- Modify: `packages/ui/src/api/generated/*` only if generated API schemas changed

- [ ] **Step 1: Document config**

Add:

```toml
[[platforms]]
type = "forgejo"
host = "codeberg.org"
token_env = "MIDDLEMAN_FORGEJO_TOKEN"

[[repos]]
platform = "forgejo"
platform_host = "codeberg.org"
owner = "forgejo"
name = "forgejo"
repo_path = "forgejo/forgejo"

[[platforms]]
type = "forgejo"
host = "forgejo.example.com"
token_env = "FORGEJO_EXAMPLE_TOKEN"

[[repos]]
platform = "forgejo"
platform_host = "forgejo.example.com"
owner = "team"
name = "service"
repo_path = "team/service"

[[platforms]]
type = "gitea"
host = "gitea.com"
token_env = "MIDDLEMAN_GITEA_TOKEN"

[[repos]]
platform = "gitea"
platform_host = "gitea.com"
owner = "gitea"
name = "tea"
repo_path = "gitea/tea"

[[platforms]]
type = "gitea"
host = "gitea.internal.example"
token_env = "GITEA_INTERNAL_TOKEN"

[[repos]]
platform = "gitea"
platform_host = "gitea.internal.example"
owner = "team"
name = "service"
repo_path = "team/service"
```

Document that token lookup is per `(type, host)`. `MIDDLEMAN_FORGEJO_TOKEN` is the convenient Codeberg default, `MIDDLEMAN_GITEA_TOKEN` is the convenient Gitea.com default, and each federated/self-hosted host should normally get its own `[[platforms]] token_env`.

Document minimum read scopes for both providers:

```text
read:repository, read:issue
```

and mutation scopes:

```text
write:repository and/or write:issue, depending on the enabled action
```

- [ ] **Step 2: Regenerate API only if needed**

If any Huma schema or route type changed, run:

```bash
make api-generate
```

Expected: generated Go and TypeScript clients update consistently. If only provider metadata changed, skip this step and state why in the commit body.

- [ ] **Step 3: Run focused verification**

Run:

```bash
go test ./internal/platform ./internal/platform/forgejo ./internal/config ./cmd/middleman ./internal/server -shuffle=on
bun test frontend/src/lib/components/settings/repoImportSelection.test.ts frontend/src/lib/components/settings/RepoImportModal.test.ts
bun --cwd frontend run test:e2e -- tests/e2e-full/settings-globs.spec.ts
```

Expected: pass. The Playwright run is required because the provider dropdown is a visible settings workflow change.

- [ ] **Step 4: Run broader verification**

Run:

```bash
make test-short
```

Expected: pass.

If visible UI changed beyond the provider dropdown, broaden the Playwright e2e-full run to cover that additional workflow before pushing, per repository instructions.

- [ ] **Step 5: Commit**

```bash
git add README.md config.example.toml context/platform-sync-invariants.md frontend/openapi/openapi.yaml internal/apiclient/generated/client.gen.go packages/ui/src/api/generated
git commit -m "docs: describe Forgejo and Gitea provider setup" -m "Documents Forgejo and Gitea token scopes, public-host defaults, self-hosted configuration, and the verified capability set."
```

## Open Questions To Resolve During Implementation

- Does Forgejo `v13` accept `SetToken` as `Authorization: token ...`, or should Middleman wrap the SDK transport to send `Authorization: Bearer ...`? Lock this down in Task 2's auth header test.
- Does Gitea `v1.26.1` use the same token header through `code.gitea.io/sdk/gitea`, or does the SDK differ from the Forgejo SDK in auth header spelling?
- Do the SDKs expose draft status on pull requests in stable fields, or must `NormalizePullRequest` infer draft from title prefixes like `WIP:` and `Draft:`?
- Do Forgejo and Gitea issue list responses include pull requests, and if so which SDK field reliably distinguishes them? Task 4 must prevent PRs from duplicating into the issue list.
- Do Forgejo Actions runs provide better CI data than commit statuses for Middleman's UI, and should they be merged into `ReadCI` or exposed as a future distinct capability?
- Does Gitea's Actions API shape match Forgejo's for the subset Middleman cares about, or should Gitea remain status-only until a container fixture proves parity?
- Which Forgejo or Gitea endpoint, if any, approves pending workflow runs in a way that matches GitHub's `WorkflowApprovalMutator` contract?
- Which Forgejo or Gitea endpoint, if any, marks a draft pull request ready in a way that matches GitHub's `ReadyForReviewMutator` contract?
- Post-MVP mutation ordering remains a product decision, but it must not block the MVP. When Task 7 is picked up, implement mutations in this order unless user priorities change: comments, issue creation, PR content edits, state changes, merge, review approval, workflow approval, ready-for-review.

## Final Test Matrix

- `go test ./internal/platform ./internal/platform/gitealike ./internal/platform/forgejo ./internal/platform/gitea -shuffle=on`
- `go test ./internal/platform/gitealike ./internal/platform/forgejo ./internal/platform/gitea -run 'Test.*Parity|Test.*Capability' -shuffle=on`
- `go test ./internal/config -shuffle=on`
- `go test ./cmd/middleman -shuffle=on`
- `go test ./internal/server -run 'Test.*Forgejo|Test.*Gitea|Test.*Provider|Test.*Import|Test.*Capability' -shuffle=on`
- `bun test frontend/src/lib/components/settings/repoImportSelection.test.ts frontend/src/lib/components/settings/RepoImportModal.test.ts`
- `make test-short`
- Optional: `MIDDLEMAN_FORGEJO_CONTAINER_TESTS=1 go test ./internal/server -run TestForgejoContainerSync -shuffle=on`
- Optional: `MIDDLEMAN_GITEA_CONTAINER_TESTS=1 go test ./internal/server -run TestGiteaContainerSync -shuffle=on`

## Kata Task Graph

This plan has been converted into kata issues in project `github.com/wesm/middleman`.

- Epic: `#56` Forgejo and Gitea provider implementation epic.
- `#57` Add Forgejo/Gitea provider metadata and host-scoped config. Ready first; no implementation blockers.
- `#58` Add Forgejo/Gitea SDK client skeletons and auth probes. Blocked by `#57`.
- `#59` Build shared gitealike DTO and provider core. Blocked by `#58`.
- `#60` Add Forgejo and Gitea SDK converters. Blocked by `#59`.
- `#61` Implement Forgejo/Gitea read APIs and typed errors. Blocked by `#60`.
- `#62` Register forge providers at startup with per-host tokens. Blocked by `#57` and `#61`.
- `#63` Import repositories from Forgejo and Gitea providers. Blocked by `#57`, `#61`, and `#62`.
- `#64` Expose Forgejo/Gitea in repository import settings UI. Blocked by `#57` and `#63`.
- `#65` Add optional Forgejo and Gitea container e2e fixtures. Blocked by `#61` and `#62`.
- `#66` Document Forgejo/Gitea setup and platform invariants. Blocked by `#64` and `#65`.
- `#67` Enable proven Forgejo/Gitea mutation capabilities. Post-MVP; blocked by `#61` and `#62`.
- `#68` Evaluate Forgejo/Gitea Actions CI parity. Post-MVP; blocked by `#61` and `#65`.

The epic `#56` is blocked by the final MVP docs task `#66` and the two post-MVP parity tasks `#67` and `#68`, so it will not appear as implementation-ready while concrete work remains.

Use `kata ready --json` to find the next unblocked task. At creation time, the first implementation-ready issue is `#57`.

## Handoff Notes

- The branch for this stack is `forgejo-provider-impl`, but the branch scope now covers both Forgejo and Gitea.
- Git-spice tracks this branch on local `main`. Local `main` is currently behind `origin/main` in another worktree, so do not restack until the local trunk situation is intentionally resolved.
- Do not prefix follow-up branch names with `codex/`.
