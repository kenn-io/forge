# GitHub Owner PAT Routing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add configuration-only GitHub owner PAT routing while accounting rate limits and sync budgets once per `(GitHub host, authenticated identity)`.

**Architecture:** Build explicit GitHub credential routes with exact-repository, owner, and host-fallback precedence. Resolve every PAT route to a stable GitHub user ID at startup, share `RateTracker` and `SyncBudget` instances between routes with the same identity, and expose one routed GitHub client per host to the existing provider registry. Keep non-GitHub providers host-scoped.

**Tech Stack:** Go, TOML config, `go-github/v88`, `shurcooL/githubv4`, Huma, SQLite migrations, testify.

## Global Constraints

- Authorization precedence is exact repository override, then `(GitHub host, repository owner)`, then host fallback.
- Rate state and sync capacity are keyed by `(GitHub host, authenticated GitHub identity)`.
- Two PATs owned by the same GitHub user share one REST tracker, one GraphQL tracker, and one sync budget.
- GitHub App installation tokens use `installation:<installation_id>` identities for reads; user-visible writes remain on a PAT identity.
- Owner mappings are configuration-only; do not add frontend credential-management UI.
- Never persist or log PAT values, authorization headers, or reversible token hashes.
- Preserve existing single-token, per-repository token, GitHub CLI, and GitHub App behavior when no owner mapping is configured.
- Ownerless user APIs use the host fallback user credential; never choose an arbitrary owner PAT.
- A PAT rotated to a different GitHub user requires restart; do not move an active route between identities in-process.
- Add migration `000040`; never edit existing migrations (`000039` is the historical activity archive).
- Run direct Go tests with `-shuffle=on`, without `-v` or `-count=1`.

---

## File Structure

### New files

- `internal/config/github_owner_token_config_test.go` — owner-token parsing, validation, precedence, and save/load coverage.
- `internal/github/identity.go` — GitHub principal types and PAT `/user` identity resolution.
- `internal/github/identity_test.go` — identity parsing, deduplication inputs, and safe failure coverage.
- `internal/github/auth_router.go` — route keys, route selection, routed REST client, and route-specific GraphQL lookup.
- `internal/github/auth_router_test.go` — exact-repo/owner/fallback routing and shared-identity behavior.
- `cmd/middleman/provider_startup_identity_test.go` — startup construction of routes and identity runtimes.
- `internal/db/migrations/000040_identity_scoped_sync_state.up.sql` — principal-aware rate table.
- `internal/db/migrations/000040_identity_scoped_sync_state.down.sql` — lossy collapse to the previous schema.

### Modified files

- `internal/config/config.go` — `GitHubOwnerTokenConfig`, validation, persistence, token-plan generation, path normalization, and env stripping.
- `internal/tokenauth/descriptor.go` — secret-free route-scoped source keys.
- `internal/tokenauth/source.go` — unchanged resolution behavior, but route-keyed `SourceSet` entries and tests.
- `internal/github/client.go` — identity cache key exposure and routed-client delegation support.
- `internal/github/graphql.go` — route-specific fetchers and identity-keyed tracker exposure.
- `internal/github/rate.go` — principal-aware tracker construction and bucket keys.
- `internal/ratelimit/rate.go` — same principal-aware tracker implementation if this branch's alias package remains the source of truth; keep one implementation, not duplicated logic.
- `internal/db/types.go` — persist `RatePrincipal`.
- `internal/db/queries.go` — principal-aware upsert/get methods.
- `internal/db/db_test.go` — migration upgrade and integrity coverage.
- `internal/ratelimit/rate_test.go` — principal isolation and hydration coverage.
- `cmd/middleman/provider_startup.go` — resolve identities, deduplicate runtimes, create routed clients/fetchers, and build clone routes.
- `cmd/middleman/main.go` — pass route-aware runtime maps into the syncer and clone manager.
- `internal/github/sync.go` — identity bucket selection for cadence, budget, snapshots, GraphQL, and operation tracker lookup.
- `internal/github/sync_test.go` — shared-budget and isolated-identity sync tests.
- `internal/server/repo_import_handlers.go` — owner-aware preview client lookup.
- `internal/server/operation_availability.go` — repository read/write identity lookup.
- `internal/server/api_types.go` and `internal/server/huma_routes.go` — safe principal-aware rate status.
- `internal/server/api_test.go` — preview, operation availability, and rate response coverage.
- `internal/gitclone/clone.go` — exact-repo/owner/fallback credential selection for networked Git.
- `internal/gitclone/auth_test.go` — owner-specific clone/fetch authentication.
- `internal/workspace/manager.go` and `internal/workspace/branch_sync.go` — pass repository identity to networked Git operations.
- `config.example.toml`, `docs/configuration.md`, `docs/troubleshooting.md`, and `README.md` — advanced configuration documentation.
- `context/platform-sync-invariants.md` and `context/github-sync-invariants.md` — replace stale host-only GitHub accounting claims with the new invariant.

---

### Task 1: Add and validate owner-scoped GitHub token configuration

**Files:**
- Create: `internal/config/github_owner_token_config_test.go`
- Modify: `internal/config/config.go`
- Modify: `config.example.toml`

**Interfaces:**
- Produces:
  ```go
  type GitHubOwnerTokenConfig struct {
      Host      string `toml:"host,omitempty" json:"host,omitempty"`
      Owner     string `toml:"owner" json:"owner"`
      TokenEnv  string `toml:"token_env,omitempty" json:"token_env,omitempty"`
      TokenFile string `toml:"token_file,omitempty" json:"token_file,omitempty"`
  }

  func (c *Config) GitHubOwnerTokenFor(host, owner string) (GitHubOwnerTokenConfig, bool)
  func (c *Config) ResolveGitHubRepoTokenSource(repo Repo) tokenauth.Descriptor
  ```
- Consumers: startup route planning in Task 4 and managed Git route planning in Task 7.

- [ ] **Step 1: Write failing config parsing and validation tests**

Create table-driven tests covering default host normalization, case-insensitive duplicate owners, missing owner, owner containing `/` or glob syntax, missing both token fields, and file-before-env order:

```go
func TestGitHubOwnerTokensValidation(t *testing.T) {
    tests := []struct {
        name    string
        config  string
        wantErr string
    }{
        {
            name: "duplicate owner is case insensitive",
            config: `
[[github_owner_tokens]]
owner = "Acme"
token_env = "ACME_ONE"

[[github_owner_tokens]]
owner = "acme"
token_env = "ACME_TWO"
`,
            wantErr: `duplicate github owner token for host "github.com" and owner "acme"`,
        },
        {
            name: "credential is required",
            config: `
[[github_owner_tokens]]
owner = "acme"
`,
            wantErr: "token_file or token_env is required",
        },
    }
    // Load each config from t.TempDir(), require the expected error, and use
    // assert.New(t) once a case has more than three assertions.
}
```

Add a precedence test with repository override, App installation, owner token, platform token, `github_token_env`, and GitHub CLI candidates. Assert the exact safe candidate sequence for reads and the mutation-marked effective PAT sequence.

- [ ] **Step 2: Run the focused config tests and confirm failure**

Run:

```bash
go test ./internal/config -run 'TestGitHubOwnerTokens|TestResolveGitHubRepoTokenSource' -shuffle=on
```

Expected: FAIL because `github_owner_tokens` is not represented and the lookup/resolver methods do not exist.

- [ ] **Step 3: Implement the config model and validation**

Add `GitHubOwnerTokens []GitHubOwnerTokenConfig` to both `Config` and `configFile`. Normalize entries before repository token conflict checks:

```go
func (c *Config) validateGitHubOwnerTokens() error {
    seen := make(map[string]struct{}, len(c.GitHubOwnerTokens))
    for i := range c.GitHubOwnerTokens {
        item := &c.GitHubOwnerTokens[i]
        item.Owner = strings.TrimSpace(item.Owner)
        item.TokenEnv = strings.TrimSpace(item.TokenEnv)
        item.TokenFile = strings.TrimSpace(item.TokenFile)
        if item.Owner == "" {
            return fmt.Errorf("config: github_owner_tokens[%d]: owner is required", i)
        }
        if strings.Contains(item.Owner, "/") || strings.ContainsAny(item.Owner, "*?[") {
            return fmt.Errorf("config: github_owner_tokens[%d]: owner must be one exact GitHub owner", i)
        }
        host, err := normalizePlatformHost(defaultPlatform, item.Host)
        if err != nil {
            return fmt.Errorf("config: github_owner_tokens[%d]: %w", i, err)
        }
        item.Host = host
        if item.TokenFile == "" && item.TokenEnv == "" {
            return fmt.Errorf("config: github_owner_tokens[%d]: token_file or token_env is required", i)
        }
        key := strings.ToLower(host) + "\x00" + strings.ToLower(item.Owner)
        if _, ok := seen[key]; ok {
            return fmt.Errorf(
                "config: github_owner_tokens[%d]: duplicate github owner token for host %q and owner %q",
                i, host, strings.ToLower(item.Owner),
            )
        }
        seen[key] = struct{}{}
    }
    return nil
}
```

Include owner token files in `normalizeTokenFilePaths`, owner token env vars in `TokenEnvNames`, and owner-token rows in `copyForSave`/`Save`.

- [ ] **Step 4: Implement explicit repo/owner/fallback descriptor construction**

Keep `TokenSourceForPlatformHost` as the host fallback builder for all providers. Add a GitHub-specific resolver that inserts only the matching owner token between App and platform candidates and preserves repository override precedence:

```go
func (c *Config) ResolveGitHubRepoTokenSource(repo Repo) tokenauth.Descriptor {
    host := repo.PlatformHostOrDefault()
    desc := tokenauth.Descriptor{
        Key: tokenauth.Key{
            Platform: defaultPlatform,
            Host:     host,
            Scope:    githubRouteScope(repo.Owner, repo.Name, repo.TokenEnv != "" || repo.TokenFile != ""),
        },
    }
    appendRepoCandidates(&desc, repo.TokenFile, repo.TokenEnv)
    if repo.TokenFile == "" && repo.TokenEnv == "" {
        appendMatchingGitHubAppCandidates(&desc, c.GitHubAppsForHost(host), repo.Owner)
    }
    if ownerToken, ok := c.GitHubOwnerTokenFor(host, repo.Owner); ok {
        appendFileEnvCandidates(&desc, ownerToken.TokenFile, ownerToken.TokenEnv)
    }
    appendGitHubHostFallbackCandidates(&desc, c, host)
    return desc
}
```

Use exact-repository scope only when a repository override exists; otherwise use owner scope so multiple repos under one owner share a route.

- [ ] **Step 5: Relax only the obsolete GitHub same-host conflict rule**

Update config validation so different GitHub route descriptors on one host are permitted, while non-GitHub same-host clone conflicts remain rejected. Do not add a compatibility shim. Add a test proving two GitHub owners may use different PATs while two providers sharing a clone host still require identical fallback credentials.

- [ ] **Step 6: Run config tests**

Run:

```bash
go test ./internal/config -run 'TestGitHubOwnerTokens|TestResolveGitHubRepoTokenSource|TestTokenEnvNames|TestSave' -shuffle=on
```

Expected: PASS.

- [ ] **Step 7: Commit the config slice**

```bash
git add internal/config/config.go internal/config/github_owner_token_config_test.go config.example.toml
git commit -m "feat: configure GitHub PATs by repository owner" \
  -m "Fine-grained PATs are resource-owner scoped, so a host default cannot authorize every configured organization. Add explicit owner mappings while preserving repository overrides and existing host fallbacks."
```

---

### Task 2: Introduce route keys and resolve PATs to stable GitHub identities

**Files:**
- Modify: `internal/tokenauth/descriptor.go`
- Modify: `internal/tokenauth/descriptor_test.go`
- Modify: `internal/tokenauth/source_test.go`
- Create: `internal/github/identity.go`
- Create: `internal/github/identity_test.go`

**Interfaces:**
- Consumes: `Config.ResolveGitHubRepoTokenSource` from Task 1.
- Produces:
  ```go
  type tokenauth.Key struct {
      Platform string
      Host     string
      Scope    string
  }

  type IdentityKey struct {
      Host      string
      Principal string
  }

  type GitHubIdentity struct {
      Key   IdentityKey
      Login string
  }

  type IdentityResolver interface {
      // ResolvePAT returns the identity and the exact token it verified.
      // Startup binds routes to that verified token (BindSourceIdentity):
      // a later reload may rotate to a token for the same principal, but a
      // token resolving to a different user is rejected until restart, so a
      // live route can never silently move between identities in-process.
      ResolvePAT(context.Context, string, tokenauth.Source) (GitHubIdentity, string, error)
  }

  func InstallationIdentity(host string, installationID int64) GitHubIdentity
  ```

- [ ] **Step 1: Write failing route-key and identity tests**

Test that `tokenauth.Key.String()` includes `Scope`, `SourceSet` stores two same-host owner routes independently, and canonical source strings remain secret-free. Add identity resolver tests using `httptest.Server`:

```go
func TestHTTPIdentityResolverResolvesStableUserID(t *testing.T) {
    // Server asserts Authorization: Bearer token-a and returns
    // {"id":123,"login":"maintainer"}.
    got, token, err := resolver.ResolvePAT(t.Context(), "github.com", source)
    require.NoError(t, err)
    assert.Equal(t, IdentityKey{Host: "github.com", Principal: "user:123"}, got.Key)
    assert.Equal(t, "maintainer", got.Login)
    assert.Equal(t, "token-a", token)
}
```

Also test non-2xx responses, zero/missing ID, redacted errors, and `InstallationIdentity("github.com", 789)`.

- [ ] **Step 2: Run the focused tests and confirm failure**

Run:

```bash
go test ./internal/tokenauth ./internal/github -run 'Test.*(Scope|IdentityResolver|InstallationIdentity)' -shuffle=on
```

Expected: FAIL because scoped keys and identity resolver types do not exist.

- [ ] **Step 3: Add `Scope` to token source keys**

Update `Key.String`, sorting, clone keys, equality tests, and safe diagnostics:

```go
type Key struct {
    Platform string
    Host     string
    Scope    string
}

func (k Key) String() string {
    return strings.Join([]string{k.Platform, k.Host, k.Scope}, "\x00")
}
```

Use scope values such as `fallback`, `owner:<case-folded-owner>`, and `repo:<case-folded-owner>/<case-folded-name>`. Do not include env names, file contents, or token values in the scope.

- [ ] **Step 4: Implement the identity resolver**

Use an isolated HTTP client authenticated by the supplied `tokenauth.Source`. Do not use the route's sync budget transport:

```go
type HTTPIdentityResolver struct {
    NewHTTPClient func(host string, source tokenauth.Source) *http.Client
}

func (r HTTPIdentityResolver) ResolvePAT(
    ctx context.Context, host string, source tokenauth.Source,
) (GitHubIdentity, string, error) {
    client := r.NewHTTPClient(host, source)
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, authenticatedUserURL(host), nil)
    if err != nil {
        return GitHubIdentity{}, "", err
    }
    resp, err := client.Do(req)
    if err != nil {
        return GitHubIdentity{}, "", fmt.Errorf("resolve GitHub identity for %s via %s: %w", host, source.Descriptor().SafeString(), err)
    }
    defer resp.Body.Close()
    // Decode only id and login; require id > 0; map non-2xx responses to a
    // safe error that never includes response authorization data. Return the
    // exact verified token alongside the identity so startup can bind it.
}
```

Use the public API guard and host-specific API URL rules already used by `NewClient`.

- [ ] **Step 5: Add identity-safe cache key helpers**

Implement:

```go
func (k IdentityKey) String() string {
    return strings.ToLower(k.Host) + "\x00" + k.Principal
}

func InstallationIdentity(host string, installationID int64) GitHubIdentity {
    return GitHubIdentity{Key: IdentityKey{
        Host: normalizedPlatformHost(host),
        Principal: fmt.Sprintf("installation:%d", installationID),
    }}
}
```

Reject non-positive installation IDs in the startup builder rather than manufacturing an identity.

- [ ] **Step 6: Run tokenauth and identity tests**

Run:

```bash
go test ./internal/tokenauth ./internal/github -run 'Test.*(Scope|SourceSet|IdentityResolver|InstallationIdentity)' -shuffle=on
```

Expected: PASS.

- [ ] **Step 7: Commit identity primitives**

```bash
git add internal/tokenauth/descriptor.go internal/tokenauth/descriptor_test.go internal/tokenauth/source_test.go internal/github/identity.go internal/github/identity_test.go
git commit -m "feat: resolve GitHub credentials to stable identities" \
  -m "Authorization routes can differ while consuming one user-level GitHub allowance. Introduce scoped token sources and resolve PATs to immutable numeric user principals so later runtime accounting can deduplicate them safely."
```

---

### Task 3: Persist and hydrate rate state by principal

**Files:**
- Create: `internal/db/migrations/000040_identity_scoped_sync_state.up.sql`
- Create: `internal/db/migrations/000040_identity_scoped_sync_state.down.sql`
- Modify: `internal/db/types.go`
- Modify: `internal/db/queries.go`
- Modify: `internal/db/queries_notifications.go`
- Modify: `internal/db/db_test.go`
- Modify: `internal/ratelimit/rate.go`
- Modify: `internal/ratelimit/rate_test.go`
- Modify: `internal/github/rate.go` only if it is not an alias/re-export of `internal/ratelimit`; keep the principal logic in one package.

**Interfaces:**
- Consumes: `IdentityKey.Principal` from Task 2.
- Produces:
  ```go
  func NewPlatformRateTracker(
      database *db.DB,
      platformName, platformHost, ratePrincipal, apiType string,
  ) *RateTracker

  func RateBucketKey(platformName, platformHost, ratePrincipal string) string
  func (rt *RateTracker) Principal() string
  ```

- [ ] **Step 1: Write failing migration and query tests**

Build a previous-version SQLite fixture containing one GitHub and one GitLab rate row. Open it through `db.Open()` and assert:

- the new table has `rate_principal`;
- the GitLab row migrated with principal `host`;
- the unassignable GitHub row was dropped;
- `PRAGMA integrity_check` returns `ok`;
- `PRAGMA foreign_key_check` returns no rows.

Add query tests proving two rows with the same host/API type but different principals remain isolated.

- [ ] **Step 2: Run the focused DB tests and confirm failure**

Run:

```bash
go test ./internal/db ./internal/ratelimit -run 'Test.*(RatePrincipal|PrincipalIsolation|MigratesRate)' -shuffle=on
```

Expected: FAIL because the schema and query signatures are host-only.

- [ ] **Step 3: Add migration `000040`**

Use a SQLite table rebuild:

```sql
CREATE TABLE middleman_rate_limits_v40 (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    platform       TEXT NOT NULL DEFAULT 'github',
    platform_host  TEXT NOT NULL,
    rate_principal TEXT NOT NULL,
    api_type       TEXT NOT NULL DEFAULT 'rest',
    requests_hour  INTEGER NOT NULL DEFAULT 0,
    hour_start     DATETIME NOT NULL,
    rate_remaining INTEGER NOT NULL DEFAULT -1,
    rate_limit     INTEGER NOT NULL DEFAULT -1,
    rate_reset_at  DATETIME,
    updated_at     DATETIME NOT NULL DEFAULT (datetime('now')),
    UNIQUE(platform, platform_host, rate_principal, api_type)
);

INSERT INTO middleman_rate_limits_v40 (
    id, platform, platform_host, rate_principal, api_type,
    requests_hour, hour_start, rate_remaining, rate_limit,
    rate_reset_at, updated_at
)
SELECT id, platform, platform_host, 'host', api_type,
       requests_hour, hour_start, rate_remaining, rate_limit,
       rate_reset_at, updated_at
FROM middleman_rate_limits
WHERE platform != 'github';
```

Drop/rename the old table. The down migration selects the most recently updated row per old unique key and comments that collapsing identities is lossy.

The same migration also rebuilds `middleman_notification_sync_watermarks` to per-repository identity — primary key `(platform, platform_host, repo_owner, repo_name)` with lowercased owner/name — retiring the host-wide `sync_cursor` and `tracked_repos_key` columns. Existing host-wide rows are dropped rather than migrated because they cannot be attributed to individual repositories; each repository full-syncs once and re-establishes its own watermark. The down migration restores the empty 000035 host-wide shape.

- [ ] **Step 4: Update DB types and query signatures**

Add `RatePrincipal string` to `db.RateLimit`. Replace query methods with principal-bearing signatures:

```go
func (d *DB) UpsertPlatformRateLimit(
    platform, platformHost, ratePrincipal, apiType string,
    requestsHour int,
    hourStart time.Time,
    rateRemaining, rateLimit int,
    rateResetAt *time.Time,
) error

func (d *DB) GetPlatformRateLimit(
    platform, platformHost, ratePrincipal, apiType string,
) (*RateLimit, error)
```

Update the GitHub convenience wrappers to require a principal; do not retain a host-only compatibility overload.

- [ ] **Step 5: Make `RateTracker` principal-aware**

Store `ratePrincipal`, include it in hydrate/persist calls, and generate opaque process keys:

```go
func RateBucketKey(platformName, platformHost, ratePrincipal string) string {
    return strings.Join([]string{
        normalizedRatePlatform(platformName),
        strings.ToLower(strings.TrimSpace(platformHost)),
        strings.TrimSpace(ratePrincipal),
    }, "\x00")
}
```

For non-GitHub callers pass `host`. Do not infer a principal inside `RateTracker`.

- [ ] **Step 6: Verify migration and rate tests**

Run:

```bash
go test ./internal/db ./internal/ratelimit -run 'Test.*(Rate|Migration|Principal)' -shuffle=on
```

Expected: PASS.

- [ ] **Step 7: Commit principal persistence**

```bash
git add internal/db/migrations/000040_identity_scoped_sync_state.up.sql internal/db/migrations/000040_identity_scoped_sync_state.down.sql internal/db/types.go internal/db/queries.go internal/db/db_test.go internal/ratelimit/rate.go internal/ratelimit/rate_test.go internal/github/rate.go
git commit -m "fix: scope GitHub rate state to authenticated identities" \
  -m "Host-only persistence merges unrelated GitHub users and installations, while separate rows per PAT would overstate capacity for one user. Persist the principal explicitly and discard legacy GitHub rows that cannot be attributed safely."
```

---

### Task 4: Build identity runtimes and deduplicate budgets at startup

**Files:**
- Modify: `cmd/middleman/provider_startup.go`
- Create: `cmd/middleman/provider_startup_identity_test.go`
- Modify: `cmd/middleman/provider_startup_split_test.go`
- Modify: `cmd/middleman/main.go`

**Interfaces:**
- Consumes: route descriptors from Task 1, `IdentityResolver` from Task 2, principal-aware trackers from Task 3.
- Produces:
  ```go
  type githubIdentityRuntime struct {
      identity GitHubIdentity
      budget   *github.SyncBudget
      rest     *github.RateTracker
      graphql  *github.RateTracker
  }

  type githubCredentialRoute struct {
      key           github.RouteKey
      source        tokenauth.Source
      readIdentity  github.IdentityKey
      writeIdentity github.IdentityKey
  }
  ```

- [ ] **Step 1: Write failing startup deduplication tests**

Use a fake identity resolver mapping `PAT_A` and `PAT_B` to `user:123`, `PAT_C` to `user:456`, and an App route to `installation:789`. Assert:

```go
assert.Same(t, runtimeForOrgA.budget, runtimeForOrgB.budget)
assert.Same(t, runtimeForOrgA.rest, runtimeForOrgB.rest)
assert.NotSame(t, runtimeForOrgA.budget, runtimeForOrgC.budget)
assert.Equal(t, "installation:789", runtimeForOrgD.readIdentity.Principal)
assert.Equal(t, "user:123", runtimeForOrgD.writeIdentity.Principal)
```

Also test startup failure when a required owner PAT cannot resolve through `/user`, and that the error contains `env:ORG_A_TOKEN` but not the token value.

- [ ] **Step 2: Run startup tests and confirm failure**

Run:

```bash
go test ./cmd/middleman -run 'TestBuildProviderStartup.*Identity|TestResolveGitHub.*Identity' -shuffle=on
```

Expected: FAIL because startup still creates host-scoped trackers and budgets.

- [ ] **Step 3: Split generic provider startup from GitHub route startup**

Keep non-GitHub factories on the existing host path using principal `host`. Add a GitHub-specific builder:

```go
func buildGitHubHostRuntime(
    ctx context.Context,
    database *db.DB,
    cfg *config.Config,
    set *tokenauth.SourceSet,
    host string,
    resolver github.IdentityResolver,
) (*github.HostRouter, map[string]*githubIdentityRuntime, error)
```

The builder must:

1. collect host fallback, owner, and explicit repo routes;
2. upsert each scoped source independently;
3. resolve read identity as App installation when applicable, otherwise PAT user;
4. resolve mutation identity through `tokenauth.WithMutationAuth`;
5. deduplicate `githubIdentityRuntime` by `IdentityKey.String()`;
6. create one `SyncBudget(cfg.BudgetPerHour())`, REST tracker, and GraphQL tracker per identity.

- [ ] **Step 4: Make reset wiring identity-scoped and idempotent**

Wire each runtime's REST tracker reset to its budget exactly once. Add a small `sync.Once`-per-window guard only if tests show duplicate reset callbacks can occur; prefer making `SyncBudget.Reset` naturally safe and attaching one callback per shared tracker.

- [ ] **Step 5: Preserve split App/PAT semantics**

For an App read route, construct read transports with the installation runtime and write/notification transports with the resolved PAT runtime. Remove the old assumption that one host has at most one read and one write tracker.

- [ ] **Step 6: Update `providerStartup` outputs**

Replace host-keyed GitHub maps with identity-keyed maps and route tables. Keep generic provider maps using principal `host`. Pass the routed GitHub client into `github.NewProviderRegistry` under the host as before.

- [ ] **Step 7: Run startup tests**

Run:

```bash
go test ./cmd/middleman -run 'TestBuildProviderStartup|TestCollectProviderTokenSources|Test.*GitHub.*Identity' -shuffle=on
```

Expected: PASS.

- [ ] **Step 8: Commit startup identity accounting**

```bash
git add cmd/middleman/provider_startup.go cmd/middleman/provider_startup_identity_test.go cmd/middleman/provider_startup_split_test.go cmd/middleman/main.go
git commit -m "feat: allocate GitHub sync budgets per identity" \
  -m "Different owner PATs can authenticate as one GitHub user, and App reads can differ from PAT writes. Resolve those principals before client construction so routes share or isolate budgets according to GitHub's actual accounting boundary."
```

---

### Task 5: Route GitHub REST and GraphQL operations by effective credential route

**Files:**
- Create: `internal/github/auth_router.go`
- Create: `internal/github/auth_router_test.go`
- Modify: `internal/github/client.go`
- Modify: `internal/github/client_test.go`
- Modify: `internal/github/graphql.go`
- Modify: `internal/github/graphql_test.go`
- Modify: `internal/github/public_api_guard_test.go`

**Interfaces:**
- Consumes: startup routes and identity runtimes from Task 4.
- Produces:
  ```go
  type RouteKey struct {
      Host  string
      Owner string
      Name  string
  }

  type Route struct {
      Key    RouteKey
      Client Client
      // DiscoveryClient narrowly serves owner repository enumeration for
      // selected-installation App routes. Owner discovery unions its listing
      // with the PAT route's listing, deduped by repository ID.
      DiscoveryClient Client
      // WriteSnapshotClient snapshots the write identity's rate state on
      // App-read/PAT-write routes; querying with the route's read client
      // would attribute the PAT's capacity to the App principal.
      WriteSnapshotClient Client
      Fetcher             *GraphQLFetcher
      ReadIdentity        IdentityKey
      WriteIdentity       IdentityKey
  }

  type HostRouter struct { /* fallback, owner routes, exact repo routes */ }

  func (r *HostRouter) RouteForRepo(owner, name string) (*Route, error)
  func (r *HostRouter) RouteForOwner(owner string) (*Route, error)
  func (r *HostRouter) ReadIdentityForRepo(owner, name string) (IdentityKey, error)
  func (r *HostRouter) WriteIdentityForRepo(owner, name string) (IdentityKey, error)
  ```

- [ ] **Step 1: Write failing route-selection tests**

Cover:

- exact repository override beats owner route;
- owner route beats fallback;
- unknown owner uses fallback;
- ownerless API uses fallback;
- owner matching is case-insensitive;
- two clients with different tokens can share the same identity runtime;
- `ListRepositoriesByOwner` unions the owner PAT route's listing with selected-App discovery, deduped by repository ID, and fails closed when either configured source fails;
- repository reads and mutations delegate to the same route but use that route's read/write client split.

Use distinct fake clients that record method, owner, repo, and token marker.

- [ ] **Step 2: Run router tests and confirm failure**

Run:

```bash
go test ./internal/github -run 'TestHostRouter|TestRoutedClient|TestGraphQLRoute' -shuffle=on
```

Expected: FAIL because host-level routing does not exist.

- [ ] **Step 3: Implement route lookup**

Use normalized map keys:

```go
func repoRouteMapKey(owner, name string) string {
    return strings.ToLower(strings.TrimSpace(owner)) + "\x00" +
        strings.ToLower(strings.TrimSpace(name))
}

func (r *HostRouter) RouteForRepo(owner, name string) (*Route, error) {
    if route := r.repos[repoRouteMapKey(owner, name)]; route != nil {
        return route, nil
    }
    return r.RouteForOwner(owner)
}
```

Return a typed missing-route error containing host and owner but no credential value.

- [ ] **Step 4: Implement the routed `github.Client`**

Embed the fallback client and override every owner-bearing method from `github.Client` so it delegates through `RouteForRepo` or `RouteForOwner`:

```go
type RoutedClient struct {
    fallback Client
    routes   *HostRouter
}

var _ Client = (*RoutedClient)(nil)

func (c *RoutedClient) GetRepository(
    ctx context.Context, owner, repo string,
) (*gh.Repository, error) {
    route, err := c.routes.RouteForRepo(owner, repo)
    if err != nil {
        return nil, err
    }
    return route.Client.GetRepository(ctx, owner, repo)
}

func (c *RoutedClient) ListRepositoriesByOwner(
    ctx context.Context, owner string,
) ([]*gh.Repository, error) {
    // Discovery unions the owner/fallback PAT route's listing with the
    // route's selected-App discovery listing, deduped by repository ID:
    // a PAT misses selection-only grants, the App lists only its selection.
    // Either configured source failing fails discovery rather than
    // silently narrowing coverage.
    return c.listRepositoriesByOwnerAcrossRoutes(ctx, owner)
}
```

Delegate ownerless notification APIs and authenticated-viewer APIs to the fallback; repository-scoped notification and read-propagation traffic routes by repository and is accounted to that route's write identity, not to the fallback. Use `internal/github/public_api_guard_test.go` to fail if a new owner-bearing interface method is added without an explicit routed implementation.

- [ ] **Step 5: Make GraphQL fetcher lookup route-specific**

Keep `GraphQLFetcher` itself single-credential. Store one fetcher per `Route` and expose it through `RouteForRepo`. Remove host-only fetcher lookup from callers. Each fetcher receives the read identity's GraphQL tracker and shared identity budget.

- [ ] **Step 6: Verify separate `go-github` caches per route**

Add a regression test where route A observes an exhausted limit and route B, on a distinct identity, still performs a request. Add a same-identity test showing both routes' response headers update the shared `RateTracker` even though the `go-github` clients are separate.

- [ ] **Step 7: Run GitHub client tests**

Run:

```bash
go test ./internal/github -run 'Test(HostRouter|RoutedClient|GraphQLRoute|PublicAPI|.*Rate.*Route)' -shuffle=on
```

Expected: PASS.

- [ ] **Step 8: Commit routed clients**

```bash
git add internal/github/auth_router.go internal/github/auth_router_test.go internal/github/client.go internal/github/client_test.go internal/github/graphql.go internal/github/graphql_test.go internal/github/public_api_guard_test.go
git commit -m "feat: route GitHub API clients by repository credential" \
  -m "Owner-scoped PATs need independent authorization and go-github caches even when they share one user's rate allowance. Add a host facade that selects exact repo, owner, or fallback routes while retaining the provider registry's host contract."
```

---

### Task 6: Make sync cadence, budgets, snapshots, and availability identity-aware

**Files:**
- Modify: `internal/github/sync.go`
- Modify: `internal/github/sync_test.go`
- Modify: `internal/server/operation_availability.go`
- Modify: `internal/server/operation_availability_test.go`
- Modify: `internal/server/api_types.go`
- Modify: `internal/server/huma_routes.go`
- Modify: `internal/server/api_test.go`

**Interfaces:**
- Consumes: `HostRouter.ReadIdentityForRepo`, `WriteIdentityForRepo`, and route fetchers from Task 5.
- Produces:
  ```go
  func (s *Syncer) RateTrackerForRepo(repo RepoRef, apiType string) (*RateTracker, bool)
  func (s *Syncer) WriteRateTrackerForRepo(repo RepoRef, apiType string) (*RateTracker, bool)
  func (s *Syncer) BudgetForRepo(repo RepoRef) (*SyncBudget, bool)
  ```

- [ ] **Step 1: Write failing sync-budget tests**

Add tests proving:

1. org A and org B use different PAT routes resolving to `user:123` and spend the same budget;
2. org C resolving to `user:456` retains its own budget when `user:123` is exhausted;
3. a reset observed through org A resets org B's shared budget;
4. an App installation pause does not delay a PAT-backed owner on the same host;
5. `nextSyncAfter` and `nextWatchSyncAfter` keys include the selected read identity.

- [ ] **Step 2: Run focused sync tests and confirm failure**

Run:

```bash
go test ./internal/github -run 'TestSyncer.*(Identity|SharedBudget|IsolatedBudget|Route)' -shuffle=on
```

Expected: FAIL because sync maps and cadence keys are host-scoped.

- [ ] **Step 3: Replace host bucket helpers with repository identity lookup**

Change helpers from:

```go
func repoRateBucketKey(repo RepoRef) string
```

to route-aware lookup:

```go
func (s *Syncer) readBucketKeyForRepo(repo RepoRef) (string, error) {
    if repoPlatform(repo) != platform.KindGitHub {
        return ratelimit.RateBucketKey(string(repoPlatform(repo)), repoHost(repo), "host"), nil
    }
    identity, err := s.githubRouter(repoHost(repo)).ReadIdentityForRepo(repo.Owner, repo.Name)
    if err != nil {
        return "", err
    }
    return ratelimit.RateBucketKey("github", identity.Host, identity.Principal), nil
}
```

Use the write identity for mutation availability and write trackers.

- [ ] **Step 4: Route GraphQL and REST sync through the same selected route**

`fetcherFor(repo)` must obtain `RouteForRepo(repo.Owner, repo.Name).Fetcher`. Ensure backoff, detail drain, active refresh, notification sync, and ETag budget spend all consult the budget attached to the request's actual identity.

- [ ] **Step 5: Refresh rate snapshots once per identity**

Iterate identity runtimes rather than host trackers. Pick one route client for each identity and update only that runtime's REST/GraphQL trackers. Key snapshot refresh cooldown by `IdentityKey.String()`.

- [ ] **Step 6: Make operation availability repository-aware**

Replace direct `RateBucketKey(provider, host)` lookups with `RateTrackerForRepo` and `WriteRateTrackerForRepo`. Preserve provider-neutral host behavior for non-GitHub repos.

- [ ] **Step 7: Return safe principal-aware rate status**

Extend the status shape:

```go
type rateLimitHostStatus struct {
    Provider       string `json:"provider"`
    PlatformHost   string `json:"platform_host"`
    RatePrincipal  string `json:"rate_principal"`
    PrincipalLabel string `json:"principal_label"`
    // existing fields remain
}
```

Use labels such as `GitHub user maintainer` and `GitHub App installation 789`. Keep the response map field named `hosts` for API compatibility only if existing clients treat it as opaque; otherwise regenerate the API with a direct rename in the same change. Do not add a legacy duplicate field.

- [ ] **Step 8: Run sync and server tests**

Run:

```bash
go test ./internal/github ./internal/server -run 'Test.*(RateLimit|Availability|SharedBudget|Identity|Snapshot)' -shuffle=on
```

Expected: PASS.

- [ ] **Step 9: Regenerate API artifacts if the OpenAPI shape changes**

Run:

```bash
make api-generate
```

Expected: exit 0 with generated artifacts matching the new safe principal fields.

- [ ] **Step 10: Commit identity-aware sync behavior**

```bash
git add internal/github/sync.go internal/github/sync_test.go internal/server/operation_availability.go internal/server/operation_availability_test.go internal/server/api_types.go internal/server/huma_routes.go internal/server/api_test.go internal/apiclient/generated
git commit -m "fix: throttle GitHub sync by authenticated identity" \
  -m "Host-level cadence and availability let one exhausted identity block unrelated users while separate PAT routes for one user could overspend. Resolve every repository to its read or write principal before consulting budgets, snapshots, and operation gates."
```

---

### Task 7: Route preview and managed Git authentication by owner and repository

**Files:**
- Modify: `internal/server/repo_import_handlers.go`
- Modify: `internal/server/api_test.go`
- Modify: `internal/gitclone/clone.go`
- Modify: `internal/gitclone/auth_test.go`
- Modify: `internal/gitclone/clone_test.go`
- Modify: `internal/workspace/manager.go`
- Modify: `internal/workspace/branch_sync.go`
- Modify: relevant workspace tests that call the changed methods.

**Interfaces:**
- Consumes: route selector from Task 5.
- Produces:
  ```go
  type gitclone.RouteResolver interface {
      SourceForRepo(platform, host, owner, name string) tokenauth.Source
      FallbackSource(host string) tokenauth.Source
  }

  func (m *Manager) RunGitForRepo(
      ctx context.Context,
      platform, host, owner, name, dir string,
      args ...string,
  ) ([]byte, error)
  ```

- [ ] **Step 1: Write failing preview routing tests**

Add an API test with two GitHub owners on `github.com`. The fake routed client returns repositories only when the request reaches the matching owner route. POST preview for each owner and assert both succeed with their own result set. Add a missing-owner-token test that returns the stable provider problem containing host and owner.

- [ ] **Step 2: Write failing Git authentication tests**

In `internal/gitclone/auth_test.go`, configure:

```text
github.com/acme/widgets -> token-a
github.com/example/tools -> token-b
github.com fallback -> fallback-token
```

Run controlled fake Git commands and assert `acme/widgets` receives token A, `example/tools` receives token B, and an ownerless `RunGit` path receives the fallback. Verify auth retry invalidates only the selected route source.

- [ ] **Step 3: Run focused tests and confirm failure**

Run:

```bash
go test ./internal/server ./internal/gitclone -run 'Test.*(RepoPreview.*Owner|Git.*Owner.*Auth|RouteSource)' -shuffle=on
```

Expected: FAIL because preview and Git auth select by host only.

- [ ] **Step 4: Make preview lookup owner-aware**

Replace:

```go
client, err := s.syncer.ClientForHost(host)
```

with:

```go
client, err := s.syncer.ClientForGitHubOwner(host, owner)
```

`ClientForGitHubOwner` returns the host routed client bound to the owner route for owner-only operations. The preview request and frontend remain unchanged.

- [ ] **Step 5: Replace clone manager's host source map with a route resolver**

Change `gitclone.New` to accept a resolver interface, not `map[string]tokenauth.Source`. Select source before every networked operation:

```go
func (m *Manager) sourceForRepo(platform, host, owner, name string) tokenauth.Source {
    if m.routes == nil {
        return nil
    }
    return m.routes.SourceForRepo(platform, host, owner, name)
}
```

Pass owner/name through `cloneBare`, `fetch`, `remote set-head`, auth retry, and invalidation. Local Git reads remain unchanged.

- [ ] **Step 6: Directly migrate workspace networked Git call sites**

Change `RunGit` call sites to pass repository identity. For workspaces, obtain owner/name from `Workspace` or the persisted repo row and thread them into:

- workspace base fetch when the base is a repository remote;
- merge-request head fetch;
- push and pull branch refresh/push.

Do not keep a host-only wrapper as a compatibility shim. Retain a distinct `RunGitForHost` only for genuinely ownerless operations, with call sites named explicitly.

- [ ] **Step 7: Confirm GitHub Apps are excluded from smart HTTP**

Build clone descriptors from mutation/user candidates. Add a test with an App read route plus owner PAT and assert Git receives the PAT, never an installation token.

- [ ] **Step 8: Run preview, clone, and workspace tests**

Run:

```bash
go test ./internal/server ./internal/gitclone ./internal/workspace -run 'Test.*(RepoPreview|Clone|Fetch|BranchSync|Workspace.*Fetch)' -shuffle=on
```

Expected: PASS.

- [ ] **Step 9: Commit owner-aware preview and Git auth**

```bash
git add internal/server/repo_import_handlers.go internal/server/api_test.go internal/gitclone/clone.go internal/gitclone/auth_test.go internal/gitclone/clone_test.go internal/workspace/manager.go internal/workspace/branch_sync.go internal/workspace/*_test.go
git commit -m "feat: use owner PATs for preview and Git transport" \
  -m "Repository discovery and smart-HTTP operations happen before or outside normal sync but still require the resource owner's credential. Thread repository identity through both paths so private repositories do not fall back to an unrelated host token."
```

---

### Task 8: Document the advanced feature and verify the complete behavior

**Files:**
- Modify: `README.md`
- Modify: `docs/configuration.md`
- Modify: `docs/troubleshooting.md`
- Modify: `context/platform-sync-invariants.md`
- Modify: `context/github-sync-invariants.md`
- Modify: any tests or generated artifacts exposed by the full validation run.

**Interfaces:**
- Consumes: all prior tasks.
- Produces: user-facing setup instructions and durable maintainer invariants.

- [ ] **Step 1: Update user documentation**

Add a concise advanced configuration section with this complete example:

```toml
[[github_owner_tokens]]
owner = "org-a"
token_env = "MIDDLEMAN_GITHUB_TOKEN_ORG_A"

[[github_owner_tokens]]
owner = "org-b"
token_file = "~/.config/middleman/tokens/org-b"
```

State explicitly:

- fine-grained PATs expand accessible owners;
- PATs issued by one user share that user's GitHub rate limit and middleman sync budget;
- distinct users and App installations have distinct `(host, identity)` budgets;
- repository overrides win, App tokens handle covered reads, owner PATs handle uncovered reads and user-attributed writes;
- owner mappings are not editable in the UI;
- restart after rotating a PAT to a different GitHub user.

- [ ] **Step 2: Update maintainer context**

Replace host-only claims in `context/platform-sync-invariants.md` with:

```text
GitHub authorization routes may be exact-repository, owner, or host fallback.
GitHub rate trackers and sync budgets are keyed by (host, authenticated identity),
where PATs resolve to user:<numeric-id> and App reads use installation:<id>.
Non-GitHub providers remain host-scoped unless their provider model later proves otherwise.
```

Record ownerless notification behavior, managed Git PAT behavior, and the restart rule in `context/github-sync-invariants.md`.

- [ ] **Step 3: Run formatting and static checks**

Run:

```bash
gofmt -w internal/config/config.go internal/config/github_owner_token_config_test.go internal/tokenauth/descriptor.go internal/tokenauth/descriptor_test.go internal/tokenauth/source_test.go internal/github/identity.go internal/github/identity_test.go internal/github/auth_router.go internal/github/auth_router_test.go internal/github/client.go internal/github/graphql.go internal/github/rate.go internal/github/sync.go cmd/middleman/provider_startup.go cmd/middleman/provider_startup_identity_test.go internal/db/types.go internal/db/queries.go internal/db/db_test.go internal/ratelimit/rate.go internal/ratelimit/rate_test.go internal/server/repo_import_handlers.go internal/server/operation_availability.go internal/server/api_types.go internal/server/huma_routes.go internal/gitclone/clone.go internal/gitclone/auth_test.go internal/workspace/manager.go internal/workspace/branch_sync.go
```

Then:

```bash
git diff --check
```

Expected: no output from `git diff --check`.

- [ ] **Step 4: Run the affected Go packages**

Run:

```bash
go test ./internal/config ./internal/tokenauth ./internal/db ./internal/ratelimit ./internal/github ./internal/gitclone ./internal/workspace ./internal/server ./cmd/middleman -shuffle=on
```

Expected: exit 0 with no failed packages.

- [ ] **Step 5: Run repository-wide short tests**

Run:

```bash
make test-short
```

Expected: exit 0.

- [ ] **Step 6: Run vet and lint**

Run:

```bash
make vet
make lint
```

Expected: both exit 0.

- [ ] **Step 7: Perform the final requirements review**

Verify each item with code/tests rather than inference:

- two owner PATs for one user share budget and rate state;
- different users on one host are isolated;
- App read identity and PAT write identity remain separate;
- preview selects the entered owner;
- exact repository override beats owner mapping;
- managed Git selects exact repo/owner PAT and excludes App tokens;
- no token or token-derived value appears in DB rows, API responses, logs, or errors;
- no frontend credential UI was added;
- legacy single-token configuration still passes startup and sync tests.

- [ ] **Step 8: Commit documentation and final integration fixes**

```bash
git add README.md docs/configuration.md docs/troubleshooting.md context/platform-sync-invariants.md context/github-sync-invariants.md config.example.toml internal cmd
git commit -m "docs: explain GitHub owner PAT identity accounting" \
  -m "Multi-PAT access is useful only when users understand that authorization scope and rate capacity are different. Document configuration precedence, shared user budgets, App installation identities, and the restart boundary for identity-changing rotations."
```

---

## Plan Self-Review

- **Spec coverage:** Configuration, precedence, identity resolution, route clients, App/PAT split, principal persistence, identity budgets, GraphQL, preview, managed Git, status, errors, documentation, and tests each have an implementation task.
- **Scope:** The plan remains configuration-only and does not add UI token management or automatic rotation.
- **Type consistency:** `IdentityKey`, `RouteKey`, `HostRouter`, principal-bearing `RateBucketKey`, and repository-aware sync lookup methods are introduced before their consumers.
- **Migration discipline:** Schema work is isolated in new migration `000040`; existing migration history remains untouched.
- **No compatibility shim:** Internal host-only GitHub methods are directly migrated where repository identity is required. Provider-neutral host behavior remains only where it is still the correct model.
