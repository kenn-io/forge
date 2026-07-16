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

## Collection modes and lifecycle

Every configured repository starts in **discovery** mode: middleman cheaply
enumerates historical issues and pull requests oldest-first so they exist as
rows, without fetching their discussion or review data. Discovery mode never
claims archive completeness.

`middleman archive start` promotes repositories to **full** mode: everything
already discovered becomes pending hydration, and inventory plus hydration
proceed until the archive is complete. Start and pause are idempotent; a
multi-repository start is validated all-or-nothing before any state changes.

Configuration changes follow the config file:

- newly configured repositories enter discovery mode;
- removing a repository pauses its archive work but keeps archived content
  and reportability;
- re-adding the same full identity `(provider, host, owner, name)` resumes
  the prior state;
- credential changes let blocked work retry on the next eligible cycle.

Daemon restarts are safe at any point: inventory cursors commit atomically
with the items of each page, and a hydrated item is either completely
mirrored or untouched.

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

After the initial backfill, a **prompt** stream keeps a full archive fresh
within the same budget: it periodically enumerates items updated since an
overlapped watermark and rehydrates them. Archive writes use the same plain
persistence as normal sync — parent snapshots apply under the provider
`updated_at` monotonic guard, ordinary comments are replaced per item on
refresh, and reviews and review threads are written additively. Child edits
or deletions that never touch the parent item's timestamp are picked up the
next time the item is rehydrated; there is no freshness SLA.

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

Archive traffic shares each provider host's `sync_budget_per_hour` bucket
and provider-reported rate reserve with normal background sync; there is no
separate archive quota or token chain, and mutations are unaffected. Archive
work always yields to index sync, notification refresh, and open-item detail
refresh. Manually starting an archive wakes the scheduler but never bypasses
budget or backoff; exhaustion is a normal `waiting_for_budget` state, not a
failure.

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
