# Provider Architecture

Use this document when adding a provider or changing the provider split. For
identity and sync invariants, also read
[`context/platform-sync-invariants.md`](./platform-sync-invariants.md).

## Package Split

Provider support is split into three layers:

1. `internal/platform` owns provider-neutral domain types, capability
   interfaces, typed platform errors, registry lookup, and DB conversion helpers.
2. `internal/platform/<provider>` owns provider API transport and normalization
   into `platform` types.
3. Existing orchestration packages (`internal/github` sync compatibility,
   server handlers, config startup, clone setup, and UI stores) consume the
   neutral interfaces or persisted DB rows.

Dependency direction must stay acyclic:

```text
internal/platform        -> neutral types, registry, persistence helpers
internal/platform/github -> GitHub SDK/client data to neutral platform types
internal/platform/gitlab -> GitLab API data to neutral platform types
cmd/internal/server/etc. -> registry plus provider-neutral DB rows
```

Do not make a provider package import server code, config startup, or another
provider package. Do not make `internal/platform` import provider-specific SDKs.

## Adding A Provider

Minimum provider checklist:

- Add provider metadata in `internal/platform/metadata.go`: kind, label, default
  host if one exists, nested-owner behavior, and case-folding behavior.
- Implement `internal/platform/<provider>` with a `Provider` plus only the
  optional interfaces it truly supports (`RepositoryReader`,
  `MergeRequestReader`, `IssueReader`, mutators, etc.). Unsupported features
  should be absent from the type and false in `Capabilities()`.
- Normalize API records into `platform.Repository`, `platform.MergeRequest`,
  `platform.Issue`, `platform.*Event`, labels, releases/tags, and CI checks.
  Preserve provider IDs, external IDs, web URLs, clone URLs, default branches,
  and canonical repo paths when available.
- Wire startup through provider factories and `(platform, host)` registry
  registration. Token failures should fail only the provider host that needs
  that token.
- Add config parsing/validation tests for provider defaults, host normalization,
  nested owner paths, duplicate detection, and token selection.
- Add DB/query tests for provider-aware repo identity and provider ID
  reconciliation before relying on sync.
- Add provider unit tests around pagination, auth/header shape, normalization,
  capability errors, and rate-limit mapping.
- Add server e2e tests with real SQLite for any new API payload or route shape
  visible to users.

## Capability Model

The base `platform.Provider` exposes only metadata and `Capabilities()`.
Feature work is opt-in through optional interfaces. Registry helpers return
typed platform errors for missing providers or missing capabilities.

Rules:

- Capability flags and implemented interfaces must agree.
- Handlers must check capabilities before performing mutations. A missing
  capability is a feature-level failure, not a whole-provider failure.
- Read sync should continue for supported resources if optional resources such
  as releases, tags, CI, or comments fail or are unsupported.
- Do not fake GitHub behavior for another provider. Add provider-specific
  normalization or explicit unsupported-capability handling instead.

## Label Capabilities

Repository label editing is provider-neutral:

- `LabelReader` lists the repo label catalog; `LabelMutator` replaces the full
  label set on a merge request or issue and returns provider-normalized labels.
- `read_labels` and `label_mutation` must be true only when the provider
  implements the matching interfaces. Do not expose editable UI or mutation
  routes from fallback/default capabilities.
- GitHub PR labels use issue-label APIs, but that mapping belongs behind the
  provider implementation, not in server handlers or frontend code.

## Route Model

Repo-scoped REST routes are provider-aware. The default-host route shape omits
host only when the provider default host applies; non-default/self-hosted
instances use the `/host/{platform_host}/...` prefix.

Examples:

```text
GET /api/v1/pulls/github/wesm/middleman/244
GET /api/v1/pulls/gitlab/group%2Fsubgroup/project/12
GET /api/v1/host/gitlab.example.com/pulls/gitlab/group%2Fsubgroup/project/12
GET /api/v1/pulls/github/wesm/middleman/244/diff
GET /api/v1/pulls/github/wesm/middleman/244/file-preview?path=README.md
```

Do not add new `/repos/{owner}/{name}/pulls/{number}/...` compatibility routes
for diff, files, commits, file preview, or future repo-scoped provider work.
The generated clients and `packages/ui/src/api/provider-routes.ts` should be the
single frontend path builder for these routes.

When adding or renaming provider-aware Huma routes, treat `OperationID` as a
generated-client contract. Keep default-host and host-prefixed variants paired,
use the same `Summary` and tag for both, and reserve the `-on-host` suffix for
the host-prefixed operation ID. Run `make api-generate` and update generated
client call sites in the same change.

## Frontend Threading

Frontend state should keep a reusable provider ref:

- `provider`
- `platformHost`
- `owner`
- `name`
- `repoPath`

Use `providerRouteParams()` and `providerItemPath()` or `providerRepoPath()` for
repo-scoped requests. Do not hand-build `/api/v1` URLs or assume GitHub defaults
inside components/stores. Host defaults may be omitted from URLs only by the
shared route helper.

## Test Boundaries

Choose the smallest boundary that catches the regression:

- provider package tests for SDK/API normalization and capability errors;
- config tests for provider selection and token/env behavior;
- DB tests for identity, provider IDs, and rename/reconciliation behavior;
- server e2e tests for API shape, capability gating, and real SQLite flows;
- frontend store/component tests for provider ref routing and response fields;
- optional container/live tests when fakes cannot validate provider API drift.

Run Go tests with `-shuffle=on`. Regenerate OpenAPI and generated clients with
`make api-generate` after Huma route, route metadata, or API type changes.
