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

## SHA-Sensitive Rules

Some PR-derived state is only valid for one head commit.

- Never carry CI status, check runs, or similar head-derived summaries forward
  when the PR head SHA changed underneath the refresh.
- Workflow-approval decisions must be tied to the correct PR identity, not just
  the head SHA. Shared SHAs across forks or sibling PRs must not leak approval
  state between items.
- When a refresh cannot prove the state belongs to the current head SHA, clear
  the stale derived state instead of preserving it.

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

For notification sync specifics, see [`context/notification-inbox.md`](./notification-inbox.md).

Also see [`context/testing.md`](./testing.md):

- Run the normal Go tests with `-shuffle=on`.
- If you change GraphQL query shape in `internal/github/graphql.go`, run the
  gated live GitHub validation as well.
