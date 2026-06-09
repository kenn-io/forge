# Design: Refresh provider tokens without restarting

## Problem

Middleman resolves provider tokens during startup, passes token strings into
provider clients, and keeps those clients for the process lifetime. When a token
expires or is rotated, the running daemon keeps sending the old token until the
user restarts it. Config reload can detect token-related config drift, but it
currently reports `restart_required` instead of repairing the clients that use
credentials.

Re-reading environment variables alone does not solve token rotation. A process
can read updates it makes to its own environment, but it cannot see a new value
exported later in a parent shell. A durable rotation source needs to live outside
the daemon's inherited environment.

## Goals

- Let users rotate provider tokens without restarting Middleman.
- Preserve current `token_env` behavior and the GitHub `gh auth token` fallback.
- Add a provider-neutral token-file path that can be updated atomically by a
  user, credential manager, or wrapper script.
- Use the same token resolution semantics for REST, GitHub GraphQL, and local
  clone fetches.
- Never log token contents, auth headers, token-bearing URLs, or command output
  that could contain a token.
- Avoid persisting decrypted token material.

## Non-Goals

- Middleman will not mutate its own environment to simulate parent-shell env
  changes.
- Middleman will not add a generic shell command token provider in this change.
  Arbitrary commands increase security, quoting, timeout, and logging risk.
- Middleman will not introduce provider-specific refresh flows beyond the
  existing GitHub CLI token lookup.

## Configuration

Add `token_file` beside `token_env` on `[[platforms]]` and `[[repos]]`.

```toml
[[platforms]]
type = "gitlab"
host = "gitlab.com"
token_file = "~/.config/middleman/tokens/gitlab.com"

[[repos]]
owner = "team"
name = "internal-app"
platform = "github"
platform_host = "github.corp.example.com"
token_file = "~/.config/middleman/tokens/ghe"
```

Precedence remains host-scoped:

1. Repo-level `token_file` if configured and non-empty.
2. Repo-level `token_env` if configured and non-empty.
3. Matching `[[platforms]] token_file` if configured and non-empty.
4. Matching `[[platforms]] token_env` if configured and non-empty.
5. Provider public-host default env var for public Forgejo/Gitea hosts where
   one exists.
6. The configured `github_token_env` value for GitHub hosts.
7. GitHub CLI fallback for GitHub hosts only.

Exact public-host defaults are intentionally provider-specific:

- GitHub `github.com` uses `github_token_env`, which defaults to
  `MIDDLEMAN_GITHUB_TOKEN`, then the GitHub CLI fallback.
- GitLab `gitlab.com` has no implicit default env var; configure `token_env`
  or `token_file` explicitly when not using another fallback.
- Forgejo `codeberg.org` uses `MIDDLEMAN_FORGEJO_TOKEN`.
- Gitea `gitea.com` uses `MIDDLEMAN_GITEA_TOKEN`.

Empty files and empty env vars are treated as absent so a more general fallback
can still supply a token. Token-file paths should be normalized during config
load: `~` resolves to the current user's home directory, and relative paths
resolve relative to the config file directory so service working-directory
changes do not change which credential file Middleman reads.

Provider clients are currently registered per `(platform, host)`, not per repo.
Validation should therefore extend the existing conflicting-`token_env` rule:
all configured repos that share a `(platform, host)` must resolve to the same
effective token source descriptor, including source kind, env var name, and
normalized token-file path. Repo-level `token_file` and `token_env` remain useful
for overriding a host's source, but two repos on the same provider host cannot
silently select different credentials unless the provider-client model changes.

## Token Source

Introduce a provider-neutral token source abstraction in the config/startup
layer. The abstraction returns the current token for a `(platform, host)` pair
and records the selected source kind. It does not expose token contents through
logs, API responses, or durable state.

The source reads env vars and token files lazily. Token file reads happen on
demand and trim surrounding whitespace, so atomic replacement of the file is
enough for the next request to use the new token. GitHub CLI fallback remains
host-scoped through `gh auth token --hostname <host>`.

For performance and subprocess safety, GitHub CLI results can be cached briefly,
but the cache must be invalidated after an authentication failure. File and env
lookups should be cheap enough to read per request without a long-lived cache.
Token files are read once per affected provider or clone operation. Rotation
tools should write a complete replacement file and then rename it over the
configured path. If a writer overwrites the file in place or renames a partial
non-empty file over the configured path, the racing operation may send or
surface that partial value once. Empty files are treated as absent for fallback
selection, file/env sources are not cached across operations, and GitHub CLI
cache entries are invalidated after authentication failures, so a bad racing
read must not poison later reads.

## Logging And Redaction

Token values must never be written to logs, API responses, database rows,
telemetry payloads, panic messages, or durable error strings. This includes:

- Raw token values from env vars, token files, GitHub CLI output, or in-memory
  token caches.
- `Authorization`, `Private-Token`, Basic auth, OAuth2, or SDK request headers.
- Clone URLs or provider URLs that contain credentials in userinfo.
- Raw stdout or stderr from token helpers, provider SDKs, git, or `gh` when that
  output could include a token.

Loggable metadata is limited to provider kind, host, source kind, env var name,
normalized token-file path, and high-level failure reason. Normalized
token-file paths may appear in local config and load errors because this
local-first tool treats those paths as configuration metadata, not secret
material. Token-file contents and URL userinfo are always secret.

The redaction layer must not depend only on token-shaped regexes. Each
`ManagedSource` registers returned token values long enough to avoid ordinary
word false positives in a bounded process-local recent-token registry before
handing them to a provider or clone operation. `RedactKnownSecrets` and the slog
redacting handler redact those registered opaque token values, token-shaped
GitHub/GitLab strings, credential-bearing URL userinfo, and any explicitly
supplied secrets. The registry is in-memory only, capped to the most recent
1024 long token values, refreshes recency when a token value is returned again,
and is never written to the database, config, API responses, logs, or
telemetry. Explicitly supplied secrets are redacted even when shorter than the
active-registry threshold.

The audit boundary includes token-source errors, provider SDK and HTTP errors,
git stdout and stderr, GitHub CLI stdout and stderr, API problem details,
workspace-launch environment sanitization, DB-persisted sync errors, telemetry
payloads, logs, and panic-adjacent durable error strings. Any code path that
wraps token-source, provider, HTTP, git, or subprocess errors must sanitize
before logging, returning, persisting, or exposing the error to callers. Tests
should use sentinel token values and assert those sentinels never appear in
captured logs or returned errors.

## Provider Clients

Provider clients should receive a token source instead of a startup token
string. Their HTTP transports should apply the current token immediately before
each outgoing request:

- GitHub REST uses an OAuth2-compatible token source so `go-github` sees the
  latest token.
- GitHub GraphQL uses the same token source for the `githubv4` client transport.
- GitLab, Forgejo, and Gitea wrap their HTTP clients with a small auth transport
  that injects the current token in the header format each SDK expects.

This keeps provider instances, rate trackers, sync budgets, and capability
registration stable while allowing credentials to change underneath them.

When a provider request returns a clear credential failure, such as HTTP 401,
Middleman should invalidate any cached GitHub CLI token for that host and retry
the request once with a fresh token. Do not retry HTTP 403 permission or scope
failures; those should continue to surface as provider permission errors. The
retry is deliberately limited to one attempt to avoid masking bad credentials,
rate-limit behavior, or provider-side outages.

If the GitHub CLI fallback cannot produce a token, it is treated as a missing
token without exposing `gh` stdout or stderr. The affected operation fails with
the normal sanitized missing-token error if no later candidate supplies a token.
Startup may still treat the CLI fallback as configured for GitHub hosts so
existing no-env-var setups do not require an eager CLI subprocess just to load
config.

## Clone Fetches

The clone manager currently receives fixed host-token strings. It should instead
receive token sources and ask the source for the current token immediately before
constructing clone or fetch credentials.

Provider API clients and token-source descriptors remain keyed by
`(platform, platform_host)`, but git credential helpers select credentials from
the clone URL host at operation time. Startup must therefore reject configured
providers that share the same hostname unless their effective token-source
descriptors are identical. After that validation, the clone manager may receive
a host-keyed source map because there is no longer an ambiguous credential to
choose for that URL host. This preserves the existing on-disk clone layout and
git's URL-host credential boundary while preventing a token for one provider
host from being silently used for another provider kind on the same hostname.

This avoids a split-brain state where API sync recovers after rotation but local
diff fetches still use the old token.

## Config Reload

Changing token contents through a token file does not require a config reload;
the next request reads the new file contents. Changing token source names or
paths is accepted only after the new config passes the same startup validation,
including same-host clone credential conflict checks. Changing token env names
also updates the runtime sanitizer state:

- If a `token_file` or `token_env` value changes to another source for an
  already configured provider host, reload should rebuild or update the token
  source descriptor without rebuilding unrelated providers. File readability is
  checked on the next affected operation, not as a reload precondition.
- If a new provider host appears, reload may continue to mark
  `restart_required` until dynamic provider registration is handled separately.
- `TokenEnvNames` should continue to include all env var names that might hold
  tokens so launched sessions keep stripping provider credentials.
- Source-set entries for removed hosts may remain until process restart, but
  removed hosts must no longer be referenced by the active provider registry or
  config snapshot. No token material is persisted in those entries.

## Errors

Missing or unreadable tokens should fail fast at startup for configured repos,
matching the current missing-token behavior. During runtime, an unreadable token
file or empty selected source should surface as a provider sync/auth error,
clone error, or GitHub CLI fallback error for that repo or operation, not crash
the daemon or fail unrelated providers.

Error messages may include provider, host, source kind, and token file path. They
must not include token values or command output that could contain token values.

## Testing

Add tests at the lowest layer that observes each behavior:

- Config tests for `token_file` parsing, precedence, path expansion, and
  fallback behavior.
- Config validation tests extending the existing host-scoped token conflict
  checks to source kind, env var name, and normalized token-file path.
- Token source tests showing env/file reads are lazy and token file replacement
  changes the next returned token.
- Redaction tests using sentinel token values to prove token source errors,
  provider auth failures, clone failures, and GitHub CLI refresh failures do not
  emit token contents in logs or returned errors.
- Provider client tests for each provider proving the second HTTP request uses a
  rotated token without reconstructing the client.
- GitHub REST and GraphQL tests showing cache invalidation and single retry
  after a 401, plus no refresh retry for 403 permission failures.
- Clone manager tests proving fetch credentials are resolved at operation time
  plus startup tests proving same-host mixed-provider clone credentials are
  rejected unless their effective source descriptors match.
- Server/config reload tests for changing token source metadata on an existing
  host without requiring a full daemon restart.

## Rollout

Existing configs keep working. Users who need rotation can add `token_file` to a
repo or platform entry and rotate the file atomically. GitHub users who rely on
the CLI keep the current no-env-var setup, with improved recovery after
`gh auth refresh` or `gh auth login` updates the CLI's stored token.

Configs with multiple repos on the same `(platform, platform_host)` still need
one effective token-source descriptor for that provider host. Configs that put
different provider kinds on the same hostname also need identical clone
token-source descriptors, or they must use distinct hostnames, because git clone
credentials are selected by URL host.
