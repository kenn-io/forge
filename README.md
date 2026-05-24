# middleman

A local-first GitHub dashboard for project maintainers. Syncs PRs and issues from your repos into SQLite, serves a fast Svelte 5 frontend from a single binary, and keeps you out of GitHub's notification inbox.

Middleman runs entirely on your machine -- no hosted service, no telemetry, no account to create. One binary, one config file, and you're up.

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

On first run, middleman creates a default config at `~/.config/middleman/config.toml` and serves the UI at **http://localhost:8091**. Add repositories from the Settings page, or edit the config file directly:

```toml
[[repos]]
owner = "your-org"
name = "your-repo"

[[repos]]
owner = "your-org"
name = "another-repo"
```

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
| `port` | `8091` | Listen port |
| `base_path` | `"/"` | URL prefix for reverse proxy deployments |
| `data_dir` | `"~/.config/middleman"` | Directory for the SQLite database |
| `activity.view_mode` | `"threaded"` | `"flat"` or `"threaded"` |
| `activity.time_range` | `"7d"` | `"24h"`, `"7d"`, `"30d"`, or `"90d"` |
| `activity.hide_closed` | `false` | Hide closed/merged items in the feed |
| `activity.hide_bots` | `false` | Hide bot activity |
| `activity.default_branch_retention_days` | `90` | Days of default-branch commits to keep for Activity |
| `activity.default_branch_max_commits` | `5000` | Maximum default-branch commit rows kept per repo branch |

### Provider Hosts

Add `platform_host` and optionally `token_env` to repos hosted on a GitHub
Enterprise instance:

```toml
[[repos]]
owner = "team"
name = "internal-app"
platform_host = "github.corp.example.com"
token_env = "GHE_TOKEN"
```

Tokens are looked up by `(provider, host)`. Each distinct host can use a
separate token env var, so `github.com`, a GitHub Enterprise host, `gitlab.com`,
`codeberg.org`, and a private Gitea host do not share credentials unless you
explicitly point them at the same env var. Repos without `platform_host` default
to that provider's public host: `github.com`, `gitlab.com`, `codeberg.org`, or
`gitea.com`. Set `default_platform_host` when you want another host to be hidden
as the implied repository host in the UI.

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

## Embedding

Middleman can be embedded as a Go library inside another application. The host creates an `Instance`, which provides an `http.Handler` for the API and frontend:

```go
inst, err := middleman.New(middleman.Options{
    Token:        os.Getenv("GITHUB_TOKEN"),
    DBPath:       "/path/to/middleman.db",
    BasePath:     "/middleman/",
    SyncInterval: 5 * time.Minute,
    Repos: []middleman.Repo{
        {Owner: "org", Name: "repo"},
    },
})
if err != nil {
    log.Fatal(err)
}
defer inst.Close()
inst.StartSync(ctx)

mux.Handle("/middleman/", inst.Handler())
```

The `EmbedConfig` option controls theming (light/dark mode, custom colors, fonts, radii) and UI defaults (hide sync controls, pin to a single repo, collapse sidebar). The `EmbedHooks` option provides lifecycle callbacks (`OnMRSynced`, `OnSyncCompleted`) so the host can react to sync events.

The frontend is also available as the `@middleman/ui` Svelte package, which exports individual views (`PRListView`, `KanbanBoardView`, `ActivityFeedView`), store factories, and context accessors. The `@middleman/ui` `Provider` component accepts an action registry for injecting custom buttons into PR and issue detail views.

## Architecture

Middleman is a single Go binary with the Svelte frontend embedded at build time. No external services -- just SQLite on disk.

```
middleman binary
  |- Config loader (TOML)
  |- Sync engine -> provider registry (GitHub/GitLab/Forgejo/Gitea readers)
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
make frontend-check # Svelte and TypeScript checks
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

MIT
