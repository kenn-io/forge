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
kata = false
docs = false
pulls = true
issues = true
reviews = true
workspaces = true
```

Set a mode to `false` to hide it. Kata and Docs start hidden because they need
external or local sources.

## Workspace agents

kenn-forge detects built-in agents on `PATH`. Add or override an agent with:

```toml
[[agents]]
key = "review"
label = "Review Agent"
command = ["review-agent", "--fast"]
```

You can also edit agents under **Settings → Agents**.

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
