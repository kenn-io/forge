# Commands

## Serve the app

```sh
middleman
middleman serve
middleman serve -config /path/to/config.toml
```

Without a subcommand, `middleman` starts the daemon and web UI.

Use the idempotent background lifecycle when a caller needs to ensure the app
is available without treating an existing compatible process as an error:

```sh
middleman start --background
middleman start --background --config /path/to/config.toml
```

The command waits for the ready daemon identity before returning. Direct
foreground starts retain their duplicate-process error behavior. Background
startup requires a loopback listener. It verifies the recorded endpoint with a
credential-free challenge before reusing the process, including when general
API authentication is disabled.

## Version

```sh
middleman version
middleman version --json
```

Prints the version, commit, and build date. Use `--json` for fleet inventory
and other integrations. The JSON contract is a single object written to stdout:

| Field | Type | Description |
| --- | --- | --- |
| `name` | string | Canonical tool name, always `middleman` |
| `version` | string | Semantic version for a release build; development identifier otherwise |
| `commit` | string | Build commit identifier |
| `buildDate` | string | UTC RFC3339 timestamp for a release build; `unknown` when not injected |

The command does not read configuration, connect to a daemon, or require a
workspace.

## Status

```sh
middleman status
middleman status -json
middleman status -config /path/to/config.toml
```

Reports whether a middleman daemon is running.

## API relay

```sh
middleman api GET /api/v1/pulls
middleman api POST /api/v1/sync
middleman api GET /api/v1/version
```

`middleman api` discovers the running daemon from the selected config and
relays one request. Use `-i` to include the HTTP status line, `--timeout` to
bound the request, and `--config` when the daemon uses another config file.
Non-2xx responses return a distinct failure exit code while preserving the
response body.

## Historical activity archive

```sh
middleman archive start --all
middleman archive start --repo 'github|github.com/owner/repo'
middleman archive status --json
middleman archive pause --all
middleman archive report --days 7
middleman archive report --start 2026-07-01 --end 2026-07-07 --verbose
```

Archive collection runs in the background within each provider host's normal
sync budget. Status reports `current`, `partial`, or blocked work honestly; a
provider that cannot supply a required dataset remains partial rather than
being treated as complete.

Reports use only middleman's local archive, so they make no provider requests,
but the middleman daemon must be running. `--days` uses rolling 24-hour UTC
periods. Date-only ranges include both named dates; RFC3339 ranges use an
inclusive start and exclusive end. Reports default to Markdown; use
`--format json`, `--output PATH`, and repeated fully qualified `--repo` filters
as needed.

Starting from an existing middleman database discovers historical issues, pull
or merge requests, comments, and supported review activity. No import from an
older standalone archive is required.

## Config

```sh
middleman config read port
middleman config read -config /path/to/config.toml port
```

The current CLI exposes a small read surface. Use the Settings UI or edit the
TOML file for normal configuration changes.

## Docs folders

```sh
middleman docs list-folders
middleman docs add-folder --name Docs ~/docs
middleman docs add-folder --id project --daemon kata-main ~/project-docs
middleman docs remove-folder project
```

These commands manage `[[doc_folders]]` in the config file.

## Agent activity hooks

```sh
middleman agent-hook install
middleman agent-hook install --agent codex
middleman agent-hook uninstall
middleman agent-hook uninstall --agent codex
```

With no `--agent`, install or remove every supported integration. Installed
hooks forward lifecycle activity to the running daemon; Codex asks for one
manual review of the installed commands through `/hooks`.

## GitHub App credentials

```sh
middleman-github-app create
middleman-github-app list
middleman-github-app install
middleman-github-app uninstall
middleman-github-app delete
middleman-github-app open
```

Use this companion CLI when you want middleman sync reads to use GitHub App
installation tokens instead of your personal access token rate limit.
