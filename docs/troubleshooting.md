# Troubleshooting

## The UI does not open

Check the daemon:

```sh
kenn-forge daemon status
```

If another kenn-forge process uses the same `data_dir`, the startup
banner shows the existing daemon. Use the reported URL instead of starting a
second daemon with the same data directory.

## The port is busy

Change the port in config:

```toml
port = 8092
```

or start with another config:

```sh
kenn-forge serve --config /path/to/config.toml
```

## Config edits are not showing up

Most config loads at startup. Restart the daemon after editing `config.toml`.

```sh
kenn-forge daemon restart
```

If you need isolated state for a test run, set `KENN_FORGE_HOME` before starting
kenn-forge.

## Repositories do not sync

Check these in order:

1. The repository exists in `[[repos]]` or Settings.
2. `platform` and `platform_host` match the provider host.
3. The token env var or token file is present in the daemon environment.
4. The token has read access to repository metadata, PRs/MRs, issues, comments,
   commits, tags, releases, and CI/status data.
5. The provider rate limit is not exhausted.

For GitHub, `gh auth token --hostname HOST` can supply the token when the
configured token source is absent. The unscoped `gh auth token` fallback applies
only to `github.com`; run `gh auth login --hostname HOST` for another host.
With `[[github_owner_tokens]]`, confirm the entered owner matches the mapping
exactly after case folding and restart after changing the PAT to one issued by
a different GitHub user. A missing owner route reports the GitHub host and owner
without exposing token material.

## Mutating actions are disabled

Actions such as approve, merge, close, reopen, or comment require both provider
support and token permission. If the provider does not support an action,
kenn-forge reports an unsupported capability instead of trying a GitHub-specific
fallback.

## GitHub sync hits rate limits

Use a GitHub App for sync reads:

```sh
kenn-forge-github-app create
kenn-forge-github-app install
kenn-forge-github-app list
```

Mutating actions still use the user credential chain so comments, approvals, and
merges are attributed to you. Multiple PAT entries issued to the same GitHub
user do not create additional capacity: they share one rate limit and one
kenn-forge sync budget. Distinct users and App installations have separate
identity-scoped budgets.

If App-backed reads work but mutations or notifications are disabled, restart
kenn-forge after adding the user PAT. App-only routes intentionally remain
read-only until startup establishes a stable write identity.

## A repository feature stays unavailable

When a provider definitively reports that issues or pull requests are disabled,
kenn-forge cools that repository feature down for 24 hours instead of retrying a
permanent failure every sync. Other repository data continues syncing. Use an
explicit repository sync after re-enabling the feature to bypass the cooldown
and clear it on success.

## An issue workspace directory already exists

If an issue workspace row was lost but its expected kenn-forge worktree is still
on disk, choose **Use Existing Directory** in the branch-conflict dialog. This
only re-registers the deterministic kenn-forge-managed directory after verifying
its repository and branch. It does not reset the branch, clean files, or remove
untracked work. Use **Use Existing Branch** only when the branch is not already
checked out in that directory.

## Docs mode has no folders

Register at least one folder:

```sh
kenn-forge docs add-folder --name Docs ~/docs
```

Then enable the mode if it is hidden:

```toml
[modes]
docs = true
```

## Kata actions show no daemons

kenn-forge does not store Kata daemon definitions. Check Kata's own config:

```text
~/.kata/config.toml
```

or set `KATA_HOME` before starting kenn-forge.

## The database will not migrate

kenn-forge stores synced data in:

```text
~/.kenn/forge/forge.db
```

If startup reports a dirty failed migration, stop kenn-forge, make a backup copy,
then move `forge.db` and any `forge.db-wal` or `forge.db-shm`
sidecars out of the data directory before starting again. Provider data will
sync again from a fresh database, but local-only state such as stars, PR
workflow statuses, and workspace links is only available in the saved copy.

If startup reports that the database is newer than the binary, upgrade Kenn
Forge.

## Need more logs

Set log environment variables before starting the daemon:

```sh
KENN_FORGE_LOG_LEVEL=debug kenn-forge daemon restart
KENN_FORGE_LOG_FILE=~/.kenn/forge/forge.log kenn-forge daemon restart
KENN_FORGE_LOG_STDERR_LEVEL=warn KENN_FORGE_LOG_FILE=~/.kenn/forge/forge.log kenn-forge daemon restart
```

Logs redact configured token-shaped values.
