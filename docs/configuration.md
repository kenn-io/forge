# Configuration

kenn-forge reads `~/.kenn/forge/config.toml`. Set `KENN_FORGE_HOME` to move
both config and app data. Most users only need repositories, credentials, and
optional modes.

Use Settings for routine changes. Edit TOML for provider hosts and advanced
options. Restart kenn-forge after changing startup settings.

## Repositories

A GitHub repository on `github.com` needs only its owner and name:

```toml
[[repos]]
owner = "team"
name = "service"
```

You can paste a common HTTPS or SSH repository URL into `owner` or `name`.
kenn-forge normalizes it.

Set the provider and host for other services or self-hosted instances:

```toml
[[platforms]]
type = "gitlab"
host = "gitlab.example.com"
token_env = "GITLAB_EXAMPLE_TOKEN"

[[repos]]
platform = "gitlab"
platform_host = "gitlab.example.com"
owner = "group/subgroup"
name = "project"
repo_path = "group/subgroup/project"
```

Repository identity includes `platform`, `platform_host`, `owner`, and `name`.
Keep `repo_path` when the provider uses nested namespaces or canonical casing.

To hide a repository from lists and pickers without removing it, open the gear
menu on its Settings row and choose "Hide from UI". Syncing continues and
direct links keep working; choose "Show in UI" to bring it back. Hiding
applies to exact repositories, not glob patterns.

## Credentials

Credentials are scoped by provider and host:

```toml
github_token_env = "KENN_FORGE_GITHUB_TOKEN"

[[platforms]]
type = "forgejo"
host = "forge.example.com"
token_env = "FORGE_EXAMPLE_TOKEN"

[[repos]]
platform = "forgejo"
platform_host = "forge.example.com"
owner = "team"
name = "private-service"
token_file = "~/.kenn/forge/tokens/private-service"
```

An exact repository `token_file` or `token_env` takes priority over broader
credentials. Empty files and variables are skipped. Token files are read on
each request, so replacing a file atomically rotates that credential.

For GitHub, kenn-forge can run `gh auth token --hostname HOST`. The unscoped
fallback applies only to `github.com`. Authenticate another host with:

```sh
gh auth login --hostname HOST
```

Public-host defaults are:

- GitHub `github.com`: `KENN_FORGE_GITHUB_TOKEN`, then the GitHub CLI.
- GitLab `gitlab.com`: no implicit variable. Configure a token source.
- Forgejo `codeberg.org`: `KENN_FORGE_FORGEJO_TOKEN`.
- Gitea `gitea.com`: `KENN_FORGE_GITEA_TOKEN`.

Grant read access for monitoring. Add write access only for comments, reviews,
state changes, edits, or merges.

### GitHub credentials by owner

Map fine-grained PATs to owners when one token cannot read every repository:

```toml
[[github_owner_tokens]]
owner = "org-a"
token_env = "KENN_FORGE_GITHUB_TOKEN_ORG_A"

[[github_owner_tokens]]
owner = "org-b"
token_file = "~/.kenn/forge/tokens/org-b"
```

Owner mappings are configured in TOML only.

GitHub selects credentials by host and owner. Exact repository credentials win.
A covered GitHub App handles reads. Owner PATs handle uncovered reads and
user-attributed writes. Host credentials and the GitHub CLI follow.

PATs for the same GitHub user share one rate limit and sync budget. Different
users and App installations have separate budgets. Restart after routing an
owner to a PAT from a different GitHub user.

### GitHub App reads

Use the companion CLI to keep sync reads off your personal rate limit:

```sh
kenn-forge-github-app create
kenn-forge-github-app install
kenn-forge-github-app list
```

The CLI writes `[[github_apps]]` entries. Mutations still use a user PAT.
After changing selected repository access on GitHub, run
`kenn-forge-github-app install` again and restart kenn-forge.

Selected repository access is a startup routing snapshot. New grants use the
PAT route until refresh. Revoked App access can return 404, and kenn-forge does
not retry that response with a PAT because 404 can also mean missing or private.

## Activity defaults

```toml
[activity]
view_mode = "threaded"
time_range = "7d"
hide_closed = false
hide_bots = false
collapse_threads = true
default_branch_retention_days = 90
default_branch_max_commits = 5000
```

These values set the initial Activity view and local default-branch retention.

## App modes

```toml
[modes]
activity = true
repos = true
docs = false
pulls = true
issues = true
reviews = true
workspaces = true
```

Set a mode to `false` to hide it. Docs starts hidden because it needs configured
local folders. Kata integration is contextual rather than a top-level mode.

## Roborev

The Reviews page reads from a separately running Roborev daemon. The default
endpoint is `http://127.0.0.1:7373`:

```toml
[roborev]
endpoint = "http://127.0.0.1:7373"
init_managed_clones = true
```

The endpoint controls the Reviews connection and requires a restart when it
changes. `init_managed_clones` is optional and defaults to `false`; you can
also change it under **Settings → Workspaces** without restarting. When it is
enabled, the endpoint must use loopback HTTP (`127.0.0.1`, `localhost`, or
`[::1]`) because Forge passes it to the Roborev CLI during workspace setup.

Set `reviews = false` under `[modes]` if you do not want the Reviews page. See
[Integrations](integrations.md#review-roborev-jobs) for the current Reviews
workflow.

## Workspace agents

kenn-forge detects built-in agents on `PATH`. Add or override an agent with:

```toml
[[agents]]
key = "review"
label = "Review Agent"
command = ["review-agent", "--fast"]
```

You can also edit agents under **Settings → Agents**.

## Workspace terminals and tmux

Workspace terminals and agent sessions run on a dedicated tmux server
(socket name `kenn-forge`), so busy kenn-forge sessions do not contend with
your personal tmux server. To inspect or attach from a regular terminal:

```sh
tmux -L kenn-forge ls
tmux -L kenn-forge attach-session -E -t <session>
```

`-E` keeps variables from your shell (including any exported provider
tokens, if you widened tmux's `update-environment`) out of the session
environment, which processes inside the workspace can read.

Set `[tmux] command` to pick a different socket or wrap the launch; the
configured command line is used verbatim:

```toml
[tmux]
command = ["tmux", "-L", "kenn-forge"]
```

Wrapper commands run with a minimal non-secret environment, because the
tmux server permanently retains the environment it was started with.
Pass any custom data a wrapper needs as command-line arguments rather
than environment variables. For the same reason, provider `token_env`
names may not reuse standard terminal variables such as `EDITOR` or
`PATH`; configuration validation rejects the collision.

Sessions started by versions that used the default tmux server keep
running there after an upgrade, but kenn-forge no longer sees them.
Reattach or clean them up with plain `tmux ls` and `tmux kill-session`.

## Docs folders

Register local Markdown folders from the CLI:

```sh
kenn-forge docs add-folder --name Notes ~/notes
kenn-forge docs list-folders
kenn-forge docs remove-folder notes
```

The equivalent config is:

```toml
[[doc_folders]]
id = "notes"
name = "Notes"
path = "/Users/you/notes"
daemon = "kata-main"
```

`daemon` is optional. Set it when task links in this folder always belong to
one Kata daemon.

## Kata daemons and repository mappings

kenn-forge reads Kata daemon definitions from `$KATA_HOME/config.toml`, or
`~/.kata/config.toml` when `KATA_HOME` is unset. Daemon credentials and URLs
stay in Kata's catalog rather than kenn-forge configuration.

Open **Settings → Kata mappings** to see which repository each Kata project
will use for workspace creation. Add a manual mapping when automatic matching
does not pick the right configured repository:

```toml
[[kata_projects]]
daemon_id = "kata-main"
project_uid = "widgets"
provider = "github"
platform_host = "github.com"
repo_path = "acme/widgets"
```

`daemon_id` scopes the mapping to one daemon. Leave it out only for a mapping
that should apply to the same project UID on every daemon. Choose an exact
repository identity available in Settings. That repository may have been found
through a configured pattern, a tracked repository, or a registered project,
but the mapping itself cannot contain a glob.

## Server and storage

```toml
sync_interval = "5m"
host = "127.0.0.1"
port = 8091
base_path = "/"
```

- `sync_interval` controls provider refreshes.
- `host` and `port` set the listener.
- `base_path` adds a URL prefix behind a reverse proxy.
- `data_dir` moves app data while leaving config under `KENN_FORGE_HOME`.

For a trusted reverse proxy or a larger SSE replay window:

```toml
allowed_hosts = ["forge.example.com", "proxy.example.com:8091"]
trust_reverse_proxy = true
sse_buffer_size = 256
```

`allowed_hosts` accepts exact host and port values. A trusted proxy must
present accepted direct and forwarded hosts. `sse_buffer_size` defaults to 256
and accepts 16 through 16384.

### MCP companion

Enable the daemon's optional loopback MCP listener with:

```toml
[mcp]
enabled = true
# port = 8092 # defaults to the main backend port plus one
diff_cache_mb = 128
```

The sessionless Streamable HTTP endpoint is
`http://127.0.0.1:<resolved-port>/mcp`. Authentication follows
`[api].require_auth`. Full diff files use a request-based least-recently-used
cache; `diff_cache_mb` defaults to 128 MiB. MCP listener or cache changes
require a daemon restart.

## Pull request stacks

```toml
[pull_requests]
prefer_github_native_stacks = true
allow_mid_stack_merges = false
```

Native GitHub stack data improves read-only detection when complete. Branch
relationships remain the fallback. kenn-forge does not create or reorder
stacks. Mid-stack merges stay blocked by default.

## Telemetry

kenn-forge sends limited anonymous telemetry by default: daemon activity, app
view names, version, commit, OS and architecture, and an anonymous install ID.
It does not send repository names, item content, tokens, usernames, hostnames,
or paths.

Disable telemetry with:

```sh
TELEMETRY_ENABLED=0 kenn-forge daemon start
```
