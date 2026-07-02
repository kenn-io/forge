# GitLab Platform Abstraction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add read-oriented GitLab repository and merge request sync while moving the current GitHub implementation behind a platform abstraction that can accept future providers.

**Architecture:** Introduce a provider-neutral `internal/platform` domain and split provider transport from sync orchestration. GitHub remains behaviorally unchanged through a GitHub adapter; GitLab starts with repository discovery, open merge request sync, issues, comments, labels, basic CI pipeline status, releases/tags, and diff clone support. Provider-specific actions such as workflow approval, ready-for-review, merge, and comment mutation are exposed through capability flags so unsupported platforms degrade explicitly instead of pretending to be GitHub.

**Tech Stack:** Go, SQLite migrations, Huma/OpenAPI, go-github/v84, GitLab API v4 via `gitlab.com/gitlab-org/api/client-go`, Svelte 5 generated API clients, Bun, full-stack Go API tests with real SQLite.

---

## Documentation Basis

- GitLab API v4 Projects API supports project metadata and uses `path_with_namespace`, `web_url`, visibility, archived state, and repository properties.
- GitLab API v4 Merge Requests API lists project merge requests with project-scoped `iid`, global `id`, branches, author, labels, draft/work-in-progress flags, merge status, timestamps, and `web_url`.
- GitLab API v4 accepts personal/project/group access tokens through token headers such as `PRIVATE-TOKEN`; the Go client also supports custom base URLs for self-hosted instances.
- GitLab's Go API client docs are at <https://pkg.go.dev/gitlab.com/gitlab-org/api/client-go>. Use `gitlab.com/gitlab-org/api/client-go` as the module/import path.
- Context7 reports the old `github.com/xanzy/go-gitlab` client as moved to `gitlab.com/gitlab-org/api/client-go`; do not add the deprecated import path or copy examples that still import `github.com/xanzy/go-gitlab`.
- The official GitLab Community Edition Docker image is `gitlab/gitlab-ce` on Docker Hub. GitLab's Docker installation docs recommend Docker Compose or Docker Engine, `GITLAB_OMNIBUS_CONFIG` for pre-configuration, mounted config/log/data volumes, and waiting for the instance to become responsive before use.

## Scope

### First GitLab Milestone

- Exact configured GitLab repos.
- GitLab repo import/preview from a group/user namespace.
- GitLab open merge request sync into existing `middleman_merge_requests`.
- GitLab issues sync into existing `middleman_issues`.
- GitLab MR and issue comments, commit events, labels, timestamps, clone URLs, releases/tags, and basic pipeline-derived CI status when exposed by the MR/list/detail APIs.
- Existing GitHub behavior, API shape, and config files keep working.

### Deliberate Follow-Ups

- GitLab mutations: comments, close/reopen, approvals, merge, ready-for-review equivalents.
- GitLab review/discussion parity beyond comments and commits.
- GitLab project glob import across all accessible token associations.
- Always-on CI against a real GitLab CE container. Containerized GitLab e2e is valuable but too slow and heavy for the default test suite.
- New UI vocabulary that renames "pull request" to a neutral "change request" everywhere.

This split keeps the first release useful and testable without dragging every platform-specific action into the abstraction at once.

## File Structure

- Create `internal/platform/types.go`: provider-neutral repository, merge request, issue, event, label, release, tag, CI, identity, capability, and state types.
- Create `internal/platform/client.go`: small interfaces for provider capabilities: repository discovery, change request read sync, issue read sync, comment read sync, release/tag sync, CI sync, and optional mutation actions.
- Create `internal/platform/registry.go`: keyed provider registry by `(platform, host)` with client lookup, capability lookup, and host display helpers.
- Create `internal/platform/persist.go`: provider-neutral persistence helpers that convert platform read models into DB rows and keep sync orchestration out of provider adapters.
- Create `internal/platform/github/adapter.go`: wraps existing GitHub transport data into neutral `platform` types without importing from `internal/github` packages that import the adapter.
- Create `internal/platform/github/normalize.go`: move GitHub SDK normalization behind the neutral output shape while keeping package dependencies acyclic.
- Create `internal/platform/gitlab/client.go`: GitLab API client construction, auth, base URL handling, pagination, and rate tracking.
- Create `internal/platform/gitlab/normalize.go`: map GitLab projects, MRs, issues, comments, labels, releases, tags, commits, and pipeline status into neutral types.
- Create `internal/platform/gitlab/client_test.go`: httptest coverage for exact project lookup, namespace listing, MR pagination, issue pagination, comments, labels, and pipeline mapping.
- Create `internal/platform/gitlab/normalize_test.go`: unit coverage for state, draft, branch, author, label, timestamp, URL, and CI status normalization.
- Create `scripts/e2e/gitlab/docker-compose.yml`: optional GitLab CE fixture using a pinned `gitlab/gitlab-ce` image tag, temp volumes, loopback HTTP port, and `GITLAB_OMNIBUS_CONFIG`.
- Create `scripts/e2e/gitlab/wait.sh`: waits for the container health/readiness endpoint and API availability before tests begin.
- Create `scripts/e2e/gitlab/bootstrap.sh`: idempotently seeds groups, subgroups, projects, branches, merge requests, issues, labels, notes, tags, and releases for real GitLab compatibility tests.
- Create `scripts/e2e/gitlab/README.md`: documents resource cost, image tag policy, required Docker privileges, cleanup, and how to run the optional fixture.
- Modify `internal/github/client.go`: keep the live GitHub transport initially, but prepare it to be consumed by `internal/platform/github`; do not add GitLab paths here.
- Modify `internal/github/sync.go` or move to `internal/syncer/sync.go`: replace direct `github.Client` calls with `platform.Client` capability interfaces and provider-neutral persistence helpers.
- Modify `internal/github/repo_config_resolver.go` or move to `internal/platform/config_resolver.go`: resolve configured repos through provider repository discovery.
- Modify `internal/db/types.go`: add explicit `RepoIdentity` and preserve `db.MergeRequest` as the local change request row.
- Modify `internal/db/queries.go`: replace hard-coded GitHub repo upserts with platform-aware upserts.
- Create or update migration `internal/db/migrations/000017_platform_repo_identity.up.sql`: add stable provider repo fields, provider-aware lookup keys, external id uniqueness, workspace/list refs, external item/event ids, and rate-limit bucket scoping needed for GitLab nested namespaces and future providers.
- Create or update migration `internal/db/migrations/000017_platform_repo_identity.down.sql`.
- Modify `internal/config/config.go`: add explicit `platform`, `platform_host`, and platform token config while preserving `github_token_env` and old repo entries.
- Modify `cmd/middleman/main.go`, `middleman.go`, and `cmd/e2e-server/main.go`: build a provider registry instead of `map[string]ghclient.Client`.
- Create or modify `internal/server/repo_ref.go`: parse and validate provider-aware repository references from query/body payloads.
- Modify `internal/server/repo_import_handlers.go`: accept provider/host in preview and bulk add requests.
- Modify `internal/server/settings_handlers.go`: expose configured repo statuses with platform and host.
- Modify `internal/server/api_types.go` and `internal/server/huma_routes.go`: add provider/host fields, platform-aware repo-reference routes, stable error codes, and capability-gated actions.
- Regenerate `frontend/openapi/openapi.yaml`, `internal/apiclient/generated/client.gen.go`, and `packages/ui/src/api/generated/*` with `make api-generate`.
- Modify `packages/ui/src/api/types.ts` and affected stores/components only where generated types require provider/host threading.
- Modify `context/github-sync-invariants.md`: generalize identity rules to platform sync invariants and leave a GitHub-specific section for GraphQL-only behavior.
- Add `context/platform-sync-invariants.md`: provider identity, capability, normalization, and test rules.
- Add `docs/config/gitlab.md` or update `README.md`: GitLab config examples and supported/unsupported features.

## Core Design Decisions

### Identity

Use `(platform, platform_host, owner, name)` as the human-readable identity and add a stable provider repository id for platforms that expose one.

```go
package platform

type Kind string

const (
	KindGitHub Kind = "github"
	KindGitLab Kind = "gitlab"
)

type RepoRef struct {
	Platform       Kind
	Host           string
	Owner          string
	Name           string
	RepoPath       string
	PlatformRepoID string
	WebURL         string
	CloneURL       string
	DefaultBranch  string
}

func (r RepoRef) DisplayName() string {
	if r.RepoPath != "" {
		return r.RepoPath
	}
	return r.Owner + "/" + r.Name
}
```

For GitHub, `Owner` remains the owner/org login and `Name` remains the repository name. For GitLab, `Owner` is the full namespace path without the project slug, so `group/subgroup/project` is stored as `Owner: "group/subgroup", Name: "project"`, and `RepoPath` is the canonical full provider path. The GitLab project `id` is stored as `PlatformRepoID` so sync can survive namespace path changes later.

### Provider-Aware Storage Identity

The database cannot keep treating every repo path as a lower-cased GitHub owner/name pair. Existing lower-case triggers and helper functions must become provider-aware before GitLab rows are inserted.

Rules:

- GitHub keeps the current case-folded lookup behavior.
- GitLab stores display identity using the canonical `path_with_namespace` returned by GitLab and preserves spelling in `owner`, `name`, and `repo_path`.
- Provider lookup keys may be separate normalized columns, such as `owner_key`, `name_key`, and `repo_path_key`, or centralized query helpers, but the behavior must be covered by migration and query tests.
- Existing case-fold triggers from earlier migrations must be superseded in the branch-local provider migration so GitHub-style case-folding only applies where appropriate. Do not edit already-landed migrations 1-16.
- `platform_repo_id` must have a partial unique index on `(platform, platform_host, platform_repo_id)` where the value is not empty.
- Add an `UpsertRepoByProviderID` or equivalent reconciliation query. When a provider returns a stable repo id, sync should prefer that identity, update `owner/name/repo_path` on rename, and avoid creating a duplicate row.
- The old `(platform, platform_host, owner, name)` uniqueness remains as the fallback before a provider id is known.

### Workspace, Activity, And List Identity

Workspace, starred/activity, list/search, dashboard, and settings rows must not infer a repository from `owner/name` alone. Every repo-scoped API response and filter that can cross repository boundaries must carry `provider`, `platform_host`, and `repo_path`.

Rules:

- Workspace associations should store either `repo_id` or the full provider ref. If they store denormalized fields, those fields must include provider and host.
- Activity filters and dedupe keys must include provider identity whenever they refer to a repository, merge request, issue, commit, label, or event.
- List/search/dashboard responses should include a reusable `RepoRefResponse` shape so frontend stores have one identity model for GitHub, GitLab, and future providers.
- UI helpers may omit `provider = github` and `platform_host = github.com` only for visible legacy GitHub URLs; internal state and generated API types must keep the full identity.

### Acyclic Package Layout

The GitHub migration must avoid an adapter cycle. The safe dependency direction is:

```text
internal/platform         -> neutral types only
internal/github           -> GitHub transport and legacy compatibility
internal/platform/github  -> imports internal/platform plus GitHub SDK/transport helpers
internal/syncer or sync   -> imports internal/platform and provider adapters
```

`internal/github/normalize.go` must not import `internal/platform/github` if the adapter imports anything from `internal/github`. Either keep GitHub transport in `internal/github` and put the adapter in a package that imports it, or move normalization into a package that imports only SDK types, `internal/platform`, and `internal/db`. Add a package import test or `go test ./...` checkpoint immediately after the adapter lands so cycles are caught before later tasks build on them.

### Config

Support new platform-aware config without breaking the old file.

```toml
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"

[[platforms]]
type = "gitlab"
host = "gitlab.com"
token_env = "MIDDLEMAN_GITLAB_TOKEN"

[[repos]]
platform = "gitlab"
platform_host = "gitlab.com"
owner = "my-group/subgroup"
name = "my-project"
```

Rules:

- Existing repos with no `platform` remain GitHub.
- Existing `platform_host` remains valid and means a GitHub host unless `platform = "gitlab"` is set.
- `github_token_env` remains the GitHub fallback.
- GitLab has no implicit `gh auth token` fallback; GitLab tokens must come from the selected env var.
- Duplicate config detection uses `(platform, platform_host, owner, name)`.

### Provider Interfaces

Keep the base provider interface to metadata and capabilities only. Feature support is discovered through optional interfaces so future providers can implement a useful subset without adding no-op methods or fake unsupported behavior.

```go
package platform

type Capabilities struct {
	ReadRepositories bool
	ReadMergeRequests bool
	ReadIssues bool
	ReadComments bool
	ReadReleases bool
	ReadCI bool
	CommentMutation bool
	StateMutation bool
	MergeMutation bool
	WorkflowApproval bool
	ReadyForReview bool
}

type Provider interface {
	Platform() Kind
	Host() string
	Capabilities() Capabilities
}

type RepositoryReader interface {
	GetRepository(ctx context.Context, ref RepoRef) (Repository, error)
	ListRepositories(ctx context.Context, owner string, opts RepositoryListOptions) ([]Repository, error)
}

type MergeRequestReader interface {
	ListOpenMergeRequests(ctx context.Context, ref RepoRef) ([]MergeRequest, error)
	GetMergeRequest(ctx context.Context, ref RepoRef, number int) (MergeRequest, error)
	ListMergeRequestEvents(ctx context.Context, ref RepoRef, number int) ([]MergeRequestEvent, error)
}

type IssueReader interface {
	ListOpenIssues(ctx context.Context, ref RepoRef) ([]Issue, error)
	GetIssue(ctx context.Context, ref RepoRef, number int) (Issue, error)
	ListIssueEvents(ctx context.Context, ref RepoRef, number int) ([]IssueEvent, error)
}
```

The registry stores `Provider` values and exposes helpers such as `RepositoryReader(platform, host)`, `MergeRequestReader(platform, host)`, and `IssueReader(platform, host)` that return typed missing-capability errors when the provider does not implement the requested optional interface. GitHub can implement extra mutation interfaces. GitLab first implements read interfaces and advertises mutation capabilities as false.

### API Repository Identity

GitLab nested namespaces make the existing `/repos/{owner}/{name}` route family unsafe because `owner` can contain `/`. New platform-aware endpoints should follow GitLab's own convention: carry the full repository path as one URL-escaped parameter, where `group/subgroup/project` becomes `group%2Fsubgroup%2Fproject`. Handlers decode that value into `owner = "group/subgroup"` and `name = "project"` before looking up the repo.

Rules:

- Keep existing GitHub-compatible routes such as `/repos/{owner}/{name}/pulls/{number}` working for GitHub and GitHub Enterprise.
- Before committing to an escaped path-param route, prove Huma and the server middleware preserve `group%2Fsubgroup%2Fproject` as one handler parameter. If that test fails, keep the same `provider`, `platform_host`, and `repo_path` JSON shape but use the fallback route shape below.
- Preferred route shape, if the router-level proof passes, uses `repo_path` as one escaped path parameter:

```text
GET  /api/v1/providers/gitlab/hosts/gitlab.com/repos/group%2Fsubgroup%2Fproject/pull-requests/12
POST /api/v1/providers/gitlab/hosts/gitlab.com/repos/group%2Fsubgroup%2Fproject/pull-requests/12/refresh
POST /api/v1/providers/gitlab/hosts/gitlab.com/repos/group%2Fsubgroup%2Fproject/issues/7/refresh
POST /api/v1/providers/gitlab/hosts/gitlab.com/repos/group%2Fsubgroup%2Fproject/pull-requests/12/actions
```

- Fallback route shape, if normal path params cannot preserve encoded slashes, uses query/body repo refs and does not rely on `%2F` surviving route matching:

```text
GET  /api/v1/items/pull-request?provider=gitlab&platform_host=gitlab.com&repo_path=group%2Fsubgroup%2Fproject&number=12
POST /api/v1/items/pull-request/refresh
POST /api/v1/items/issue/refresh
```

Fallback route matrix:

| Workflow | Method | Preferred escaped-path route | Fallback route | Fallback identity fields |
| --- | --- | --- | --- | --- |
| PR detail | `GET` | `/api/v1/providers/{provider}/hosts/{platform_host}/repos/{repo_path}/pull-requests/{number}` | `/api/v1/items/pull-request` | query: `provider`, `platform_host`, `repo_path`, `number` |
| Issue detail | `GET` | `/api/v1/providers/{provider}/hosts/{platform_host}/repos/{repo_path}/issues/{number}` | `/api/v1/items/issue` | query: `provider`, `platform_host`, `repo_path`, `number` |
| PR refresh | `POST` | `/api/v1/providers/{provider}/hosts/{platform_host}/repos/{repo_path}/pull-requests/{number}/refresh` | `/api/v1/items/pull-request/refresh` | JSON body: `repo.provider`, `repo.platform_host`, `repo.repo_path`, `number` |
| Issue refresh | `POST` | `/api/v1/providers/{provider}/hosts/{platform_host}/repos/{repo_path}/issues/{number}/refresh` | `/api/v1/items/issue/refresh` | JSON body: `repo.provider`, `repo.platform_host`, `repo.repo_path`, `number` |
| PR comments/events refresh | `POST` | `/api/v1/providers/{provider}/hosts/{platform_host}/repos/{repo_path}/pull-requests/{number}/events/refresh` | `/api/v1/items/pull-request/events/refresh` | JSON body: `repo.provider`, `repo.platform_host`, `repo.repo_path`, `number` |
| Issue comments/events refresh | `POST` | `/api/v1/providers/{provider}/hosts/{platform_host}/repos/{repo_path}/issues/{number}/events/refresh` | `/api/v1/items/issue/events/refresh` | JSON body: `repo.provider`, `repo.platform_host`, `repo.repo_path`, `number` |
| PR action/mutation | `POST` | `/api/v1/providers/{provider}/hosts/{platform_host}/repos/{repo_path}/pull-requests/{number}/actions` | `/api/v1/items/pull-request/actions` | JSON body: `repo.provider`, `repo.platform_host`, `repo.repo_path`, `number`, `action`, action-specific fields |
| Issue action/mutation | `POST` | `/api/v1/providers/{provider}/hosts/{platform_host}/repos/{repo_path}/issues/{number}/actions` | `/api/v1/items/issue/actions` | JSON body: `repo.provider`, `repo.platform_host`, `repo.repo_path`, `number`, `action`, action-specific fields |
| Repo detail/settings | `GET` | `/api/v1/providers/{provider}/hosts/{platform_host}/repos/{repo_path}` | `/api/v1/repo` | query: `provider`, `platform_host`, `repo_path` |

Only one route family should be implemented for new provider-aware clients after the Task 5 router proof: the escaped-path family if it works, otherwise the fallback family above. Existing GitHub compatibility routes remain wrappers either way.

- Request-body routes may use the same escaped repo path or the structured repo ref shape when the action already has a JSON body:

```json
{
  "repo": {
    "provider": "gitlab",
    "platform_host": "gitlab.com",
    "repo_path": "group/subgroup/project"
  },
  "number": 12
}
```

- JSON request and response bodies use `provider` as the client-facing discriminator. Server internals can map `provider` to the existing `platform` storage field. New route path segments should also use `providers/{provider}` so generated clients expose `Provider` consistently.
- Legacy GitHub routes should return the new `provider`, `platform_host`, and `repo_path` fields in response bodies as soon as the response types change. Compatibility is route-level, not response-shape-level, because frontend stores need one item identity shape.
- Generated clients and frontend route refs must carry `provider`, `platform_host`, `repo_path`, and `number`; helper methods can split `repo_path` into `owner/name` only at DB and provider boundaries. For GitHub UI URLs, `provider` and default `github.com` host may remain omitted in the visible route only when the route helper can reconstruct the full repo ref.
- Add e2e coverage for `group/subgroup/project` detail load and refresh through the Task 5 route shape before GitLab sync is considered complete.
- The route proof must exercise the checked-in generated Go API client and frontend route helpers, not only the server handler. Tests should prove exactly one encode/decode cycle for `repo_path = "group/subgroup/project"`, including base path middleware and a `platform_host` that contains a port.
- The route proof examples must include at least these repo refs:
  - `gitlab.com`, `group/subgroup/project`
  - `gitlab.example.com:8443`, `group/subgroup/subgroup2/project`
  - `gitlab.com`, `Group/SubGroup/Project` after provider lookup returns that canonical spelling
  - `gitlab.com`, `group/subgroup/my-project_v2`

### Error Taxonomy

Platform errors need stable machine-readable codes so the UI can distinguish unsupported actions from missing auth or permission problems.

```go
type PlatformErrorCode string

const (
	ErrCodeUnsupportedCapability PlatformErrorCode = "unsupported_capability"
	ErrCodeProviderNotConfigured PlatformErrorCode = "provider_not_configured"
	ErrCodeMissingToken          PlatformErrorCode = "missing_token"
	ErrCodeInvalidRepoRef        PlatformErrorCode = "invalid_repo_ref"
	ErrCodePermissionDenied      PlatformErrorCode = "permission_denied"
	ErrCodeNotFound              PlatformErrorCode = "not_found"
	ErrCodeRateLimited           PlatformErrorCode = "rate_limited"
)
```

Server handlers should return these codes in JSON error details for platform-aware routes. GitHub legacy routes may keep their current text messages until they are migrated, but new GitLab paths must use the coded errors from the start.

| Source error | Platform code | HTTP status | Response detail |
| --- | --- | --- | --- |
| Provider registry miss | `provider_not_configured` | `502 Bad Gateway` | `{ "code": "provider_not_configured", "provider": "...", "platform_host": "..." }` |
| Missing token during startup/client creation | `missing_token` | startup error or `502 Bad Gateway` in settings flows | `{ "code": "missing_token", "provider": "...", "token_env": "..." }` |
| Config or request repo ref validation failure | `invalid_repo_ref` | `400 Bad Request` | `{ "code": "invalid_repo_ref", "field": "repo_path" }` |
| Provider optional interface/capability absent | `unsupported_capability` | `501 Not Implemented` for route families, `409 Conflict` for item-specific actions | `{ "code": "unsupported_capability", "capability": "merge_mutation" }` |
| GitHub/GitLab 401 or 403 | `permission_denied` | `502 Bad Gateway` for background/preview flows, provider status stored on sync rows | `{ "code": "permission_denied" }` |
| GitHub/GitLab 404 | `not_found` | `404 Not Found` for direct user refresh/detail, `502 Bad Gateway` for startup/import validation | `{ "code": "not_found" }` |
| GitHub/GitLab rate-limit response | `rate_limited` | `429 Too Many Requests` for foreground actions, sync backoff for background | `{ "code": "rate_limited", "reset_at": "..." }` |

Provider clients should wrap raw GitHub/GitLab errors into typed platform errors at the transport boundary when status codes are known. HTTP handlers should only map typed platform errors to response codes and should avoid parsing raw provider error strings.

### Provider-Scoped Item And Event IDs

GitHub and GitLab both expose numeric ids for the first milestone, but those ids are not globally unique across providers, repositories, or item kinds.

Rules:

- Keep existing numeric `platform_id` columns for GitHub compatibility and GitLab's numeric `id` values.
- Add `platform_external_id TEXT NOT NULL DEFAULT ''` where future providers may expose string ids for merge requests, issues, events, labels, releases, tags, and CI checks.
- Event dedupe keys must include provider, host, repo id or repo ref, parent item kind, parent item number or provider id, event kind, and provider event id or external id.
- Add collision tests where GitHub and GitLab return the same numeric event id, where two GitLab projects return the same note id, and where an MR note and issue note share an id.
- Labels should be unique per `(repo_id, name)` for display, but provider label ids or external ids should be stored when available and scoped by provider/repo.

### GitLab Mapping

Map GitLab fields into middleman rows as follows:

- `projects.id` -> `middleman_repos.platform_repo_id`.
- `projects.path_with_namespace` -> `owner/name`, with the last path segment as `name`.
- `projects.http_url_to_repo` -> repo clone URL when available.
- `merge_requests.id` -> `middleman_merge_requests.platform_id`.
- `merge_requests.iid` -> `middleman_merge_requests.number`.
- `merge_requests.web_url` -> `url`.
- `merge_requests.author.username` -> `author`; `author.name` -> `author_display_name`.
- `merge_requests.state` -> `open`, `closed`, or `merged`.
- `merge_requests.draft || work_in_progress` -> `is_draft`.
- `merge_requests.source_branch` -> `head_branch`; `target_branch` -> `base_branch`.
- `merge_requests.sha` -> `platform_head_sha` when present.
- `merge_requests.diff_refs.base_sha` or target branch commit -> `platform_base_sha` when present.
- `merge_requests.merge_status` / `detailed_merge_status` -> `mergeable_state`.
- `head_pipeline.status` -> `ci_status` and one synthesized `db.CICheck` named `GitLab Pipeline`.
- GitLab labels become `db.Label` rows. If GitLab returns only names for an endpoint, store color as empty and update richer label fields when the project labels endpoint is available.

### GitLab Client Path Handling

GitLab's HTTP API documents project identifiers as either numeric ids or URL-encoded paths. In the Go client, project-scoped method parameters should receive the raw `path_with_namespace` string, such as `group/subgroup/project`, unless middleman is constructing a raw HTTP URL itself. Passing an already escaped path such as `group%2Fsubgroup%2Fproject` into client-go methods risks double escaping.

Rules:

- Use numeric project ids after exact project lookup whenever possible.
- Before the project id is known, pass raw `path_with_namespace` values to client-go project methods and let the client encode path parameters.
- Escape `repo_path` exactly once in middleman's own Huma route helpers and generated-client route helpers.
- Add httptest assertions for raw path input, escaped route input, and a regression case where already escaped input must not produce a double-escaped GitLab request path.

### GitLab Namespace And Discussion Mapping

Repo import/preview must define how namespaces are resolved:

- Treat preview `owner` as a namespace path. Try group lookup/listing first; if the provider reports not found or not a group, try user/project listing filtered to that namespace.
- For groups, include subgroup projects in the first milestone when the GitLab API supports it, paginate all result pages, exclude archived projects by default, and return archived status in preview rows if the API includes it.
- Pattern matching applies to canonical `path_with_namespace` and project `name`. Keep matching case-insensitive for UI convenience, but persist the canonical provider path returned by lookup.
- Exact repository add must call project lookup and store the canonical `path_with_namespace`, project id, default branch, web URL, and clone URL from the response.

Comments and event sync start with:

- Merge request notes and issue notes: store non-system notes as comments/events.
- System notes: exclude from comment counts in the first milestone unless they are explicitly mapped to a known middleman event kind; document unsupported system-note parity.
- MR commits: store commit events from the MR commits endpoint.
- Discussions: do not attempt full threaded discussion parity in the first milestone. If the API endpoint returns discussions, flatten resolvable notes only when they map cleanly to existing event rows; otherwise leave the review/discussion parity follow-up intact.

Preview/import must also be bounded for large GitLab instances:

- Use request context cancellation all the way into provider pagination. If the browser cancels or the server deadline expires, stop fetching more pages.
- Default preview limit: return at most 200 rows. Hard cap: 1000 rows, even if a caller asks for more.
- Fetch GitLab pages with `per_page=100` when supported, and stop once the return limit is reached. Do not keep scanning pages only to discard extra rows.
- Add a foreground preview timeout of 20 seconds unless the server already has a stricter request deadline.
- Preview responses include `returned_count`, `scanned_count`, `truncated`, and `partial_errors`. `partial_errors` should include provider error codes and namespace/page context without leaking tokens.
- If the first provider request fails, return the typed platform error and no preview rows.
- If later pages fail after at least one successful page, return the successful rows with `truncated = true` and `partial_errors` populated.
- Exact repository add is not a partial operation: project lookup must either resolve one project and add it, or fail with a typed platform error.

### GitLab E2E Test Tiers

Use two e2e tiers instead of making every test boot a real GitLab instance:

- Fast e2e tier: always-on tests use `httptest.Server` fake GitLab API responses and real SQLite. These cover sync contracts, edge cases, timeouts, pagination, route identity, and provider-scoped persistence deterministically.
- Container compatibility tier: opt-in tests boot `gitlab/gitlab-ce` with Docker Compose, run the bootstrap script, and then exercise middleman's GitLab client against the real GitLab API.

Container fixture rules:

- Gate the container tier behind an explicit env var such as `MIDDLEMAN_GITLAB_CONTAINER_E2E=1` and a make target such as `make test-gitlab-container`. Do not run it in `make test-short`.
- Pin `GITLAB_CE_IMAGE` to a known `gitlab/gitlab-ce:<version>-ce.0` tag in CI. Avoid `latest` for repeatable tests, but allow local override through an environment variable.
- Use temporary host directories for `/etc/gitlab`, `/var/log/gitlab`, and `/var/opt/gitlab`; cleanup must remove the container, network, and volumes unless `MIDDLEMAN_KEEP_GITLAB_FIXTURE=1` is set for debugging.
- Configure loopback-only HTTP access with `external_url 'http://127.0.0.1:<port>'` through `GITLAB_OMNIBUS_CONFIG`. HTTPS, custom CAs, SSH clone, and runners stay out of scope for this fixture.
- Bootstrap should be idempotent and write a JSON manifest containing the base URL, token env var name, project ids, canonical `path_with_namespace` values, MR iids, issue iids, tag names, release names, and expected API-visible URLs.
- Prefer GitLab Rails runner or documented API calls for bootstrap. The script should create a dedicated test user or token, not depend on a human login session.
- Seed at least: `middleman/top-level`, `middleman/subgroup/nested-project`, a project with mixed-case display names where GitLab canonicalization can be observed, one archived project for preview filtering, one open MR with labels and notes, one open issue with labels and notes, commits on source and target branches, a tag, and a release.
- CI pipeline status in the real-container tier is optional unless the fixture also provisions a runner or uses GitLab-supported commit status APIs. The fake API tier remains the required coverage for every CI status mapping branch.
- Give the fixture a long startup budget, such as 10 minutes, and fail with a clear message that includes recent container logs when GitLab never becomes ready.

### GitLab CI Mapping

Middleman stores one aggregate `ci_status` plus a JSON list of checks. GitLab starts with a single synthesized check for the MR head pipeline.

| GitLab pipeline status | Middleman check status | Middleman conclusion | Aggregate `ci_status` |
| --- | --- | --- | --- |
| `created`, `waiting_for_resource`, `preparing`, `pending`, `running` | `in_progress` | empty | `pending` |
| `success` | `completed` | `success` | `success` |
| `failed` | `completed` | `failure` | `failure` |
| `canceled`, `cancelled` | `completed` | `cancelled` | `failure` |
| `skipped` | `completed` | `skipped` | `success` |
| `manual`, `scheduled` | `queued` | empty | `pending` |
| unknown non-empty status | `completed` | `neutral` | `neutral` |
| missing pipeline | empty list | empty | empty |

If the MR list response omits `head_pipeline`, detail sync should request the MR detail endpoint once before concluding CI is missing. If both omit pipeline data, leave `ci_status` and `ci_checks_json` empty rather than carrying stale data across a head SHA change.

### Existing Row Backfill

Migration `000017` must be safe for existing databases:

- Existing rows keep `platform = 'github'` and their current `platform_host`.
- `platform_repo_id`, `web_url`, `clone_url`, and `default_branch` default to empty strings in SQL so the migration is instant and reversible.
- The first successful provider sync fills these fields from provider repository metadata.
- For GitHub rows, a failed repository metadata fetch must not block PR/issue sync; it only leaves the new fields empty for that cycle.
- For GitLab rows, exact project resolution must fill `platform_repo_id` before sync proceeds when the project is reachable.

### Provider-Aware Rate Limits

Rate-limit storage must distinguish providers and hosts, because GitHub REST, GitHub GraphQL, and GitLab REST have different buckets and reset semantics.

Rules:

- Migrate rate-limit rows to key by `(platform, platform_host, api_type)` where `api_type` is a provider-local bucket such as `rest`, `graphql`, or `gitlab-rest`.
- Existing GitHub rows backfill to `platform = 'github'`.
- Add tests proving GitHub REST and GitLab REST rate-limit rows do not overwrite each other on the same host string.

### Provider-Scoped Schema Impact Matrix

The provider migration work should use this table as the implementation checklist. Tables not listed here should either be unaffected because they already point through a provider-aware parent row, or be added to the table before implementation starts.

| Table or API surface | Change | Backfill and cleanup | Indexes/query helpers |
| --- | --- | --- | --- |
| `middleman_repos` | Add `repo_path`, lookup keys, `platform_repo_id`, `web_url`, `clone_url`, `default_branch` | Existing rows backfill to `platform = 'github'`, `repo_path = owner || '/' || name`, GitHub case-folded keys, empty provider metadata | Unique `(platform, platform_host, owner_key, name_key)` or equivalent helper; partial unique `(platform, platform_host, platform_repo_id)` where non-empty; `UpsertRepoByProviderID` |
| `middleman_merge_requests` | Add `platform_external_id`; keep numeric `platform_id` | Existing rows keep empty external id because GitHub numeric ids remain valid | Keep unique `(repo_id, number)` and `(repo_id, platform_id)`; add partial unique `(repo_id, platform_external_id)` where non-empty |
| `middleman_issues` | Add `platform_external_id`; keep numeric `platform_id` | Existing rows keep empty external id | Keep unique `(repo_id, number)` and `(repo_id, platform_id)`; add partial unique `(repo_id, platform_external_id)` where non-empty |
| `middleman_mr_events` | Add `platform_external_id`; rebuild new event dedupe keys only for newly synced rows | Historical rows remain tied to provider through `merge_request_id -> repo_id`; no destructive cleanup required | New dedupe helper includes provider, host, repo, parent item kind/id, event kind, and provider event id/external id |
| `middleman_issue_events` | Add `platform_external_id`; rebuild new event dedupe keys only for newly synced rows | Historical rows remain tied to provider through `issue_id -> repo_id`; no destructive cleanup required | Same provider-scoped dedupe helper as MR events |
| `middleman_labels` | Add `platform_external_id`; keep name uniqueness per repo | Existing rows keep empty external id; label display remains `(repo_id, name)` | Keep unique `(repo_id, name)`; add partial unique `(repo_id, platform_external_id)` where non-empty |
| `middleman_merge_request_labels`, `middleman_issue_labels` | No schema change | Parent item and label rows carry provider identity | Query helpers join through item/label parents |
| `middleman_repo_overviews` | No repo identity columns needed; review `releases_json` for provider external ids before GitLab releases land | Rows are keyed by `repo_id`, so historical data already inherits provider identity | Release/tag persistence helpers should include provider external ids inside JSON or move to normalized tables before non-numeric ids are needed |
| `middleman_workspaces` | Add `platform`, `repo_path`, and preferably nullable `repo_id`; keep old denormalized fields as compatibility columns during migration | Existing rows backfill `platform = 'github'`; resolve `repo_id` by current host/owner/name where possible. If no repo exists, preserve old fields and leave `repo_id` null until the workspace is re-associated | Unique workspace identity becomes `(platform, platform_host, repo_path_key, item_type, item_number)` or `(repo_id, item_type, item_number)` when `repo_id` is known; replace lower-case workspace triggers with provider-aware keys |
| `middleman_workspace_setup_events`, `middleman_workspace_tmux_sessions` | No schema change | Inherit provider identity through `workspace_id` | No new indexes |
| `middleman_starred_items` | No schema change | Already keyed by `repo_id`, `item_type`, `number` | API/list helpers must return `RepoRefResponse` from joined repo rows |
| `middleman_stacks`, `middleman_stack_members` | No schema change | Already keyed by `repo_id` or `merge_request_id` | Stack queries must join repos when returning route refs |
| `middleman_kanban_state`, `middleman_mr_worktree_links` | No schema change | Inherit provider identity through `merge_request_id` | API/list helpers must join repos when returning route refs |
| `middleman_rate_limits` | Add `platform`; keep `platform_host` and `api_type` | Existing rows backfill `platform = 'github'`; preserve bucket values | Unique `(platform, platform_host, api_type)` |
| List/search/dashboard/activity/settings API responses | Response-shape change, not necessarily table change | Historical rows tied through `repo_id` need no cleanup; unresolved historical workspace rows return their preserved GitHub-compatible ref | Use one `RepoRefResponse` helper rather than ad hoc owner/name reconstruction |

### Self-Hosted GitLab URLs

GitLab host configuration is host-only in the first milestone:

- `platform_host = "gitlab.example.com"` maps to API base URL `https://gitlab.example.com/api/v4`.
- `platform_host = "gitlab.example.com:8443"` is valid and maps to `https://gitlab.example.com:8443/api/v4`.
- A host value with `https://` or a trailing slash is normalized to the hostname and rejected if it includes a non-root path.
- GitLab installations under a subpath, such as `https://example.com/gitlab`, are explicitly unsupported in this milestone and should fail validation with `invalid_repo_ref`.
- Custom CA and insecure TLS settings are out of scope. Users must rely on the host OS trust store.

### Repo Path Canonicalization

- GitHub repo paths are case-folded to match current behavior.
- GitLab repo paths are stored using the canonical `path_with_namespace` returned by GitLab. User input can be normalized after a successful project lookup, but duplicate detection should compare the provider-canonical path rather than lowercasing arbitrary input.
- UI search can remain case-insensitive, but provider lookups and persisted identity should preserve GitLab's canonical path spelling.

## Implementation Tasks

Standing rule for API work: every task that changes Huma routes, request schemas, response schemas, or OpenAPI-visible error details must run `make api-generate` in that same task and commit the regenerated artifacts with the contract change.

### Task 1: Add Neutral Platform Types

**Files:**
- Create: `internal/platform/types.go`
- Create: `internal/platform/client.go`
- Create: `internal/platform/registry.go`
- Create: `internal/platform/errors.go`
- Test: `internal/platform/registry_test.go`

- [ ] Write tests for registry lookup by `(platform, host)`, missing client errors, optional reader lookup, and capability lookup.
- [ ] Add `Kind`, `RepoRef`, `Repository`, `MergeRequest`, `Issue`, `Label`, `MergeRequestEvent`, `IssueEvent`, `Release`, `Tag`, `CICheck`, and `Capabilities`.
- [ ] Give repo-scoped platform models both numeric `PlatformID` and string `PlatformExternalID` fields where future providers may not expose integer ids.
- [ ] Add base `Provider` plus narrow reader and optional mutation interfaces; do not embed reader interfaces in `Provider`.
- [ ] Add `PlatformErrorCode` constants and typed errors for unsupported capability, missing provider, missing token, invalid repo ref, permission denied, not found, and rate limited.
- [ ] Run `go test ./internal/platform -shuffle=on`.
- [ ] Commit: `feat: define provider-neutral platform contracts`.

### Task 2: Persist Repository Identity And Provider Metadata

**Files:**
- Modify: `internal/db/types.go`
- Modify: `internal/db/queries.go`
- Create: `internal/db/migrations/000017_platform_repo_identity.up.sql`
- Create: `internal/db/migrations/000017_platform_repo_identity.down.sql`
- Test: `internal/db/queries_test.go`

- [ ] Add only the repo identity and provider metadata migration in this task:

```sql
ALTER TABLE middleman_repos ADD COLUMN platform_repo_id TEXT NOT NULL DEFAULT '';
ALTER TABLE middleman_repos ADD COLUMN repo_path TEXT NOT NULL DEFAULT '';
ALTER TABLE middleman_repos ADD COLUMN owner_key TEXT NOT NULL DEFAULT '';
ALTER TABLE middleman_repos ADD COLUMN name_key TEXT NOT NULL DEFAULT '';
ALTER TABLE middleman_repos ADD COLUMN repo_path_key TEXT NOT NULL DEFAULT '';
ALTER TABLE middleman_repos ADD COLUMN web_url TEXT NOT NULL DEFAULT '';
ALTER TABLE middleman_repos ADD COLUMN clone_url TEXT NOT NULL DEFAULT '';
ALTER TABLE middleman_repos ADD COLUMN default_branch TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_repos_platform_repo_id
    ON middleman_repos(platform, platform_host, platform_repo_id)
    WHERE platform_repo_id <> '';
```

- [ ] Replace `UpsertRepo(ctx, host, owner, name)` with `UpsertRepo(ctx, identity db.RepoIdentity)`.
- [ ] Add `UpsertRepoByProviderID(ctx, identity db.RepoIdentity)` or equivalent reconciliation that updates owner/name/repo_path on provider rename instead of creating a new row.
- [ ] Add `UpdateRepoProviderMetadata(ctx, repoID, metadata)` for `platform_repo_id`, `web_url`, `clone_url`, and `default_branch`.
- [ ] Keep a small compatibility helper for GitHub-heavy tests:

```go
func GitHubRepoIdentity(host, owner, name string) RepoIdentity {
	return RepoIdentity{Platform: "github", PlatformHost: host, Owner: owner, Name: name}
}
```

- [ ] Update queries to select and scan new repo fields.
- [ ] Add migration tests proving existing rows keep `platform = 'github'`, new metadata fields default to empty strings, GitHub upserts still use `platform = 'github'`, GitLab upserts use `platform = 'gitlab'`, and the same host/owner/name on different platforms creates different rows.
- [ ] Add query tests proving provider metadata can be filled after the initial upsert without changing the repo identity.
- [ ] Run `go test ./internal/db -shuffle=on`.
- [ ] Commit: `feat: persist provider-aware repository identity`.

### Task 2A: Replace Repo And Workspace Canonicalization

**Files:**
- Modify: `internal/db/queries.go`
- Modify: `internal/db/migrations/000017_platform_repo_identity.up.sql`
- Modify: `internal/db/migrations/000017_platform_repo_identity.down.sql`
- Test: `internal/db/queries_test.go`

- [ ] Replace or supersede existing lower-case repo and workspace triggers with provider-aware canonicalization. GitHub keys are case-folded; GitLab display fields preserve canonical provider spelling while lookup keys are derived from provider-canonical paths.
- [ ] Backfill `repo_path`, `owner_key`, `name_key`, and `repo_path_key` for existing GitHub repos and workspaces.
- [ ] Keep this schema work folded into `000017_platform_repo_identity` while the branch is in flight; do not add `000018` for this feature.
- [ ] Add migration and query tests proving GitHub rows still case-fold, GitLab rows preserve canonical `path_with_namespace`, lookup keys prevent duplicates, and `platform_repo_id` uniqueness updates renamed repos.
- [ ] Add historical cleanup tests for workspace rows that cannot be tied to a repo id: they should be preserved as GitHub-compatible refs rather than deleted.
- [ ] Run `go test ./internal/db -run 'Test.*Canonical|Test.*Repo.*Identity|Test.*Workspace' -shuffle=on`.
- [ ] Commit: `refactor: make repository canonicalization provider-aware`.

### Task 2B: Make Workspace And List Refs Provider-Aware

**Files:**
- Modify: `internal/db/types.go`
- Modify: `internal/db/queries.go`
- Modify: `internal/db/migrations/000017_platform_repo_identity.up.sql`
- Modify: `internal/db/migrations/000017_platform_repo_identity.down.sql`
- Test: `internal/db/queries_test.go`
- Test: affected server API tests that return workspace, starred, activity, list, search, dashboard, or settings rows

- [ ] Apply the `middleman_workspaces`, `middleman_starred_items`, stack, kanban, worktree-link, and API response rows from the Provider-Scoped Schema Impact Matrix.
- [ ] Prefer `repo_id` for persisted associations; keep denormalized provider refs only where the existing workflow needs them before a repo row exists.
- [ ] Return `RepoRefResponse` from list/search/dashboard/workspace/starred/activity/settings query helpers.
- [ ] Add tests proving historical rows joined through `repo_id` need no cleanup, and unresolved historical workspace rows still return their preserved GitHub-compatible ref.
- [ ] Run `go test ./internal/db ./internal/server -run 'Test.*Workspace|Test.*Activity|Test.*Search|Test.*Dashboard|Test.*Settings' -shuffle=on`.
- [ ] Commit: `refactor: carry provider refs through workspace and list data`.

### Task 2C: Scope External Item And Event IDs

**Files:**
- Modify: `internal/db/types.go`
- Modify: `internal/db/queries.go`
- Modify: `internal/db/migrations/000017_platform_repo_identity.up.sql`
- Modify: `internal/db/migrations/000017_platform_repo_identity.down.sql`
- Test: `internal/db/queries_test.go`

- [ ] Apply the `platform_external_id` rows from the Provider-Scoped Schema Impact Matrix for merge requests, issues, events, labels, releases/tags if normalized, and CI checks if normalized.
- [ ] Keep existing numeric `platform_id` columns for GitHub and GitLab numeric ids.
- [ ] Update event dedupe helpers to include provider, host, repo, parent item kind/id, event kind, and provider event id/external id.
- [ ] Add collision tests for same numeric event id across providers, same GitLab note id across projects, and same note id on MR versus issue events.
- [ ] Run `go test ./internal/db -run 'Test.*ExternalID|Test.*Event.*Dedupe|Test.*Label' -shuffle=on`.
- [ ] Commit: `refactor: scope provider item event identity`.

### Task 2D: Key Rate Limits By Provider

**Files:**
- Modify: `internal/db/types.go`
- Modify: `internal/db/queries.go`
- Modify: `internal/db/migrations/000017_platform_repo_identity.up.sql`
- Modify: `internal/db/migrations/000017_platform_repo_identity.down.sql`
- Test: `internal/db/queries_test.go`

- [ ] Add `platform` to `middleman_rate_limits` and backfill existing rows to `github`.
- [ ] Change the rate-limit unique key to `(platform, platform_host, api_type)`.
- [ ] Update rate-limit query helpers to require platform, host, and provider-local API type.
- [ ] Add tests proving GitHub REST, GitHub GraphQL, and GitLab REST buckets do not overwrite each other.
- [ ] Run `go test ./internal/db -run 'Test.*RateLimit' -shuffle=on`.
- [ ] Commit: `refactor: scope rate limits by provider`.

### Task 3: Generalize Config And Token Resolution

**Files:**
- Modify: `internal/config/config.go`
- Modify: `config.example.toml`
- Test: `internal/config/config_test.go`

- [ ] Add `Platform` to `config.Repo` and `PlatformConfig` to `config.Config`.
- [ ] Normalize repo platform to `github` when omitted.
- [ ] Keep `GitHubToken()` and add `TokenForPlatformHost(platform, host, repoTokenEnv string)`.
- [ ] Teach URL parsing to accept GitLab HTTP/SSH URLs, including nested namespace paths.
- [ ] Reject ambiguous GitLab URLs with fewer than two path segments.
- [ ] Normalize `https://gitlab.example.com/` to `gitlab.example.com`; preserve valid host ports such as `gitlab.example.com:8443`; reject `https://example.com/gitlab` subpath installs with `invalid_repo_ref`.
- [ ] Add tests for old GitHub config, GitHub Enterprise config, GitLab env token config, nested GitLab namespace config, self-hosted GitLab host normalization, host port preservation, subpath rejection, duplicate detection across platform/host, and token env conflicts scoped to `(platform, host)`.
- [ ] Run `go test ./internal/config -shuffle=on`.
- [ ] Commit: `feat: configure repositories by platform`.

### Task 4: Wrap GitHub In The Platform Adapter

**Files:**
- Create: `internal/platform/github/adapter.go`
- Create: `internal/platform/github/normalize.go`
- Test: `internal/platform/github/normalize_test.go`
- Modify: `internal/github/normalize.go`
- Modify: `internal/github/sync_test.go`

- [ ] Move GitHub normalization logic to return `platform.MergeRequest`, `platform.Issue`, and event types.
- [ ] Keep compatibility functions `NormalizePR`, `NormalizeIssue`, and event normalizers until the syncer is migrated, but keep package dependencies acyclic. If the adapter imports `internal/github`, compatibility wrappers must not import the adapter back.
- [ ] Add a short package-layout check in the task review: `go test ./internal/github ./internal/platform/github -shuffle=on` must pass before syncer work starts, so import cycles are caught early.
- [ ] Add adapter tests proving GitHub PR number/id/state/draft/branches/labels/CI events match current persisted DB behavior.
- [ ] Run `go test ./internal/github ./internal/platform/github -shuffle=on`.
- [ ] Commit: `refactor: adapt GitHub data through platform models`.

### Task 5: Add Platform-Aware Repo Routes

**Files:**
- Create: `internal/server/repo_ref.go`
- Modify: `internal/server/huma_routes.go`
- Modify: `internal/server/api_types.go`
- Modify: `packages/ui/src/routes.ts`
- Modify: `packages/ui/src/api/types.ts`
- Test: `internal/server/api_test.go`
- Test: `packages/ui/src/routes.test.ts`

- [ ] Write the first server integration test before adding the final route contract. It must issue real HTTP requests with `group%2Fsubgroup%2Fproject`, `group%2Fsubgroup%2Fsubgroup2%2Fproject`, `Group%2FSubGroup%2FProject`, and `group%2Fsubgroup%2Fmy-project_v2`, then assert the handler receives one `repo_path` value, not split path segments.
- [ ] Extend the route proof through the generated Go API client and frontend route helper, including base path middleware and `platform_host = "gitlab.example.com:8443"`. The test must prove exactly one encode/decode cycle for every route proof repo ref from the API Repository Identity section.
- [ ] If Huma/server routing cannot preserve `%2F` as one parameter, switch this task to the query/body fallback route shape from the API Repository Identity section before proceeding.
- [ ] Add `RepoRefInput`/`RepoRefResponse` API shapes with `provider`, `platform_host`, and `repo_path`.
- [ ] Add parser tests for `repo_path = "owner/repo"` and `repo_path = "group/subgroup/project"`.
- [ ] Update list/search/dashboard/workspace/starred/activity response types to carry `RepoRefResponse` rather than ad hoc owner/name fields.
- [ ] Add the selected platform-aware route family. Use escaped `repo_path` as one route parameter if the router proof passes; otherwise use the fallback query/body routes from the API Repository Identity section:

```text
GET /api/v1/providers/{provider}/hosts/{platform_host}/repos/{repo_path}/pull-requests/{number}
GET /api/v1/providers/{provider}/hosts/{platform_host}/repos/{repo_path}/issues/{number}
POST /api/v1/providers/{provider}/hosts/{platform_host}/repos/{repo_path}/pull-requests/{number}/refresh
POST /api/v1/providers/{provider}/hosts/{platform_host}/repos/{repo_path}/issues/{number}/refresh
```

- [ ] Keep existing GitHub routes as compatibility wrappers that build a full repo ref and call the same handler internals.
- [ ] Ensure legacy GitHub route responses include `provider`, `platform_host`, and `repo_path` once the shared response types change.
- [ ] Return coded platform errors from new routes for invalid repo refs, missing provider clients, and unsupported capabilities.
- [ ] Run `make api-generate` immediately after the route contract changes.
- [ ] Run `go test ./internal/server -run 'Test.*RepoRef|Test.*Pull.*Route|Test.*Issue.*Route' -shuffle=on`.
- [ ] Run frontend route/type tests that cover the selected nested GitLab repo-path route family.
- [ ] Commit: `feat: add platform-aware repository routes`.

### Task 6: Wire Syncer Registry Lookup

**Files:**
- Modify: `internal/github/sync.go`
- Modify: `internal/github/repo_config_resolver.go`
- Create or modify: `internal/platform/persist.go`
- Test: `internal/github/sync_test.go`
- Test: `internal/github/repo_config_resolver_test.go`

- [ ] Change `Syncer.clients` from `map[string]Client` to the platform registry while keeping all configured clients GitHub adapters.
- [ ] Change syncer client lookup to request optional `RepositoryReader`, `MergeRequestReader`, and `IssueReader` interfaces as needed.
- [ ] Move provider-model-to-DB persistence into provider-neutral helpers so GitLab does not need to mimic GitHub-specific sync internals.
- [ ] Keep GitHub GraphQL bulk fetch, ETag invalidation, and detailed diff behavior as optional paths layered around the neutral persistence flow.
- [ ] Keep public method behavior unchanged for existing GitHub callers.
- [ ] Add tests for missing provider, missing optional reader, and duplicate owner/name on different platforms.
- [ ] Run `go test ./internal/github -run 'Test.*Client|Test.*Resolve|Test.*Tracked' -shuffle=on`.
- [ ] Commit: `refactor: resolve sync clients through platform registry`.

### Task 7: Propagate Full RepoRef Through Sync Paths

**Files:**
- Modify: `internal/github/sync.go`
- Modify: `internal/github/queue.go`
- Modify: `internal/github/repo_config_resolver.go`
- Test: `internal/github/sync_test.go`
- Test: `internal/github/repo_config_resolver_test.go`

- [ ] Change `RepoRef` to carry `Platform`, `Host`, `Owner`, `Name`, `RepoPath`, and `PlatformRepoID`.
- [ ] Update `syncRepo`, `indexSyncRepo`, `SyncMR`, `SyncIssue`, detail queue, backfill, and watched MR paths to pass a full `RepoRef` instead of reconstructing host/owner/name at call sites.
- [ ] Add tests proving the same `owner/name/number` on GitHub and GitLab resolves to the intended platform row and does not cross-sync.
- [ ] Run `go test ./internal/github -run 'Test.*Sync|Test.*Issue|Test.*MR' -shuffle=on`.
- [ ] Commit: `refactor: thread full repository refs through sync`.

### Task 8: Preserve GitHub Optimizations And Diff Behavior

**Files:**
- Modify: `internal/github/sync.go`
- Modify: `internal/platform/client.go`
- Test: `internal/github/sync_test.go`
- Test: `internal/github/merged_diff_test.go`

- [ ] Preserve GitHub GraphQL bulk fetch as a GitHub-only optimization behind an optional `BulkMergeRequestFetcher` interface.
- [ ] Preserve ETag invalidation as an optional conditional-list capability. GitLab can no-op initially.
- [ ] Use provider-supplied clone URLs for diff sync instead of formatting `https://host/owner/name.git`.
- [ ] Add tests proving GitHub GraphQL bulk fetch still runs when available, ETag invalidation still recovers failed GitHub list syncs, and diff clone URLs support GitLab nested namespace paths.
- [ ] Run `go test ./internal/github -run 'Test.*GraphQL|Test.*ETag|Test.*Diff|Test.*Merged' -shuffle=on`.
- [ ] Commit: `refactor: keep GitHub sync optimizations optional`.

### Task 9: Add GitLab Client And Normalization

**Files:**
- Create: `internal/platform/gitlab/client.go`
- Create: `internal/platform/gitlab/normalize.go`
- Create: `internal/platform/gitlab/client_test.go`
- Create: `internal/platform/gitlab/normalize_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] Add `gitlab.com/gitlab-org/api/client-go` with `go get gitlab.com/gitlab-org/api/client-go`.
- [ ] Import the GitLab client as `gitlab "gitlab.com/gitlab-org/api/client-go"` and use the package docs at <https://pkg.go.dev/gitlab.com/gitlab-org/api/client-go> as the source of truth for client APIs.
- [ ] Build clients with `gitlab.NewClient(token, gitlab.WithBaseURL("https://<host>/api/v4"))`.
- [ ] Add a constructor option for test-only base URL injection or provider registry injection so e2e tests can point the GitLab client at `httptest.Server` without weakening production host validation.
- [ ] Implement exact project lookup using raw `path_with_namespace` when calling client-go methods, not pre-escaped strings. Use numeric project ids for later project-scoped calls after lookup succeeds.
- [ ] Add tests proving raw `group/subgroup/project` produces the expected GitLab request and already escaped `group%2Fsubgroup%2Fproject` is rejected or normalized before it can double-escape.
- [ ] Implement namespace listing for repo preview. For groups, use group project listing with subgroups when available; for users, fall back to project search/listing filtered by namespace path; paginate only until the preview limit is reached and exclude archived projects by default.
- [ ] Respect preview context cancellation, the 20 second foreground deadline, default 200 row return limit, and hard 1000 row cap.
- [ ] Implement MR list/detail pagination with project `id` when known and raw `path_with_namespace` when not.
- [ ] Implement issues, MR notes, issue notes, MR commits, releases, tags, and basic pipeline status reads.
- [ ] Store non-system notes as comments/events, exclude system notes unless explicitly mapped to a known middleman event kind, and keep full threaded discussion parity out of the first milestone.
- [ ] Normalize GitLab `iid` as middleman `number`; never use the global MR id as `number`.
- [ ] Implement the GitLab CI mapping table exactly: pending/running statuses become `pending`, success and skipped become `success`, failed/canceled become `failure`, manual/scheduled become `pending`, unknown statuses become `neutral`, and missing pipelines leave CI empty.
- [ ] Add httptest fixtures for paginated GitLab responses, namespace group/user fallback, preview timeout/cancellation, truncated preview metadata, partial page failure metadata, archived project filtering, non-system/system note handling, pipeline status mapping, missing pipeline fallback, and self-hosted base URL construction.
- [ ] Run `go test ./internal/platform/gitlab -shuffle=on`.
- [ ] Commit: `feat: add GitLab read client`.

### Task 10: Wire Runtime Client Construction

**Files:**
- Modify: `cmd/middleman/main.go`
- Modify: `middleman.go`
- Modify: `cmd/e2e-server/main.go`
- Test: `cmd/middleman/main_test.go`
- Test: `middleman_test.go`

- [ ] Build per-platform host token maps from config.
- [ ] Construct GitHub adapters for GitHub platform entries.
- [ ] Construct GitLab clients for GitLab platform entries.
- [ ] Seed public GitHub as before so existing settings import works with old configs.
- [ ] Stop failing startup when no GitHub token exists but only GitLab repos are configured.
- [ ] Preserve the existing "GitHub token missing" error for configs that still require GitHub.
- [ ] Run `go test ./cmd/middleman . -shuffle=on`.
- [ ] Commit: `feat: wire platform registry at startup`.

### Task 11: Provider-Aware Repository Import

**Files:**
- Modify: `internal/server/repo_import_handlers.go`
- Modify: `internal/server/settings_handlers.go`
- Modify: `internal/server/api_types.go`
- Test: `internal/server/settings_e2e_test.go`
- Test: `internal/server/api_test.go`

- [ ] Extend preview request:

```json
{
  "provider": "gitlab",
  "platform_host": "gitlab.com",
  "owner": "my-group/subgroup",
  "pattern": "service-*",
  "limit": 200
}
```

- [ ] Default omitted `provider` to `github` and omitted `platform_host` to the provider default.
- [ ] Return provider and host on preview rows.
- [ ] Add preview response metadata: `returned_count`, `scanned_count`, `truncated`, and `partial_errors`.
- [ ] Enforce default preview limit 200, hard cap 1000, and 20 second foreground timeout for GitLab namespace previews.
- [ ] Return a full `RepoRefResponse` on preview, settings, workspace, starred/activity, list, and search rows so generated clients never have to reconstruct provider identity from owner/name alone.
- [ ] Update bulk add request rows to include provider, host, and `repo_path`.
- [ ] Validate exact rows through the matching provider registry client.
- [ ] Resolve GitLab preview namespaces with the group-first/user-fallback rules from GitLab Namespace And Discussion Mapping, including subgroup pagination and archived-project filtering.
- [ ] Store the provider-canonical `repo_path` returned by lookup. GitHub paths continue to case-fold; GitLab paths preserve `path_with_namespace`.
- [ ] Keep old GitHub-only requests working.
- [ ] Add full-stack e2e coverage using a fake GitLab API server and real SQLite through test-only provider registry or base URL injection: preview GitLab repos, add one exact repo, trigger syncer tracked repo update.
- [ ] Add e2e coverage for a truncated GitLab preview and a partial page failure after at least one successful page.
- [ ] Run `make api-generate` immediately after changing preview or bulk request/response contracts.
- [ ] Run `go test ./internal/server -run 'Test.*Repo.*' -shuffle=on`.
- [ ] Commit: `feat: import repositories from configured platforms`.

### Task 12: Gate Provider-Specific Actions

**Files:**
- Modify: `internal/server/huma_routes.go`
- Modify: `internal/server/api_types.go`
- Modify: `packages/ui/src/api/types.ts`
- Modify: PR/MR action components that call GitHub-only routes
- Test: `internal/server/api_test.go`
- Test: affected frontend component tests

- [ ] Add capabilities to repository or item detail responses.
- [ ] Return `409 Conflict` or `501 Not Implemented` for mutation routes when the provider lacks the capability, with `unsupported_capability`.
- [ ] Return `provider_not_configured`, `missing_token`, `invalid_repo_ref`, `permission_denied`, `not_found`, and `rate_limited` from the new platform-aware route family where applicable.
- [ ] Hide or disable unsupported action buttons in the frontend for GitLab rows.
- [ ] Keep GitHub action behavior unchanged.
- [ ] Run `make api-generate`.
- [ ] Run `go test ./internal/server -shuffle=on`.
- [ ] Run `bun test` in affected frontend/package workspaces according to existing package scripts.
- [ ] Commit: `feat: expose platform action capabilities`.

### Task 13: GitLab Sync E2E

**Files:**
- Create or modify: `internal/server/gitlab_sync_e2e_test.go`
- Modify: `internal/testutil/fixture_client.go` only if shared fixture support is useful
- Test: `internal/server/gitlab_sync_e2e_test.go`

- [ ] Start a fake GitLab API server with endpoints for project lookup, MRs, issues, comments, releases, tags, commits, and pipeline status.
- [ ] Configure middleman with one GitLab repo and token env var, and route the provider through test-only registry/base URL injection rather than relying on production `https://<host>/api/v4` construction.
- [ ] Run the syncer once against real SQLite.
- [ ] Assert repo row has `platform = 'gitlab'`, GitLab host, namespace path, project id, web URL, clone URL, and default branch.
- [ ] Assert MR row has `number = iid`, `platform_id = id`, draft/state/branches/labels/CI status populated.
- [ ] Assert issue row and comments/events are populated.
- [ ] Assert nested namespace detail and refresh calls work through the Task 5 route shape selected by the router proof: escaped `repo_path` path parameter when supported, otherwise the query/body fallback.
- [ ] Assert list/search/dashboard/workspace/starred/activity API responses include `provider`, `platform_host`, and `repo_path` for GitLab rows.
- [ ] Assert provider-scoped event dedupe does not collapse GitHub and GitLab rows with the same provider event id.
- [ ] Assert GitHub rows remain absent.
- [ ] Run `go test ./internal/server -run TestGitLabSync -shuffle=on`.
- [ ] Commit: `test: cover GitLab repository sync end to end`.

### Task 13A: Optional Real GitLab CE Container E2E

**Files:**
- Create: `scripts/e2e/gitlab/docker-compose.yml`
- Create: `scripts/e2e/gitlab/wait.sh`
- Create: `scripts/e2e/gitlab/bootstrap.sh`
- Create: `scripts/e2e/gitlab/README.md`
- Create or modify: `internal/server/gitlab_container_e2e_test.go`
- Modify: `Makefile`

- [ ] Add `make test-gitlab-container` that requires `MIDDLEMAN_GITLAB_CONTAINER_E2E=1`, Docker, and Docker Compose.
- [ ] Use `gitlab/gitlab-ce` with a pinned `GITLAB_CE_IMAGE` default, loopback-only HTTP port, temporary volumes, and `GITLAB_OMNIBUS_CONFIG` for `external_url`.
- [ ] Add `wait.sh` to wait for GitLab readiness and print recent container logs on timeout.
- [ ] Add `bootstrap.sh` to idempotently create the seeded groups, subgroups, projects, branches, merge requests, issues, labels, notes, tags, and releases described in GitLab E2E Test Tiers.
- [ ] Have bootstrap emit a fixture manifest JSON consumed by the Go e2e test instead of hard-coding ids that GitLab assigns dynamically.
- [ ] Configure middleman against the container base URL through test-only provider base URL injection and run a real sync into SQLite.
- [ ] Assert exact project lookup, namespace preview, archived filtering, nested namespace path preservation, MR/issue sync, comments/events, labels, tags, and releases against the real GitLab API.
- [ ] Keep CI pipeline mapping assertions in the fake API e2e unless the container fixture provisions a supported status source.
- [ ] Ensure cleanup removes containers, networks, and temp volumes by default; preserve them only when `MIDDLEMAN_KEEP_GITLAB_FIXTURE=1`.
- [ ] Run `MIDDLEMAN_GITLAB_CONTAINER_E2E=1 make test-gitlab-container` locally or in an opt-in CI job before marking GitLab support production-ready.
- [ ] Commit: `test: add optional GitLab CE container e2e fixture`.

### Task 14: Documentation And Invariants

**Files:**
- Create: `context/platform-sync-invariants.md`
- Modify: `context/github-sync-invariants.md`
- Modify: `README.md`
- Modify: `config.example.toml`

- [ ] Document identity as `(platform, platform_host, owner, name)`.
- [ ] Document GitLab nested namespace handling and escaped `repo_path` route conventions.
- [ ] Document supported GitLab features and unsupported mutation actions.
- [ ] Document token env configuration for GitLab and self-hosted GitLab.
- [ ] Run `go test ./... -shuffle=on`.
- [ ] Commit: `docs: describe platform sync invariants`.

## Kata Task Graph

This plan has been converted into kata issues in project `github.com/wesm/middleman`.

- Epic: `#1` GitLab platform abstraction implementation.
- `#2` Add neutral platform contracts. Ready first; no implementation blockers.
- `#3` Persist provider-aware repository identity. Blocked by `#2`.
- `#4` Replace repo and workspace canonicalization. Blocked by `#3`.
- `#5` Carry provider refs through workspace and list data. Blocked by `#4`.
- `#6` Scope provider item and event identity. Blocked by `#5`.
- `#7` Scope rate limits by provider. Blocked by `#6`.
- `#8` Configure repositories by provider. Blocked by `#2`.
- `#9` Adapt GitHub data through platform models. Blocked by `#2`.
- `#10` Add platform-aware repository routes. Blocked by `#5`.
- `#11` Resolve sync clients through provider registry. Blocked by `#2`, `#3`, `#8`, and `#9`.
- `#12` Thread full repository refs through sync. Blocked by `#11`.
- `#13` Keep GitHub sync optimizations optional. Blocked by `#12`.
- `#14` Add GitLab read client and normalization. Blocked by `#2` and `#8`.
- `#15` Wire platform registry at startup. Blocked by `#8`, `#9`, and `#14`.
- `#16` Import repositories from configured providers. Blocked by `#10`, `#14`, and `#15`.
- `#17` Expose and gate provider action capabilities. Blocked by `#10` and `#15`.
- `#18` Cover GitLab repository sync end to end. Blocked by `#12`, `#14`, `#15`, `#16`, and `#17`.
- `#19` Add optional GitLab CE container e2e fixture. Blocked by `#14` and `#18`.
- `#20` Document platform sync invariants. Blocked by `#17`, `#18`, and `#21`.
- `#21` Review provider abstraction extensibility. Blocked by `#1` progress and currently blocks final documentation.

Use `kata ready --json` to find the next unblocked task. At creation time, the first implementation-ready issue is `#2`.

## Verification

Run these before declaring the implementation complete:

```bash
make api-generate
git diff --exit-code -- frontend/openapi/openapi.yaml internal/apiclient/generated packages/ui/src/api/generated
go test ./... -shuffle=on
make frontend
make test-short
MIDDLEMAN_GITLAB_CONTAINER_E2E=1 make test-gitlab-container  # opt-in before production-ready GitLab support
```

If `make api-generate` changes files, commit the generated artifacts and rerun the affected Go/frontend tests after generation. If frontend action gating or route behavior changes are visible, also run the relevant Playwright e2e tests rather than relying only on unit tests.

Do not run `npm` commands; use Bun for frontend work.

## Risk Notes

- GitLab nested namespaces are the main identity wrinkle. The plan stores the full namespace in `owner`; Task 5 must prove the server can preserve a single escaped `repo_path` route parameter before using that route shape. If the proof fails, the implementation must use the query/body fallback while still threading `provider`, `platform_host`, and `repo_path` everywhere it addresses a repo-scoped item.
- GitHub GraphQL bulk sync should stay GitHub-specific. Trying to model GitLab through the GitHub GraphQL bulk fetcher will make the abstraction brittle.
- Mutation parity is intentionally not part of the first GitLab milestone. Capability flags make this visible and prevent GitLab rows from showing buttons that cannot work.
- Rate limits are not identical across providers. Rate-limit rows must be keyed by `(platform, platform_host, api_type)` so GitHub REST, GitHub GraphQL, and GitLab REST cannot overwrite each other.
- The GitLab CE container fixture is a compatibility check, not the default correctness harness. Keep fake API e2e coverage for every edge case because the real container is slow, stateful, and harder to diagnose.
- Keep `db.MergeRequest` as the local row type for now. Renaming database and generated API vocabulary to "change request" is a separate migration-sized project.

## Self-Review

- Spec coverage: covers GitLab repo fetching, GitLab MR/issue sync, GitHub migration into an abstraction, future provider extensibility, config, DB, server, frontend, fake API e2e, optional GitLab CE container e2e, docs, and tests.
- Placeholder scan: no placeholder markers, deferred code stubs, or unnamed files remain.
- Type consistency: the plan consistently uses `platform.Kind`, `platform.RepoRef`, `Capabilities`, and provider reader interfaces.
- Scope check: this is one large but coherent platform-enablement project. If execution feels too broad, split after Task 5: first land the abstraction with GitHub unchanged, then land GitLab as a second branch.
