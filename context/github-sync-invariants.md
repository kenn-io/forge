# GitHub Sync Invariants

Use this document for changes in `internal/github/`, GitHub adapter code,
sync-triggering server handlers, fixture clients, and tests that rely on
GitHub-derived freshness. For provider-neutral identity rules, package layout,
and provider capability rules, start with
[`context/platform-sync-invariants.md`](./platform-sync-invariants.md) and
[`context/provider-architecture.md`](./provider-architecture.md).

## Purpose

- Keep sync correctness rules explicit.
- Preserve the distinction between identity, freshness, and optional fallback
  data.
- Prevent review-only regressions around `platform_host`, head-SHA drift,
  timeline parity, and fallback fetch paths.

## Identity Rules

GitHub entities in middleman are not identified by owner/name/number alone.
The provider-neutral identity is `(platform, platform_host, owner, name)`;
this document focuses on GitHub-specific default-host behavior and GitHub-only
sync optimizations.

- Repository identity is `(github, platform_host, owner, name)`.
- PR and issue identity is `(github, platform_host, owner, name, number)`.
- Workspace association repair and list filtering must preserve that
  provider/host-aware identity.
- GitHub owner/name are case-folded lookup keys; do not apply that rule to
  providers whose metadata preserves nested or mixed-case paths.

Rules:

- Treat `platform_host` as part of every persisted GitHub object identity.
- When a caller explicitly supplies `platform_host`, honor it all the way
  through query, sync, and response shaping.
- Only fall back to the default host when the request truly omits host and the
  route semantics allow an implied GitHub host.
- New repo-scoped API work should use provider-aware routes and generated
  clients, not new `/repos/{owner}/{name}/pulls/{number}/...` GitHub-only
  compatibility paths.
- Do not constrain repo-scoped listing queries to one host unless the caller
  asked for that host.

## Freshness Rules

Bulk sync and detail sync have different jobs, but they must not disagree about
what "current" means.

- Bulk sync keeps tracked repos, open PRs/issues, and cheap derived state fresh.
- Detail sync populates comments, reviews, commits, and richer timeline data for
  one item.
- If a PR or issue is marked as detail-fetched, the persisted fields that power
  the user-visible detail view must match that claim.

For pull requests, that means:

- Detail freshness must cover comments, reviews, commits, and stored PR system
  timeline events together.
- `last_activity_at` and similar derived fields must follow the freshest
  persisted activity, not just one subset of the detail payload.
- Background sync cooldowns are allowed, but user-initiated refreshes must still
  be able to promote a stronger sync intent over an in-flight background fetch.
- Recently active open PRs in the fast-sync lane are cadence-gated by activity
  age, not just by membership in `active_pr_window`
  (`internal/github/sync.go::activeMRRefreshInterval`). Hot PRs use
  `active_pr_refresh_interval`; older PRs still inside the window fall back to
  a slower cadence so the Activity view stays fresh without spending the same
  request rate on hours-old rows. A missing `detail_fetched_at` remains due
  immediately (`internal/github/sync.go::activeMRDueForFastSync`).
- GitHub detail ETags reduce both payload work and middleman's eager-refresh
  budget spend for unchanged PRs; the sync budget transport does not count
  `304 Not Modified` responses (`internal/github/budget_transport.go::budgetTransport`).
  Active watched-PR sync must use the same persisted pull-request ETag path as
  detail drain (`internal/github/sync.go::syncMRForRepo`,
  `internal/github/sync.go::getPullRequestForDetail`,
  `internal/github/sync.go::markUnchangedMRDetailFetched`). Manual/API PR
  refreshes must bypass that PR ETag gate so rerun checks, workflow approval,
  comments, reviews, and commits can refresh even when GitHub's PR resource is
  unchanged (`internal/github/sync.go::SyncMR`,
  `internal/server/huma_routes.go::syncPR`). Cadence control is still required
  because changed PRs correctly fall through to comments, reviews, commits, CI,
  and workflow approval refreshes.

## Timeline Event Rules

PR timeline storage is intentionally selective.

- Keep the existing event families stable: comments, reviews, commits, force
  pushes, and the currently supported PR system events.
- Review comments are UI-aware but are not part of the stored sync model unless
  they can be fetched within the supported timeline path.
- If bulk sync persists PR system events, detail sync must persist the same
  family so filters and `detail_fetched_at` do not lie.
- Optional timeline fetch failures may degrade that event family, but should not
  drop the entire PR detail refresh when the rest of the detail payload is still
  usable.
- When an optional timeline fetch fails after earlier timeline data was stored,
  derived `last_activity_at` must still preserve the latest stored non-comment
  event time. A transient failure must not make an already-observed force push,
  review, commit, or other PR system event stop driving dashboard freshness.

## SHA-Sensitive Rules

Some PR-derived state is only valid for one head commit.

- Never carry CI status, check runs, or similar head-derived summaries forward
  when the PR head SHA changed underneath the refresh.
- Workflow-approval decisions must be tied to the correct PR identity, not just
  the head SHA. Shared SHAs across forks or sibling PRs must not leak approval
  state between items.
- When a refresh cannot prove the state belongs to the current head SHA, clear
  the stale derived state instead of preserving it.
- `MRReviewThread.Range.DiffHeadSHA` preserves the comment commit even for
  current threads and is distinct from `MergeRequest.DiffHeadSHA` (the synced
  diff snapshot); suggestion apply uses the stored thread head to reject stale
  suggestions (`internal/github/sync.go::githubReviewLineRange`).
- Suggestion apply commits to the PR head repo/branch bound by
  `createCommitOnBranch.expectedHeadOid`, never the base repo. Reject
  whitespace-padded paths (do not trim), preserve terminal blank replacement
  lines, and do not re-add a trailing newline when a suggestion deletes every
  line. Mutation-time `NOT_FOUND`/could-not-resolve failures are head repo or
  branch races and map to conflict `head_repo_unknown`, not `not_found`
  (`internal/github/client.go::ApplyReviewSuggestions`).
- Apply-suggestion consumes REST content reads plus the GraphQL
  `createCommitOnBranch` mutation; the provider reports both buckets via
  `OperationRateLimitBuckets(platform.OperationApplyReviewSuggestion)` so a
  paused GraphQL budget disables the operation without making GraphQL
  provider-neutral.

## Fallback Data Rules

GitHub data sources are intentionally layered and may remain GitHub-specific
behind the provider split.

- Repos without usable releases may fall back to tags for version-like timeline
  context.
- Repository import for the authenticated owner may need a different GitHub API
  path than generic org/user repo listing so private owned repos are included.
- Fallbacks must preserve the same response shape and user-visible semantics as
  the primary path whenever possible.

Use fallback paths to keep user-visible GitHub features working, not to silently
change what a field means. Provider-neutral persistence should receive the same
semantic shape regardless of whether data came from GraphQL, REST, tags, or
fallback repository listing.

## GitHub App Manifest Flow

`middleman-github-app create` uses GitHub's App Manifest flow so sync can read
with installation tokens. Even though middleman disables webhooks and polls,
the manifest must still include a syntactically valid `hook_attributes.url`;
GitHub's live manifest validator can report the missing hook URL as a generic
`"url" wasn't supplied` error. Do not remove that hook URL from
`internal/githubapp/manifest.go::NewManifest`; keep
`cmd/middleman-github-app/e2e_test.go::TestCreateFlowEndToEnd` asserting the
serialized manifest shape so the fake cannot accept a payload GitHub rejects.

GitHub App installation tokens are account-scoped, not host-scoped. An app
installation for one owner must not authenticate reads for another owner just
because both repos share the same host. Repo-scoped GitHub reads must resolve app
tokens with the repository owner in context, and ownerless contexts such as
clone auth must fall through to PAT/`gh` credentials. This owner scoping governs
endpoint selection, not just token resolution: choose an installation-token-only
read endpoint (such as installation-repositories listing) only when the requested
owner actually resolves to an app installation. Gating it on whether the host has
any active app sends a PAT-backed owner that shares the host with another owner's
app to an endpoint its credential cannot use, which fails even though the token
chain "correctly" falls back to the PAT.
Config may carry multiple `[[github_apps]]` rows for one host, but those rows
represent distinct app credentials. Management commands must target one row by
app owner/installation account or app id, and duplicate installation accounts on
the same host are invalid. Selected-repository coverage applies only to repos
owned by that row's `installation_account`, and the install CLI must not warn
that an installation on one account "cannot reach" repos owned by another
account. Re-running `install` after a coverage failure (or against a restored
config) reconfigures the existing installation instead of minting a new
installation id, so on a clean install-poll timeout the flow adopts an
already-present installation rather than only ever waiting for a newly created
one. Adoption runs only after a clean poll deadline and is bounded by intent:
adopt only the app's sole installation when its account is the recorded
installation account or owns a configured repo that resolves to the app.
Multiple installations or a lone installation on an unrelated account leave the
deadline as a timeout instead of recording the wrong account. A transient probe
error or a user interrupt is not a clean deadline: it surfaces the original
error or cancellation unchanged and never adopts.

## Testing Expectations

Changes in this area should usually add or update tests at the boundary where
the regression would show up.

- `internal/github/*_test.go` and `internal/platform/github/*_test.go` for
  GraphQL parsing, normalization, adapter compatibility, optional failure
  handling, and sync sequencing.
- `internal/server/api_test.go` when the bug would surface through HTTP payloads
  or sync-triggering handlers.
- Fixture-client coverage when a fake GitHub path needs to model private repos,
  edited comments, or timeline families consistently.

For notification sync specifics, see [`context/notifications-in-activity.md`](./notifications-in-activity.md).

Also see [`context/testing.md`](./testing.md):

- Run the normal Go tests with `-shuffle=on`.
- If you change GraphQL query shape in `internal/github/graphql.go`, run the
  gated live GitHub validation as well.
