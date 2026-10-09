# Configuration

Kenn Forge reads `~/.kenn/forge/config.toml`. Set `KENN_FORGE_HOME` to move
both config and app data. Most users only need repositories, credentials, and
optional modes.

Use Settings for routine changes. Edit TOML for provider hosts and advanced
options. Restart Forge after changing startup settings.

Configure private PR cards and action buttons with TOML-only
[`external_context` sources](external-pr-context.md).

## Repositories

### Faster GitHub updates

An optional shared relay receives messages from GitHub when something changes.
Forge stays connected to the relay, receives each change as it happens, and
fetches the changed items using its own GitHub credentials. Add the private
feed URL supplied by the person running the relay, then restart Forge:

```toml
[relay]
url = "https://relay.example.com"
```

Use the HTTPS origin without `/activity` or a webhook path. In a fleet, add
this setting to the hub. An empty URL disables the relay. Normal syncing
continues. See [Faster GitHub updates](activity-relay.md) for setup, access,
and a walkthrough to check that it works. The relay supports GitHub.com.
With `workflow_run` and `check_run` webhooks enabled, the relay batches Actions
updates once per minute. Forge refreshes PR checks when each batch arrives.
While **Actions** is open, it also refreshes the selected workflow's run list.
The usual API limits still apply; no extra setting is needed.

### Repository selection

A GitHub repository on `github.com` needs only its owner and name:

```toml
[[repos]]
owner = "team"
name = "service"
```

You can paste a common HTTPS or SSH repository URL into `owner` or `name`.
Forge normalizes it.

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

### Self-hosted Gitea transport

Gitea uses `https://<host>` by default. Set an explicit API URL when a trusted
private deployment serves Gitea over HTTP:

```toml
[[platforms]]
type = "gitea"
host = "gitea.example.test:3000"
base_url = "http://gitea.example.test:3000"
allow_insecure = true
token_env = "GITEA_PRIVATE_TOKEN"
```

`host` remains the repository identity and must match Gitea's advertised clone
URLs. `allow_insecure` acknowledges that API and Git credentials can travel
without TLS. Forge rejects plain-HTTP or mismatched clone URLs before
using them.

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

When no token source is declared for a host, Forge reuses the credential the
provider's CLI stores for that host:

- GitHub: `gh auth token --hostname HOST`. The unscoped fallback applies only
  to `github.com`.
- GitLab: the token `glab` holds for the host, from its config file or the
  operating-system keyring. `GITLAB_TOKEN` and similar variables are not used
  here; declare `token_env` to use one.
- Forgejo and Gitea: the token `fj` ([forgejo-cli](https://codeberg.org/forgejo-contrib/forgejo-cli))
  stores for the host in its keys file. Expired OAuth logins are skipped; run
  any `fj` command against the host to refresh them.

Authenticate a host with the matching CLI:

```sh
gh auth login --hostname HOST
glab auth login --hostname HOST
fj --host HOST auth login
```

Public-host defaults are:

- GitHub `github.com`: `KENN_FORGE_GITHUB_TOKEN`, then the GitHub CLI.
- GitLab `gitlab.com`: no implicit variable, then the GitLab CLI.
- Forgejo `codeberg.org`: `KENN_FORGE_FORGEJO_TOKEN`, then the Forgejo CLI.
- Gitea `gitea.com`: `KENN_FORGE_GITEA_TOKEN`, then the Forgejo CLI.
- Bitbucket Cloud `bitbucket.org`: `KENN_FORGE_BITBUCKET_TOKEN`, then host-scoped `BKT_*` settings.

Grant read access for monitoring. Add write access only for comments, reviews,
state changes, edits, or merges.

### Bitbucket Cloud

Add your repository using its workspace name and repository slug:

```toml
[[repos]]
platform = "bitbucket"
owner = "example-workspace"
name = "widgets"
token_env = "KENN_FORGE_BITBUCKET_TOKEN"
```

Set `KENN_FORGE_BITBUCKET_TOKEN` to `account-email:api-token` for an Atlassian
API token, or to the token alone for an OAuth or repository access token.
You can use `token_file` instead of `token_env`. Grant read access to
repositories and pull requests for monitoring. Add write access for actions
such as commenting, reviewing, and merging. To select reviewers by name, also
grant `read:workspace:bitbucket`. Forge lists workspace members and submits their
account IDs. Existing reviewers outside the workspace can still be removed.

Merging in Forge requires an account API token whose repository write permission
can be checked. OAuth and repository access tokens can read data, but Forge
does not enable merges with those credentials. Branch restrictions and token
scopes still apply.

If you use the community `bkt` CLI, Forge can read its environment settings.
Set `BKT_HOST=https://bitbucket.org` and `BKT_TOKEN`. For bearer tokens, set
`BKT_AUTH_METHOD=bearer`. For basic authentication, set `BKT_USERNAME`.
Forge uses its own configured credentials first. It does not read saved `bkt`
logins or Atlassian `acli` credentials.

With Cloud, you can:

- Discover repositories and follow pull requests, tags, and commit statuses.
- Read, create, and comment on issues when the repository has issues enabled.
- Comment, reply, view and resolve inline threads, and change reviewers.
- Approve pull requests, request changes, and merge using merge, squash, or rebase.

Forge does not yet support Cloud workflow controls, notifications, labels,
publishing review drafts, editing pull requests or changing their state outside
a merge, or collecting historical archives.

### Bitbucket Data Center

Set `platform_host` to your server's hostname, `owner` to the project key,
and `name` to the repository slug. Forge uses Cloud for `bitbucket.org` and
Data Center for other hosts.

```toml
[[repos]]
platform = "bitbucket"
platform_host = "bitbucket.example.com"
owner = "PROJECT"
name = "widgets"
token_env = "BITBUCKET_DC_CREDENTIAL"
```

Set `BITBUCKET_DC_CREDENTIAL` to `username:personal-access-token`. Forge uses
this credential for both the API and Git.

With Data Center, you can discover repositories, follow pull requests, tags,
and commit statuses, and approve, edit, reopen, or merge pull requests.
Merging requires a user personal access token with repository write permission.
Forge offers merge, squash, and rebase when enabled in the repository settings;
branch restrictions still apply.

Forge does not yet support Data Center comments or inline threads, issues,
workflows, notifications, reviewer changes, or publishing review drafts. Your server must serve Bitbucket
at the hostname root, such as `https://bitbucket.example.com/`. A path such as
`https://example.com/bitbucket/` is not supported.

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

GitHub Apps are an advanced option for more sync capacity. Forge uses an App's
installation quota for sync reads, leaving your personal quota for other tools
and actions.

An App can read only the repositories covered by its installation and granted
permissions. Owning the App does not grant repository access. An installation
in one organization does not cover another organization's repositories.
Repositories outside that installation still need another credential route.

The API quota belongs to each installation. Repositories in that installation
share it; installing an App does not raise your personal token's rate limit.

On GitHub.com, installation REST limits start at 5,000 requests per hour and
can scale to 12,500 with repository and organization size. Installations on
GitHub Enterprise Cloud organizations get 15,000. See
[GitHub's installation rate limits](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api#primary-rate-limit-for-github-app-installations)
for the current rules.

Creating an organization-owned App requires organization owner access or
permission to manage all of the organization's GitHub Apps. Installation may
also require organization approval. If you do not have that access, ask an
organization owner to help with setup. See
[GitHub's App registration requirements](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/registering-a-github-app).

Replace `your-org` with your organization:

```sh
kenn-forge-github-app create --org your-org
kenn-forge-github-app list
kenn-forge daemon restart
```

The create command opens a browser for App creation and installation. Choose
the organization and repositories Forge should read. Omit `--org` to create a
personally owned App. If you need to finish installation later, run
`kenn-forge-github-app install --owner your-org`.

Forge's [local sync budget](#sync-budget) still applies to ordinary App sync
reads. Raise
`sync_budget_per_hour` if the local ceiling stops sync while the installation
has quota left.

For a busy historical archive, create a second App with its own installation
budget:

```sh
kenn-forge-github-app create --org your-org --role archive
kenn-forge-github-app install --app-id <archive-app-id>
```

The archive App must be installed on the same repository account. Archive
reads use it only for repositories it covers; ordinary sync and mutations keep
using the normal App/PAT routes. The two Apps must be distinct GitHub Apps.
The role is also visible in `kenn-forge-github-app list` and can be set
explicitly as `role = "archive"` in `[[github_apps]]`.

The CLI writes `[[github_apps]]` entries. Mutations still use a user PAT.
After changing selected repository access on GitHub, run
`kenn-forge-github-app install` again and restart Forge.

Selected repository access is a startup routing snapshot. New grants use the
PAT route until refresh. Revoked App access can return 404, and Forge does
not retry that response with a PAT because 404 can also mean missing or private.

## Airplane mode

Turn on **Settings → Sync → Airplane mode** to pause automatic background
sync after any running update finishes. Relay updates, opening an item, and
manual **Sync** remain available, including a manually requested full sync.
The status bar shows **airplane mode** while enabled; click it to return to
the setting.

Airplane mode is off by default and stays enabled across restarts until you
turn it off. It applies only to the Forge instance you configure. On a fleet
spoke, it pauses local background refresh; the hub keeps its own setting.

The equivalent top-level option, before any `[section]` header, is:

```toml
airplane_mode = true
```

Changes apply without restarting Forge.

## Sync budget

`sync_budget_per_hour` limits the API requests Forge spends on live background
sync each hour. It defaults to 500. For a large or active repository, raise
**Hourly sync budget** under **Settings > Sync**, or set it in
`~/.kenn/forge/config.toml`:

```toml
sync_budget_per_hour = 3000
```

Put this at the top level, before any `[section]` or `[[repos]]` header. If
the key already exists, change its value. Either way the new limit applies
without a restart and keeps the requests already spent this hour.

The value must be between 50 and 15,000, the largest hourly quota GitHub
grants a single account. Omitting it or setting it to `0` uses the
500-request default; zero does not disable the ceiling.

The same configured limit applies to each budget. GitHub repositories share a
budget when they use the same GitHub user or App installation on a host.
Other providers share a budget per provider host.

Forge reserves 10% for discovering new and closed issues and pull requests.
With a limit of 3,000, optional live work such as detail refreshes
stops at 2,700, while discovery can use the full 3,000.

GitHub archive requests reserved against provider quota do not spend this
local allowance. Other archive paths can use it. See
[Archive sync capacity](archive.md#sync-capacity) for provider reserves and
local-budget behavior.

Raising this value lets Forge use more of your provider quota. It does not
increase that quota. Leave room for other tools that use the same account,
and check the provider's remaining quota before raising it again. See
[Local sync ceiling reached](troubleshooting.md#local-sync-ceiling-reached)
for recovery steps.

If GitHub's quota is the bottleneck, [set up a GitHub App](#github-app-reads)
to move sync reads to an installation quota.

## Activity defaults

Set these in **Settings → Activity**, or edit the `[activity]` section:

```toml
[activity]
view_mode = "threaded"
time_range = "7d"
hide_closed = false
hide_bots = false
collapse_threads = true
item_types = ["pr", "issue"]
event_types = ["comment", "review", "commit", "force_push"]
hide_notifications = false
hide_default_branch = false
roll_up_commits = false
default_branch_retention_days = 90
default_branch_max_commits = 5000
```

Filter defaults apply when opening Activity; explicit filters in the URL take
precedence. An empty `item_types` or `event_types` list selects none. The
`commit` event filter controls default-branch commits; PR commits remain part of
their PR timeline. The retention limits control locally stored default-branch history.

## App modes

```toml
[modes]
activity = true
repos = true
docs = false
actions = false
pulls = true
issues = true
workspaces = true
```

Actions and Docs default to `false`; enable them after their provider workflows
or local folders are ready. Disabling Actions removes both its top-level page
and pull-request menu while leaving provider workflow access available to API
clients. Kata integration is contextual rather than a top-level mode. Set any
other mode to `false` to hide it.

## Roborev

The Reviews panel in local workspaces reads from a separately running Roborev
daemon when displayed. The default endpoint is `http://127.0.0.1:7373`:

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

See [Integrations](integrations.md#review-roborev-jobs) for the Reviews panel
workflow.

## Workspace agents

Forge detects built-in agents on `PATH`. Add or override an agent with:

```toml
[[agents]]
key = "review"
label = "Review Agent"
command = ["review-agent", "--fast"]
```

You can also edit agents under **Settings → Agents**.

## Quick actions

A quick action creates the workspace for the current pull request or issue,
launches one configured agent, and sends a preset prompt as its first message.
Quick actions appear behind the lightning icon on the **Create Workspace**
button. Configure them under **Settings → Quick actions** or in TOML:

```toml
[[quick_actions]]
label = "Rebase"
agent = "codex"
prompt = "rebase this pull request onto main"

[[quick_actions]]
label = "Triage"
agent = "opencode"
prompt = """
/triage-pr
question all assumptions
"""
```

`agent` is a workspace agent key. Labels must be unique (case-insensitive).
Prompts may contain printable text and line breaks, and are limited to 64 KiB,
the same rules as a terminal agent's first message. A
quick action whose agent is missing or unavailable stays visible but disabled
until the agent is fixed.

## Workspace terminals and tmux

Workspace terminals and agent sessions run on a dedicated tmux server
(socket name `kenn-forge`), so busy Forge sessions do not contend with
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
running there after an upgrade, but Forge no longer sees them.
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

Forge reads Kata daemon definitions from `$KATA_HOME/config.toml`, or
`~/.kata/config.toml` when `KATA_HOME` is unset. Daemon credentials and URLs
stay in Kata's catalog rather than Forge configuration.

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
- `base_path` adds a URL prefix behind a reverse proxy. It cannot start with
  `api`, `healthz`, or `livez`, which Forge serves at the root.
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

When the reverse proxy preserves the browser's original `Host` header, keep
forwarded-host trust disabled:

```toml
allowed_hosts = ["build-a.<tailnet>.ts.net"]
trust_reverse_proxy = false
```

In this mode Forge validates the raw `Host` value and ignores forwarded-host
headers. This is the simpler choice for a private Caddy or Tailscale proxy that
does not rewrite `Host`.

Set `trust_reverse_proxy = true` only when every browser request comes through
a trusted proxy that supplies an accepted `Forwarded` or `X-Forwarded-Host`
value. Direct browser requests without that forwarded authority are rejected.
Host validation does not consume a forwarded scheme; configure HTTPS at the
proxy and use HTTPS origins for federation.

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
require a daemon restart. The companion stays on `127.0.0.1` when `host` is
not loopback; clients on other machines use `/mcp` on the main port with the
daemon bearer token instead (see [Kenn Forge MCP](kenn-forge-mcp.md)).

## Pull request stacks

```toml
[pull_requests]
prefer_github_native_stacks = true
allow_mid_stack_merges = false
```

Native GitHub stack data improves read-only detection when complete. Branch
relationships remain the fallback. Forge does not create or reorder
stacks. Mid-stack merges stay blocked by default.

## Telemetry

Forge sends limited anonymous telemetry by default: daemon activity, an app
open about once a day per browser, which screens were opened (each counted once
per installation per UTC day), version, commit, OS and architecture, and an
anonymous install ID. The browser reports app opens to the daemon, never to the
analytics service.
Closing a tab or leaving it hidden for 30 minutes reports `session_ended` with
`surface: web` and a `duration_bucket` of `under_1m`, `1_to_5m`, `5_to_30m`,
`30m_to_2h`, or `over_2h`.
Tabs opened before an upgrade may report `over_30m`, meaning more than 30 minutes
without finer detail. Forge accepts that bucket and rejects missing or unknown durations.
Each duration sums visible time across tab switches and excludes hidden time.
A hidden tab that the browser or phone closes without notice reports nothing.
Screen names are activity, actions, repos, repo-browser, pulls, issues, docs,
workspaces, terminal, workspace-item, settings, project-intake, design-system,
and onboarding. Screen counts persist across tabs and daemon restarts.
An upgrade preserves prior screen counts in the private `telemetry-daily.json`
file beside the database. Keep that file to retain daily counts across restarts.
It does not send repository names, item content, tokens, usernames, hostnames,
or paths.

Disable telemetry with:

```sh
TELEMETRY_ENABLED=0 kenn-forge daemon start
```

## Container startup

Build a local image from the repository root:

```sh
docker build -t kenn-forge:local .
```

The image runs `kenn-forge serve` directly as uid/gid 1000, with git, tmux,
OpenSSH client, curl, and CA certificates. Release image publication is managed
by the external release pipeline.

Persist `/home/forge` so configuration, repositories, and the automatically
minted bearer token survive container replacement. Bind-mounted directories
must be writable by uid 1000. A Docker named volume inherits the image ownership.

For browser access from outside the Docker host, put an HTTPS reverse proxy in
front of Forge and keep the direct HTTP port private. This example publishes
the port only on host loopback and assumes the proxy runs on the Docker host:

```sh
docker run -d --name forge \
  -p 127.0.0.1:8091:8091 \
  -v forge-home:/home/forge \
  -e KENN_FORGE_ALLOWED_HOSTS=127.0.0.1:8091,forge.example \
  -e KENN_FORGE_TRUST_REVERSE_PROXY=true \
  kenn-forge:local
```

Configure the proxy to terminate HTTPS for `forge.example`, forward to
`127.0.0.1:8091`, and send the original public authority in
`X-Forwarded-Host` or `Forwarded`. For example, this Caddy site requires a
hostname with a trusted certificate:

```caddyfile
forge.example {
    reverse_proxy 127.0.0.1:8091 {
        header_up X-Forwarded-Host {http.request.host}
    }
}
```

The allowlist includes both the backend and public authorities because Forge
checks the raw `Host` and forwarded public host. If the proxy runs in another
container, connect it to Forge over a private Docker network and do not publish
Forge's port to host interfaces.

Read the login token with `docker exec forge cat /home/forge/.kenn/forge/auth_token`,
then open `https://forge.example/?auth_token=<token>` through the proxy. Do not
open the token-bearing URL over the direct HTTP listener. Keep the token out of
proxy access logs. The persisted token file stays private (mode 0600).
The image defaults to an authenticated `0.0.0.0:8091` listener. Wildcard listeners
require API authentication; existing specific-address configurations retain
their behavior. Declare each external/backend Host authority you use.

Startup settings accept these environment variables:

| Variable | Setting |
| --- | --- |
| `KENN_FORGE_HOST` | Listen IP |
| `KENN_FORGE_PORT` | Listen port, 1–65535 |
| `KENN_FORGE_ALLOWED_HOSTS` | Comma-separated exact Host authorities; empty clears the list |
| `KENN_FORGE_REQUIRE_AUTH` | Require bearer authentication |
| `KENN_FORGE_TRUST_REVERSE_PROXY` | Validate forwarded Host as well as backend Host |
| `KENN_FORGE_ROBOREV_ENDPOINT` | Absolute HTTP(S) URL of a separate Roborev service |
| `KENN_FORGE_DATA_DIR` | Data directory |
| `KENN_FORGE_HOME` | Configuration/discovery home |

`serve --host` and `--port` override environment values. Precedence is flags,
environment, TOML, then defaults. Invalid winning values stop startup; malformed
TOML still fails. Runtime overrides survive file reloads and remain outside the
saved TOML, so changing settings does not seed deployment values into the file.
Startup logs report effective settings and their sources.

The image probes `/healthz` at IPv4 loopback using `KENN_FORGE_PORT` and a matching
forwarded Host, including in trusted-proxy mode. If you choose a specific host or an IPv6-only bind, or override the port through
a flag or TOML, supply a matching Docker healthcheck.
`/livez` reports process liveness; `/healthz` reports application readiness.

Run Roborev as a separate service and set its endpoint above. Install agent CLIs
into the persistent `/home/forge/.local/bin`, or build a derived image with them.
