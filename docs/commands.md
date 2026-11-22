# Commands

To see the complete Kenn Forge command tree, run `kenn-forge` or
`kenn-forge --help`. Flags apply only to the commands that use them. Check a
command's help output before scripting.

## Set up local embeddings

Run local embeddings setup from the CLI:

```sh
kenn-forge embeddings local
kenn-forge embeddings local --yes --config /path/to/config.toml
```

Setup asks for confirmation before downloading files and before switching to
the built-in server. After it finishes, restart Forge to apply the new
settings.

## Manage the daemon

Start, inspect, restart, or stop the background daemon:

```sh
kenn-forge daemon start
kenn-forge daemon status
kenn-forge daemon status --json
kenn-forge daemon restart
kenn-forge daemon stop
```

Both `daemon start` and `daemon stop` are idempotent. `daemon start` returns
after the daemon publishes its ready identity. If a compatible process is
already running, `daemon start` safely reuses it. After you change startup
configuration or replace the binary, run `daemon restart`.

On Unix, `daemon stop` sends SIGTERM once. If the daemon does not exit within
2 minutes 16 seconds, which the CLI prints as `2m16s`, the command leaves it
running and tells you to inspect and terminate it manually instead of
force-killing a PID that may have been reused.

For foreground development or diagnosis, run `serve` directly:

```sh
kenn-forge serve
kenn-forge serve --config /path/to/config.toml
```

Foreground starts reject a second process using the same data directory.

## Check build information

Print the version and build metadata for the binary:

```sh
kenn-forge version
kenn-forge version --json
```

The JSON output includes `name`, `version`, `commit`, and `buildDate`, where
`buildDate` is a UTC RFC3339 timestamp or `unknown` for an uninjected build.
This command does not read configuration or connect to a daemon.

## Query and sync data

You can query and sync Forge data from the CLI with `activity`, `issues`,
`pulls`, `repos`, `repos summaries`, `stacks`, `workspaces`, `rate-limits`, and
`sync`. Before scripting against a command's output shape, check its help:

```sh
kenn-forge pulls --help
kenn-forge repos
kenn-forge repos summaries
kenn-forge sync --help
```

By default, `pulls` and `issues` print summaries capped at 50 results and write
a hint to stderr when more items match. To get full API records 50 at a time,
pass `--full`. To fetch a single full record, run `pulls get` or `issues get`.
To return every match, add `--limit 0`.

Pull summaries include `number`, `repo`, `title`, `author`, `state`,
`is_draft`, `ci_status`, `review_decision`, `last_activity_at`, and `url`.
Issue summaries include `number`, `repo`, `title`, `author`, `state`,
`last_activity_at`, and `url`. In both, the `repo` object includes `provider`,
`platform_host`, `owner`, and `name`. JSON, YAML, and JSONL output all print
these summaries. If a script reads bodies or other full-record fields, pass
`--full`. If it needs every match, pass `--limit 0` as well.

`repos` lists your configured repositories, and `repos summaries` lists their
summaries. The older `repo-summaries` spelling stays callable for at least one
release, hidden from help and completion, and prints a deprecation warning to
stderr.

For API request examples and output formats, run `kenn-forge quickstart`. For
MCP connectivity and client configuration, run `kenn-forge mcp quickstart`.

## Relay an API request

Send authenticated HTTP requests to the selected daemon:

```sh
kenn-forge api list
kenn-forge api GET /api/v1/version
kenn-forge api POST /api/v1/sync
kenn-forge api -i GET /api/v1/pulls
```

`kenn-forge api` finds the selected daemon and attaches its local credential.
To read a request body from stdin, pass `--data @-`. The command exits with
status `0` for a 2xx response, `1` for any other HTTP status, and `2` if no
request was made.

## Manage historical archives

Start, inspect, pause, and report on historical activity archives:

```sh
kenn-forge archive start --all
kenn-forge archive start --repo 'github|github.com/owner/repo'
kenn-forge archive status --json
kenn-forge archive pause --all
kenn-forge archive report --days 7
kenn-forge archive report --start 2026-07-01 --end 2026-07-07 --verbose
```

To target more than one repository, repeat the `--repo` flag. Reports default
to Markdown, and you can add `--format json` or `--output PATH` when you need
JSON or file output.

For coverage and status rules, see [Historical activity archive](archive.md).

## Read configuration

Read a configuration value from the CLI:

```sh
kenn-forge config read port
kenn-forge config read --config /path/to/config.toml port
```

For normal configuration changes, use Settings in the app or edit your TOML
file.

## Set up and manage a fleet

Configure a hub or spoke and manage fleet enrollment:

```sh
kenn-forge fleet setup hub --tailscale --tailscale-login you@example.com
kenn-forge fleet setup spoke --tailscale
kenn-forge fleet setup hub --origin https://forge.internal.example
kenn-forge fleet setup spoke --origin https://spoke.internal.example
kenn-forge fleet enrollment-token --base-url https://forge.internal.example
kenn-forge fleet join https://forge.internal.example \
  --base-url https://spoke.internal.example
kenn-forge fleet prepare-spoke
kenn-forge fleet abort-preparation
kenn-forge fleet revoke ENROLLMENT_ID
```

`fleet setup` requires exactly one publication mode. `--tailscale` manages a
Tailscale Serve mapping and browser identity, while `--origin` uses
operator-managed private HTTPS and never invokes Tailscale. For the complete
enrollment and verification workflow, including
[replacing a hub](federated-fleet.md#replace-the-hub), see
[Federated Forge](federated-fleet.md).

If your service uses a custom config file, pass the same
`--config /path/to/config.toml` flag to every fleet command. Joining and
preparing use the existing local daemon without reinstalling its service. To
move an already configured spoke to a new hub, you do not need `fleet setup`.

## Manage Docs folders

List, add, or remove `[[doc_folders]]` entries in the selected config file:

```sh
kenn-forge docs list-folders
kenn-forge docs add-folder --name Docs ~/docs
kenn-forge docs add-folder --id project --daemon kata-main ~/project-docs
kenn-forge docs remove-folder project
```

## Install agent activity hooks

To connect Claude Code and Codex to Forge, run `kenn-forge agent setup`. If
you want to manage hooks without MCP registration, use `agent-hook`:

```sh
kenn-forge agent-hook install
kenn-forge agent-hook install --agent codex
kenn-forge agent-hook uninstall
kenn-forge agent-hook uninstall --agent codex
```

When you omit `--agent`, `install` and `uninstall` act on every supported
integration. The installed hooks report lifecycle activity to the running
daemon. Codex prompts you once through `/hooks` to review the installed
commands. On Windows, Claude hooks need Claude Code 2.1.139 or later. After
upgrading Forge, rerun `kenn-forge agent-hook install` so it rewrites existing
Claude hooks.

## Stand in for the GitHub CLI

```sh
kenn-forge gh pr list --json number,title | cat
```

`kenn-forge gh` answers supported `gh` pull request queries from Forge data
and passes everything else to the real `gh`. A `gh` link to `kenn-forge`
behaves the same way. For details, see [GitHub CLI shim](gh-shim.md).

## Manage GitHub App credentials

Use these commands when you want sync or archive reads to use GitHub App
installation tokens:

```sh
kenn-forge github-app create
kenn-forge github-app create --org your-org
kenn-forge github-app create --role archive
kenn-forge github-app list
kenn-forge github-app install
kenn-forge github-app uninstall
kenn-forge github-app delete
kenn-forge github-app open
```

Pass `--org your-org` to create an organization-owned App, or omit `--org` for
a personally owned one. Creation includes a browser-based installation step.
For setup details and rate limits, see
[GitHub App reads](configuration.md#github-app-reads).

Passing `--role archive` creates a separate installation route for historical
archive work. Comments, reviews, state changes, and merges still use the user
PAT chain.
