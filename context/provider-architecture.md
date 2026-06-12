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

### GitLab Mutation Semantics

GitLab declares the full mutation capability set, but several operations map
onto different upstream semantics than GitHub/Forgejo/Gitea. Do not assume
provider-equivalent behavior from the flags alone:

- Merge methods: `squash` sets GitLab's squash flag; `merge` accepts under
  the project's configured merge method — on fast-forward or semi-linear
  projects that accept does not create a merge commit, so treat
  `AllowMergeCommit` as "non-squash accept allowed", not as a per-request
  merge-commit guarantee. `squash_option` bounds the squash flag in both
  directions (`never` forbids squash; `always` forbids non-squash
  accepts). `rebase` cannot be requested per merge (it is a project
  setting) and returns a typed `unsupported_capability` error with
  capability `merge_method_rebase`. Surfacing GitLab's `merge_method`
  (merge/ff/rebase_merge) as a provider-accurate action label is follow-up
  work; until then the generic method labels apply.
- Head binding: merge and approve pass the locally synced head SHA — the
  commit the user actually reviewed — so a source-branch push after review
  is rejected upstream instead of acting on unreviewed code. The
  `mutation_head_binding` capability marks providers that enforce this
  (currently GitLab); others treat the expected head SHA as advisory. For
  enforcing providers a missing local head SHA always fails closed with a
  `409 conflict` and deliberately does not sync: persisting a fresh head
  would let a retry from the same stale UI mutate a commit nobody
  reviewed. The head populates through the normal review cycle (detail
  view or periodic sync). The stored head is still only a cache, so every
  response shape that embeds the MergeRequest row (list and detail alike)
  exposes `platform_head_sha`, and merge/approve accept an optional
  `expected_head_sha` that must match the stored head before the provider
  is called. When supplied, the pin is also enforced at the provider,
  with different strength for merges and approvals. Merge pins are
  provider-gated wherever the API supports them: GitHub's `sha`,
  GitLab's `sha`, and Gitea/Forgejo's `head_commit_id` all reject a
  moved head upstream. The Gitea/Forgejo SDKs report any non-2xx merge
  response as merged-false with a nil error and an unread body, so the
  transports capture merge rejections at the HTTP layer
  (`gitealike.MergeRejectionCaptureTransport`) and surface the real
  status and message — without that, a rejected merge would be
  recorded as a successful one. Both providers answer 409 for
  unrelated merge conflicts too, so only their distinctive
  head-mismatch messages (the `IsErrSHADoesNotMatch` branch of both
  providers' merge endpoints: "head out of date" on current Gitea and
  Forgejo, "head target does not match" on older Gitea) classify as
  `stale_state`; any other 409 stays a generic conflict. If a future
  provider release changes the phrase, the failure mode is the safe
  direction — a true head mismatch presents as a generic conflict
  rather than a false stale — and the container e2e fixtures probe
  the real rejection shape live. Approval pins are provider-gated only on
  GitLab, whose approvals API rejects a mismatched `sha` atomically.
  GitHub/Gitea/Forgejo review commit ids record rather than gate, so
  approvals there are pre-checked against the live provider head
  before submission and verified again after it: if the head moved
  while the review submitted, the approval is revoked upstream
  (GitHub review dismissal, Gitea/Forgejo review deletion) and the
  caller gets `stale_state` — the same reload-and-re-review flow as a
  pre-check rejection. Revocation runs as the same token user who
  approved and needs that user's write access to stick; if it fails,
  the typed error says the approval may still stand on the moved
  head, and the wire response carries `details.revocation`
  (`succeeded`/`failed`) plus `details.review_id` per
  `context/error-handling.md` so clients can tell a cleanly revoked
  approval from one the user must remove manually. The post-submit
  verification fails closed: a head that cannot be read at all (error,
  missing PR, empty SHA) is treated exactly like a moved head — the
  approval is revoked, because an approval whose head cannot be proven
  must not stand on potentially unreviewed code.
  `mutation_head_binding` additionally marks providers where the pin is
  REQUIRED — omitted pins are rejected — and where approval itself is
  provider-gated. The SPA captures the pin when a merge or approval form
  opens, so a background refresh cannot silently rebind an open form to
  a head the user has not seen. Conflict responses carry `details.reason`
  (`stale_state`, `conflict`, or `head_unknown`) per
  `context/error-handling.md`; only stale heads trigger a server-side
  MR resync — `head_unknown` recovery is client-initiated. The SPA's PR
  detail view echoes the rendered head on merge and approve, disables
  head-bound actions preflight while no head is synced, and branches on
  the conflict reasons: `stale_state` reloads the detail (a sync-enabled
  load) and prompts a re-review, `head_unknown` reloads the same way and
  keeps head-bound actions disabled until the response carries
  `platform_head_sha`, and a generic `conflict` surfaces the provider
  message in place.
- Reviews: GitLab has no `request_changes` state. `SupportedReviewActions` is
  comment/approve only, and publishing a request-changes review returns the
  typed `unsupportedCapability` envelope (`review_action_request_changes`).
- Approvals: the approvals API carries no body and returns approval state,
  not a review object. A non-empty approve body is posted as a regular MR
  note first, and the returned "review/approved" event is synthesized with an
  empty body (the note syncs in as its own comment). If approval fails after
  the note posted, the typed error says so; retrying repeats the comment
  because GitLab rejects duplicate approvals by the same user.
- Comment edits: GitLab's note responses do not include the discussion ID, so
  edited events carry an empty `ThreadID`; the event upsert preserves the
  stored `thread_id` when the incoming value is NULL.

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
