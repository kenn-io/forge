# Historical activity archive

Kenn Forge can backfill historical provider activity into its local SQLite database and keep it fresh under the normal sync budget. Reports read only local data, so they remain deterministic and do not spend provider requests.

For command syntax, see [Commands](commands.md#historical-activity-archive).

## What is archived

A full archive inventories supported historical issues and pull or merge requests, then schedules each item through the same detail-sync path used by normal sync. Comments, reviews, inline threads, and other supported detail are therefore normalized and persisted by one canonical implementation.

The archive mirrors current provider truth. It does not retain deleted content, previous edit versions, commits, CI history, releases, labels, assignments, or other lifecycle events.

Provider capabilities determine coverage. Unsupported historical inventory is reported explicitly as partial coverage. For example, GitLab historical merge-request inventory is currently unsupported because its offset pagination cannot guarantee a complete traversal across equal timestamps; GitLab issue history and updated-item maintenance remain available.

## Workflow

Every configured repository begins in discovery mode. Discovery inventories supported historical item identities without hydrating each item.

Use `kenn-forge archive start` to promote selected repositories, or all configured repositories, to full archival. Use `kenn-forge archive pause` to stop new archive work without deleting data or progress. Start and pause are idempotent.

Archive work is incremental and resumable across daemon restarts. Inventory pages record item identities and durable cursors; admitted item work then invokes normal sync and records the archive outcome. A failed or interrupted operation is retried from durable progress while unrelated repositories continue independently.

There is no manual cursor-reset command. Invalid cursors, provider page limits, repository access loss, and contract violations block only the affected scope and remain visible in status rather than silently restarting potentially unbounded work.

## Status and coverage

`kenn-forge archive status` reports repository-scoped progress using the full
provider identity. Use `--repo-id` to inspect a retained historical repository
incarnation that no longer owns its former route. Common states are:

- **current** — supported inventory and item hydration are complete;
- **partial** — one or more inventory scopes or items are unsupported or terminally blocked;
- **running** — archive work is eligible or in progress;
- **waiting_for_budget** — live-work reserves or provider limits currently prevent archive requests;
- **paused** — the operator or configuration has paused archive work;
- **blocked** — a scoped provider, cursor, access, or contract error requires attention.

Error details are sanitized. Removing a repository from configuration pauses its archive state while keeping archived content and reports; re-adding the same provider and host identity resumes it.

## Reports

`kenn-forge archive report` reads SQLite for a UTC time range and optional
repository filters. Route filters select the current repository at a path;
`--repo-id` selects one immutable repository incarnation, including retained
history after route reuse. Summary output aggregates activity and contributors.
Verbose output includes individual records. JSON output is available for
automation.

Reports reflect supported coverage. Check archive status before treating a report as complete.

## Scheduling and provider budget

Live maintainer workflows always outrank archive traffic. Archive work uses bounded requests, releases provider admission before database work, and spends only surplus capacity above the live reserve. If safe surplus is unavailable, archive work waits while normal sync continues.
