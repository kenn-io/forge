# Commands

Run `kenn-forge` or `kenn-forge --help` for the complete command tree. Flags
are scoped to the commands that use them; check command help before scripting.

## Manage the daemon

```sh
kenn-forge daemon start
kenn-forge daemon status
kenn-forge daemon status --json
kenn-forge daemon restart
kenn-forge daemon stop
```

`daemon start` and `daemon stop` are idempotent. Start returns after the daemon
publishes its ready identity and safely reuses a compatible running process.
Use `daemon restart` after changing startup configuration or replacing the
binary.

On Unix, stop sends SIGTERM once. If the daemon does not exit within 15 seconds,
the command leaves it running and directs you to inspect and terminate it
manually rather than risking a force-kill of a reused PID.

For foreground development or diagnosis, use:

```sh
kenn-forge serve
kenn-forge serve --config /path/to/config.toml
```

Foreground starts reject a second process using the same data directory.

## Check build information

```sh
kenn-forge version
kenn-forge version --json
```

The JSON object contains `name`, `version`, `commit`, and `buildDate`.
`buildDate` is a UTC RFC3339 timestamp or `unknown` for an uninjected build.
This command does not read config or connect to a daemon.

## Query and sync data

The CLI includes commands for `activity`, `issues`, `pulls`, `repos`,
`repo-summaries`, `stacks`, `workspaces`, `rate-limits`, and `sync`. Use the
command help before scripting an output shape:

```sh
kenn-forge pulls --help
kenn-forge sync --help
```

## Relay an API request

```sh
kenn-forge api list
kenn-forge api GET /api/v1/version
kenn-forge api POST /api/v1/sync
kenn-forge api -i GET /api/v1/pulls
```

`kenn-forge api` discovers the selected daemon and supplies its local
credential. Use `--data @-` to read a request body from stdin. Exit status 0
means 2xx, 1 means another HTTP status, and 2 means no request was made.

## Manage historical archives

```sh
kenn-forge archive start --all
kenn-forge archive start --repo 'github|github.com/owner/repo'
kenn-forge archive status --json
kenn-forge archive pause --all
kenn-forge archive report --days 7
kenn-forge archive report --start 2026-07-01 --end 2026-07-07 --verbose
```

Use repeated `--repo` flags for multiple repositories. Reports default to
Markdown. Add `--format json` or `--output PATH` when needed.

See [Historical activity archive](archive.md) for coverage and status rules.

## Read configuration

```sh
kenn-forge config read port
kenn-forge config read --config /path/to/config.toml port
```

Use Settings or edit TOML for normal configuration changes.

## Manage Docs folders

```sh
kenn-forge docs list-folders
kenn-forge docs add-folder --name Docs ~/docs
kenn-forge docs add-folder --id project --daemon kata-main ~/project-docs
kenn-forge docs remove-folder project
```

These commands manage `[[doc_folders]]` in the selected config file.

## Install agent activity hooks

```sh
kenn-forge agent-hook install
kenn-forge agent-hook install --agent codex
kenn-forge agent-hook uninstall
kenn-forge agent-hook uninstall --agent codex
```

Without `--agent`, install or remove every supported integration. Installed
hooks send lifecycle activity to the running daemon. Codex asks you to review
the installed commands through `/hooks` once.

## Manage GitHub App credentials

```sh
kenn-forge-github-app create
kenn-forge-github-app list
kenn-forge-github-app install
kenn-forge-github-app uninstall
kenn-forge-github-app delete
kenn-forge-github-app open
```

Use this companion CLI when sync reads should use GitHub App installation
tokens. Comments, reviews, state changes, and merges still use the user PAT
chain.
