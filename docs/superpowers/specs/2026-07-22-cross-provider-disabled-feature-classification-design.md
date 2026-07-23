# Cross-Provider Disabled Repository Feature Classification

## Problem

PR #719 introduces a provider-neutral `repository_feature_disabled` error and
uses it to defer background issue or merge-request work for 24 hours. GitHub
currently produces that error from definitive HTTP 410 responses. GitLab,
Gitea, and Forgejo expose authoritative repository feature flags, but their
issue and merge-request read paths currently discard those flags and map
disabled endpoints as ordinary permission or not-found failures.

That leaves non-GitHub repositories retrying a permanently disabled scope as a
transient failure. The shared cooldown machinery is already provider-neutral;
the missing piece is definitive classification inside each provider adapter.

## Goals

- Classify disabled issue and merge-request reads for GitLab, Gitea, and
  Forgejo as `repository_feature_disabled`.
- Cover live index, item lookup, event/timeline, and archive inventory reads.
- Preserve the existing 24-hour cooldown, explicit-sync bypass, reservation,
  and provider-attempt lifecycle without adding provider-specific syncer paths.
- Avoid extra provider requests on successful issue and merge-request reads.
- Preserve the original error whenever repository metadata does not
  definitively confirm that the feature is disabled.

## Non-goals

- Persist repository feature state or cooldown state in the database.
- Add a compatibility alias, legacy fallback, or dual classification path.
- Change mutation availability or mutation error mapping.
- Infer disabled state from HTTP status or message text alone.
- Change GitHub's existing definitive HTTP 410 classification.
- Treat provider-wide unsupported capabilities as repository-disabled
  features.

## Decision

Use error-triggered, provider-local metadata confirmation.

An issue or merge-request read executes normally. If it fails with a provider
status that can also represent a disabled repository feature, the adapter reads
repository metadata once. A definitive disabled flag converts the original
operation error into `platform.RepositoryFeatureDisabled`. Otherwise the
adapter returns the original mapped error unchanged.

This keeps successful operations at their current request count, confines
provider-specific knowledge to provider adapters, and feeds the existing
provider-neutral cooldown flow.

## Repository Feature State

Add a tri-state feature model to `platform.Repository`:

```go
type RepositoryFeatures struct {
    IssuesEnabled        *bool
    MergeRequestsEnabled *bool
}
```

A nil pointer means the provider response does not authoritatively expose the
state. Non-nil false means the feature is definitively disabled. Non-nil true
means it is enabled.

GitLab populates the state from `issues_access_level` and
`merge_requests_access_level`: `disabled` is false, enabled/private/public are
true, and a missing or unrecognized level is unknown. The shared Gitea-like
repository DTO gains the same tri-state fields; Gitea and Forgejo populate them
from `has_issues` and `has_pull_requests`, and shared normalization copies them
into `platform.Repository`.

The supported repository endpoints define these fields as part of their normal
response shape, so the three adapters return known values. Other providers or
future partial repository responses may leave either value nil.

`platform.Repository` exposes one small feature lookup helper so adapters do
not duplicate capability-name switches.

## Candidate Errors

Metadata confirmation runs only for HTTP 403, 404, or 410 equivalents from an
issue or merge-request read.

- Context cancellation and deadline errors return immediately.
- Authentication, rate-limit, transport, and 5xx failures retain their current
  behavior and do not cause another provider request.
- Status alone is never sufficient. A 403 or 404 becomes
  `repository_feature_disabled` only when the repository metadata request
  succeeds and reports the matching feature as disabled.
- If the metadata request fails, reports the feature enabled, or cannot report
  the state, the adapter preserves the original operation error and mapping.

The typed disabled error wraps the original feature-operation error, not the
metadata response, so logs retain the boundary that triggered classification.

## Provider Adapter Flow

### GitLab

GitLab adds a client helper that accepts the repository reference, feature,
and original operation error. It recognizes GitLab's typed 403/404/410 shapes,
reads the project through the existing repository endpoint, and returns either
the typed disabled error or the existing `mapGitLabError` result.

The helper is applied before generic mapping at these read boundaries:

- open merge-request list and merge-request lookup;
- merge-request discussions and commits;
- open issue list and issue lookup;
- issue discussions;
- issue and merge-request archive inventory/maintenance pages.

Shared discussion-page helpers receive enough repository context to classify
their own errors before callers aggregate pages.

### Gitea and Forgejo

The shared Gitea-like `Provider` adds the equivalent helper. It inspects the
shared `HTTPError`, calls `Transport.GetRepository` directly to avoid recursive
provider mapping, and checks the normalized repository feature state.

The helper is applied before `mapError` or not-found suppression at these read
boundaries:

- open pull-request list and pull-request lookup;
- pull-request comments, reviews, and commits;
- open issue list and issue lookup;
- issue comments and optional timeline reads;
- issue and pull-request archive inventory/maintenance pages.

Timeline handling must confirm disabled state before treating a plain 404 as an
unavailable optional timeline endpoint.

The shared implementation covers both providers. Concrete Gitea and Forgejo
transports only need to preserve the HTTP status and populate repository flags.

## Cooldown and Recovery Flow

No syncer or archive scheduler policy changes are required.

1. Existing admission reserves a due repository-feature probe.
2. The feature request reaches the provider and fails.
3. The adapter confirms the disabled flag and returns the typed error.
4. Existing sync and archive completion logic records the next probe time and
   stops the affected scope.
5. Other repository scopes continue normally.
6. An explicit sync or an expired background cooldown reaches the provider
   again. If the feature has been re-enabled, the feature request succeeds and
   existing completion clears the cooldown.

The metadata confirmation is part of an already-attempted provider operation.
If archive budget or admission prevents the confirmation request, the adapter
cannot prove disabled state and returns the original error; it must not create
a cooldown from stale or incomplete evidence.

## Testing

Tests cover owned classification and adapter seams, not SDK behavior.

### Platform and shared Gitea-like tests

- Verify tri-state feature lookup for issues, merge requests, and unknown
  state.
- Use a fake transport to table-test candidate versus non-candidate errors,
  enabled/disabled/unknown metadata, and metadata lookup failure.
- Exercise every shared issue/MR read boundary so classification happens before
  generic mapping and optional-timeline 404 suppression.
- Verify the typed error retains provider kind, host, feature, and original
  cause.

### GitLab tests

- Use the existing HTTP test server pattern to return candidate issue and
  merge-request failures plus project metadata with the matching feature
  disabled.
- Cover representative live list/detail/discussion paths and both archive page
  paths.
- Verify enabled or unavailable metadata preserves the existing permission or
  not-found error.

### Gitea and Forgejo tests

- Verify repository conversion carries `has_issues` and
  `has_pull_requests` into the shared DTO.
- Add one client-level disabled-issues integration test for each concrete
  adapter, proving the HTTP error and repository response reach the shared
  classifier.
- Rely on the shared boundary table for exhaustive issue/MR method coverage;
  do not duplicate the same behavior for both SDK wrappers.

### Regression verification

- Run focused platform, GitLab, Gitea, Forgejo, Gitea-like, archive, and sync
  tests with shuffle enabled.
- Run race-focused cooldown tests because the new typed errors feed the shared
  reservation lifecycle.
- Run `make nilaway`, `scripts/context-sync --check`, and
  `make test-short-precommit` before pushing.

## Documentation

Update the platform sync invariant and retry/backoff context to state that
GitHub classifies definitive endpoint responses directly, while GitLab,
Gitea, and Forgejo confirm candidate endpoint failures against authoritative
repository feature metadata.
