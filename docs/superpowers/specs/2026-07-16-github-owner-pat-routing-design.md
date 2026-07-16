# GitHub Owner PAT Routing Design

## Summary

Middleman will support multiple GitHub personal access tokens selected by repository owner. This is a configuration-only advanced feature: users configure owner-to-credential mappings in TOML, and middleman applies them to sync, repository preview/import, mutations, and managed Git network operations without adding a credential-management UI.

The design separates authorization routing from capacity accounting:

- **Authorization route:** `(GitHub host, repository owner)` selects a credential chain that can access that owner's repositories.
- **Accounting identity:** `(GitHub host, authenticated GitHub identity)` owns the observed GitHub rate-limit state and middleman's configured sync budget.

Different PATs issued by the same GitHub user remain distinct authorization routes but share one accounting identity. GitHub App installation tokens use the installation ID as their accounting identity. This matches GitHub's rate-limit model: PAT limits belong to the authenticated user, while installation-token limits belong to the installation.

## Scope

This change includes:

- TOML configuration for owner-scoped GitHub PATs.
- Owner-aware credential precedence for reads, mutations, preview/import, and managed Git clone/fetch operations.
- Startup resolution of PATs to stable GitHub user identities.
- A bounded, configuration-derived pool of GitHub clients and GraphQL fetchers.
- Rate-limit persistence and sync budgets keyed by host and authenticated identity.
- Correct sharing when multiple PATs authenticate as the same user.
- Documentation for setup, precedence, capacity behavior, and restart requirements.

This change does not include:

- A Settings UI for adding or editing tokens.
- Automatic PAT creation, rotation, or permission management.
- Load balancing requests across multiple credentials that can access the same owner.
- Treating multiple PATs for one user as additional GitHub capacity.
- Live identity reassignment after a token rotates to a different GitHub user.

## Configuration

Add a top-level array of owner-scoped GitHub token mappings:

```toml
[[github_owner_tokens]]
host = "github.com"
owner = "org-a"
token_env = "MIDDLEMAN_GITHUB_TOKEN_ORG_A"

[[github_owner_tokens]]
host = "github.com"
owner = "org-b"
token_file = "~/.config/middleman/tokens/org-b"
```

`host` defaults to `github.com` when omitted, but remains part of the mapping identity. Owner matching is case-insensitive because GitHub owner names are case-insensitive lookup keys.

Each mapping must satisfy these rules:

- `owner` is required and cannot contain glob syntax or `/`.
- `host` must normalize as a GitHub platform host.
- At least one of `token_file` and `token_env` is required.
- When both are present, `token_file` is checked before `token_env`, matching existing token-source precedence.
- Duplicate `(host, owner)` mappings are rejected.
- Token file paths are normalized relative to the config file in the same way as existing repository and platform token files.
- Owner token environment names are included in runtime secret stripping and log redaction.

No PAT value is persisted in middleman's database or written back into the configuration file.

## Credential Precedence

### GitHub API reads

For a repository-scoped read, middleman resolves candidates in this order:

1. Repository `token_file`.
2. Repository `token_env`.
3. A GitHub App installation covering the requested owner.
4. Owner-scoped `token_file`.
5. Owner-scoped `token_env`.
6. Platform `token_file`.
7. Platform `token_env`.
8. `github_token_env` for `github.com`.
9. The GitHub CLI credential for the host.

Repository overrides remain terminal with respect to GitHub Apps: an explicit repository credential does not fall through to an installation token. GitHub Apps continue to outrank owner and host PATs for sync reads so installation capacity is used when configured.

### GitHub mutations

Mutation-marked resolution skips GitHub App candidates so user-visible writes remain attributed to the user. The order is:

1. Repository `token_file`.
2. Repository `token_env`.
3. Owner-scoped `token_file`.
4. Owner-scoped `token_env`.
5. Platform `token_file`.
6. Platform `token_env`.
7. `github_token_env` for `github.com`.
8. The GitHub CLI credential for the host.

### Ownerless GitHub APIs

Owner-scoped credentials are not candidates for ownerless APIs. Global notification listing and other user-scoped calls continue to use the host/default user credential chain. Repository-scoped notification calls may use owner routing when the request carries repository identity.

This avoids arbitrarily choosing one owner credential when several configured PATs may authenticate as different GitHub users.

## Authorization Routes

A credential route is a configuration-derived object containing:

- normalized GitHub host;
- optional canonical owner scope;
- effective read credential source;
- effective mutation credential source;
- read accounting identity;
- mutation accounting identity;
- a stable, secret-free route identifier derived from the canonical source descriptors.

The route identifier distinguishes credentials with different authorization scopes. It must never contain a token value or a reversible token hash.

Routes are selected as follows:

```text
(host, owner) -> exact owner route, otherwise host fallback route
```

The number of routes is bounded by configuration. Middleman keeps route clients alive for the daemon lifetime rather than using an LRU. Eviction would discard HTTP connection pools, ETag state, `go-github` rate caches, and installation-token caches without a meaningful memory benefit for a configuration-sized set.

## GitHub Identity Resolution

### PAT identities

At startup, middleman resolves each distinct effective PAT credential through the host's authenticated-user endpoint:

```http
GET /user
```

The response's immutable numeric user ID defines the accounting principal:

```text
user:<numeric-id>
```

The login is retained only as safe diagnostic metadata because a login can change. Identity discovery uses the PAT credential directly and does not consume an owner route's normal sync budget. The request still observes GitHub's real rate headers and startup fails if a required configured owner credential cannot be resolved.

If multiple PAT routes return the same numeric user ID, they share one identity runtime. This is mandatory because GitHub combines PAT, OAuth, and user-authorized app traffic into the authenticated user's primary limit.

### GitHub App identities

An installation-token read route has an identity known from configuration:

```text
installation:<installation-id>
```

No `/user` request is made for installation tokens. The mutation side of the same owner route resolves through its PAT chain and therefore normally has a `user:<id>` identity.

### Rotation behavior

Token file and environment values remain lazily resolved for request authentication, preserving ordinary same-user token rotation. Identity assignment is established at startup.

If a credential rotates to a PAT owned by a different GitHub user, middleman must be restarted before using it. On a `401`, retry invalidation may reload the token, but it must not silently move traffic to a new accounting identity in the running process. Documentation will state this explicitly.

## Identity Runtimes

An identity runtime is keyed by:

```text
(GitHub host, principal kind, principal ID)
```

Conceptually:

```go
type IdentityKey struct {
    Host      string
    Principal string // user:<id> or installation:<id>
}

type IdentityRuntime struct {
    Budget       *SyncBudget
    REST         *RateTracker
    GraphQL      *RateTracker
}
```

Mutation and notification paths use the runtime for the identity that actually authenticates those requests. Interactive mutations update provider rate state but do not spend the background sync budget. Background notification work spends the default user identity's sync budget because it is background GitHub traffic authenticated as that user.

Each configured identity receives the configured `sync_budget_per_hour`. Adding another PAT for the same user does not add another budget. A distinct user or App installation receives a distinct budget.

Example:

```text
org-a -> PAT_A -> user:123
org-b -> PAT_B -> user:123
org-c -> PAT_C -> user:456
org-d -> installation token -> installation:789
```

The resulting identity budgets are:

```text
github.com / user:123
github.com / user:456
github.com / installation:789
```

If `PAT_A` spends 1,500 calls and `PAT_B` spends 2,000 calls from a budget of 4,000, `user:123` has 500 calls remaining.

## REST Client Routing

Middleman will expose one host-level GitHub client to the provider registry, preserving the existing `(platform, platform_host)` registry contract. Internally, that host client routes each operation to a configuration-bounded route client.

Repository-scoped methods already receive `owner`; they select the exact owner route and delegate to its `go-github` clients. Ownerless methods use the fallback route. Reads, writes, and notifications remain separate `go-github` client instances where their identities differ, preventing one credential's cached exhaustion from preemptively blocking another identity.

Each route client is wired to the rate trackers and sync budget of the identity used by that transport. Two route clients backed by different PATs for the same user therefore have separate authorization and HTTP state but share the same `RateTracker` and `SyncBudget` pointers.

The repository preview handler will request the GitHub client for `(host, owner)` rather than only `host`. No request-body or frontend change is required because preview already carries the owner.

## GraphQL Routing

GitHub GraphQL fetches are repository-scoped and already execute per repository. The fetcher lookup changes from host-only to credential-route-aware:

```text
(host, owner) -> route GraphQL fetcher
```

Each fetcher uses the route's read credential and the corresponding identity's GraphQL tracker and sync budget. Repositories for different owners are never queried through a token selected for another owner.

Any future multi-repository GraphQL batch must partition inputs by credential route before issuing queries. Results may be merged only after each route-specific query completes.

## Managed Git Authentication

Managed Git authentication must follow the same authorization route as API access when repository identity is available.

The clone manager will resolve network credentials by `(host, owner)` for:

- initial bare clone;
- fetch;
- remote `set-head`;
- explicit repository-scoped workspace fetches.

Local Git reads remain unauthenticated. Ownerless network operations continue to use the host fallback route.

GitHub App installation tokens remain excluded from Git smart-HTTP authentication. Clone/fetch routes use the mutation/user credential chain: repository override, owner PAT, platform PAT, host default, then GitHub CLI. This avoids using short-lived installation tokens in persisted Git remotes and preserves user-owned Git authentication behavior.

The existing same-host cross-provider clone-token validation remains for non-GitHub providers and ownerless fallback routes. GitHub owner routes are permitted to differ because repository owner is available to managed clone/fetch operations.

## Rate-Limit Persistence

Add `rate_principal` to the persisted key. The unique identity becomes:

```text
(platform, platform_host, rate_principal, api_type)
```

For GitHub, `rate_principal` is `user:<id>` or `installation:<id>`. Other providers use a stable compatibility principal such as `host`, preserving their existing host-scoped behavior.

The migration creates the next numbered migration pair and rebuilds `middleman_rate_limits` with the new column and unique constraint. Existing non-GitHub rows migrate with principal `host`. Existing GitHub host-only rows are not copied because they cannot be safely attributed to a user or installation. The next authenticated response or rate-limit snapshot repopulates correct identity-scoped state.

The down migration is necessarily lossy for GitHub identities. It collapses multiple identity rows to one row per previous `(platform, host, api_type)` key, selecting the most recently updated row, and documents that loss in SQL comments.

`RateTracker` gains a principal field, includes it in persistence and bucket keys, and exposes it for diagnostics. A GitHub bucket key is no longer just the host.

## Budget Reset and Throttling

A sync budget belongs to one identity runtime and resets with that identity's observed GitHub window. Multiple credential routes sharing the identity share the same reset callback target.

The reset must be idempotent for a window transition because more than one route client may observe the same identity's reset. An unrelated identity on the same host must never reset, pause, or throttle another identity's budget.

Cadence gates such as `nextSyncAfter` and watched-MR throttling must use the repository's selected read identity bucket. A paused App installation must not stop a PAT-backed owner on the same host, and a paused user identity must stop every owner route whose PATs resolve to that user.

Rate-limit snapshot refreshes must run once per identity and use a route client authenticated as that identity. The result updates only that identity's REST and GraphQL trackers.

## Runtime Status and Operation Availability

Internal rate and budget maps use identity bucket keys. Repository-scoped operation availability resolves the repository's read or mutation identity before consulting the relevant tracker.

The rate-limit API reports one entry per `(provider, host, principal)` and includes a safe principal label. It must not expose token source names or token-derived identifiers. Existing single-identity installations continue to produce one entry for each host.

No credential-management UI is added. If the existing rate display consumes these entries, it treats the map key as opaque and displays host plus safe identity label when more than one identity exists.

## Error Handling

Configuration and startup errors use safe source descriptors only:

- duplicate owner mapping;
- missing owner token source;
- PAT identity resolution failure;
- PAT `/user` response without a positive numeric ID;
- owner route with no usable read or mutation credential;
- token rotation requiring restart after identity mismatch is detected.

Errors may include host, owner, environment variable name, token file path, and authenticated login. They must never include token values, authorization headers, or token-derived hashes.

Preview and sync continue to route provider errors through the existing stable problem envelopes. A missing owner credential identifies the GitHub host and owner in the error detail.

## Documentation

Update configuration and troubleshooting documentation with:

- the `[[github_owner_tokens]]` syntax;
- precedence relative to repository overrides, GitHub Apps, platform credentials, and `github_token_env`;
- the fact that fine-grained PATs expand repository access but do not add capacity when they belong to the same user;
- the `(host, authenticated identity)` budget model;
- the restart requirement when a rotated PAT belongs to a different user;
- the absence of a Settings UI for this advanced feature.

## Testing

Coverage will include:

- config parsing, normalization, duplicate rejection, save/load, path normalization, environment stripping, and precedence;
- token-source owner scoping for PAT candidates and mutation behavior;
- identity discovery and deduplication for two PATs owned by one user;
- distinct identity runtimes for different users and App installations;
- shared budget spend and reset across different PAT routes for one user;
- isolated pause and throttle behavior between identities on one host;
- REST and GraphQL routing by owner;
- preview using the entered owner's route;
- managed Git clone/fetch selecting the owner's PAT;
- rate-limit migration, persistence, hydration, and integrity checks;
- existing single-token and GitHub App configurations remaining unchanged.

Go tests will be invoked with `-shuffle=on`. Migration tests will open real SQLite databases through `db.Open()` and verify `PRAGMA integrity_check` and `PRAGMA foreign_key_check` after upgrade.

## Estimated Effort

A production-quality implementation is estimated at 10–16 engineering days:

| Area | Estimate |
|---|---:|
| Owner-token configuration and precedence | 1–2 days |
| PAT identity discovery and deduplication | 1–2 days |
| Credential-route client pool | 2–3 days |
| Identity-scoped budgets and rate state | 2–3 days |
| Database migration and query changes | 1–2 days |
| GraphQL, preview, and managed Git routing | 1–2 days |
| Documentation and full test coverage | 2–3 days |

The central invariant is:

> Authorization is selected by `(GitHub host, repository owner)`. Rate state and sync capacity are accounted by `(GitHub host, authenticated GitHub identity)`.
