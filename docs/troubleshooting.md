# Troubleshooting

## The UI does not open

Check the daemon:

```sh
kenn-forge daemon status
```

If another Kenn Forge process uses the same `data_dir`, the startup
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
Forge.

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
Forge reports an unsupported capability instead of trying a GitHub-specific
fallback.

## GitHub sync hits rate limits

Use a GitHub App for sync reads:

```sh
kenn-forge-github-app create
kenn-forge-github-app install
kenn-forge-github-app list
```

If ordinary sync is healthy but historical archive work is competing for the
same installation budget, add a separate App with
`kenn-forge-github-app create --role archive`, install it on the repository
account, and restart Forge. `kenn-forge-github-app list` shows each App's
role and independent rate-limit state.

Mutating actions still use the user credential chain so comments, approvals, and
merges are attributed to you. Multiple PAT entries issued to the same GitHub
user do not create additional capacity: they share one rate limit and one
Forge sync budget. Distinct users and App installations have separate
identity-scoped budgets.

If App-backed reads work but mutations or notifications are disabled, restart
Forge after adding the user PAT. App-only routes intentionally remain
read-only until startup establishes a stable write identity.

## A repository feature stays unavailable

When a provider definitively reports that issues or pull requests are disabled,
Forge cools that repository feature down for 24 hours instead of retrying a
permanent failure every sync. Other repository data continues syncing. Use an
explicit repository sync after re-enabling the feature to bypass the cooldown
and clear it on success.

## An issue workspace directory already exists

If an issue workspace row was lost but its expected Forge worktree is still
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

Forge does not store Kata daemon definitions. Check Kata's own config:

```text
~/.kata/config.toml
```

or set `KATA_HOME` before starting Forge.

The catalog entry must be connected and report a supported Kata API schema.
The UI keeps an incompatible daemon visible and explains whether Kata or
Forge needs an upgrade.

## A Kata workspace has no repository

Open **Settings → Kata mappings**. The effective mappings table shows how each
project resolves and whether Forge found an automatic match. Add a manual
override for any project with no repository or the wrong repository.

Choose an exact repository identity available in Settings. The list can include
repositories found through configured patterns, tracked repositories, and
registered projects. A glob expression is not a valid mapping target.

## The Roborev daemon is not reachable

Forge does not start Roborev. Confirm the daemon is running, then check its
status endpoint:

```sh
curl http://127.0.0.1:7373/api/status
```

If Roborev listens elsewhere, update the endpoint and restart Forge:

```toml
[roborev]
endpoint = "http://127.0.0.1:17373"
```

The Reviews page shows the configured scheme and host when the connection
fails. It does not expose credentials or a path from the configured URL.

If workspace setup fails in the `repository_hooks` stage, check the error in
the workspace setup history. Enabling **Initialize Roborev in managed clones**
requires `roborev` on `PATH`, a running daemon, and a loopback HTTP endpoint.
Fix the reported issue and retry the workspace; Forge keeps the created
worktree but waits to start the terminal until hook setup succeeds.

## The database will not migrate

Forge stores synced data in:

```text
~/.kenn/forge/forge.db
```

If startup reports a dirty failed migration, stop Forge, make a backup copy,
then move `forge.db` and any `forge.db-wal` or `forge.db-shm`
sidecars out of the data directory before starting again. Provider data will
sync again from a fresh database, but local-only state such as stars, PR
workflow statuses, and workspace links is only available in the saved copy.

If startup reports that the database is newer than the binary, upgrade
Forge.

## Need more logs

Set log environment variables before starting the daemon:

```sh
KENN_FORGE_LOG_LEVEL=debug kenn-forge daemon restart
KENN_FORGE_LOG_FILE=~/.kenn/forge/forge.log kenn-forge daemon restart
KENN_FORGE_LOG_STDERR_LEVEL=warn KENN_FORGE_LOG_FILE=~/.kenn/forge/forge.log kenn-forge daemon restart
```

Logs redact configured token-shaped values.
