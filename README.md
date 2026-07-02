# middleman

A local-first maintainer console. The original core syncs PRs and issues from your repos into SQLite, serves a fast Svelte 5 frontend from a single binary, and keeps you out of provider notification inboxes.

Middleman runs entirely on your machine -- no hosted service, no account to create. One binary, one config file, and you're up.

This workstream expands middleman beyond provider PR/MR triage with first-class
modes for external Kata task daemons, local markdown docs, and msgvault-backed
message search. Those domains stay owned by their source systems: Kata task
data remains in Kata daemons, docs remain on disk, and msgvault data remains in
msgvault.

## Features

### Activity feed

A unified timeline of comments, reviews, and commits across all your repos. Switch between flat and threaded views. Threaded view groups events by PR/issue and collapses long commit runs for readability.

Filter by time range (24h / 7d / 30d / 90d), event type, repo, item type (PRs vs issues), or free-text search. Hide closed items and bot noise with a toggle.

### Pull request management

Browse, search, and filter PRs across repos. Group by repo or show a flat list. From the detail view you can:

- **Comment** directly on a PR
- **Approve** a PR
- **Merge** with your choice of merge commit, squash, or rebase
- **Mark draft PRs as ready** for review
- **Close and reopen** PRs
- **Star** items for quick filtering

Review decisions, diff stats (additions/deletions), CI status, merge conflict indicators, and branch info are visible at a glance.

### Diff view

Inline diffs with a collapsible file tree sidebar. Files are grouped by directory and show status badges (modified, added, deleted, renamed) with per-file addition/deletion counts. Syntax highlighting via Shiki with light/dark theme support.

Filter the file tree by name, toggle whitespace visibility, and adjust tab width. Navigate between files with `j`/`k`. Each file section is independently collapsible.

### Kanban board

Track PRs through **New / Reviewing / Waiting / Awaiting Merge** columns with drag-and-drop. Kanban state is local to middleman -- it doesn't touch your GitHub labels or projects.

### Issue tracking

Same filtering, search, and detail view as PRs. Post comments, close/reopen, and star issues without context-switching to GitHub.

### CI checks

Expandable check run section on each PR shows pass/fail/pending status with color-coded indicators and direct links to the failing run on GitHub.

### Sync engine

- Runs immediately on startup, then on a configurable interval (default 5 minutes)
- Opening a PR or issue triggers an immediate sync for that item
- The active detail view polls every 60 seconds for new comments
- Progress is visible in the status bar; errors surface clearly

### Keyboard navigation

| Key | Action |
|-----|--------|
| `j` / `k` | Move through the list (or between files in diff view) |
| `1` / `2` | Switch between list and kanban views |
| `Escape` | Close detail view / clear selection |

### Other

- **Dark mode** -- auto-detects system preference, with a manual toggle
- **GitHub Enterprise, GitLab, Forgejo, and Gitea** -- set
  `platform`/`platform_host` per repo to connect to other provider hosts
- **Copy to clipboard** -- one-click copy of PR/issue bodies and comments
- **Settings UI** -- add/remove repos and configure activity feed defaults from the browser
- **Reverse proxy support** -- deploy behind a proxy with the `base_path` config
- **Version info** -- `middleman version` prints the version, commit, and build date

### Additional modes in this integration branch

- **Kata** -- talk to external Kata daemons discovered from Kata's own
  `$KATA_HOME/config.toml` and runtime records.
- **Docs** -- browse, view, edit, search, and publish configured markdown
  folders.
- **Messages** -- search and inspect msgvault-backed messages with safe HTML and
  image handling.
- **Federated fleet** -- one daemon (the hub) merges snapshots from remote
  middleman daemons, proxies mutations to the host that owns the resource,
  and hands out native tmux attach commands fleet-wide. Peers are reached
  over HTTP or SSH (no exposed remote listener required). See
  [docs/federated-fleet.md](docs/federated-fleet.md).

## Quickstart

### Requirements

- Go 1.26+
- [Bun](https://bun.sh/) (or install via [mise](https://mise.jdx.dev/))
- A provider token with read access to the configured repos. GitHub can use a
  classic or fine-grained token; GitLab, Forgejo, and Gitea use host-scoped
  tokens from config.

### Build and run

```sh
git clone https://github.com/wesm/middleman.git
cd middleman
make build
```

Set your token and start middleman:

```sh
export MIDDLEMAN_GITHUB_TOKEN=ghp_your_token_here
./middleman
```

If you use the [GitHub CLI](https://cli.github.com/), middleman will use `gh auth token` automatically -- no env var needed.

For token rotation without restarting middleman, configure `token_file` on a
repo or provider entry and replace that file atomically when the token changes.
Middleman reads token files on demand and trims surrounding whitespace.

On first run, middleman creates a default config at `~/.config/middleman/config.toml` and serves the UI at **http://localhost:8091**. Add repositories from the Settings page, or edit the config file directly:

```toml
[[repos]]
owner = "your-org"
name = "your-repo"

[[repos]]
owner = "your-org"
name = "another-repo"
```

To expose Go profiler endpoints for local diagnostics, start a separate listener:

```sh
MIDDLEMAN_PPROF_ADDR=127.0.0.1:6060 ./middleman
# or
./middleman serve -pprof-addr 127.0.0.1:6060
```

The listener is disabled when the address is empty and serves the standard `/debug/pprof/` endpoints.

### Install to PATH

```sh
make install   # installs to ~/.local/bin
```

## Configuration

All fields are optional. Repos can be added in the config file or through the Settings UI.

| Field | Default | Description |
|-------|---------|-------------|
| `sync_interval` | `"5m"` | How often to pull from configured providers |
| `github_token_env` | `"MIDDLEMAN_GITHUB_TOKEN"` | Env var holding the default GitHub token |
| `default_platform_host` | `"github.com"` | Host treated as implicit in repository UI labels |
| `host` | `"127.0.0.1"` | Listen address |
| `port` | `8091` | Listen port, from 1 to 65535 |
| `base_path` | `"/"` | URL prefix for reverse proxy deployments |
| `data_dir` | `"~/.config/middleman"` | Directory for the SQLite database |
| `activity.view_mode` | `"threaded"` | `"flat"` or `"threaded"` |
| `activity.time_range` | `"7d"` | `"24h"`, `"7d"`, `"30d"`, or `"90d"` |
| `activity.hide_closed` | `false` | Hide closed/merged items in the feed |
| `activity.hide_bots` | `false` | Hide bot activity |
| `activity.default_branch_retention_days` | `90` | Days of default-branch commits to keep for Activity |
| `activity.default_branch_max_commits` | `5000` | Maximum default-branch commit rows kept per repo branch |

The integration branch also adds docs-folder and msgvault configuration. Kata
daemon definitions are intentionally not stored in middleman config; middleman
reads the Kata daemon catalog from `$KATA_HOME/config.toml`, defaulting to
`~/.kata/config.toml`.

### Provider Hosts

Add `platform_host` and optionally `token_env` or `token_file` to repos hosted
on a GitHub Enterprise instance:

```toml
[[repos]]
owner = "team"
name = "internal-app"
platform_host = "github.corp.example.com"
token_env = "GHE_TOKEN"
```

Tokens can come from `token_file`, `token_env`, exact public-host defaults, or
the GitHub CLI fallback. Use `token_file` when you need rotation without
restarting Middleman: write the new token to a temporary file, then atomically
rename it over the configured path. Middleman reads token files on demand and
trims surrounding whitespace.

For a repo or platform entry, `token_file` is checked before `token_env`; empty
token files and empty env vars are treated as absent so the next configured
fallback can supply a token. Public-host defaults are:

- GitHub `github.com`: `github_token_env`, defaulting to
  `MIDDLEMAN_GITHUB_TOKEN`, then GitHub CLI fallback.
- GitLab `gitlab.com`: no implicit default env var; configure `token_env` or
  `token_file`.
- Forgejo `codeberg.org`: `MIDDLEMAN_FORGEJO_TOKEN`.
- Gitea `gitea.com`: `MIDDLEMAN_GITEA_TOKEN`.

Tokens are looked up by `(provider, host)`. Each distinct provider host can use
a separate token source, so `github.com`, a GitHub Enterprise host,
`gitlab.com`, `codeberg.org`, and a private Gitea host do not share API
credentials unless you explicitly point them at the same source. Repos without
`platform_host` default to that provider's public host: `github.com`,
`gitlab.com`, `codeberg.org`, or `gitea.com`. Set `default_platform_host` when
you want another host to be hidden as the implied repository host in the UI.

Git clone credentials are selected by URL host. If two provider kinds use the
same hostname, they must resolve to the same effective token source or use
separate hostnames.

Example provider-level token file:

```toml
[[platforms]]
type = "gitlab"
host = "gitlab.com"
token_file = "~/.config/middleman/tokens/gitlab.com"
```

Minimum read access is enough for sync: repository metadata, pull or merge
requests, issues, comments, commits, tags, releases, and CI/status data. Enable
write access only if you want middleman to post comments, edit titles/bodies,
change issue or PR state, approve reviews, or merge.

GitLab hosts are configured through `[[platforms]]`, then referenced by repos:

```toml
[[platforms]]
type = "gitlab"
host = "gitlab.com"
token_env = "MIDDLEMAN_GITLAB_TOKEN"

[[repos]]
platform = "gitlab"
platform_host = "gitlab.com"
owner = "my-group/subgroup"
name = "my-project"
repo_path = "my-group/subgroup/my-project"
```

GitLab nested namespaces are preserved in `owner` and `repo_path`. Mutating
actions are exposed only when the provider reports support for that capability;
unsupported provider actions return a typed capability error instead of trying a
GitHub-only route.

Forgejo and Gitea use the same provider-host shape. Public Forgejo defaults to
Codeberg, and public Gitea defaults to gitea.com:

```toml
[[repos]]
platform = "forgejo"
platform_host = "codeberg.org"
owner = "forgejo"
name = "forgejo"

[[repos]]
platform = "gitea"
platform_host = "gitea.com"
owner = "gitea"
name = "tea"
```

Self-hosted Forgejo and Gitea instances should be declared in `[[platforms]]`
with their own token env var, then referenced from repos:

```toml
[[platforms]]
type = "forgejo"
host = "forgejo.internal.example"
token_env = "FORGEJO_INTERNAL_TOKEN"

[[platforms]]
type = "gitea"
host = "gitea.internal.example"
token_env = "GITEA_INTERNAL_TOKEN"

[[repos]]
platform = "forgejo"
platform_host = "forgejo.internal.example"
owner = "team"
name = "service"

[[repos]]
platform = "gitea"
platform_host = "gitea.internal.example"
owner = "team"
name = "ops"
```

Forgejo and Gitea preserve owner and repo casing as returned by the server.
Unlike GitLab, nested owners are not supported for these providers; `repo_path`
is normally the same as `owner/name` and is most useful when middleman parsed a
repository URL or needs to preserve provider-canonical casing.

## Telemetry

Middleman sends limited anonymous telemetry to PostHog: `daemon_active` with repo count and `app_loaded` with view name, plus version, commit, OS/arch, `application: "middleman"`, and an anonymous install ID.
It disables PostHog person profile processing and IP geolocation for every capture. It does not send repo names, PR/issue content, provider tokens, usernames, hostnames, or paths; set `TELEMETRY_ENABLED=0` to disable it.

## Thin clients

Middleman runs as a standalone daemon. Native apps and scripts reach
it without out-of-band configuration: probe the flock on
`<data_dir>/middleman.lock`, read `middleman.run.json` for the listen
address and base path, authenticate with the bearer token minted at
`<data_dir>/auth_token`, and use `/api/v1` plus the SSE event stream
(Last-Event-ID replay supported). The `middleman api` CLI verb wraps
this discovery + auth for one-shot calls; webview hosts can load SPA
routes directly and read daemon-side UI state from the served
`window.__middleman_config`. See `docs/federated-fleet.md` for the
fleet and daemon contract details.

## Architecture

Middleman is a single Go binary with the Svelte frontend embedded at build time.
The provider dashboard stores synced provider state in SQLite. Additional modes
may talk to local external services such as Kata daemons and msgvault.

```
middleman binary
  |- Config loader (TOML)
  |- Sync engine -> provider registry (GitHub/GitLab/Forgejo/Gitea readers)
  |- Mode adapters -> Kata daemons, markdown folders, msgvault
  |- SQLite database (WAL mode, pure Go driver)
  +- HTTP server (Huma) -> REST API + embedded SPA
```

- **No CGO required** -- uses [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite), a pure Go SQLite implementation
- **Loopback only** -- binds to 127.0.0.1 by default; this is a personal tool, not a shared service
- **Graceful shutdown** -- handles SIGINT/SIGTERM cleanly

## Database

Middleman uses SQLite with embedded SQL migrations in `internal/db/migrations/`, applied on startup via `github.com/golang-migrate/migrate/v4`.

On startup:

- **Fresh database**: all embedded migrations are applied.
- **Legacy database without `schema_migrations`**: middleman assumes the pre-migration schema is baseline version 1 and migrates forward.
- **Dirty or failed migration state**: startup fails and instructs you to delete the database file and let middleman recreate it.
- **Newer database** (migration version > binary): startup fails and instructs you to upgrade middleman.

If a migration cannot be applied cleanly, delete `~/.config/middleman/middleman.db` and let middleman recreate it. Sync data will be repopulated from GitHub on the next run; local-only state (kanban columns, stars, and worktree links) is lost.

## Development

Run the Go backend and Vite dev server in parallel:

```sh
make air-install    # one-time: install air for live reload
make dev            # Go server on :8091 with live reload
make frontend-dev   # Vite on :5174, proxies /api to Go
```

### Docker Compose dev stack

Use the `mise` tasks to manage compose stack with a token fetched from host GitHub CLI:

```sh
mise run dev-compose       # docker compose up
mise run dev-compose-logs  # docker compose logs -f
mise run dev-compose-down  # docker compose down
```

Compose behavior:
- Uses repo-local `docker/dev-config.toml` so compose config stays isolated from native runs
- Stores SQLite state in Docker volume as `/data/middleman.db` via `data_dir = "/data"`
- Exposes backend on `http://127.0.0.1:18090` and frontend dev server on `http://127.0.0.1:15173`

### Custom config file

Use custom config file for both processes with shared env override:

```sh
MIDDLEMAN_CONFIG=/path/to/config.toml make dev
MIDDLEMAN_CONFIG=/path/to/config.toml make frontend-dev
```

### Ephemeral dev stack

Run backend and frontend together on two free loopback ports with an isolated
copy of your configured SQLite state:

```sh
make dev-ephemeral
```

The command writes a generated config, database directory, logs, and typed
status JSON into `tmp/dev-ephemeral`. By default it snapshots the source SQLite
database into the generated data directory. The status file sits next to the
generated config as `dev-ephemeral.json` and records the launcher PID, backend
PID, frontend PID, selected ports, URLs, config path, and data directory. If the
default stack is already running, another `make dev-ephemeral` prints the
existing status instead of starting a second stack. Stale status files are
removed and replaced.

Pass `ARGS` to control the generated run:

```sh
make dev-ephemeral ARGS="-work-dir tmp/my-run"
make dev-ephemeral ARGS="-backend-port 19091 -frontend-port 15174"
make dev-ephemeral ARGS="-fresh-db"
```

Use `-work-dir` when you intentionally want a separate concurrent stack.
The ephemeral launcher is currently supported on Unix-like development
environments only.

Other targets:

```sh
make build          # Debug build with embedded frontend
make build-release  # Optimized, stripped release binary
make test           # All Go tests
make test-short     # Fast tests only
make lint           # golangci-lint
make frontend-check # Vite+ formatting, lint, type, and Svelte checks
make api-generate   # Regenerate OpenAPI spec and clients
make clean          # Remove build artifacts
```

### Pre-commit hooks

Managed with [prek](https://github.com/j178/prek):

```sh
brew install prek
prek install
```

## License

Middleman is source-available software, licensed under the
[Elastic License 2.0](LICENSE) (ELv2).

You can use, copy, modify, and redistribute it for free. The main restriction is
that you may not provide Middleman to third parties as a hosted or managed
service that gives users access to a substantial set of its features. You also
may not remove the project's licensing or copyright notices. The
[LICENSE](LICENSE) file is the authoritative text; this paragraph is a
non-binding summary.

Contributions made before the relicense remain available under the MIT License;
see the [NOTICE](NOTICE) file.

A commercial license is available for uses not permitted by ELv2. For commercial
licensing, contact [Kenn Software](https://kenn.io) at info@kenn.io.
