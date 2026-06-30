# Configuration

middleman reads TOML from:

```text
~/.config/middleman/config.toml
```

Set `MIDDLEMAN_HOME` to use a different config and data directory. Most users
only need repositories, tokens, and optional modes.

## Basic server settings

```toml
sync_interval = "5m"
host = "127.0.0.1"
port = 8091
base_path = "/"
```

- `sync_interval`: how often provider data is refreshed.
- `host` and `port`: where the local daemon listens.
- `base_path`: URL prefix when serving behind a reverse proxy.
- `data_dir`: local database and app state location. Leave unset for the
  default; set `MIDDLEMAN_HOME` to relocate both config and data, or use an
  absolute `data_dir` path to move only app state.

## Repositories

GitHub repositories can use the default provider settings:

```toml
[[repos]]
owner = "kenn-io"
name = "middleman"
```

You can also paste a repository URL into `owner` or `name`; middleman normalizes
common HTTPS and SSH forms.

For providers or hosts outside the default GitHub host, set `platform` and
`platform_host`:

```toml
[[repos]]
platform = "gitlab"
platform_host = "gitlab.com"
owner = "group/subgroup"
name = "project"
repo_path = "group/subgroup/project"
```

For self-hosted providers, declare the host once:

```toml
[[platforms]]
type = "forgejo"
host = "forgejo.internal.example"
token_env = "FORGEJO_INTERNAL_TOKEN"

[[repos]]
platform = "forgejo"
platform_host = "forgejo.internal.example"
owner = "team"
name = "service"
```

## Tokens

Token lookup is scoped by provider and host. Use one of these sources:

```toml
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"

[[platforms]]
type = "gitlab"
host = "gitlab.com"
token_env = "MIDDLEMAN_GITLAB_TOKEN"

[[repos]]
owner = "team"
name = "private-repo"
token_file = "~/.config/middleman/tokens/private-repo"
```

For GitHub, middleman can also fall back to `gh auth token`.

Use read access for monitoring. Add write access only when you want middleman to
comment, approve, close, reopen, edit, or merge.

For GitHub rate-limit isolation, use the companion CLI:

```sh
middleman-github-app create
middleman-github-app list
```

The app credentials are written to `[[github_apps]]` in the same config file.

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

These settings control the initial Activity feed state and how much
default-branch commit activity is retained locally.

## Modes

```toml
[modes]
activity = true
repos = true
kata = false
docs = false
messages = false
pulls = true
issues = true
board = true
reviews = true
workspaces = true
```

Set a mode to `false` to hide it from the app. Kata, Docs, and Messages default
to hidden because they depend on external or local sources.

## Docs folders

Register markdown folders from the CLI:

```sh
middleman docs add-folder --name Notes ~/notes
middleman docs list-folders
middleman docs remove-folder notes
```

This writes `[[doc_folders]]` entries:

```toml
[[doc_folders]]
id = "notes"
name = "Notes"
path = "/Users/you/notes"
```

## Messages

Messages mode uses msgvault:

```toml
[modes]
messages = true

[msgvault]
url = "http://127.0.0.1:8080"
api_key_env = "MSGVAULT_API_KEY"
```

Plain HTTP is accepted only for loopback hosts. Use HTTPS for remote msgvault
servers.

## Telemetry

middleman sends limited anonymous telemetry by default: daemon activity, app
load view names, version, commit, OS/arch, and an anonymous install ID.

It does not send repo names, PR or issue content, tokens, usernames, hostnames,
or paths.

Disable telemetry with:

```sh
TELEMETRY_ENABLED=0 middleman
```
