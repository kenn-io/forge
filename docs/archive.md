# Historical activity archive

Middleman can build and maintain a complete historical archive of provider
activity for the repositories it tracks: every issue and pull/merge request,
their ordinary discussion comments, submitted reviews, and inline review
comments. Archived data lives in middleman's own SQLite database and feeds
deterministic offline reports.

This document is the single reference for the archive's scope and semantics.
It supersedes the design documents that previously lived under
`docs/superpowers/`. For the CLI surface, see
[Commands](commands.md#historical-activity-archive).

## Scope

The archive is a completeness workflow over middleman's existing
provider-neutral data, not a second copy of it. Issues, pull requests,
comments, and reviews are stored once, in the same tables the regular sync
and detail views use. Separate archive state records which repositories and
items have actually been inventoried and hydrated; the presence of an item
row alone never implies a complete archive.

Archived per repository:

- issues and pull/merge requests, across all states;
- ordinary discussion comments on issues and pull requests;
- submitted pull-request reviews (author, verdict, body, time);
- inline review-thread comments, including replies.

Out of scope: commit, force-push, CI, release, label, assignment, and
lifecycle event families; previous versions of edited content; content the
provider has deleted. The archive mirrors current provider truth — edits
overwrite, deletions disappear, and no tombstones are kept.

## Shared sync and ingestion architecture

The archive is a completeness policy over the regular sync pipeline, not a
second provider-ingestion stack. Regular sync, interactive detail refresh,
and archive work use the same provider reads, normalization, error
classification, rate accounting, and domain persistence.

Provider integrations expose one canonical page-oriented read operation for
each dataset. Regular sync may drain those pages immediately when it needs a
complete interactive snapshot; archive work may commit an opaque cursor after
each page so it can resume later. Whole-dataset helpers are collectors over
the page operation, not separate endpoint implementations. A provider must
not maintain parallel normal-sync and archive-sync request or normalization
paths for the same dataset.

All complete observations publish through one revision-aware ingestion API:

- parent issue and merge-request snapshots use the same monotonic provider
  timestamp and local revision rules;
- ordinary comments use the same replacement semantics;
- submitted reviews and review threads use the same stable-identity additive
  semantics;
- child datasets publish only while the parent revision that produced them is
  current.

Regular sync supplies complete datasets collected in memory. Archive work
supplies complete datasets assembled from durable staged pages. Staged pages
are resumable work records, not a second copy of domain content, and both
callers use the same final publication operation.

A complete regular-sync observation also advances matching archive work when
all of the following are true: the exact repository and item are tracked, the
full dataset page sequence was fetched, and the parent revision is still
current. Domain publication and archive completion commit atomically. Partial
or best-effort regular-sync reads never claim archive completeness. This lets
active items become fully archived through normal maintenance instead of
being fetched again solely for archive bookkeeping.

Provider-host work uses one priority and budget coordinator. Normal sync and
archive selectors may independently decide what work is due, but they do not
implement separate admission, reserve, or host-concurrency policies. Every
regular-sync class — interactive item refresh, open and watched item detail,
notifications, and current index sync — outranks every archive class. Archive
maintenance outranks historical hydration, and discovery inventory is lowest.

Sharing provider and persistence primitives must not make live work a consumer
of the archive scheduler. A live refresh starts directly, never waits for an
archive repository or item claim, cursor, staged page, retry time, completeness
transition, or report transaction, and does not stage provider pages on disk.
Absent, stale, paused, or internally inconsistent archive state leaves archive
work pending but cannot reject or delay an otherwise valid live domain write.
When a live write can also advance archive coverage, that advancement adds no
provider request and has no archive-state precondition; a stale conditional
archive update simply affects zero rows.

Priority is enforced at every provider-request boundary, not only when an
archive item is first claimed. Once live work is runnable, the coordinator
admits no new archive request for that provider host. Archive execution is
page-bounded and may not retain host admission while traversing multiple pages,
items, or datasets. An already-started provider request cannot be preempted, so
one such request is the maximum archive-caused delay before live work proceeds.
Database staging or reporting must never hold provider-host admission.

Archive traffic may spend only time-released surplus after the coordinator
reserves a hard live floor and the provider's normal rate reserve. Every shared
provider operation declares a conservative worst-case request cost. For a
host, the hard live floor `H` is the largest supported interactive detail cost,
plus one current-index page cost, plus one notification page cost when that
provider supports notifications. Unsupported classes contribute zero. The hard
floor is never available to archive work. If the configured budget `B` is less
than or equal to `H`, archive provider requests remain disabled for that host
while live sync continues normally.

Surplus release is deliberately back-loaded within the one-hour sync-budget
window. With observed provider reset `R` and current time `t`, the elapsed
fraction is `f = clamp(1 - (R - t) / 1 hour, 0, 1)`. The cumulative archive
spend ceiling is `floor((B - H) * f²)`. A new archive request is admitted only
when its declared cost fits both that ceiling and the remaining budget above
`H`. Consequently archive work is conservative early, may catch up faster as
reset approaches, and can consume nearly all otherwise expiring surplus
immediately before reset without touching the hard live floor. The
provider-reported rate reserve is an independent hard gate and is never
relaxed by this ramp.

The coordinator uses the provider tracker's observed reset timestamp as the
window boundary. If it is absent, stale, or more than one hour in the future,
archive admission uses `f = 0` rather than guessing from wall-clock hour
boundaries. A provider window reset clears cumulative archive spend and wakes
pending archive work. All operation-cost declarations, the live-floor
calculation, archive spend, and reset transition are observable in scheduler
status or diagnostics so a stalled archive can be explained without debug
logging.

This policy cannot reserve capacity for an unbounded, unknowable future burst;
it guarantees instead that runnable live work is never displaced, the hard
live floor is never spent by archive work, and unreleased surplus remains
available to live sync. Archive exhaustion is expected, while archive work
must never be the reason the hard live floor is unavailable. This remains one
shared budget policy, not an independent archive quota.

Responsibilities remain distinct:

- regular sync owns selection of open, watched, recent, and interactively
  requested items, plus CI and workflow-approval refreshes;
- archive state owns historical inventory cursors, durable staged pages,
  retries, terminal outcomes, coverage, completeness, and reports;
- shared ingestion owns provider requests, pagination, normalization, error
  classification, budget accounting, revision safety, and domain writes.

The historical inventory maintained by archive discovery replaces the former
regular-sync historical backfill. There must be only one background crawler
whose purpose is to exhaust old issues and pull or merge requests.

These boundaries are architectural invariants. Provider tests must exercise
the canonical page reader, and sync/archive integration tests must prove that
both consumers publish identical normalized datasets through the shared
revision-aware path. Priority tests must prove that runnable live work is
admitted before archive work, that the hard live floor survives archive spend,
that archive admission increases monotonically and follows the back-loaded
ceiling as reset approaches, that an unknown reset stays conservative, that
live work waits for at most one already-started archive request, and that
broken or paused archive state cannot fail a live refresh. Adding an
archive-only provider request, normalizer, domain writer, or admission
framework for an already supported dataset is a design regression.

## Collection modes and lifecycle

Every configured repository starts in **discovery** mode. Normal sync records
items it encounters, while the archive inventory cheaply enumerates remaining
historical issues and pull requests oldest-first so there is one exhaustive
historical crawler. Discovery does not fetch discussion or review data solely
for the archive and never claims archive completeness.

`middleman archive start` promotes repositories to **full** mode. Inventory
continues, and only supported datasets not already proven complete by shared
ingestion become pending hydration. Start and pause are idempotent; a
multi-repository start is validated all-or-nothing before any state changes.

Configuration changes follow the config file:

- newly configured repositories enter discovery mode;
- removing a repository pauses its archive work but keeps archived content
  and reportability;
- re-adding the same full identity `(provider, host, owner, name)` resumes
  the prior state;
- credential changes let blocked work retry on the next eligible cycle.

Daemon restarts are safe at any point: inventory cursors commit atomically
with the items of each page, staged dataset pages resume from durable cursors,
and shared publication either commits the complete current-revision dataset
with its archive progress or leaves both untouched.

## Status and coverage

Repository archive status is derived from durable state:

- **current** — inventory is exhausted, every supported dataset of every
  discovered item is mirrored, and no work is failed or retry-due.
- **partial** — a required dataset is unsupported by the provider, or one or
  more items are terminally blocked. Unsupported capabilities are reported
  explicitly; they are never silently counted as complete.
- **running / waiting_for_budget / paused / blocked** — operational states
  with sanitized error details and retry times where applicable.

Completeness is always scoped to the exact repository identity
`(provider, platform_host, owner, name)`. Contributor identity in reports is
likewise the provider login scoped by provider and host; equal login strings
from different hosts are never merged.

A provider that cannot enumerate a required dataset yields explicit partial
coverage. GitLab merge-request approvals are not historical review
submissions, so GitLab archives report the submitted-review dataset as
unsupported unless its API can return historical approval actions with
stable identity, actor, and timestamp.

## Maintenance

After the initial backfill, a **prompt** selector keeps a full archive fresh
within the same budget. It periodically enumerates items updated since an
overlapped watermark and queues unresolved current-revision datasets. It uses
the same canonical page readers and ingestion path as regular sync; it does
not run a parallel refresh implementation.

A complete regular-sync refresh may satisfy that queued archive work
atomically. Archive hydration therefore skips a provider request when shared
ingestion has already completed the current revision. Ordinary comments are
replaced per item on refresh, while reviews and review threads are written
additively. Child edits or deletions that never touch the parent item's
timestamp are picked up the next time the item is selected; there is no
freshness SLA.

## Removed and moved items

Item lookups distinguish present, removed, moved, and inaccessible outcomes,
and a bare 404 is never treated as authoritative deletion while repository
access itself is in doubt.

An upstream deletion or transfer marks the item's archive work row terminal
(removed upstream or inaccessible) so it stops consuming hydration work and
does not block archive completeness. The already-mirrored local content —
the item row, comments, reviews, labels, and any local middleman state —
is kept as-is; upstream deletions linger locally until the row is removed
by other means. Transfers are not followed: the source becomes removed
upstream, and the destination repository's own archive discovers the item
if it is configured.

## Rate limits and scheduling

All provider reads pass through the same provider-host priority and budget
coordinator. Archive traffic shares the host's `sync_budget_per_hour` bucket,
provider-reported rate reserve, credentials, and concurrency limits with
normal background sync; there is no separate archive token chain or admission
framework, and mutations are unaffected. The coordinator preserves the hard
live floor and time-releases surplus archive capacity more aggressively as the
observed provider reset approaches. Every archive request still yields to
interactive, open, watched, notification, and current-index work. Manually
starting an archive wakes its durable work selector but never bypasses shared
priority, live floor, release ceiling, provider reserve, or backoff; exhaustion
is a normal `waiting_for_budget` state, not a failure.

## Reports

Reports are computed from SQLite only and never trigger provider requests.
The daemon must be running; the CLI is a thin client of its API.

- Ranges are half-open UTC intervals. `--days N` subtracts exact 24-hour
  periods from now; date-only values are UTC calendar boundaries with an
  inclusive user-supplied end date.
- Activity is attributed by event time: items and comments by creation,
  reviews by submission. Edits do not create activity.
- Each report is built in one read-only transaction, so coverage, totals,
  and details observe a single database snapshot.
- Output is deterministic: identical query input and database state produce
  byte-identical Markdown and JSON, with stable tie-breakers and no
  generation timestamps.
- Summary reports are unbounded. Detailed reports stop at 10,000 records or
  32 MiB of stored title/body text and report the overage; narrow the range
  or repository scope to proceed.
