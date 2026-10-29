# Retries, Backoff, and Single-Flight Dedup

Use this document for transient upstream retries, rate-limit gates, scheduling
cadence, or single-flight request deduplication.

kenn-forge has three distinct wait patterns. Keep them separate:

- **Transient retry** — short bounded retries for flaky upstream failures.
- **Rate-limit gate** — wait until provider quota resets.
- **Scheduling cadence** — steady periodic loops.

Only transient retry is consolidated in a shared package; rate-limit gates and
cadence loops are intentionally separate.

## Transient retry

- Transient retry goes through `internal/retry`, only for idempotent operations,
  and only for errors the caller's classifier marks transient (`internal/retry/retry.go`).
- Never stack transient retry on paths that already retry, such as the
  `internal/github` REST client.
- Frontend idempotent reads use `retryIdempotentRead`; never add custom retry loops
  or Retry buttons, and a read that still fails shows its error
  (`frontend/src/lib/api/retry-policy.ts`).
- Scope upstream timeout child contexts to the upstream call; later local database
  and filesystem work keeps the request context
  (`internal/server/kata/workspace.go::getKataProjectMappings`).
- Keep the git transient matcher conservative; a false positive turns a permanent
  failure into a multi-second hang (`internal/gitclone/retry.go`).
- Git HTTP stalls use a separate low-speed timeout so active clones keep the longer
  budget; retry only existing idempotent operations
  (`internal/gitclone/clone.go::Manager.gitRunnerAuthed`).

## Rate-limit gates

- Rate-limit gates wait for quota resets or an observed-throttling cooldown; they
  never retry mutations automatically.
- Never wrap rate-limit paths in `backoff.Retry`, `RetryAfterError`, or a new retry
  abstraction unless a separate design changes rate-limit policy.
- Unknown-quota exhaustion uses one capped local cooldown shared by sync and action
  availability, reset by success and kept separate from provider reset data
  (`internal/ratelimit/rate.go::RetryAt`).
- Archive budget refusals are scheduling waits: they add no failure attempts and
  preserve prior failures until a successful collection
  (`internal/db/queries_dataset_progress.go::DB.FailArchiveItemSync`).
- Budget-wait status comes only from pending items with future retry times, not from
  failed items awaiting their own retry (`internal/db/queries_archive.go::deriveArchiveProgress`).

## Scheduling cadence

- Steady background cadence is scheduling policy; never migrate ticker-driven sync or
  refresh loops into `cenkalti/backoff` (`internal/github/sync.go::Syncer.Start`).
- The archive worker uses backoff only as an idle-delay calculator, not a retry wrapper:
  it doubles to a five-minute cap and resets on any wake or worked pass
  (`internal/github/sync.go::Syncer.runArchiveLoop`).
- Archive provider work counts as worked even if a later quota refusal stops the pass
  (`internal/archive/scheduler.go::Service.finishWork`).
- Manual workflow dispatch is never retried; follow only the run ID dispatch returned,
  never infer it from ref, actor, or time
  (`internal/server/workflowapi/dispatch_follow.go::Handler.followDispatch`).
- Live provider work waits for preempted archive requests only while its caller is
  active; a canceled waiter must not release the archive lease or leak live-work counts
  (`internal/github/sync.go::beginProviderWork`).

## Sync coalescing

- Provider-index sync keeps at most one coalesced follow-up: bursts neither drop
  accepted work nor build an unbounded queue, and nil-versus-empty scope is preserved
  (`internal/github/sync.go::triggerRunWithCadence`).
- Coalesced scoped user bypasses stay bound to stable repository identity; ad-hoc
  triggers return after admission while provider execution stays asynchronous.
- Public sync status stays running across follow-up handoffs, and a stale idle
  snapshot must never overwrite a later run (`internal/github/sync.go::runOnceWithSlot`).

## Repository-disabled feature cooldown

- Repository-disabled issue and merge-request scopes are probed in the background at
  most once per 24 hours (`internal/github/feature_cooldown.go::repositoryFeatureProbe`).
- Explicit sync bypasses cooldowns that existed when the run began; a newer disabled
  result still gates follow-on lanes, and a concurrent disabled renewal wins.
- Follow-on lanes after index completion need a fresh probe; clearing the index
  cooldown does not authorize an unchanged-list comment refresh.
- Cooldown keys use provider-canonical repository identity; host, owner/name case,
  and derived-path aliases must converge (`internal/github/feature_cooldown.go::repositoryFeatureKey`).
- Classify repository-disabled reads before generic fallback; GitHub's 410 is definitive,
  other providers confirm 403/404/410 against repository metadata
  (`platform/gitealike/feature_disabled.go::Provider.repositoryFeatureError`).
- Unconfirmed disabled-candidate errors are preserved as ordinary errors.
- Custom GitHub GraphQL timeline transports must keep structured non-2xx errors so
  410 classification works (`platform/github/client.go::Client.ListPullRequestTimelineEvents`).
- Recording a disabled result must keep earlier same-scope item failures so retry and
  ETag invalidation state survive the cooldown (`internal/github/sync.go::indexSyncRepo`).
- Apply the cooldown gate before shared budget exhaustion so suppressed work cannot
  starve unaffected scopes (`internal/github/sync.go::drainDetailQueue`).
- Archive inventory and maintenance bypass cached cooldowns and commit empty streams
  only after a live disabled response; hydration honors the cooldown.
- A live recovery reopens an unsupported historical archive stream only when the
  provider declares that inventory capability.

## Long-lived stream recovery

- Hub event-stream recovery is connection lifecycle policy, not a request retry budget:
  a spoke reconnects indefinitely with a short jittered ceiling
  (`internal/providerplane/events.go::EventClient.Run`).
- A valid SSE response resets the failure count; every failure kind uses the bounded
  wait so none can hot-loop, and cancellation interrupts reads and timers immediately.
- Every successful hub connection requests provider reconciliation and refreshes sync
  status after the replay-complete barrier and before it is reported healthy.
- A stale hub cursor or poison frame updates only the private hub cursor; never copy a
  remote ID into the spoke's browser cursor
  (`internal/providerplane/events.go::EventClient.resynchronize`).
- The activity relay subscription has no durable cursor: reconnects never replay, and
  missed hints are left to ordinary syncing (`internal/github/relay.go::RunRelay`).
- Relay backoff resets only after a stream stays open for the full ceiling, so a relay
  that accepts and drops connections cannot cause a reconnect storm.
- The relay does no reconciliation on connect.

## Single-flight dedup

- Prefer dedup over retry when multiple in-process call sites hit the same upstream
  resource; dedup removes the cause, retry only absorbs the effect.
- Concurrent `EnsureClone` callers share one clone/fetch per storage namespace and
  repository (`internal/gitclone/clone.go::ensureCloneInNamespaceValidated`).
- A caller whose context is already canceled must not join a clone slot.
- Clone slot keys must be unambiguous; separator-free joins let `foo/barbaz` collide
  with `foobar/baz`.
- The clone runner context is detached from any one waiter and bounded, so one cancel
  cannot abort shared work and a stuck git process cannot hold the slot forever.
- Only the slot starter deletes an invalid clone; invalid clones are renamed aside so
  readers never see partial deletion, and cleanup failure is terminal.
- GitHub App token mints share one in-flight result per credential; only failures of
  the minting caller's own context bypass caching
  (`internal/tokenauth/source.go::githubAppTokenStore.resolve`).
- Token mint failures without retry headers cool down for five seconds; server-directed
  retry deadlines are capped at one hour.
- Unauthorized retries evict only the exact rejected token
  (`internal/tokenauth/source.go::githubAppTokenStore.invalidateToken`).
- Roborev discovery keeps definitive results until managed-clone initialization
  confirms a registration; invalidation fences older refreshes and each waiter can
  cancel independently (`internal/server/roborevapi/roborev_repositories.go::RoborevRepositoryProbe`).

## Tests

- Test policy decisions, not the backoff library: classifiers, permanent-error
  wrapping, budgets, dedup keys, and reset conditions.
