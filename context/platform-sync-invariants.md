# Platform Sync Invariants

Use this document for changes that touch provider-aware repository identity,
sync, import, server routes, settings, or API responses. For package layout,
provider interfaces, and the checklist for adding a new provider, read
[`context/provider-architecture.md`](./provider-architecture.md) first.

## Identity

Repository identity is `(platform, platform_host, owner, name)`, with
`repo_path` as the provider-canonical full path and provider IDs used for
reconciliation when available.

- `platform` is the provider kind, such as `github`, `gitlab`, `forgejo`, or
  `gitea`.
- `platform_host` is the normalized host for that provider. Preserve ports.
- `owner` and `name` are provider-canonical display/config fields.
- `repo_path` carries the full provider path when `owner/name` is not enough.
- `platform_repo_id` / provider external IDs are stable provider identities;
  prefer them for rename reconciliation, but never drop human-readable fields.

GitLab nested namespaces make `repo_path` mandatory for reliable addressing:
`group/subgroup/project` has owner `group/subgroup` and name `project`.
GitHub repositories can continue to omit `repo_path` when the path is exactly
`owner/name`.

Forgejo and Gitea use GitHub-like two-segment repository paths. Preserve
provider-canonical owner/name casing; do not lowercase them like GitLab.
`repo_path` is normally `owner/name` and is primarily a canonicalization aid for
URL-parsed config or provider responses.

Do not identify repos, merge requests, issues, events, stars, workspaces, or
activity rows by owner/name/number alone. Thread the full provider ref through
requests, sync queues, persistence, and responses. Dedupe keys for items and
events must be scoped by persisted repo ID or full provider identity.

## Provider Hosts And Tokens

Each configured provider host may have its own token env var.

- Legacy GitHub config still defaults to `github` on `github.com`.
- GitLab public config defaults to `gitlab.com`.
- Forgejo public config defaults to `codeberg.org`.
- Gitea public config defaults to `gitea.com`.
- Self-hosted hosts are hostnames with optional ports, not URL paths.
- A missing token should fail only the provider host that needs it.

Provider clients must be registered by `(platform, platform_host)`. Provider
startup builds host-scoped rate trackers, budgets, clone tokens, GitHub GraphQL
fetchers where applicable, and a `platform.Registry`. A third provider should
add metadata, a factory, and an implementation; it should not masquerade as
GitHub, GitLab, Forgejo, or Gitea.

Token lookup is also scoped by `(provider, platform_host)`. A repo-level
`token_env` overrides that repo. Otherwise the configured `[[platforms]]`
entry for the same provider/host wins, then the provider public-host default is
used where supported: `MIDDLEMAN_GITHUB_TOKEN`, `MIDDLEMAN_GITLAB_TOKEN`,
`MIDDLEMAN_FORGEJO_TOKEN`, or `MIDDLEMAN_GITEA_TOKEN`. Do not let a token for
one provider host leak into another host with the same hostname string under a
different provider kind.

Minimum read scope should cover repository metadata, merge requests or pull
requests, issues, comments, commits, tags, releases, and CI/status data. Write
scopes are only required for mutation capabilities: comments, issue creation,
issue or PR content/state changes, merge, review approval, workflow approval,
or ready-for-review.

## Sync Capabilities

Middleman reads repositories, merge requests, issues, releases, tags, CI, and
timeline/comment-like events through provider capability interfaces in
`internal/platform`. Providers implement only supported optional interfaces;
registry helpers return typed errors for missing providers or capabilities.

- Missing optional capabilities should degrade that feature with a typed
  platform error, not break unrelated sync work.
- Mutation routes must check provider capabilities before posting comments,
  changing state, merging, requesting review, or approving workflows.
  Server handlers translate these typed platform errors into the stable problem
  envelope described in [`context/error-handling.md`](./error-handling.md).
- Forgejo and Gitea currently expose only SDK-proven mutations: comments,
  issue creation, issue and PR content/state edits, merge, and review approval.
  Workflow approval and ready-for-review must remain hidden or return typed
  `unsupported_capability` errors until proven per provider.
- GitHub GraphQL bulk fetch, ETag recovery, and detailed diff behavior are
  GitHub-only optimizations. Keep them optional around the neutral persistence
  path.
- Provider-supplied web URLs, clone URLs, default branches, platform ids, and
  external ids should be persisted when available instead of reconstructed from
  host/owner/name.

## Label Catalogs And Mutations

Label picker data comes from a cached repo catalog, not from labels currently
assigned to visible items. Catalog refresh marks which labels are currently
selectable while preserving historical assigned labels until item sync removes
them. Stale `GET repo labels` responses should return cached labels immediately
and enqueue a deduped background refresh with `syncing=true`; catalog errors are
repo metadata and must not fail normal PR/issue sync.

Label mutations replace the full desired label name set. Server handlers must
check `read_labels` and `label_mutation`, reject missing/null/empty/duplicate or
non-catalog names, call the provider mutator first, then persist the returned
provider labels to SQLite so the next sync does not revert the edit.

## GitLab Shape

GitLab API calls address projects by numeric id or URL-escaped path with
slashes. Middleman should prefer the stored provider id after resolution and
preserve `path_with_namespace` as `repo_path`.

GitLab merge request and issue `iid` values are repo-scoped numbers. Persist
provider object ids separately from user-visible numbers, and scope events by
provider identity so equal GitHub/GitLab ids do not collide.

## Forgejo And Gitea Shape

Forgejo and Gitea use owner/name repository addressing in the REST and SDK
surfaces. Middleman should still persist provider repo IDs and external object
IDs when available, but route and config identity should remain
`(provider, host, owner, name)` with optional `repo_path` for canonical display.

Codeberg is Forgejo's public host. gitea.com is Gitea's public host.
Self-hosted Forgejo and Gitea instances are separate provider-host entries even
when they have the same owner/name pairs as public repos.

Actions/CI parity is provider-specific. Forgejo reads Actions runs through the
shared gitealike provider. Gitea reads repository workflow runs only when the
pinned SDK and server version expose the Gitea 1.26+ `/actions/runs` API; older
Gitea hosts stay status-only. Gitealike CI normalization must merge commit
statuses and Actions runs without duplicating a check already represented by
the status endpoint. Neither Forgejo nor Gitea should claim workflow approval or
ready-for-review support unless the provider interface and server/UI capability
tests prove those exact operations.

## Import And Routes

Repository import requests and route/query shapes should carry
`provider`, `platform_host`, and either `repo_path` or exact `owner/name`.

- Provider-aware item routes use `/pulls/{provider}/{owner}/{name}/{number}`,
  `/issues/{provider}/{owner}/{name}/{number}`, and `/repo/{provider}/{owner}/{name}`.
- Non-default hosts use the `/host/{platform_host}/...` route prefix.
- Do not add new `/repos/{owner}/{name}/pulls/{number}/...` compatibility
  routes for diff, files, commits, file preview, or other repo-scoped provider
  work. Route through the provider-aware generated clients instead.
- Frontend route state must encode slashes, host ports, mixed case, and special
  characters exactly once, via shared provider route helpers.
- New provider-aware routes should not require ad hoc URL construction in
  stores/components.

## Testing

Provider work should be covered at the boundary where a regression would show:

- provider package tests for API normalization, pagination, auth/header shape,
  and capability errors;
- config tests for provider defaults, host normalization, duplicate detection,
  and token/env selection;
- sync tests for full provider refs, optional capability behavior, and DB
  identity scoping;
- server e2e tests with real SQLite for API payloads, route shape,
  capability-gated actions, and settings/import flows;
- frontend store/component tests for provider route helpers and provider refs;
- optional live/container tests for provider API compatibility when fakes are
  too weak to catch endpoint or auth drift.

Run Go tests with `-shuffle=on`. Use the GitLab CE container fixture for
changes that need real GitLab REST behavior. Use the optional Forgejo/Gitea
container fixtures when fake transports are too weak to prove gitealike REST
behavior.
