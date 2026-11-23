# Troubleshooting

## The UI does not open

Check whether the Kenn Forge daemon is running:

```sh
kenn-forge daemon status
```

If another Forge process is already using the same `data_dir`, the startup banner prints the existing daemon's address under `listening on`. Connect to that address instead of starting a second daemon against the same data directory.

## The port is busy

Set a different port in `config.toml`:

```toml
port = 8092
```

Alternatively, start Forge with another configuration file:

```sh
kenn-forge serve --config /path/to/config.toml
```

## Config edits are not showing up

Forge watches `config.toml` and applies changes to repository, activity, terminal, agent, and docs settings while it runs. Startup settings, such as the listen address and port, base path, sync interval, data directory, and provider hosts, take effect only after you restart the daemon. Changing scoped GitHub credential routes, such as `[[github_owner_tokens]]`, or adding or removing a GitHub App also requires a restart:

```sh
kenn-forge daemon restart
```

If you need isolated state for a test run, set `KENN_FORGE_HOME` before starting Forge.

## Repositories do not sync

Walk through these checks in order:

1. Confirm the repository is listed in `[[repos]]` or **Settings**.
2. Confirm `platform` and `platform_host` match the provider host.
3. Confirm the token environment variable or token file is available in the daemon environment.
4. Confirm the token has read access to repository metadata, pull requests or merge requests, issues, comments, commits, tags, releases, and CI or status data.
5. Confirm that neither the [local sync ceiling](#local-sync-ceiling-reached) nor the provider rate limit is exhausted.

When the configured token source is absent, Forge can pull a token from your provider CLI: `gh auth token --hostname HOST` for GitHub, the `glab` credential for the host on GitLab, and the `fj` keys file entry for the host on Forgejo or Gitea. The unscoped `gh auth token` fallback applies only to `github.com`. To log in to another host, run `gh auth login --hostname HOST`, `glab auth login --hostname HOST`, or `fj --host HOST auth login`. Forge skips expired `fj` OAuth logins until any `fj` command refreshes them.

If you use `[[github_owner_tokens]]`, confirm that the entered owner matches the mapping exactly after case folding, and restart Forge after switching to a PAT issued by a different GitHub user. When an owner route is missing, Forge reports the GitHub host and owner without exposing token material.

## Local sync ceiling reached

Seeing `local sync ceiling reached (500 / 500)` in the status bar means Forge has spent its local hourly sync allowance. Sync requests that need more of that allowance must wait, even though the provider may still have quota available.

Forge spreads watched pull request refreshes and detail fetches across the hour, so it can pause while part of the local ceiling is still unspent. Work resumes as the hour progresses. To inspect how the current window is spending its budget by kind of sync work, run `kenn-forge rate-limits`.

To give a large or active repository more capacity:

1. Open **Settings > Sync**.
2. Raise **Hourly sync budget**, for example to 3,000 requests per hour.
3. Confirm that the local ceiling now shows a limit of 3,000. The new limit applies immediately and Forge starts a sync.

You can set the same limit with `sync_budget_per_hour` in `config.toml` (see [Sync budget](configuration.md#sync-budget)), or wait for the local hourly window to reset. Clicking sync again does not clear the spent allowance.

Raise the limit only while the provider has quota to spare. If the provider quota is exhausted too, wait for it to reset or configure a [GitHub App for sync reads](configuration.md#github-app-reads). See [Sync budget](configuration.md#sync-budget) for the default, shared budgets, and the capacity Forge reserves for discovering issues and pull requests.

## Mutating actions are disabled

Actions such as approve, merge, close, reopen, and comment require both provider support and token permission. If your provider does not support an action, Forge reports an unsupported capability instead of attempting a GitHub-specific fallback.

## GitHub sync hits rate limits

For more GitHub API capacity, follow the advanced [GitHub App setup](configuration.md#github-app-reads). App installations have their own quota, and organization installations can qualify for higher rate limits. The setup guide covers both organization and personal ownership, and App access applies only to the installed repositories. Because organization setup requires owner access or delegated App permissions, you may need an administrator's help.

Forge's [local sync ceiling](#local-sync-ceiling-reached) still applies to ordinary sync. If the App has quota left while sync stops at the local limit, raise the local ceiling.

If ordinary sync is healthy while historical archive work competes for the same installation budget:

1. Create a separate archive App with `kenn-forge github-app create --role archive`.
2. Install the App on the repository account.
3. Restart Forge.

Run `kenn-forge github-app list` to check each App's role and independent rate-limit state, and see [Archive sync capacity](archive.md#sync-capacity) for how provider quota and reserves control historical work.

Mutating actions still use your user credential chain so comments, approvals, and merges are attributed to you. Multiple PAT entries issued to the same GitHub user do not add capacity. They share one rate limit and one Forge sync budget. Distinct users and App installations each get separate identity-scoped budgets.

If App-backed reads work while mutations or notifications stay disabled, add your user PAT and restart Forge. App-only routes intentionally stay read-only until startup establishes a stable write identity.

## A repository feature stays unavailable

When a provider definitively reports that issues or pull requests are disabled, Forge puts that repository feature on a 24-hour cooldown instead of retrying a permanent failure on every sync. Other repository data continues syncing. After you re-enable the feature, run an explicit repository sync to bypass the cooldown and clear it on success.

## An issue workspace directory already exists

If an issue workspace row is missing while its expected Forge worktree is still on disk, select **Use Existing Directory** in the branch-conflict dialog. That option re-registers the deterministic Forge-managed directory after verifying its repository and branch, without resetting the branch, cleaning files, or removing untracked work. Select **Use Existing Branch** only when the branch is not already checked out in that directory.

## Docs mode has no folders

1. Register at least one folder:

   ```sh
   kenn-forge docs add-folder --name Docs ~/docs
   ```

2. If Docs mode is hidden, enable it in `config.toml`:

   ```toml
   [modes]
   docs = true
   ```

## Kata actions show no daemons

Forge does not store Kata daemon definitions. Check Kata's own configuration file at `~/.kata/config.toml`, or set `KATA_HOME` before starting Forge.

The catalog entry must be connected and report a supported Kata API schema. If a daemon is incompatible, the UI keeps it visible and tells you whether Kata or Forge needs an upgrade.

## A Kata workspace has no repository

Open **Settings → Kata mappings** to check the effective mappings table, which shows how each project resolves and whether Forge found an automatic match. If a project has no repository or points to the wrong one, add a manual override.

Select an exact repository identity from the list available in Settings. That list can include repositories found through configured patterns, tracked repositories, and registered projects. Glob expressions are not valid mapping targets.

## The Roborev daemon is not reachable

Forge does not start Roborev for you. Confirm the daemon is running, then check its status endpoint:

```sh
curl http://127.0.0.1:7373/api/status
```

If Roborev listens on another address, update the endpoint in `config.toml` and restart Forge:

```toml
[roborev]
endpoint = "http://127.0.0.1:17373"
```

To use **Initialize Roborev in managed clones**, you need `roborev` on `PATH`, a running daemon, and a loopback HTTP endpoint. If initialization fails, Forge logs `workspace Roborev setup incomplete`, restores the previous hooks, and still opens the workspace.

After you fix the reported issue, retry initialization from the workspace terminal with the endpoint configured in Forge:

```sh
roborev --server http://127.0.0.1:7373 init --no-daemon
```

## The database will not migrate

Forge stores synced data in `~/.kenn/forge/forge.db`.

If startup reports a dirty failed migration:

1. Stop Forge.
2. Make a backup copy.
3. Move `forge.db` and any `forge.db-wal` or `forge.db-shm` sidecars out of the data directory.
4. Start Forge again.

Provider data syncs again into the fresh database, while local-only state such as stars and workspace links stays only in your backup copy.

If startup reports that the database is newer than the binary, upgrade Forge.

## Need more logs

Set log environment variables before starting the daemon:

```sh
KENN_FORGE_LOG_LEVEL=debug kenn-forge daemon restart
KENN_FORGE_LOG_FILE=~/.kenn/forge/forge.log kenn-forge daemon restart
KENN_FORGE_LOG_STDERR_LEVEL=warn KENN_FORGE_LOG_FILE=~/.kenn/forge/forge.log kenn-forge daemon restart
```

Forge redacts configured token-shaped values from log output.
With `KENN_FORGE_DESKTOP=1`, Forge appends to `<data_dir>/forge.log` after loading config unless `KENN_FORGE_LOG_FILE` specifies another path; Forge does not rotate the file.
