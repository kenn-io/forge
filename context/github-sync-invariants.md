# GitHub Sync Invariants

Use this document for changes in `internal/github/`, GitHub adapter code,
sync-triggering server handlers, fixture clients, and tests that rely on
GitHub-derived freshness. For provider-neutral identity rules, package layout,
and provider capability rules, start with
[`context/platform-sync-invariants.md`](./platform-sync-invariants.md) and
[`context/provider-architecture.md`](./provider-architecture.md). Notification
sync lives in [`context/notifications-in-activity.md`](./notifications-in-activity.md);
test lanes and live GraphQL validation live in [`context/testing.md`](./testing.md).

## Purpose

- GitHub API clients, sync, quota tracking, and mutations exist only on a hub; spokes keep only
  lazy smart-HTTP Git credentials (`cmd/kenn-forge/provider_startup.go::buildServeControlPlanes`).
- `--disable-sync` suppresses hub refresh without removing foreground provider operations.

## Identity Rules

- Treat `platform_host` as part of every persisted GitHub object identity; workspace association
  repair and list filtering must preserve it.
- GitHub owner/name are case-folded lookup keys; do not apply that to providers whose metadata
  preserves nested or mixed-case paths.
- Honor an explicitly supplied `platform_host` through query, sync, and response shaping. Fall
  back to the default host only when the request omits it and the route allows an implied host.
- Do not constrain repo-scoped listing queries to one host unless the caller asked for it.

## Freshness Rules

Bulk sync and detail sync have different jobs, but must not disagree about what "current" means.

- Bulk sync keeps tracked repos, open PRs/issues, and cheap derived state fresh; detail sync
  fills comments, reviews, commits, and timeline data for one item.
- If a PR or issue is marked detail-fetched, the persisted fields behind the detail view must
  match that claim. For PRs, freshness covers comments, reviews, review threads, commits, and
  stored system timeline events together.
- `last_activity_at` is the provider's authoritative `updated_at`; child datasets, child failures,
  and local clocks never redefine it (`internal/db/queries_snapshot_children.go::CommitMergeRequestChildSnapshot`).
- Notification `source_updated_at` is only a staleness hint for hot/warm admission; never persist
  it as `last_activity_at` (`internal/github/watched_sync.go::hotAndWarmOpenMRs`).
- Only comment sync writes a stored PR's `comment_count`; provider list counts are absent or
  count other notes (`internal/github/index_sync.go::Syncer.indexUpsertMR`).
- Startup restores credential cadence gates from the oldest error-free completion among tracked
  repositories; failed and never-synced repositories bypass the gate on the first pass
  (`internal/github/sync.go::Syncer.liveRepoSyncTimes`).
- Airplane mode pauses scheduled sync after the current pass; relay hints, item visits, manual
  sync, and queued notification read propagation stay enabled, and startup or reload while paused
  reuse known identities instead of provider discovery (`internal/github/sync.go::Syncer.AutomaticSyncEnabled`).
- User-initiated refreshes must be able to promote a stronger sync intent over an in-flight
  background fetch.
- Scheduled and immediate watched-PR passes are serialized for the whole pass so provider work
  and host cadence state have one owner (`internal/github/watched_sync.go::syncWatchedMRs`).

Cadence and budget:

- The hot set is the last 10 unique open PR details viewed and persists across restarts. Hot and
  workspace-linked PRs use the watched cadence; terminal PRs are evicted
  (`internal/db/queries_hot_merge_requests.go::RecordHotMergeRequestViews`).
- A detail read records its hot view in the background and never waits on the single SQLite
  writer, whose fsyncs can stall for seconds (`internal/server/pullapi/hot_views.go::recordHotView`).
- Dormant open items get a daily check, not hourly hydration; closed history stays out of routine
  polling (`internal/github/queue.go::BuildQueue`).
- Daily coverage is a target, not permission to exceed quota; show remaining overdue open items
  rather than implying complete freshness (`internal/github/detail_queue.go::countOverdueDetails`).
- A detail item's worst-case cost is soft admission; pagination and child hydration may exceed it
  because the transport counts actual wire attempts (`internal/github/detail_queue.go::drainDetailQueue`).
- Unknown or missing mergeability stays eligible for detail refresh; it can resolve after a base
  push without changing the PR's `updated_at` (`internal/github/detail_queue.go::buildDetailQueueItems`).

ETags and 304s:

- The sync budget does not count `304 Not Modified`; cadence control is still required because
  child reads spend quota (`internal/github/budget_transport.go::budgetTransport`).
- Watched-PR sync must use the same persisted PR ETag path as detail drain
  (`internal/github/mr_sync.go::SyncMROnProviderIfChanged`).
- Head-commit CI reads (check runs, combined status, workflow runs) revalidate in the transport
  and answer a 304 with the cached 200, headers included, since a 304 carries no `Link`. Callers
  never see the 304 (`internal/github/revalidating_transport.go::revalidatingTransport`). Manual and
  post-mutation refreshes revalidate too: a 304 proves the cached answer is what GitHub would send.
- A parent 304 still refreshes children, because comment edits, deletions, moderation, and CI do
  not change the parent: an open PR page refreshes all children; background polls refresh
  comments, moderation, review threads, and pending CI (`internal/github/mr_detail.go::markUnchangedMRDetailFetched`).
- Comment-only polling on parent 304s still respects dormant-item cadence.
- The watched pass first probes its GitHub PRs in batched GraphQL reads, 25 PRs per request for one
  point. A PR whose probe fingerprint equals the one taken just before its last successful full
  read skips every provider read except the due merge-queue and blocked-PR merge-rules follow-ups,
  and is marked detail-fetched. Unknown mergeability never matches, and a full read is still forced
  every 30 minutes, because the probe misses edits inside review threads and moderation beyond the
  100 most recently updated comments (`internal/github/pr_freshness.go::prFreshnessBaselines`).
- Check counts by state cannot tell which check holds which result, so a matching probe still
  revalidates head CI. A full read that logged a dropped child refresh through `logSyncFailure`,
  such as a failed CI read, or that did not set the detail-fetched marker because a concurrent
  sync superseded it, leaves a never-matching marker instead of a baseline. Merge queue and merge
  rules follow-ups are outside that tracking. Probes register as active detail work, and
  repositories in a disabled-PR cooldown are not probed
  (`internal/github/sync_failures.go::withDegradedSyncTracking`).
- Merged or closed PRs, a missing parent, stale diff SHAs, or missing local diff commits force a
  full page fetch. Manual refreshes stay unconditional (`internal/github/mr_sync.go::SyncMROnProvider`).
- After a changed issue page, list comments unconditionally: a validator cached by another
  refresh can vouch for comments it never committed (`internal/github/issue_detail.go::fetchIssueDetail`).
- Issue-page polls keep their own ETag apart from full detail, so a page poll never marks full
  detail fresh and background sync still reconciles renames and timelines (`internal/github/issue_sync.go::syncIssueForRepo`).
- PR conversation comments and review threads share one freshness boundary; keep detail stale
  while either family is incomplete (`internal/github/bulk_sync.go::syncOpenMRFromBulk`).
- A partial GraphQL issue-comment page is observation-only: merge its visibility states over
  stored moderation metadata, never replace it (`internal/github/issue_sync.go::refreshIssueTimeline`).

Snapshots and merge state:

- Provider-ID collisions keep the destination MR ID and merge its review data before applying the
  newest snapshot (`internal/github/snapshots.go::CommitMergeRequestParentSnapshot`).
- A snapshot moved from an old repository identity carries a stale-identity marker; manual refresh
  keeps the workspace's prior head-repository trust until a post-move snapshot with authoritative
  head-repo data clears it (`internal/github/index_sync.go::reclassifyWorkspaceHeadRepoTrust`).
- Merge status observation times are stamped with when Forge sent the request and written with the
  value they time; a value carried unchanged keeps its stored time.
- A field's time is set whenever the response authoritatively reported it, even when empty;
  mergeable state `""`/`"unknown"` is never an observation, and a path that doesn't carry a field
  never stamps it (`internal/github/mr_sync.go::clearUnknownMergeableStateTime`).
- Bulk GraphQL mergeability comes from `mergeStateStatus`, which carries branch protection;
  `mergeable` alone would overwrite a REST `blocked` with `clean` (`platform/github/graphql_models.go::MergeStateToREST`).
- GitHub computes `mergeStateStatus` and `viewerCanMergeAsAdmin` for the asking credential, so a
  split-auth sync App can see `BLOCKED` where the merging user sees `CLEAN`. Reads as the merge
  credential write `merge_rules`: each repository sync reads every open PR in one paginated query,
  and each detail sync rereads its PR. Pull request syncs never write it, so they cannot flip the
  merge credential's answer back (`internal/github/merge_bypass.go::Syncer.refreshRepoMergeRules`,
  `internal/github/merge_bypass.go::Syncer.refreshMergeBypassAfterDetailSync`). See
  [`context/deferred-merge.md`](./deferred-merge.md) for what Forge offers.

## Merge Queue Rules

- Merge queue state is read per pull request, as the GitHub Pull Requests extension for VS Code
  does; never read or snapshot a repository's whole queue
  (`internal/github/merge_queue.go::GraphQLFetcher.FetchPullRequestMergeQueue`).
- Show GitHub's answer at read time: an entry, a latest enqueue (unknown position), or a latest
  non-`merged` removal and its reason. A read finding the PR closed or merged clears it. No
  attempt matching across reads (maintainer decision).
- Queue state belongs to the base branch it was read for; a retargeted PR is re-read, and the
  merge action offers the queue only when the stored base matches
  (`internal/server/pullapi/routes.go::Handler.mergeQueueMutatorFor`).
- Entering or leaving a queue does not change `updated_at`, so every successful live PR detail
  sync checks queue state when due; bulk hydration checks each PR at most hourly, counting failed
  reads. The async merge API's `enqueued` result is final and cannot replace this
  (`internal/github/merge_queue.go::Syncer.refreshMergeQueueAfterDetailSync`).
- A blocked PR re-reads its queue state on every detail sync, because a newly required queue can
  be what blocks it.
- A PR is due unless it shows no state and its base branch was read without a queue within 6
  hours. Merge queue caches are keyed by the repository's stable key, never its route
  (`internal/github/merge_queue.go::Syncer.refreshPullRequestMergeQueueIfDue`).
- Never request `mergeQueue.configuration` in the PR read: GitHub refuses it to an App with only
  the Merge queues permission, and one refused field fails the whole query
  (`internal/github/merge_queue.go::Syncer.mergeQueueMergeMethod`).
- Queue reads use the read credential and fall back to the write credential when GitHub answers
  "Resource not accessible by integration"; the App permission is optional and only keeps reads
  on the App's quota (`internal/github/merge_queue.go::Syncer.mergeQueueRead`).
- When the read after an enqueue cannot run, store the PR as queued at an unknown position even if
  the request was cancelled, since GitHub accepted it (`internal/github/merge_queue.go::Syncer.RecordPullRequestEnqueued`).
- A terminal state write, including a first insert as closed or merged, deletes the PR's queue
  state through a SQL trigger, so reopening never revives it
  (`internal/db/migrations/000068_merge_queue_states.up.sql`).
- Hosts whose schema rejects the merge queue fields latch them off per fetcher until restart.
- GitHub's "Changes must be made through the merge queue" refusal is `platform.ErrMergeQueueRequired`
  and the merge handler enqueues instead; do not rely on synced queue state alone, since a sync
  App may be unable to read `mergeQueue` (`internal/server/pullapi/routes.go::Handler.mergePRWithBodyAdmitted`).
- An enqueue sends `merge_action: merge_queue` with no method, title, or message, which GitHub
  rejects for queued merges. `enqueued` is success with `queued: true`, never merged, and runs no
  merge-completion side effects except keeping a requested workspace deletion. Only GitHub.com enqueues.

## Timeline Event Rules

PR timeline storage is intentionally selective.

- Keep the existing event families stable: comments, reviews, commits, force pushes, and the
  supported PR system events. If bulk sync persists a family, detail sync must persist it too.
- Optional timeline fetch failures may degrade that event family but must not drop an otherwise
  usable PR detail refresh.
- Review comments are not part of the stored sync model unless fetchable within the timeline path.
- GitHub commit events use committer login/name and committed date as actor/time when present,
  keep every known original author in metadata, and fall back independently to author data.
- Merged-actor repair runs after every successful MR index cycle independent of open-list
  freshness, revalidates stored provider identity before persisting, and rejects non-merged
  parents (`internal/github/merged_actor.go::reconcileMergedActorEvents`).
- Keep one canonical merged lifecycle row: authored rows dominate actorless ones, and detail
  synthesis treats any authored merge as canonical regardless of timestamp
  (`internal/db/queries.go::upsertMREventsTx`).
- Merged-actor provider reads are active-detail work and preempt archive leases.
- Actor repairs must emit `pr_detail_refreshed` with canonical repository identity; broad
  `data_changed` does not refresh an open detail.

## SHA-Sensitive Rules

Some PR-derived state is only valid for one head commit.

- Never carry CI status, check runs, or similar head-derived summaries forward when the head SHA
  changed. When a refresh cannot prove state belongs to the current head, clear it.
- Workflow-approval decisions are tied to PR identity, not just head SHA; shared SHAs across forks
  or sibling PRs must not leak approval state.
- Only clone-verified head ancestry may set commit-event `obsolete`; a provider relisting on an
  unverifiable head may provisionally clear it, and unverified, unlisted events keep their last
  verified flag. The frontend never infers obsolescence (`internal/github/commit_liveness.go::computeCommitLiveness`).
- Liveness rides the sync round's own revision-guarded snapshot write, never a separate writer;
  the only allowed cache is a memo of pure (head, candidates) reachability.
- The open-to-terminal transition is finalized inside the parent snapshot transaction, and no
  terminal MR is ever recomputed.
- UI mutations never write local state eagerly: merge re-reads the provider, and state edits
  commit the mutator's returned MR, so adapters must return the complete updated MR.
- Ancestry reads run in-process with a bounded visit budget, never git subprocesses; SHA-256
  repositories are out of scope (`internal/gitclone/reachability.go::CommitsReachableFrom`).
- A fixture whose PR head is a real clone commit must seed commit events reachable from that head,
  or liveness flags them obsolete (`internal/testutil/diff_repo.go::alignWidgetsPR1CommitEvents`).
- A review thread's `DiffHeadSHA` is the comment's commit, distinct from the PR's synced diff head;
  suggestion apply uses the thread's to reject stale suggestions (`platform/github/provider.go::GithubReviewLineRange`).
- Suggestion apply commits to the PR head repo/branch bound by `expectedHeadOid`, never the base.
  Reject whitespace-padded paths, preserve terminal blank lines, and do not re-add a trailing
  newline when every line is deleted (`platform/github/client_reviews.go::Client.ApplyReviewSuggestions`).
- Mutation-time `NOT_FOUND` during suggestion apply is a head repo/branch race and maps to
  conflict `head_repo_unknown`, not `not_found`.
- Suggestion apply spends REST and GraphQL, and reports both buckets so a paused GraphQL budget
  disables it without making GraphQL provider-neutral.

## Fallback Data Rules

GitHub data sources are layered and may stay GitHub-specific behind the provider split.

- Repos without usable releases may fall back to tags for version-like timeline context.
- Import for the authenticated owner may need a different API path so private owned repos appear.
- Fallbacks must keep the primary path's response shape and meaning; provider-neutral persistence
  receives the same semantic shape from GraphQL, REST, tags, or fallback listing.

## Native Stack Rules

- GitHub.com merges are asynchronous operations polled to terminal `merged`; `pending`/`enqueued`
  never count as merged. GHES stays synchronous with no probe fallback
  (`platform/github/merge_async.go::Client.MergePullRequest`).
- Preserve required-rebase failures verbatim; do not use private website routes or local
  force-pushes until GitHub exposes a token-authenticated stack-rebase API.
- Confirmed native stacks claim and order their PRs first; branch inference always runs afterward
  on unclaimed PRs, including when the preview is disabled or failing
  (`internal/stacks/detect.go::RunDetectionWithNativeStacks`).
- Native projections use the bottom PR number as the neutral stack key; GitHub's stack number is
  cache-only.
- Merging a native member also merges every unmerged member below it, so the merge UI names every
  PR that lands; a branch projection of the same row clears `native`
  (`frontend/src/lib/components/detail/stack-merge.ts`).
- Native member `mergeable`/`mergeStateStatus` compare each PR with its parent branch, not the
  stack base; a stack can conflict on `main` while every member is `CLEAN`.
- A PR belongs to at most one projected stack; any overlap makes the native projection ambiguous
  and branch inference owns the repository for that pass.
- Confirmation reconciles against currently observed open-PR hints, never cached member state. A
  stack holding a merged or closed member is refetched on a bounded schedule anchored to its own
  observation time (`internal/github/native_stack_sync.go::nativeStackObservationExpired`).
- Only a query that requested the preview fields may replace stack hints.
- Only a refresh that resolved every target seeds confirmation; an incomplete one evicts the PR
  list ETag and projects nothing that pass (`internal/github/native_stack_sync.go::refreshGitHubNativeStackCache`).
- The preview must not widen the blast radius of the list it rides on: a bad hint costs the hint,
  not the list (`platform/github/native_stacks.go::decodeNativeStackHint`).
- Preview-only GraphQL fields must be absent from disabled query shapes; `@include(false)` does not
  bypass schema validation. Schema rejection drops the fields for that host, not bulk fetch.
- Disabling the preference synchronously restores branch projections and leaves cached native rows
  dormant, for every repository holding cached stacks, tracked or not. Boot with it off reconciles too
  (`internal/server/syncevents/native_stack_settings.go::Handlers.ReconcileGitHubNativeStackProjection`).
- The syncer's preference swap is the transition authority: reconcile on the swap's own previous
  value, on the server-lifecycle context, never a separately read config or the request's context.
- The swap takes cfgMu and the projection lock, and reconciliation rechecks the current preference
  under the projection lock, so a disable that lost to a later enable cannot replay over it
  (`internal/github/syncer_settings.go::SetPreferGitHubNativeStacks`).
- Native results carry the preference generation, so a sync that began while the preview was
  enabled cannot reinstate it after disable (`internal/github/native_stack_sync.go::dropStaleNativeStackResults`).

## Historical Archive Rules

Tracking and archived repositories:

- Configured repositories seed durable archive discovery before sync cutover, with no legacy cursor
  translation (`internal/github/tracked_repos.go::SetReposWithContext`).
- Provider-archived repositories are configurable and archive-only: live sync, notification
  polling, and watched fast sync skip them; archive discovery and hydration treat them like any repo.
- Archived flips apply without restart: settings add/refresh, config reload, and each sync pass
  re-resolve archived state, so an upstream unarchive returns a repo to live sync
  (`internal/github/repo_identity.go::reconcileArchivedRepos`).
- Archived refreshes honor the credential bucket's eligibility and cadence and register as
  provider work, so they never spend budget a live repository would be denied.
- A live repository that resolves archived mid-pass stops before syncing content; detail drain and
  per-item sync stop for archived refs except under the archive budget (`internal/github/repo_sync.go::syncRepo`).
- Catalog republication without fresh metadata preserves the tracked archived flag, and a newer
  tracked flip stands over an in-flight operation's metadata (`internal/github/repo_identity.go::publishResolvedRepository`).
- Archived inclusion in listings is an explicit request; config expansion includes archived
  projects, while import previews keep GitLab's server-side `archived=false` filter.

Tracked-set identity and provenance:

- Deduplicate tracked repos by stable provider ID, falling back to route; a rename must not sync or
  archive-seed a repository twice. Resolved refs replace fallback refs, never the reverse
  (`internal/github/repo_config_resolver.go::ExpandedRepoSet`).
- Exact-resolved refs record the config entry they came from; glob refs record none. Config
  resolution is the only author of that provenance.
- When an exact entry cannot resolve, match tracked refs by provenance before route, so a
  provider-side rename does not synthesize a duplicate, identity-less ref
  (`internal/github/repo_config_resolver.go::FallbackConfiguredRepoRefs`).
- Route-keyed recovery never crosses conflicting stable provider IDs; a reused route must not
  inherit the displaced repository's provenance.
- Config-entry matching honors provenance scoped to provider and host; removing an entry clears
  its provenance so a future entry with the same path cannot bind the wrong repository.
- Startup recovers a failed exact entry's stable identity through catalog route history and
  registers its credential alias (`cmd/kenn-forge/main.go::fallbackExactFromDB`).
- Publications locate their tracked slot by the resolved stable ID first and never displace an
  entry whose ID conflicts; when IDs differ, the snapshot's archived flag is meaningless.

Archive worker:

- Seeding degrades per repository: a bad ref is logged and skipped, never fatal, and excluded
  from authentication retry; only batch reconciliation errors propagate
  (`internal/archive/service.go::EnsureConfigured`).
- Removal pausing (`configuration_removed`) requires a complete picture; when any ref fails
  without a known row, defer pausing entirely rather than risk pausing the wrong repository.
- While an unresolvable ref stays in config, genuinely removed repositories keep collecting; the
  recurring deferral warning is the intended operator signal.
- Worker passes resolve tolerantly: provider-classified failures are dropped per repository, but
  infrastructure errors surface (`internal/archive/service.go::resolveRepositoriesTolerant`).
- The worker backs off while idle up to five minutes; completed syncs wake it, and only hosts that
  denied or preempted archive work wake it on provider-work release (`internal/github/archive_sync.go::runArchiveLoop`).
- Archive scheduling is eventually consistent by maintainer decision: a late wake that delays
  eligible work until the next backoff pass is accepted. Do not add wake bookkeeping for it.
- Archive code owns historical identity inventory only; hydration uses ordinary item sync, not
  archive-specific lookup or persistence (`internal/github/archive_sync.go::SyncArchiveItem`).
- Archive hydration bypasses parent-detail ETags, and treats timeline failures as hard errors
  where ordinary refresh is best-effort.
- Post-hydration completeness uses the repository ID that sync resolved, never the caller's route.

Archive pacing:

- With a known registry pacing window, archive admission paces off provider quota alone: each
  pool's remaining minus its own archive reserve (`max(limit/5, RateReserveBuffer)`), checked at
  admission and every wire attempt (`internal/github/budget.go::ArchiveProviderReserve`).
- Reserves are per pool; never apply the smallest pool's reserve to a larger one. The local
  ceiling neither enlarges nor shrinks the provider envelope.
- An active registry with an incomplete or expired window defers as provider-quota-unknown; it
  never falls through to local pacing (`internal/github/archive_sync.go::Admit`).
- Every other archive path spends local hourly surplus above `archiveLiveFloor`. Live work
  preempts the archive lease.
- An unobserved archive reservation stays deducted until a current header accounts for it or the
  window rolls (`internal/github/quota.go::quotaTransport`).

Inventory and lookups:

- Every issue-only lookup rejects a PR-shaped Issues API response; `SyncItemByNumber` is the
  kind-dispatching exception.
- Updated scans overlap the durable watermark; issue cursor identity stays bound to the original
  boundary (`platform/github/provider_pages.go::ListMergeRequestsPage`).
- Durable PR inventory bypasses the list ETag cache; a bodyless 304 must not fail an unchanged scan
  (`platform/github/pages.go::Client.ListInventoryPullRequestsPage`).
- Issue-only repositories return pulls 404; classify feature-disabled only for explicit
  `has_pull_requests=false` (`platform/github/provider.go::Provider.mergeRequestsDisabledByRepository`).
- Repository probes classify only auth/access/not-found; transient failures stay retryable and
  non-destructive. Lookups compare the response repository with the requested one, so transfers
  become moved outcomes (`platform/github/provider_pages.go::ArchiveDestination`).
- A 410 from the single-issue endpoint after repository-wide issue disablement is classified means
  deleted; map it to `removed_upstream` only there (`platform/github/provider_pages.go::Provider.classifyIssueLookup`).
- A PR 404 proves removal only when the repository probe matches the tracked provider key.
- A provider-confirmed removed PR becomes `removed_upstream` and keeps its last content; fresh live
  inventory restores it even at unchanged timestamps (`internal/db/queries_archive.go::RecordLiveMergeRequestPresence`).
- A previously-open issue whose lookup is a true removal is tombstoned closed locally; transfers
  and bare 404s keep failing so maintainers see them (`internal/github/issue_sync.go::tombstoneRemovedIssue`).
- Archive failures preserve typed authentication and reset-aware rate-limit errors so scheduling
  defers instead of hot-looping (`platform/github/provider_pages.go::archiveTransportError`).
- An issue without `updated_at` uses `created_at`; zero timestamps must not bypass monotonic
  snapshot acceptance (`platform/github/normalize.go::NormalizeIssue`).

Merged lifecycle evidence:

- Archive completion revalidates merge evidence against the transaction-current row; a mismatch stays
  retryable, never complete.
- Partial detail must not erase durable lifecycle data: keep the stored merge time when the
  provider omits it, and keep stored parent timing when GitHub omits it
  (`internal/github/mr_sync.go::preserveMergedAtIfOmitted`).

## Owner Routes And Identity Accounting

GitHub authorization is selected by `(host, repository owner)`, with exact repository overrides
ahead of owner mappings and the host fallback. Rate state, budgets, cadence, and snapshots are
selected separately by `(host, authenticated identity)`.

- When scoped routes cover configured repositories, the implicit ownerless fallback is
  best-effort; only an explicitly configured fallback fails startup. A `github_token_env` equal to
  the built-in default is not explicit, because Load, Save, and the sample config all write it
  (`internal/config/config.go::Config.HasExplicitGitHubTokenEnv`).
- A router with no exact, owner, or fallback route is a routing failure; fail closed
  (`internal/github/auth_router.go::MissingRouteError`).
- Repository preview selects the entered owner's route before it is tracked. Ownerless APIs use
  only the host fallback, never an arbitrary owner PAT.
- App-only routes may read, but notifications and mutations stay disabled until restart
  establishes a user identity; their reads leave viewer permissions unknown
  (`platform/github/client_repository.go::Client.withViewerOverlay`).
- Background requests on the write credential charge the write identity's budget; foreground
  mutations are uncharged. Live work registers every principal it will touch, so a shared-PAT
  archive is preempted (`internal/github/client.go::NewClient`).
- The PAT viewer overlay is cached per repository and credential and revalidated by ETag on every
  read; do not replace it with a time-based cache (`platform/github/client_repository.go::Client.viewerRepoOverlay`).
- Reload probes share one fresh installation-token cache per batch; reusing the live cache would
  let a revoked installation pass validation (`internal/tokenauth/source.go::SourceSet.NewProbeBatch`).
- Token-file rotation within one GitHub user is hot-reloadable. Changing the user, adding a write
  identity to an App-only route, or adding, removing, or changing scoped routes requires restart.
- Selected-repository App installation listing is owner-scoped discovery only, never a fallback
  for other operations. Owner discovery unions PAT and App listings and fails closed if either fails
  (`internal/github/auth_router.go::RoutedClient.listRepositoriesByOwnerAcrossRoutes`).
- `RoutedClient` embeds `Client`, so an optional interface it does not re-declare silently reports
  unsupported on every routed host. Add a repository-routed method and an assertion for each
  (`internal/github/public_api_guard_test.go::TestRoutedClientExplicitlyImplementsOwnerBearingClientMethods`).
- A call made during repository sync routes by repository even for host-scoped endpoints, and must
  pass `tokenauth.WithGitHubOwner`, since an ownerless path skips the App and bills the PAT
  (`internal/github/auth_router.go::RoutedClient.GetUserForRepo`).

Git credentials:

- Workspace Git uses exact-repository or owner PAT routes and never an App installation token.
  Before injecting a PAT, require upstream `origin`, reject local URL rewrites, and validate every
  origin URL.
- Forge's own clone store never pushes, so it uses the read chain with a covering App first,
  selected by repository owner; App-only Forge can maintain clones
  (`cmd/kenn-forge/provider_startup.go::gitStartup.ReadSourceForRepo`).
- Thread full provider, host, owner, and repository identity and the normalized platform through
  clone and local reads, or an unqualified GitHub ref picks no credential route.
- Workspace clones are path-scoped while other clone storage is identity-partitioned, so shared
  full-stack fixtures must seed both (`internal/testutil/diff_repo.go::SetupDiffRepo`).
- Spoke clone-read admission uses read credentials; workspace launch still requires a mutation
  credential (`internal/gitclone/clone.go::Manager.RequireReadCredentialRoute`).
- A nil `tokenauth.Source` is not fail-closed: `gitclone` runs git with no credential. A resolver
  that cannot serve a repository returns a source whose `Token` reports the missing route
  (`cmd/kenn-forge/provider_startup.go::missingRouteTokenSource`).

Sync status:

- A credential-caused clone/fetch failure stays in `git_access` until the next success or removal,
  follows verified identity across renames, and logs only on start or reason change
  (`internal/github/git_access.go::Syncer.recordGitAccess`).
- Token resolution failures must carry `gitclone.ErrCredentialUnavailable`, or the classification
  is lost in the sanitized Git error chain.
- A failed full sync stays in `repo_errors` until the next successful sync or removal, through
  skipped passes, budget waits, and cancellation; run-level `last_error` is too transient alone
  (`internal/github/repo_sync_problems.go::Syncer.recordRepoSync`).
- A sync status field the UI reads must be declared in the live-event schema, which drops
  undeclared keys (`frontend/src/lib/stores/provider-events-workflow.ts::SyncStatusEvent`).

## Credential-Aware Quota

- Provider quota is keyed by identity and REST/GraphQL resource, so a user response never
  overwrites an App pool (`internal/github/quota.go::QuotaRegistry`).
- Prefer valid core response headers over the `/rate_limit` core body, which can report full
  capacity while the same response reports exhaustion (`platform/github/client_rate.go::Client.GetRateLimitSnapshot`).
- Each transport chain carries a fixed identity: reads spend the read identity; mutations and
  notifications the write identity (`internal/github/client.go::WithQuotaAccounting`).
- A scheduling decision reads the pool of the credential that will do the work; a host-wide
  tracker lets one credential's exhaustion suppress another's (`internal/github/reserve.go::graphQLReadAllowed`).
- Gate background eligibility on REST only; GraphQL reads are optimizations with a REST fallback,
  and the GraphQL reserve applies where it is spent (`internal/github/reserve.go::repoEligibility`).
- There is one background reserve check, on the snapshot cadence, read by every consumer. Do not
  add per-repository, per-item, or per-page reserve checks (`internal/github/reserve.go::backgroundReserveExhausted`).
- The reserve is a soft foreground buffer: a credential that crosses it keeps spending until the
  window turns. The local hourly ceiling is the hard per-wire guard.
- When a credential is unobserved since restart, fall back to persisted rate state; an elapsed
  persisted reset window must not gate (`internal/github/reserve.go::persistedReserve`).
- The local `sync_budget_per_hour` ceiling is separate from provider quota and reported apart.
- The local ceiling rolls its hourly window on its own clock and never needs a provider response
  to reset (`internal/github/budget.go::rollLocked`).
- Refunds are dropped once the reservation's window elapses, so a late 304 cannot raise the new
  window's ceiling (`internal/github/budget_spend.go::refundKind`).
- Discovery (open lists, full-sync and archived-preflight identity reads) spends the essential
  reserve; optional spend stops at limit minus reserve so it never starves discovery. User-initiated
  direct syncs are not budgeted (`internal/github/budget.go::TrySpendEssential`).
- Spend kinds follow the workflow that requested the work, and nested identity reads inherit it;
  reserve admission stays separate (`internal/github/index_sync.go::indexSyncRepo`).
- Per-item enrichment inside an essential list fetch is optional and degrades on refusal instead
  of failing the list (`internal/github/budget_transport.go::WithoutEssentialSyncBudget`).
- A local-ceiling refusal stops GraphQL retries and logs once per repository. Refused list fetches
  keep list ETags, since nothing reached the wire; refused item work evicts them so closures recover
  (`internal/github/index_sync.go::indexSyncRepo`).
- Budget waits preserve the last completed repository health, including earlier failures; a mixed
  cycle keeps the real failure alongside the ceiling cause.
- Notification reads and ack propagation gate on the write identity's REST pool, by worst-case
  request count (`internal/github/notifications_sync.go::ensureNotificationBudget`).

## GitHub App Manifest Flow

- The App manifest must include a valid `hook_attributes.url` even though Forge polls; GitHub
  reports its absence as a generic `"url" wasn't supplied`. Keep the e2e test asserting manifest
  shape (`internal/githubapp/manifest.go::NewManifest`).
- A covering App installation leads every read chain, even over a repository token override,
  because installation tokens carry their own budget; the override still signs writes
  (`internal/config/config.go::Config.ResolveGitHubRepoTokenSource`).
- Installation tokens are account-scoped: never use one owner's installation for another owner on
  the same host. Ownerless contexts fall through to PAT/`gh`, and choose installation-only
  endpoints only when the requested owner resolves to an installation.
- Private `user-attachments` return 404 to installation tokens, so the image proxy uses the PAT/`gh`
  chain (`platform/github/markdown_images.go::GetMarkdownImage`).
- Some uploads 404 for every credential yet render on GitHub; on 404, render through
  `POST /markdown` and fetch the signed URL, accepting only GitHub's signed image host.
- Video attachments redirect to signed storage on another host, and Go drops `Authorization` on
  that redirect (`platform/github/markdown_media.go::Client.OpenMarkdownMedia`).
- Repository-file images are proxied only for the route's own repository and are type-sniffed and
  marked mutable; the frontend must apply the same URL rules (`platform/github/markdown_images.go::getRepositoryFileImage`).
- Multiple `[[github_apps]]` rows per host are distinct credentials: management commands target
  one row, and duplicate installation accounts on a host are invalid.
- Selected-repository coverage applies only to repos owned by the row's `installation_account`;
  the install CLI must not warn about cross-account repos.
- The selected-repository list is a startup snapshot; changes need `install` and a restart. Never
  retry PAT credentials after an App-backed 404, which would hide stale or revoked App access.
- Re-running `install` adopts an existing installation only after a clean poll deadline, and only
  the app's sole installation on the recorded or configured account; transient errors and
  interrupts never adopt.

## Activity relay

- Relay hints accelerate normal polling; each consumer still uses its own credentials and rate
  gates (`internal/github/relay.go::refreshRelayHint`).
- Nothing about a hint is persisted. A hint that cannot run now (budget, cooldown, catalog) is
  dropped and left to ordinary syncing, and provider work never stalls the stream
  (`internal/github/relay.go::relayQueue`).
- Format failures stay visible across reconnects until a valid hint proves decoding recovered;
  accepting a stream is not evidence of compatibility (`internal/github/relay.go::RunRelay`).
- An outage logs one warning and one reconnect notice; per-retry logs stay at debug.
- Relay ref reads must not advance the normal-sync list ETag, which also gates the richer GraphQL
  refresh (`internal/github/relay.go::refreshRelayRefs`).
- `workflow_run` and `check_run` refresh checks only for explicit PR references; never fan an
  unassociated event out across PRs (`internal/activityrelay/http.go::reduce`).
- Feed repository IDs are GitHub integer repository IDs; match tracked repositories by ID.
- Batch Actions in the relay, never in consumers: at most one checks hint per PR and one runs hint
  per repository per minute (`internal/activityrelay/broadcast.go::Broadcaster`).
- Reserve bounded workflow-list capacity at every stage (relay pending batches, subscriber buffers,
  consumer queue) so PR-check traffic cannot crowd out hints with no ordinary-sync fallback
  (`internal/github/relay.go::relayWorkflowReserve`).
- Check hints refresh only known open PRs with a head SHA; never upgrade them to full PR syncs.
- Workflow-list hints only notify foreground views, so background quota reserves must not
  suppress them.
- A parent ETag does not show whether comment content changed; child-change hints need
  unconditional detail reads (`internal/github/issue_detail.go::getIssueForDetail`).
- Relay status travels with sync status, is absent when the consumer is off, and is not proof that
  provider refreshes finished (`internal/github/relay.go::updateRelayStatus`).
- Relay status alone must not reload provider data (`cmd/kenn-forge/provider_startup.go::wireSyncStatus`).

## GitHub CLI shim

- A partial sync/archive is not a complete `gh pr list` result; obtain complete lists through
  sync or delegate the invocation, never silently omit fields or filters (`internal/ghshim/storage.go::Read`).
- Preserve upstream `gh` output bytes for intercepted commands; keep real-CLI differential tests.
- Shim reads use local stored data without requiring a syncer; otherwise delegate to `gh`. Never
  add provider fetches or a TTL cache (`internal/server/pullapi/gh_shim.go`).
- A missing numeric view may use the existing hub read API before delegating to `gh`.
- Log shim calls through `slog` at debug, never a separate usage file, with the full argv as a
  string so redaction applies (`internal/cli/ghcli/ghcli.go::logCall`).
- The shim is the `kenn-forge gh` subcommand; do not reintroduce a separate binary. Pass gh
  arguments through unparsed and unnormalized (`cmd/kenn-forge/cli.go::cliArgs`).
- The shim is reached directly or through a `gh` link to `kenn-forge`.
- A `gh` wrapper around `kenn-forge gh` must not loop: pass-through marks the chosen executable
  and a hop cap stops other cycles (`internal/cli/ghcli/ghcli.go::Run`).
