# Troubleshooting

## The UI does not open

Check the daemon:

```sh
middleman status
```

If another middleman is already running on the same `data_dir`, the startup
banner shows the existing daemon. Use the reported URL instead of starting a
second daemon with the same data directory.

## The port is busy

Change the port in config:

```toml
port = 8092
```

or start with another config:

```sh
middleman serve -config /path/to/config.toml
```

## Config edits are not showing up

Most config is read at startup. Restart the daemon after editing
`config.toml`.

If you need isolated state for a test run, set `MIDDLEMAN_HOME` before starting
middleman.

## Repositories do not sync

Check these in order:

1. The repository exists in `[[repos]]` or Settings.
2. `platform` and `platform_host` match the provider host.
3. The token env var or token file is present in the daemon environment.
4. The token has read access to repository metadata, PRs/MRs, issues, comments,
   commits, tags, releases, and CI/status data.
5. The provider rate limit is not exhausted.

For GitHub, `gh auth token` can supply the token when `MIDDLEMAN_GITHUB_TOKEN`
is not set.

## Mutating actions are disabled

Actions such as approve, merge, close, reopen, or comment require both provider
support and token permission. If the provider does not support an action,
middleman reports an unsupported capability instead of trying a GitHub-specific
fallback.

## GitHub sync hits rate limits

Use a GitHub App for sync reads:

```sh
middleman-github-app create
middleman-github-app install
middleman-github-app list
```

Mutating actions still use the user credential chain so comments, approvals, and
merges are attributed to you.

## Docs mode has no folders

Register at least one folder:

```sh
middleman docs add-folder --name Docs ~/docs
```

Then enable the mode if it is hidden:

```toml
[modes]
docs = true
```

## Kata mode has no daemons

middleman does not store Kata daemon definitions. Check Kata's own config:

```text
~/.kata/config.toml
```

or set `KATA_HOME` before starting middleman.

## Messages mode is unavailable

Messages requires both mode visibility and msgvault config:

```toml
[modes]
messages = true

[msgvault]
url = "http://127.0.0.1:8080"
api_key_env = "MSGVAULT_API_KEY"
```

The API key environment variable must be set in the daemon environment.

## The database will not migrate

middleman stores synced data in:

```text
~/.config/middleman/middleman.db
```

If startup reports a dirty failed migration, stop middleman, make a backup copy,
then move `middleman.db` and any `middleman.db-wal` or `middleman.db-shm`
sidecars out of the data directory before starting again. Provider data will
sync again from a fresh database, but local-only state such as stars, kanban
columns, and workspace links is only available in the saved copy.

If startup reports that the database is newer than the binary, upgrade
middleman.

## Need more logs

Set log environment variables before starting the daemon:

```sh
MIDDLEMAN_LOG_LEVEL=debug middleman
MIDDLEMAN_LOG_FILE=~/.config/middleman/middleman.log middleman
MIDDLEMAN_LOG_STDERR_LEVEL=warn MIDDLEMAN_LOG_FILE=~/.config/middleman/middleman.log middleman
```

Logs redact configured token-shaped values.
