# Configuration

Kenn Forge reads its configuration from `~/.kenn/forge/config.toml`. To move
both configuration and app data to another directory, set `KENN_FORGE_HOME`.
Most setups need only repositories, credentials, and any optional modes you
want enabled.

For routine changes, use Settings in the app. For provider hosts and advanced
options, edit the TOML file directly. If you change startup settings, restart
Forge.

To configure private PR cards and action buttons, add TOML-only
[`external_context` sources](external-pr-context.md).

## Repositories

### Faster GitHub updates

An optional shared relay receives messages from GitHub when something changes.
Forge stays connected to the relay, receives each change as it happens, and
fetches the changed items with its own GitHub credentials. To enable it, add
the private feed URL from the person running the relay and restart Forge:

```toml
[relay]
url = "https://relay.example.com"
```

Use the HTTPS origin without `/activity` or a webhook path. In a fleet, add
this setting on the hub. Leaving the URL empty disables the relay while normal
syncing continues. For setup, access, and a walkthrough to check that it works,
see [Faster GitHub updates](activity-relay.md).

The relay supports GitHub.com. When `workflow_run` and `check_run` webhooks are
enabled, the relay batches Actions updates once per minute, and Forge refreshes
PR checks as each batch arrives. While **Actions** is open, Forge also
refreshes the selected workflow's run list. The usual API limits still apply,
and no extra setting is needed.

### Repository selection

For a GitHub repository on `github.com`, you need only its owner and name:

```toml
[[repos]]
owner = "team"
name = "service"
```

You can also paste a common HTTPS or SSH repository URL into `owner` or `name`,
and Forge normalizes it for you.

For other services or self-hosted instances, set the provider and host:

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

Repository identity consists of `platform`, `platform_host`, `owner`, and
`name`. Keep `repo_path` when the provider uses nested namespaces or canonical
casing.

### Self-hosted Gitea transport

Gitea uses `https://<host>` by default. If a trusted private deployment serves
Gitea over HTTP, set an explicit API URL:

```toml
[[platforms]]
type = "gitea"
host = "gitea.example.test:3000"
base_url = "http://gitea.example.test:3000"
allow_insecure = true
token_env = "GITEA_PRIVATE_TOKEN"
```

`host` remains the repository identity and must match Gitea's advertised clone
URLs. Setting `allow_insecure = true` acknowledges that API and Git credentials
can travel without TLS. Before Forge uses a clone URL, it always rejects URLs
that do not match the configured host and repository, and it rejects plain-HTTP
clone URLs unless `allow_insecure = true` is set.

To hide a repository from lists and pickers without removing it, open the gear
menu on its Settings row and choose **Hide from UI**. Syncing continues and
direct links keep working; choose **Show in UI** to bring it back. Hiding
applies to exact repositories, not glob patterns.

## Credentials

Forge scopes credentials by provider and host:

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

Setting `token_file` or `token_env` on an exact repository overrides broader
credentials. Forge skips any empty file or variable. Because Forge reads token
files on each request, replacing a file atomically rotates that credential.

If you do not set a token source for a host, Forge reuses the credential the
provider's CLI stores for that host:

- **GitHub.** `gh auth token --hostname HOST`. The unscoped fallback applies
  only to `github.com`.
- **GitLab.** The token `glab` holds for the host, from its config file or the
  operating-system keyring. Forge does not read `GITLAB_TOKEN` or similar
  variables here; declare `token_env` to use one.
- **Forgejo and Gitea.** The token that the `fj` CLI
  ([forgejo-cli](https://codeberg.org/forgejo-contrib/forgejo-cli)) saves for
  the host in its keys file. Forge skips expired OAuth logins; run any `fj`
  command against the host to refresh them.

Authenticate a host with the matching CLI:

```sh
gh auth login --hostname HOST
glab auth login --hostname HOST
fj --host HOST auth login
```

Public hosts use these defaults:

- **GitHub `github.com`.** `KENN_FORGE_GITHUB_TOKEN`, then the GitHub CLI.
- **GitLab `gitlab.com`.** No implicit variable, then the GitLab CLI.
- **Forgejo `codeberg.org`.** `KENN_FORGE_FORGEJO_TOKEN`, then the Forgejo CLI.
- **Gitea `gitea.com`.** `KENN_FORGE_GITEA_TOKEN`, then the Forgejo CLI.
- **Bitbucket Cloud `bitbucket.org`.** `KENN_FORGE_BITBUCKET_TOKEN`, then
  host-scoped `BKT_*` settings.

Grant read access for monitoring. Add write access only when you want comments,
reviews, state changes, edits, or merges.

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
API token, or to the token alone for an OAuth or repository access token. You
can use `token_file` instead of `token_env`. Grant read access to repositories
and pull requests for monitoring, and add write access for actions such as
commenting, reviewing, and merging. To pick reviewers by name, grant
`read:workspace:bitbucket` as well. Forge lists workspace members and submits
their account IDs. You can still remove existing reviewers who are not
workspace members.

Merging in Forge requires an account API token whose repository write permission
Forge can check. OAuth and repository access tokens can read data, but Forge
does not enable merges with them. Branch restrictions and token scopes still
apply.

If you use the community `bkt` CLI, Forge can read its environment settings.
Set `BKT_HOST=https://bitbucket.org` and `BKT_TOKEN`, plus
`BKT_AUTH_METHOD=bearer` for bearer tokens or `BKT_USERNAME` for basic
authentication. Forge uses its own configured credentials first and does not
read saved `bkt` logins or Atlassian `acli` credentials.

With Bitbucket Cloud, you can:

- Discover repositories and follow pull requests, tags, and commit statuses.
- Read, create, and comment on issues when the repository has issues enabled.
- Comment, reply, view and resolve inline threads, and change reviewers.
- Approve pull requests, request changes, and merge using merge, squash, or
  rebase.

Forge does not yet support Cloud workflow controls, notifications, labels,
publishing review drafts, editing pull requests or changing their state outside
a merge, or collecting historical archives.

### Bitbucket Data Center

Set `platform_host` to your server's hostname, `owner` to the project key, and
`name` to the repository slug. Forge uses Cloud for `bitbucket.org` and Data
Center for all other hosts.

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
and commit statuses, and approve, edit, reopen, or merge pull requests. Merging
requires a user personal access token with repository write permission. Forge
offers merge, squash, and rebase when enabled in the repository settings, and
branch restrictions still apply.

Forge does not yet support Data Center comments or inline threads, issues,
workflows, notifications, reviewer changes, or publishing review drafts. Your
server must serve Bitbucket at the hostname root, such as
`https://bitbucket.example.com/`. A path such as
`https://example.com/bitbucket/` is not supported.

### GitHub credentials by owner

When one token cannot read every repository, map fine-grained PATs to owners:

```toml
[[github_owner_tokens]]
owner = "org-a"
token_env = "KENN_FORGE_GITHUB_TOKEN_ORG_A"

[[github_owner_tokens]]
owner = "org-b"
token_file = "~/.kenn/forge/tokens/org-b"
```

Configure owner mappings in TOML only.

For GitHub, Forge selects credentials by host and owner. If a GitHub App
installation covers a repository, that installation handles reads even when the
repository also sets its own `token_file` or `token_env`. Among PATs, exact
repository credentials come first, ahead of owner PATs. Owner PATs handle
uncovered reads and user-attributed writes. Host credentials come next in the
order, and the GitHub CLI comes last.

PATs for the same GitHub user share one rate limit and sync budget, while
different users and App installations have separate budgets. After routing an
owner to a PAT from a different GitHub user, restart Forge.

### GitHub App reads

GitHub Apps are an advanced option when you need more sync capacity. Forge uses
an App's installation quota for sync reads, leaving your personal quota for
other tools and actions.

An App can read only the repositories covered by its installation and granted
permissions. Owning the App does not grant repository access, and an
installation in one organization does not cover another organization's
repositories. Repositories outside that installation still need another
credential route.

Each installation owns its own API quota. Repositories in that installation
share it, and installing an App does not raise your personal token's rate
limit.

On GitHub.com, installation REST limits start at 5,000 requests per hour and
can scale to 12,500 with repository and organization size. Installations on
GitHub Enterprise Cloud organizations get 15,000. For current rules, see
[GitHub's installation rate limits](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api#primary-rate-limit-for-github-app-installations).

To create an organization-owned App, you need organization owner access or
permission to manage all of the organization's GitHub Apps, and installation
may also require organization approval. If you do not have that access, ask an
organization owner to help with setup. See
[GitHub's App registration requirements](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/registering-a-github-app).

Replace `your-org` with your organization:

```sh
kenn-forge github-app create --org your-org
kenn-forge github-app list
kenn-forge daemon restart
```

The `create` command opens a browser for App creation and installation, where
you choose the organization and repositories Forge should read. To create a
personally owned App, omit `--org`. If you need to finish installation later,
run `kenn-forge github-app install --owner your-org`.

Forge's [local sync budget](#sync-budget) still applies to ordinary App sync
reads. If the local ceiling stops sync while the installation still has quota
left, raise `sync_budget_per_hour`.

For a busy historical archive, create a second App with its own installation
budget:

```sh
kenn-forge github-app create --org your-org --role archive
kenn-forge github-app install --app-id <archive-app-id>
```

Install the archive App on the same repository account. Archive reads use it
only for repositories it covers, while ordinary sync and mutations keep using
the normal App and PAT routes. The two Apps must be distinct GitHub Apps. You
can see the role in `kenn-forge github-app list` and set it explicitly as
`role = "archive"` in `[[github_apps]]`.

The `github-app` commands save `[[github_apps]]` entries to your configuration.
Forge still uses a user PAT for mutations. After changing selected repository
access on GitHub, run `kenn-forge github-app install` again and restart Forge.

Selected repository access is a startup routing snapshot. Newly granted
repositories keep using the PAT route until that snapshot is refreshed. Revoked
App access can return 404, and Forge does not retry that response with a PAT
because 404 can also mean a missing or private repository.

## Airplane mode

To pause automatic background sync after any running update finishes, turn on
**Settings → Sync → Airplane mode**. Relay updates, opening an item, and manual
**Sync** remain available, including a manually requested full sync. While it
is on, the status bar shows **airplane mode**; click it to jump back to the
setting.

Airplane mode defaults to off. Once you turn it on, it stays enabled across
restarts until you turn it off. It applies only to the Forge instance you
configure. On a fleet spoke, it pauses local background refresh while the hub
keeps its own setting.

To configure it in TOML, add this top-level option before any `[section]`
header:

```toml
airplane_mode = true
```

Changes take effect without restarting Forge.

## Sync budget

`sync_budget_per_hour` limits how many API requests Forge spends on live
background sync each hour, with a default of 500. For a large or active
repository, raise **Hourly sync budget** under **Settings → Sync**, or set it
in `~/.kenn/forge/config.toml`:

```toml
sync_budget_per_hour = 3000
```

Place this key at the top level, before any `[section]` or `[[repos]]` header,
or update the existing key if it is already there. Either way, the new limit
applies without a restart and keeps the count of requests already spent this
hour.

The value must be between 50 and 15,000, the largest hourly quota GitHub grants
a single account. Omitting it or setting it to `0` uses the 500-request
default; zero does not disable the ceiling.

The same configured limit applies to each budget. GitHub repositories share a
budget when they use the same GitHub user or App installation on a host, while
other providers share a budget per provider host.

Forge reserves 10% for discovering new and closed issues and pull requests.
With a limit of 3,000, optional live work such as detail refreshes stops at
2,700, while discovery can use the full 3,000.

Forge spreads watched pull request refreshes and detail fetches across the
hour. It can pause while part of the ceiling is still unspent and resume as the
hour goes on.

To see this hour's `spent` total broken down by kind, run
`kenn-forge rate-limits`:

- `discovery`: repository sync, including issue and pull request lists,
  repository overviews, releases, and tags, plus notification polling.
- `watched_pr_refresh`: refreshes of watched pull requests.
- `detail`: issue and pull request reads, comments, user lookups, and
  notification read updates.
- `archive`: archive requests charged to this allowance.

The four subtotals add up to `spent`.

GitHub archive requests reserved against provider quota do not spend this local
allowance, while other archive paths can use it. For provider reserves and
local-budget behavior, see [Archive sync capacity](archive.md#sync-capacity).

Raising this value lets Forge use more of your provider quota, but it does not
increase that quota. Leave room for other tools that use the same account, and
check your provider's remaining quota before raising it again. For recovery
steps, see
[Local sync ceiling reached](troubleshooting.md#local-sync-ceiling-reached).

If GitHub's quota is the bottleneck, [set up a GitHub App](#github-app-reads)
to move sync reads to an installation quota.

## Activity defaults

Configure Activity defaults under **Settings → Activity**, or edit the
`[activity]` section:

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

These filter defaults apply when you open Activity, and explicit filters in the
URL take precedence. An empty `item_types` or `event_types` list selects none.
The `commit` event filter controls default-branch commits, while PR commits
stay part of their PR timeline. The retention limits control how much
default-branch history Forge stores locally.

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

Actions and Docs default to `false`; enable them once their provider workflows
or local folders are ready. Disabling Actions hides both its top-level page and
pull-request menu while leaving provider workflow access available to API
clients. Kata integration is contextual rather than a top-level mode. To hide
any other mode, set it to `false`.

## Roborev

When displayed, the Reviews panel in local workspaces reads from a separately
running Roborev daemon. The default endpoint is `http://127.0.0.1:7373`:

```toml
[roborev]
endpoint = "http://127.0.0.1:7373"
init_managed_clones = true
```

The endpoint controls the Reviews connection. If you change it, restart Forge.
`init_managed_clones` is optional and defaults to `false`, and you can also
change it under **Settings → Workspaces** without restarting. When
`init_managed_clones` is enabled, `endpoint` must use loopback HTTP
(`127.0.0.1`, `localhost`, or `[::1]`) because Forge passes it to the Roborev
CLI during workspace setup.

For the Reviews panel workflow, see
[Integrations](integrations.md#review-roborev-jobs).

## Semantic search

Forge can embed pull request and issue titles, descriptions, and comments so
agents can find work by meaning instead of keywords, for example when building
a worklist about "flaky CI" or looking for duplicates of a bug. Code and diffs
are never embedded.

To enable local search, run `kenn-forge embeddings local`. Forge prefers a
running Ollama with Nomic, then the shared built-in server. With your consent,
setup downloads missing shared assets. Run setup again if you want to switch
from automatic discovery or a configured endpoint to the built-in server; a
configured endpoint takes precedence over `local_embeddings = "builtin"`.
Restart Forge after setup.

Turning on the built-in server records a shared choice. When another Kenn app
records that choice, the Forge that syncs providers enables the built-in server
at its next launch if it has no embeddings configuration. Ollama setup stays
local to Forge, and a saved built-in configuration enables Forge without
recording shared consent. Forge has no setting or command to clear the shared
choice.

To configure an endpoint yourself, for example Ollama:

```toml
[embeddings]
base_url = "http://127.0.0.1:11434/v1"
model = "nomic-embed-text"
dims = 768
document_prefix = "search_document: "
query_prefix = "search_query: "
```

You must set `base_url`, `model`, and `dims` together. For a hosted provider,
set `api_key` to the key itself, `{ env = "NAME" }`, or `{ file = "PATH" }`.
Forge accepts plaintext HTTP only for loopback unless
`trust_private_network = true`. Optional `batch_size`, `timeout_seconds`,
`request_dimensions`, `input_type_mode`, `model_context_tokens`, and
`max_batch_tokens` tune requests for endpoints that need it.

The discovered built-in server defaults to eight inputs per request, a
five-minute request timeout, and one request at a time, and explicit
`batch_size` and `timeout_seconds` settings override those defaults. Configured
endpoints and discovered Ollama default to 32 inputs and a 30-second timeout.

The Forge that syncs providers builds the index in `semantic.db` under
`data_dir` after each sync and serves searches for its whole fleet. Changing
`[embeddings]` or `local_embeddings` requires a restart. If you switch a
compatible Nomic recipe to the built-in server, Forge keeps the existing index.
Changing the model, width, fingerprint salt, or prefix rebuilds the index, and
searches return partial results while the backfill runs. The index is derived
data. Deleting `semantic.db` only costs a rebuild.

## Workspace agents

Forge detects built-in agents on `PATH`. To add or override an agent, add an
`[[agents]]` entry:

```toml
[[agents]]
key = "review"
label = "Review Agent"
command = ["review-agent", "--fast"]
env = { REVIEW_MODE = "strict" }
```

Use `env` to set environment variables for that agent only. Forge applies them
over the variables it removes from agent sessions, such as tokens. Each value
is a single line. For a Codex ACP agent,
`env = { INITIAL_AGENT_MODE = "agent-full-access" }` starts every chat in that
mode without Forge first asking Codex which mode its settings allow.

You can also edit agents, including their environment, under
**Settings → Workspace agents**.

## Quick actions

A quick action creates a workspace for the current pull request or issue,
launches one configured agent, and sends a preset prompt as its first message.
Quick actions appear behind the lightning icon on the **Create Workspace**
button. You can configure them under **Settings → Quick actions** or in TOML:

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

`agent` references a workspace agent key, and labels must be unique regardless
of case. Prompts can contain printable text and line breaks up to 64 KiB,
matching the rules for a terminal agent's first message. If a quick action's
agent is missing or unavailable, the action stays visible but disabled until
you fix the agent.

For an ACP chat agent with `protocol = "acp"`, a quick action can also pick the
chat's model, mode, effort, or any other option the agent offers. Under
**Settings → Quick actions**, open **Agent options** on the action, and Forge
starts the agent once to list its choices. Any option left on
**Remembered choice** uses whatever you last picked in a chat with that agent.
In TOML, key each option by the ID the agent reports:

```toml
[[quick_actions]]
label = "Deep review"
agent = "chat"
prompt = "review this pull request"

[quick_actions.acp_options]
model = "deep"
effort = "high"
```

These choices apply only to chats started by the quick action and never change
your remembered choices. If the agent no longer offers a configured choice, the
chat starts without it and lets you know.

## Where new workspaces run

`workspaces.default_execution_target` names the machine where workspaces
created from this Forge's UI run. Leave it empty to create workspaces on this
Forge. It accepts a fleet member's HTTPS origin or node ID, or `devbox:<id>`
for a connected devbox:

```toml
[workspaces]
default_execution_target = "https://build-b.example.test"
```

Naming this Forge itself creates workspaces locally. If the named machine is
unavailable, Forge never falls back to this Forge; instead, the page offers the
machines that are available. See
[Choose where new workspaces run](federated-fleet.md#choose-where-new-workspaces-run).

## Workspace terminals and tmux

Workspace terminals and agent sessions run on a dedicated tmux server using the
`kenn-forge` socket, so busy Forge sessions do not contend with your personal
tmux server. To inspect or attach from a regular terminal:

```sh
tmux -L kenn-forge ls
tmux -L kenn-forge attach-session -E -t <session>
```

Processes inside the workspace can read the session environment. Passing `-E`
keeps your shell's variables out of that environment, and if you widened tmux's
`update-environment`, it keeps out any exported provider tokens too.

To pick a different socket or wrap the launch, set `[tmux] command`. Forge uses
the configured command line verbatim:

```toml
[tmux]
command = ["tmux", "-L", "kenn-forge"]
```

Wrapper commands run with a minimal non-secret environment because the tmux
server permanently retains the environment it starts with. Pass any custom data
a wrapper needs as command-line arguments rather than environment variables.
For the same reason, provider `token_env` names cannot reuse standard terminal
variables such as `EDITOR` or `PATH`, and configuration validation rejects the
collision.

Sessions started by older versions that used the default tmux server keep
running there after an upgrade, though Forge no longer sees them. Reattach or
clean them up with plain `tmux ls` and `tmux kill-session`.

### Stopping idle workspaces

Without tmux, such as on Windows, each workspace's agents and shells keep
running until you delete the workspace, holding their consoles. To stop them in
workspaces nobody has looked at for a set number of hours, configure an idle
timeout:

```toml
[workspaces]
idle_runtime_stop_hours = 12
```

A workspace counts as in use while its page is open in a visible browser tab
and whenever someone types into one of its terminals or chats from any client.
An agent stops once it has finished its turn or is waiting on an approval or an
answer, provided it can resume its conversation and has no pending background
work or scheduled wakeups. Any pending approval or question is lost; ask again
after it resumes. A working agent, a terminal agent that was never prompted, and chat agents started before you upgraded Forge keep running, as do
command sessions and terminals in project worktrees. Shell tabs stop as well,
along with anything running in them. The workspace terminal stops unless a
terminal is connected to it and starts a fresh shell when opened. Because Forge
learns that a terminal agent finished its turn or resumed a conversation from
the agent's activity hooks, an agent without those hooks keeps running.

Opening the workspace page resumes each stopped agent's conversation and starts
fresh shells. A stopped chat displays its conversation right away and restarts
its agent when you send a message. A runtime used only from a non-browser
client waits for the page to show before resuming. Forge's tools report a
stopped runtime as `parked`. Idle time carries across Forge restarts, and a
restart relaunches nothing that stopped. Turning the setting on starts every
workspace's clock at that moment. Turning it off hands stopped agents and chats
back to normal recovery, and stopped shell tabs show as errored, as after a
reboot. The default value, `0`, never stops idle runtimes.

## Docs folders

Register local Markdown folders from the CLI:

```sh
kenn-forge docs add-folder --name Notes ~/notes
kenn-forge docs list-folders
kenn-forge docs remove-folder notes
```

The equivalent TOML configuration is:

```toml
[[doc_folders]]
id = "notes"
name = "Notes"
path = "/Users/you/notes"
daemon = "kata-main"
```

`daemon` is optional. Set it when task links in that folder always belong to
one Kata daemon.

## Kata daemons and repository mappings

Forge reads Kata daemon definitions from `$KATA_HOME/config.toml`, or from
`~/.kata/config.toml` when `KATA_HOME` is unset. Daemon credentials and URLs
stay in Kata's catalog rather than in Forge configuration.

To see which repository each Kata project uses for workspace creation, open
**Settings → Kata mappings**. When automatic matching does not pick the right
configured repository, add a manual mapping:

```toml
[[kata_projects]]
daemon_id = "kata-main"
project_uid = "widgets"
provider = "github"
platform_host = "github.com"
repo_path = "acme/widgets"
```

`daemon_id` scopes the mapping to one daemon; omit it only when the mapping
should apply to the same project UID on every daemon. Pick an exact repository
identity available in Settings. That repository can come from a configured
pattern, a tracked repository, or a registered project, but the mapping itself
cannot contain a glob.

## Server and storage

```toml
sync_interval = "5m"
host = "127.0.0.1"
port = 8091
base_path = "/"
```

- `sync_interval` sets how often Forge refreshes providers.
- After a restart, accounts with rate tracking resume their schedule from their
  oldest healthy live repository's successful sync plus `sync_interval`, while
  overdue accounts and failed or never-synced repositories refresh at startup.
- `host` and `port` set the listener address and port.
- `base_path` adds a URL prefix behind a reverse proxy. It cannot start with
  `api`, `healthz`, or `livez`, which Forge serves at the root.
- `data_dir` moves app data while leaving config under `KENN_FORGE_HOME`.

For a trusted reverse proxy or a larger SSE replay window:

```toml
allowed_hosts = ["forge.example.com", "proxy.example.com:8091"]
trust_reverse_proxy = true
sse_buffer_size = 256
```

`allowed_hosts` accepts exact host and port values, and a trusted proxy must
present accepted direct and forwarded hosts. `sse_buffer_size` defaults to 256
and accepts values from 16 through 16384.

If your reverse proxy preserves the browser's original `Host` header, keep
forwarded-host trust disabled:

```toml
allowed_hosts = ["build-a.<tailnet>.ts.net"]
trust_reverse_proxy = false
```

In this mode, Forge validates the raw `Host` value and ignores forwarded-host
headers. That is the simpler choice for a private Caddy or Tailscale proxy that
does not rewrite `Host`.

Set `trust_reverse_proxy = true` only when every browser request comes through
a trusted proxy that supplies an accepted `Forwarded` or `X-Forwarded-Host`
value; Forge rejects direct browser requests that lack that forwarded
authority. Host validation does not consume a forwarded scheme. Configure HTTPS
at the proxy and use HTTPS origins for federation.

### Tailscale Serve access

When you publish Forge with Tailscale Serve, allowed Tailscale logins open it
without a token. API authentication must stay on or Forge refuses to start, and
other clients still need the token:

```toml
[api]
require_auth = true

[api.tailscale_serve]
enabled = true
allowed_users = ["you@example.com"]
app_capability = "example.com/cap/forge"
```

Tagged devices such as CI runners send no login, so `allowed_users` cannot
admit them. Instead, set `app_capability` to a capability granted in your
tailnet policy. Anyone holding that capability gets full Forge access, the same
as an allowed login. Enabled mode requires `allowed_users`, `app_capability`,
or both, and you must restart Forge after changing either.
`kenn-forge fleet setup hub --tailscale` fills in the logins and preserves any
`app_capability` you added. For the tailnet grant and the Serve command, see
the [fleet guide](federated-fleet.md#set-up-the-hub).

### MCP companion

The MCP companion starts by default on Forge's own port when `host` is a
loopback IP, `mcp.enabled` and `mcp.port` are omitted, and Forge is not an
execution worker. Loopback IP hosts include `127.0.0.1`; other hosts, such as
`0.0.0.0` or a tailnet IP, require `enabled = true`. To turn the companion off:

```toml
[mcp]
enabled = false
```

With `enabled = true` and no `port`, MCP also uses Forge's own port. To run a
separate loopback listener, set `port`. A saved `port` with `enabled` omitted
keeps MCP off. Older versions dropped explicit `false` values on save. Set
`enabled = false` again to preserve an earlier opt-out.

The sessionless Streamable HTTP endpoint is
`http://127.0.0.1:<resolved-port>/mcp`, and authentication follows
`[api].require_auth`. Full diff files use a request-based least-recently-used
cache whose size `diff_cache_mb` defaults to 128 MiB. Changing the MCP listener
or cache requires a daemon restart. When `host` is not loopback, the companion
stays on `127.0.0.1`; clients on other machines use `/mcp` on the main port
with the daemon bearer token instead. For details, see
[Kenn Forge MCP](kenn-forge-mcp.md).

## Pull request stacks

```toml
[pull_requests]
prefer_github_native_stacks = true
allow_mid_stack_merges = false
```

By default, Forge reads GitHub's own stack membership, so a stack created with
`gh stack` shows **Merge stack** or **Enqueue stack** on its top pull request,
and that single action lands every unmerged pull request below it. To detect
stacks from branch relationships alone, set
`prefer_github_native_stacks = false`; Forge also falls back to branch
relationships when GitHub's stack data is unusable. Forge never creates or
reorders stacks. For stacks Forge infers from branches, it blocks mid-stack
merges by default.

## Usage reporting

Forge can send general, anonymous usage telemetry. Unless you opt in or set
`TELEMETRY_ENABLED=1`, telemetry is off. Development builds never send it.

Even after you opt in, any of `KENN_FORGE_TELEMETRY_ENABLED=0`,
`TELEMETRY_ENABLED=0`, `DO_NOT_TRACK=1`, or `CI` turns it off. Restart the
daemon after changing these variables.

## Container startup

Build a local image from the repository root:

```sh
docker build -t kenn-forge:local .
```

The image runs `kenn-forge serve` directly as uid and gid 1000, with git, tmux,
OpenSSH client, curl, and CA certificates. An external release pipeline manages
release image publication.

Persist `/home/forge` so configuration, repositories, and the automatically
minted bearer token survive container replacement. Bind-mounted directories
must be writable by uid 1000, while a Docker named volume inherits the image
ownership.

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
`127.0.0.1:8091`, and send the original public authority in `X-Forwarded-Host`
or `Forwarded`. For example, this Caddy site requires a hostname with a trusted
certificate:

```caddyfile
forge.example {
    reverse_proxy 127.0.0.1:8091 {
        header_up X-Forwarded-Host {http.request.host}
    }
}
```

Include both the backend and public authorities in the allowlist because Forge
checks both the raw `Host` and the forwarded public host. If the proxy runs in
another container, connect it to Forge over a private Docker network and do not
publish Forge's port to host interfaces.

To sign in, read the login token with
`docker exec forge cat /home/forge/.kenn/forge/auth_token`, then open
`https://forge.example/?auth_token=<token>` through the proxy. Do not open the
token-bearing URL over the direct HTTP listener, and keep the token out of
proxy access logs. The persisted token file stays private with mode 0600. The
image defaults to an authenticated `0.0.0.0:8091` listener. Wildcard listeners
require API authentication, while existing specific-address configurations keep
their behavior. Declare every external and backend `Host` authority you use.

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

`serve --host` and `--port` override environment values. Precedence runs from
flags to environment variables, then TOML, then defaults. Invalid winning
values stop startup, and malformed TOML still fails. Runtime overrides survive
file reloads and stay outside the saved TOML, so changing settings does not
write deployment values into the file. Startup logs report the effective
settings and where each one came from.

The image probes `/healthz` on IPv4 loopback using `KENN_FORGE_PORT` and a
matching forwarded `Host`, including in trusted-proxy mode. If you choose a
specific host or an IPv6-only bind, or override the port with a flag or TOML,
supply a matching Docker healthcheck. `/livez` reports process liveness, and
`/healthz` reports application readiness.

Run Roborev as a separate service and set its endpoint above. Install agent CLIs
into the persistent `/home/forge/.local/bin`, or build a derived image with them.
