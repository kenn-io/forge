# Historical activity archive

You can use Kenn Forge to backfill provider activity into its local SQLite database and run local reports on it. Archive work runs alongside live sync and spends only spare provider capacity, so it keeps a reserve for live work without waiting for sync to go idle. Reports read only local data and do not spend provider requests.

## Sync capacity

On GitHub, archive sync uses spare REST and GraphQL quota from the credential that reads the repository. Forge reserves 20% of each API's quota for live work, with a minimum reserve of 200, and spreads the rest across the quota window. An archive item starts only when the API's remaining quota sits above a floor that begins at the API's limit and drops evenly to the reserve at reset. Halfway through a 5,000-request window, that floor is 3,000. New items start while more than that remains, and wait otherwise. As the window passes, quota that live sync leaves unspent becomes archive capacity, and any item already underway runs to completion as long as the reserve holds.

Archive requests run alongside sync passes and live refreshes without waiting for live sync, so a busy instance keeps archiving.

Requests backed by this provider quota do not draw from your local `sync_budget_per_hour` allowance, and they can keep running even after that local ceiling runs out. Raising the local ceiling does not add provider quota. If GitHub's required quota data is unknown or expired, archive work waits for fresh data.

Other archive paths, including Forgejo and Gitea hosts that do not report rate limits, draw from your spare [local sync budget](configuration.md#sync-budget). They share that allowance with live sync and keep capacity in reserve for live work.

If you run a busy GitHub archive, you can configure a [separate archive App](configuration.md#github-app-reads) to give covered repositories their own installation quota. It must be a distinct App from the one you use for ordinary sync.

## Coverage

Full archival collects supported historical issues, pull requests or merge requests, comments, reviews, and inline threads. The archive mirrors current provider state, and it does not retain deleted content, previous edits, commits, CI history, releases, labels, assignments, or unrelated lifecycle events.

What Forge archives depends on what each provider supports, and any unsupported inventory appears as partial coverage. GitLab merge-request history is currently partial because its pagination cannot guarantee a complete traversal across equal timestamps, while GitLab issue history and updated-item maintenance remain available.

## Start

Every configured repository begins in discovery mode, which records item identities without loading every detail.

To promote one repository or all configured repositories to full archival, run:

```sh
kenn-forge archive start --repo 'github|github.com/owner/repo'
kenn-forge archive start --all
```

Archive progress survives daemon restarts, and each repository advances independently.

## Monitor

Check archive progress for your repositories with:

```sh
kenn-forge archive status --json
```

Each repository's `last_progress_at` field records its latest committed inventory page or completed item lookup, including terminal lookups. It stays empty until the first committed page or completed lookup after an upgrade, and blocks, retries, and restarts leave it unchanged.

Common states include:

- **current.** Supported inventory and item details are complete.
- **partial.** Completed archival has unsupported or inaccessible coverage.
- **running.** Archive work is ready or in progress.
- **waiting_for_budget.** Live work or provider limits consume the safe budget.
- **paused.** Configuration or an operator paused new work.
- **blocked.** Work cannot advance until a provider, cursor, access, or contract error receives attention.

Error messages identify the affected scope without exposing credentials. If you remove a repository, Forge pauses its archive state while keeping its data and progress. Adding the same provider and host identity back resumes it.

## Pause and resume

To stop new archive work without deleting stored data, pause all repositories or a single one:

```sh
kenn-forge archive pause --all
kenn-forge archive pause --repo 'github|github.com/owner/repo'
```

Both `start` and `pause` are idempotent, and starting the same scope resumes your durable progress.

Forge has no manual cursor reset. Invalid cursors, page limits, access loss, and contract errors block only the affected scope.

## Report

Generate a summary of archived activity for a rolling window or a specific date range:

```sh
kenn-forge archive report --days 7
kenn-forge archive report --start 2026-07-01 --end 2026-07-07 --verbose
```

All report ranges use UTC. The `--days` flag selects rolling 24-hour periods, date-only ranges include both dates, and RFC3339 ranges use an inclusive start and an exclusive end.

Reports output Markdown by default. For automation, pass `--format json`, `--output PATH`, and repeated `--repo` filters. Check archive status before treating a report as complete.

For a short syntax reference, see [Commands](commands.md#manage-historical-archives).
