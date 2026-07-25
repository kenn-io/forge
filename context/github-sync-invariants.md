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
- Budgeted detail drain treats each queue item's worst-case cost as soft admission;
  provider pagination and child hydration may exceed it because the transport counts
  actual wire attempts (`internal/github/sync.go::drainDetailQueue`).

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

## Native Stack Rules

- Confirmed native stacks claim and order their PRs first; branch inference always
  runs afterward on every unclaimed PR, including when the preview is disabled,
  incomplete, or failing. (`internal/stacks/detect.go::RunDetectionWithNativeStacks`)
- Compare current PR hints with cached stack rows; scan `/stacks` newest-first
  and stop once every target is found or passed.
  (`internal/github/native_stack_sync.go::refreshGitHubNativeStackCache`)
- Native projections use the bottom PR number as the neutral stack key; GitHub's
  independent stack number remains cache-only.
  (`internal/stacks/detect.go::persistStackChain`)
- Disabling the preference synchronously restores branch-derived projections;
  cached native rows remain dormant. The syncer's preference is the transition
  authority — every server binds it to the boot config and reconciles on the
  swap's own previous value, never on a separately read config snapshot, so
  concurrent writers cannot reconcile twice or not at all. The swap happens under
  cfgMu so the preference order matches the persisted order, and the
  reconciliation that follows the unlock is committed-state work: it runs on the
  server-lifecycle context, never the request's, and rechecks the current
  preference under the projection lock so a disable that lost to a later enable
  cannot replay over it.
  (`internal/server/native_stack_settings.go::reconcileGitHubNativeStackProjection`,
  `internal/github/sync.go::SetPreferGitHubNativeStacks`)
- Preview-only GraphQL fields must be absent from disabled query shapes;
  `@include(false)` does not bypass schema validation on servers without those
  fields. Schema rejection drops the fields for that host instead of abandoning
  bulk fetch. (`internal/github/graphql.go::isNativeStackSchemaRejection`)
- Confirmation reconciles against currently observed open-PR hints, never cached
  or payload member state. Hints cannot attest to merged or closed members, so a
  stack holding one is refetched on a bounded schedule and its confirmation ages
  out rather than surviving every 304. The deadline is anchored to each stack's
  own observation time and the earliest one wins, so re-confirming an old stack
  during an unrelated refresh cannot extend its window.
  (`internal/github/native_stack_sync.go::cachedStackMatchesCurrentHints`,
  `internal/github/native_stack_sync.go::nativeStackObservationExpired`)
- A pull request may belong to at most one projected stack. Member eviction
  would silently shorten the stack written first and hide a preceding merge
  blocker, and projecting one side of an overlap does the same to the other, so
  an overlap makes the whole native projection ambiguous and branch inference
  owns the repository for that pass.
  (`internal/stacks/detect.go::RunDetectionWithNativeStacks`)
- Only a query that requested the preview fields may replace stack hints;
  a GraphQL shape that dropped them says nothing about membership and must leave
  REST-derived hints intact. (`internal/github/graphql.go::RepoBulkResult`)
- Only a refresh that resolved every target seeds the confirmation a later 304
  reuses; an incomplete refresh evicts the pull-request list ETag so the next
  sync retries. It also projects nothing for that pass, not the subset it did
  confirm: an unresolved stack is invisible to the overlap scan, so a confirmed
  stack could claim a pull request the unresolved one holds and hide its
  predecessor. A target dropped without being persisted -- fetch failure,
  malformed row, or disagreement with current hints -- makes the pass partial.
  (`internal/github/native_stack_sync.go::refreshGitHubNativeStackCache`)
- Native results carry the preference generation and project under the shared
  stack-projection lock, so a sync that began while the preview was enabled
  cannot reinstate it afterward.
  (`internal/github/sync.go::dropStaleNativeStackResults`)

## Historical Archive Rules

- The legacy closed-item backfill is retired; configured repositories seed durable archive discovery before sync cutover, with no cursor translation. (`internal/github/sync.go::SetReposWithContext`)
- Initial issue and pull-request inventory includes all states in stable created-time ascending order; issue enumeration excludes PR-shaped rows. (`internal/github/pages.go::ListIssuesPage`, `internal/github/pages.go::ListMergeRequestsPage`)
- Updated issue scans query one second before the durable watermark while keeping cursor identity bound to the original boundary. Updated pull-request scans run newest-first across the same overlap. (`internal/github/pages.go::ListIssuesPage`, `internal/github/pages.go::ListMergeRequestsPage`)
- Repository probes classify only authentication/access/not-found responses; transient probe failures remain retryable and non-destructive. Issue and pull-request lookups compare the response repository with the requested source identity so transfers become moved outcomes instead of source-owned snapshots. (`internal/github/pages.go::archiveRepositoryProbeError`, `internal/github/pages.go::githubArchiveDestination`)
- Archive REST and GraphQL failures must preserve typed authentication and reset-aware rate-limit errors so scheduling defers rather than hot-looping generic retries. (`internal/github/pages.go::archiveTransportError`)
- GitHub archive code owns historical identity inventory only; hydration must invoke ordinary item sync instead of adding archive-specific lookup, normalization, or persistence. (`internal/github/pages.go::ListIssuesPage`, `internal/github/sync.go::SyncArchiveItem`)
- Archive requests use shared sync budgets above `archiveLiveFloor`: provider reset signals release quadratic surplus, while headerless Gitealike hosts use configured local hourly surplus. The transport attributes every attempt, and live work preempts the archive lease. (`internal/github/budget.go::LocalArchiveSpendAvailable`, `internal/github/sync.go::Admit`)
- A GitHub issue without `updated_at` uses `created_at` as both its freshness and initial activity boundary; zero timestamps must not bypass monotonic snapshot acceptance. (`internal/platform/github/normalize.go::NormalizeIssue`)

## Owner Routes And Identity Accounting

GitHub authorization is selected by `(host, repository owner)`, with exact
repository overrides ahead of owner mappings and the host fallback. Rate state,
sync budgets, cadence, and snapshots are selected separately by `(host,
authenticated identity)`. Different PATs resolving to the same GitHub user ID
must share one runtime; App reads use their installation identity.

- Startup PAT identity discovery must use a bounded per-request context
  (`internal/github/identity.go::HTTPIdentityResolver.ResolvePAT`).
- When required scoped routes cover configured repositories, the implicit
  ownerless fallback is probed best-effort: a resolvable token keeps ownerless
  APIs routed, while a missing or invalid one is skipped with a warning instead
  of failing startup. Only an explicitly configured host fallback fails hard; a
  `github_token_env` equal to the built-in default does not count as explicit
  because Load, Save, and the sample config all materialize that name
  (`internal/config/config.go::Config.HasExplicitGitHubTokenEnv`,
  `cmd/middleman/provider_startup.go::buildGitHubIdentityRuntimes`).
- A configured router with no exact, owner, or fallback route is a routing
  failure; operation availability must fail closed instead of treating it as an
  unrouted legacy host (`internal/github/auth_router.go::MissingRouteError`,
  `internal/server/operation_availability.go::writeCredentialGateForRepo`).
- Background requests on the write credential (viewer-permission overlay,
  notifications, queued read propagation) charge the write identity's sync
  budget — the transport's context gate keeps foreground mutations uncharged —
  and live work registers provider work for every principal it will touch,
  read and write, so a shared-PAT archive is preempted
  (`internal/github/client.go::NewClient`, `internal/github/sync.go::syncRepo`,
  `internal/github/notifications_sync.go::ProcessQueuedNotificationReads`).
- Reload probes share one fresh installation-token cache per validation batch:
  per-route caches would multiply minting, while reusing the live cache lets a
  revoked installation or replaced private key pass validation until the cached
  token expires (`internal/tokenauth/source.go::SourceSet.NewProbeBatch`).

Repository `token_file` and `token_env` overrides are exact-only; reject them on
name globs rather than creating a literal route that discovered repositories
cannot select (`internal/config/config.go::Config.validate`).

Repository preview must select the entered owner's route even before that owner
has a tracked repository. Ownerless APIs may use only the host fallback; never
borrow an arbitrary owner PAT. Repository notifications use the user/write
identity. App-only routes may read, but notifications and mutations remain
disabled until restart establishes a stable user identity.

Notification sync watermarks are per repository identity, never host-wide: a
repository whose credential route is unavailable or exhausted reports its error
without holding back watermark advancement for healthy repositories on the same
host (`internal/github/notifications_sync.go::Syncer.syncNotificationsForRepo`).

Queued read-acknowledgement backoff is scoped the same way. A rate limit belongs
to the credential that hit it — on either refetch leg or the mark-read — so
defer only that identity's repositories and keep propagating the batch's other
identities; the pass still returns the rate limit so the host records its error
(`internal/github/notifications_sync.go::Syncer.ProcessQueuedNotificationReads`,
`internal/db/queries_notifications.go::DB.DeferQueuedNotificationAcksForRepos`).

Selected-repository App routes may expose installation-repository listing as an
owner-scoped discovery route, but that route must never become a fallback for
other repository operations. Owner discovery unions the PAT route's listing
with the selected-App listing and dedupes by repository ID — a PAT lists
everything it can access but misses selection-only grants, while the App client
lists only its selection — and fails closed if either configured source fails
rather than silently narrowing coverage
(`internal/github/auth_router.go::RoutedClient.listRepositoriesByOwnerAcrossRoutes`).

`RoutedClient` embeds the `Client` interface, so any optional capability
interface it does not re-declare disappears from behind the wrapper and
`gitHubClientProvider.Capabilities()` silently reports that capability as
unsupported on every routed host. When adding an optional GitHub client
interface, give `RoutedClient` a repository-routed method and add its
`_ iface = (*RoutedClient)(nil)` assertion; carry owner and repository name in
the interface so exact `repo:` routes pick their own credential
(`internal/github/auth_router.go::RoutedClient.GetMarkdownImage`). List each
optional interface in the routing guard so an unrouted owner-bearing method
fails a test instead of silently disabling the feature
(`internal/github/public_api_guard_test.go::TestRoutedClientExplicitlyImplementsOwnerBearingClientMethods`).

A wire call issued during repository sync routes by repository even when the
endpoint itself is host-scoped (`/users/{login}` for author display names).
Owner-only and App-only configurations have no host fallback route, so a
fallback-only lookup fails for every repository and, where a fallback does
exist, spends the wrong identity's budget. Such a call must also pass
`tokenauth.WithGitHubOwner`: the transport derives the owner from the request
path, so an ownerless path silently skips the App candidate and pays with the
PAT for a read the route's tracker bills to the installation
(`internal/github/auth_router.go::RoutedClient.GetUserForRepo`).

Managed Git uses exact-repository or owner PAT routes with mutation context and
must never expose an App installation token to smart HTTP. Thread full provider,
host, owner, and repository identity through clone/fetch and local reads, passing
the normalized platform (`repoPlatform(repo)`) so an unqualified GitHub ref still
picks its credential route instead of none;
partition non-GitHub clone storage by provider on shared hosts. Before injecting
a PAT into workspace fetch or push, require the branch upstream to be `origin`,
reject repository-local URL rewrites, and validate every origin fetch/push URL.

A nil `tokenauth.Source` is not fail-closed: `gitclone` reads it as permission to
run git with no credential, which succeeds against any public repository and
spends no identity's budget. A route resolver that cannot serve a repository must
return a source whose `Token` reports the missing route
(`cmd/middleman/provider_startup.go::missingRouteTokenSource`).

Token-file rotation within the same GitHub user is hot-reloadable. Changing the
authenticated user, adding a write identity to an App-only route, or adding or
removing a bounded route requires restart. Added, removed, or descriptor-changed scoped routes require restart. The live
bounded router keeps its boot descriptor until restart so it cannot lose auth or
move to a different identity while retaining the old trackers and budget.

## GitHub App Manifest Flow

`middleman-github-app create` uses GitHub's App Manifest flow so sync can read
with installation tokens. Even though middleman disables webhooks and polls,
the manifest must still include a syntactically valid `hook_attributes.url`;
GitHub's live manifest validator can report the missing hook URL as a generic
`"url" wasn't supplied` error. Do not remove that hook URL from
`internal/githubapp/manifest.go::NewManifest`; keep
`cmd/middleman-github-app/e2e_test.go::TestCreateFlowEndToEnd` asserting the
serialized manifest shape so the fake cannot accept a payload GitHub rejects.

A covering App installation leads every read chain, including on a repository
that configures its own `token_env`/`token_file`: installation tokens carry
their own rate-limit budget, so reads always prefer them and a repository
override must never displace one. That override is still the first PAT, so it
signs that repository's writes — mutation resolution skips App candidates
(`internal/config/config.go::Config.ResolveGitHubRepoTokenSource`,
`internal/tokenauth/source.go::WithMutationAuth`). Dropping the App candidate
also costs the owner its only selection-only discovery credential.

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
- Private `user-attachments` reads are the exception to app-token-first reads:
  GitHub returns 404 to installation tokens, so the repo-scoped image proxy must
  use the user's PAT/`gh` chain (`internal/github/client.go::GetMarkdownImage`).
Config may carry multiple `[[github_apps]]` rows for one host, but those rows
represent distinct app credentials. Management commands must target one row by
app owner/installation account or app id, and duplicate installation accounts on
the same host are invalid. Selected-repository coverage applies only to repos
owned by that row's `installation_account`, and the install CLI must not warn
that an installation on one account "cannot reach" repos owned by another
account. The recorded selected-repository list is a startup routing snapshot:
expanded access remains on the PAT route and narrowed access may return 404
until `middleman-github-app install` refreshes the snapshot and middleman is
restarted. Do not retry PAT credentials after an App-backed repository 404;
GitHub uses the same response for absent, private, and inaccessible repositories,
so automatic fallback would hide stale or revoked App access.

Re-running `install` after a coverage failure (or against a restored
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
