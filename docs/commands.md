# Commands

## Serve the app

```sh
kenn-forge
kenn-forge serve
kenn-forge serve -config /path/to/config.toml
```

Without a subcommand, `kenn-forge` starts the daemon and web UI.

Use the idempotent background lifecycle when a caller needs to ensure the app
is available without treating an existing compatible process as an error:

```sh
kenn-forge start --background
kenn-forge start --background --config /path/to/config.toml
```

The command waits for the ready daemon identity before returning. Direct
foreground starts retain their duplicate-process error behavior. Background
startup requires a loopback listener. It verifies the recorded endpoint with a
credential-free challenge before reusing the process, including when general
API authentication is disabled.

## Version

```sh
kenn-forge version
kenn-forge version --json
```

Prints the version, commit, and build date. Use `--json` for fleet inventory
and other integrations. The JSON contract is a single object written to stdout:

| Field | Type | Description |
| --- | --- | --- |
| `name` | string | Canonical tool name, always `kenn-forge` |
| `version` | string | Semantic version for a release build; development identifier otherwise |
| `commit` | string | Build commit identifier |
| `buildDate` | string | UTC RFC3339 timestamp for a release build; `unknown` when not injected |

The command does not read configuration, connect to a daemon, or require a
workspace.

## Status

```sh
kenn-forge status
kenn-forge status -json
kenn-forge status -config /path/to/config.toml
```

Reports whether a kenn-forge daemon is running.

## API relay

```sh
kenn-forge api GET /api/v1/pulls
kenn-forge api POST /api/v1/sync
kenn-forge api GET /api/v1/version
```

`kenn-forge api` discovers the running daemon from the selected config and
relays one request. Use `-i` to include the HTTP status line, `--timeout` to
bound the request, and `--config` when the daemon uses another config file.
Non-2xx responses return a distinct failure exit code while preserving the
response body.

## Historical activity archive

```sh
kenn-forge archive start --all
kenn-forge archive start --repo 'github|github.com/owner/repo'
kenn-forge archive status --json
kenn-forge archive status --repo-id 42
kenn-forge archive pause --all
kenn-forge archive report --days 7
kenn-forge archive report --start 2026-07-01 --end 2026-07-07 --verbose
kenn-forge archive report --days 7 --repo-id 42
```

Archive collection runs in the background within each provider host's normal
sync budget. Status reports `current`, `partial`, or blocked work honestly; a
provider that cannot supply a required dataset remains partial rather than
being treated as complete.

Reports use only kenn-forge's local archive, so they make no provider requests,
but the kenn-forge daemon must be running. `--days` uses rolling 24-hour UTC
periods. Date-only ranges include both named dates; RFC3339 ranges use an
inclusive start and exclusive end. Reports default to Markdown; use
`--format json`, `--output PATH`, repeated fully qualified `--repo` filters,
and repeated immutable `--repo-id` filters
as needed.

Starting from an existing kenn-forge database discovers historical issues, pull
or merge requests, comments, and supported review activity. No import from an
older standalone archive is required.

## Config

```sh
kenn-forge config read port
kenn-forge config read -config /path/to/config.toml port
```

The current CLI exposes a small read surface. Use the Settings UI or edit the
TOML file for normal configuration changes.

## Docs folders

```sh
kenn-forge docs list-folders
kenn-forge docs add-folder --name Docs ~/docs
kenn-forge docs add-folder --id project --daemon kata-main ~/project-docs
kenn-forge docs remove-folder project
```

These commands manage `[[doc_folders]]` in the config file.

## Agent activity hooks

```sh
kenn-forge agent-hook install
kenn-forge agent-hook install --agent codex
kenn-forge agent-hook uninstall
kenn-forge agent-hook uninstall --agent codex
```

With no `--agent`, install or remove every supported integration. Installed
hooks forward lifecycle activity to the running daemon; Codex asks for one
manual review of the installed commands through `/hooks`.

## GitHub App credentials

```sh
kenn-forge-github-app create
kenn-forge-github-app list
kenn-forge-github-app install
kenn-forge-github-app uninstall
kenn-forge-github-app delete
kenn-forge-github-app open
```

Use this companion CLI when you want kenn-forge sync reads to use GitHub App
installation tokens instead of your personal access token rate limit.
