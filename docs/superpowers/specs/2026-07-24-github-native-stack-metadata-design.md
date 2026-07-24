# GitHub Native Stack Metadata Design

**Date:** 2026-07-24
**Status:** Approved for implementation

## Goal

Let maintainers opt into GitHub's private-preview stacked pull request data so
GitHub's authoritative grouping and ordering improve middleman's existing stack
support. Native stack data takes precedence wherever it is complete and usable;
middleman's current branch-based detection remains the fallback and continues to
work when the preview is disabled, unavailable, incomplete, or temporarily
failing.

## Scope

- Add read-only support for GitHub's native stack metadata.
- Reuse the existing middleman stack tables, API responses, detail panel, merge
  safeguards, and other stack-aware workflows.
- Do not add stack creation, extension, unstacking, or other GitHub mutations.
- Do not add a second stack UI, API surface, or provider-neutral capability.
- Keep every non-GitHub provider on the existing branch-based detector.
- Preserve repository identity through the existing repository row, which
  includes provider, platform host, owner, and repository name.

## Setting

Add `prefer_github_native_stacks` to the server-backed pull request settings,
defaulting to `false`. The Pull Requests settings panel exposes it as an
opt-in **Prefer GitHub native stacks** toggle and identifies the integration as
a GitHub preview.

The setting is global but affects only repositories using the GitHub provider.
It must round-trip through `Config`, `configFile`, `Save`, the settings API, the
generated client schema, the shared settings store, and the settings UI. Turning
the setting off immediately restores the current detector for every repository;
the native cache remains dormant so it can be validated and reused if the
setting is enabled again.

## GitHub Data Sources

GitHub exposes a small `stack` object on REST pull request resources and
read-only `stack` and `stackEntry` fields on GraphQL `PullRequest`. These fields
identify the stack number, size, position, and ultimate base branch. Middleman
captures those hints from the REST and GraphQL pull request paths it already
uses so the normal GitHub sync optimization remains intact.

The current `go-github` release and its main branch do not model the preview
fields or Stacks API. Middleman will use the library's existing authenticated
raw-request support and local preview-specific response types rather than add a
second HTTP client or wait for an upstream release.

The pull request hints determine whether any native catalog work is useful.
When the cache needs a stack refreshed, middleman reads the paginated endpoint:

```text
GET /repos/{owner}/{repo}/stacks?per_page=100&page={page}
```

The returned resource supplies the complete member list ordered from bottom to
top. Middleman performs no Stacks API request when the current pull request hints
are already consistent with confirmed cached results.

## Native Stack Cache

Add one forward migration containing two GitHub-specific cache tables:

- `github_native_stacks`, keyed by repository and repository-scoped GitHub stack
  number, stores the global GitHub stack ID, size, ultimate base ref, open state,
  GitHub creation time, a canonical content fingerprint, and the UTC time it was
  last observed.
- `github_native_stack_members`, keyed by cached stack and position, stores the
  pull request number, state, draft and merged state, head ref, and head SHA.

The cache stays separate from `middleman_stacks`. The native tables preserve the
provider payload needed to plan future reads; the existing tables remain the
provider-neutral, currently displayable projection used by middleman's API and
UI. Native members are identified by pull request number inside their
provider-and-host-aware repository, not by owner/repository/number alone.

A cached native stack is eligible for projection only when its complete member
list has been confirmed and every member needed by the existing stack response
can be resolved to a middleman merge-request row. An incomplete or unresolved
native stack does not produce a truncated stack; it falls back to branch-based
detection for that sync.

## Targeted Pagination

Each repository sync compares current pull request hints with the durable native
cache. It builds a target set containing:

- stack numbers referenced by pull requests but absent from the cache;
- cached stacks whose reported size or member positions no longer match the
  current hints;
- cached stacks whose members now report a different stack or no stack.

If the target set is empty, confirmed cached native stacks are reused without a
Stacks API call. Otherwise middleman scans the catalog newest-first, upserts
matching resources atomically with their members, and removes a target from the
set when it is refreshed.

Stack numbers are repository-scoped and the list endpoint is ordered by stack
number newest-first. Once a page's lowest number is below an unresolved target,
that target cannot appear on a later page and is confirmed absent. Pagination
stops as soon as every target has either been refreshed or confirmed absent, or
when GitHub reports no next page. Confirmed-absent targets are removed from the
native cache.

This stopping rule avoids rescanning historical stacks while still finding:

- a newly created stack, because current PR hints reference an uncached number;
- a member added to or removed from an old stack, because size or position hints
  stop matching the cache;
- a dissolved stack, because formerly cached members no longer report the
  cached number and the ordered scan proves that resource absent.

## Native-First Reconciliation

Stack reconciliation runs in this order after a successful repository sync:

1. Load confirmed, usable native stacks for the repository.
2. Project them into the existing `middleman_stacks` and
   `middleman_stack_members` tables, preserving GitHub's grouping and
   bottom-to-top ordering. The projection's `base_number` is the bottom pull
   request number; GitHub's independent stack number remains in the native cache.
3. Mark every merge request assigned by a native stack as claimed.
4. Run the existing branch-based detector only across unclaimed merge requests.
5. Reconcile stale derived stacks while preserving every active native and
   inferred stack produced by the current run.

Branch inference may fill repositories and pull requests for which GitHub
provides no usable native membership, but it may never reassign a claimed pull
request, merge two native stacks, reorder native members, or delete an active
native projection. When no native stack is usable, the detector receives the
same repository inventory it receives today.

Native stack names continue to use middleman's existing derived-name behavior;
GitHub's preview resource has a number but no separate human-authored name.

## Failure Behavior

Native stack support is supplemental and must not turn GitHub sync into a hard
dependency on the preview API.

- `404 Not Found` means the repository does not have usable preview access for
  that sync. Ignore its cached native projections and run the existing detector
  over the full repository inventory.
- Authentication, rate-limit, transport, decoding, or other request failures are
  non-fatal to stack detection. Keep previously confirmed cache rows unchanged,
  do not apply suspect targets, and infer those pull requests.
- If preview-only GraphQL fields are rejected, continue through the existing
  query shape without them. Preview metadata must never turn an otherwise valid
  pull request sync into a repository sync failure.
- A partially completed paginated read may update only targets whose complete
  resources were confirmed. It must not delete or project unresolved targets.
- Malformed resources, duplicate positions, mismatched sizes, missing member
  numbers, or unresolved merge-request rows make that native stack unusable for
  the current projection. Other confirmed stacks remain eligible.
- Disabling the setting performs no preview reads and ignores the cache without
  deleting it.

Failures should be logged with provider, platform host, repository identity,
operation, and stack number where known. They do not change the existing public
stack API or error envelopes.

## Testing

Add focused coverage at the owning boundaries:

- Config save/load and settings API round-trip tests cover the non-default flag.
- A settings component test proves the opt-in toggle saves, updates shared state,
  and rolls back on failure. This interaction does not require a new Playwright
  workflow.
- GitHub client tests decode REST pull request hints, GraphQL stack fields, and
  paginated native stack resources through the existing authenticated client.
- Database tests cover migration from the prior schema, atomic cache/member
  replacement, structured reads, confirmed-absent deletion, and repository
  isolation.
- Reconciliation tests prove native grouping and ordering win over contradictory
  branch relationships, claimed PRs cannot be reassigned, and inference still
  detects unclaimed stacks.
- Sync tests prove a consistent cache causes no catalog request; new, changed,
  and dissolved stacks create the correct targets; pagination stops after every
  target is found or passed; and unrelated historical pages are not fetched.
- Failure tests cover preview `404`, transient request failure, partial
  pagination, malformed resources, and unresolved members without disrupting
  inferred stacks.
- A real SQLite server test proves the existing stack endpoint and pull-request
  detail response expose a native-first result without introducing a new wire
  shape.

Existing provider-neutral stack tests remain the regression suite for the
fallback behavior. Changes to the GitHub GraphQL query also require the gated
live GitHub validation described in `context/github-sync-invariants.md` when
preview access is available.
