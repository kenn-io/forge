# Devboxes

You can run Kenn Forge agent workspaces in your account on a shared Linux
machine while keeping your usual Forge instance as the controller. From your
controller, you pick work, supervise agents, review diffs, and push branches.
The devbox holds the worktrees, tools, agent processes, and tmux sessions, and
closing your browser does not stop them.

This initial deployment is operator-managed for trusted teams on Tailscale, and
it is not a container sandbox. Each developer gets a separate non-root Linux
account. Administrators can read account data and installed agent credentials,
while your personal GitHub credentials stay on the controller.

## Daily workflow

1. Open **Settings → Workspaces** on your usual Forge hub or standalone Forge.
   Under **Workspace machines**, you see this Forge machine and every devbox
   assigned to you, listed once with its current state: **Not connected**,
   **Online**, **Offline**, or **Maintenance**. The registry that supplied the
   list appears below it. If you have not configured a registry yet, enter the
   address from your operator and select **Find devboxes**. You never enter
   ports or tokens. Select **Connect** beside a machine, and Forge checks its
   account identity and updates that row. To check for new assignments without
   removing saved connections, select **Refresh**.
2. To run new workspaces on a connected machine, select the radio button beside
   it. Existing workspaces stay where you created them, and you can still pick
   another connected machine in the new-workspace dialog. If your default
   machine is unavailable, Forge does not silently move work to your local
   machine. Instead, the settings row shows **Offline** with a **Reconnect**
   action.
3. Create a workspace from a repository, pull request, or issue. Remote pull
   request and issue actions open the workspace in **Workspaces**. The first
   clone of each repository in your account can take longer, and you do not need
   a second local checkout.
4. Start an agent, give it the task, and inspect its output and diff. Files and
   tests stay on the devbox, and the agent uses the tools and subscription
   installed in your account. You can edit files manually if you want, though
   the normal workflow is to ask the agent to make the change.
5. Push the branch. Commits use your configured name and GitHub noreply email,
   while the GitHub App authenticates the push. After you push, Forge checks
   GitHub's resolved author and committer IDs and displays a warning if it
   cannot verify attribution. A verification failure does not retry the push.
6. Review and merge the pull request in your usual trusted Forge or GitHub
   session, then delete the remote workspace explicitly when you finish. This
   initial deployment does not clean up workspaces automatically after a merge.

After your computer wakes from sleep, reconnect to see progress. You receive no
new notifications from worker activity while the controller is disconnected.
Existing terminals and Git work survive an expired source-context lease, though
starting another agent for a pull request or issue needs fresh context from the
controller. If a worker credential changes, select **Reconnect** in settings.
Removing a connection leaves its remote work intact.

This deployment does not include preview-port discovery or forwarding. To
preview an app in your browser, read the port from the agent output and use your
terminal client's explicit port forwarding. Do not expose application listeners
publicly to get a preview.

## Repository access and commit identity

You work in your own Linux account on the devbox. Your operator links that
account's numeric Linux user ID to your GitHub user ID and configures your
commit name and email. Forge uses that link to check repository access, and Git
uses the name and email to identify new commits.

### How the credential broker works

Each devbox runs a small credential broker service that supplies temporary
GitHub credentials to Git. The broker runs under its own service account, which
holds the GitHub App's private key. Development accounts receive short-lived
tokens, and the deployment keeps the App key readable only by the broker service
account. Your personal GitHub credentials stay on the controller.

Repository access follows these steps:

1. **The operator enables the repository.** Your operator installs the
   organization's GitHub App on the repositories you need and adds developer
   accounts to the broker configuration. Connecting a devbox in Forge does not
   itself grant GitHub access.
2. **Git asks for a credential.** When Forge clones a repository, or when Git
   fetches or pushes from a managed worktree, Git asks the broker for access to
   that specific repository. Agents and SSH shells use the same Git helper. For
   forks the App can read, `git` requests use the App token of the fork's
   immediate organization parent, including terminal pushes such as
   `git push https://github.com/contributor-a/project.git HEAD:feature-branch`.
   GitHub limits those pushes to open pull request heads with maintainer edits
   allowed.
3. **The broker identifies the caller and checks access.** The request goes over
   a local Unix socket, where Linux supplies the caller's user ID. The broker
   looks up that ID in its configured account list and checks the linked GitHub
   user's current organization membership, repository permissions, configured
   App installation, and repository identity.
4. **Git receives a temporary token.** The broker requests a GitHub App
   installation token scoped to that single repository. Read access lets you
   clone and fetch, and write access also permits pushes, subject to GitHub's
   branch rules. The helper passes the token to Git without saving it as a
   stored credential.
5. **Later requests repeat the access checks.** The broker can reuse a token
   cached in memory, and it still checks your access before returning it. It
   replaces tokens near expiry, while tokens already issued stay usable until
   they expire or are revoked.

This initial devbox integration supports repositories on `github.com`, and you
do not need a personal access token or `gh auth login` in your devbox account.
For the operator setup, see
[GitHub App and branch rules](#github-app-and-branch-rules).

### How commits are linked to you

Forge sets `user.name` and `user.email` in each managed worktree from your
configured developer identity. Use the GitHub-provided noreply address shown in
your GitHub email settings. GitHub uses the email in a commit to
[associate it with your account](https://docs.github.com/en/account-and-profile/how-tos/email-preferences/setting-your-commit-email-address).
New commits an agent creates in your account therefore use your configured
identity, the same way commits from your SSH shell do.

Each identity has a distinct job:

| Identity | What it means |
| --- | --- |
| GitHub App | Authenticates the push and any pull request actions made through the devbox wrapper. |
| Commit author | The person credited with the original change, recorded in the commit. |
| Committer | The person who created this version of the commit, also recorded in the commit. |

For a new commit, both the author and committer normally identify you. During a
cherry-pick or rebase, Git can preserve someone else's author identity while
recording you as the committer. The App can push either kind of commit without
becoming its author.

Before starting an agent or pushing through Forge, Forge validates the
worktree's configured identity. After a push, the controller asks GitHub which
accounts it resolved for the branch's latest commit and compares their numeric
user IDs with your enrolled GitHub ID. Forge then reports a match, a preserved
original author, a mismatch, or an unverified result. If the email is wrong or
Forge cannot reach GitHub, Forge does not repeat a successful push. Commit
attribution does not provide a cryptographic signature or prove who typed the
change.

## Using an SSH terminal client

You can connect with an SSH terminal client to the same Unix account using your
operator-approved development key with agent forwarding disabled. When the
worker uses that account's default tmux server, you can list and attach to its
existing sessions. Configure `window-size latest` in system tmux settings so two
attached clients do not constrain each other to the smaller terminal. Forge
still owns its worktrees and workspace lifecycle.

To start repository-only work from the remote terminal without running a second
Forge daemon, create a workspace with the API CLI:

```sh
kenn-forge api --config /etc/forge-worker/user-a.toml \
  POST /api/v1/worker/workspaces --data '{"repository":{"provider":"github","platform_host":"github.com","owner":"example-org","name":"project-a"},"branch":"work/task-a"}'
```

The response returns the workspace ID and setup state. Use the same CLI with
`GET /api/v1/workspaces/ID` to wait for `ready`, then start a configured agent:

```sh
kenn-forge api --config /etc/forge-worker/user-a.toml \
  POST /api/v1/workspaces/ID/runtime/sessions --data '{"target_key":"codex"}'
```

For pull request and issue workspaces, create them through the controller. The
worker cannot obtain or validate that source context itself. Git in managed
worktrees uses the same broker helper from Forge, an agent, and an SSH shell. Do
not run `gh auth login` on the devbox. To run supported pull request commands,
use the devbox GitHub wrapper:

```sh
kenn-forge devbox github --socket /run/forge-broker/broker.sock \
  --repository example-org/project-a -- pr create
```

The wrapper supports `pr create`, `edit`, `comment`, `view`, `list`, `checks`,
`close`, `reopen`, and `ready`, and it refuses `close`, `reopen`, and `ready` on
pull requests authored by anyone other than the App. Its token goes only to that
child process. When the broker rejects a request, such as when a repository is
not admitted or you lack write permission for it, Git and the wrapper explain
the failure.

## Operator setup

Forge provides generic worker, registry, and credential-broker commands, while
your provisioning repository owns users, SSH admission, packages, service units,
roster assignments, agent credentials, and network policy. The examples below
use ordinary Linux accounts, systemd, and Caddy, and require no private
deployment tooling. Keep your chosen versions and configuration in your own
automation.

### What runs where

| Host | Processes and state |
| --- | --- |
| Your existing controller | Full Forge with its browser UI, provider access and saved devbox connections. |
| Each devbox | One credential broker and one worker per developer account. Worktrees, tools, agent credentials and tmux sessions live here. |
| Registry host | One registry process, a roster and copies of worker connection tokens, behind an HTTPS proxy. It needs no worktrees, agents or GitHub App key. |

The registry handles discovery and connection setup, while workspace requests
and terminal traffic go directly from the controller to the worker. An existing
connection does not depend on the registry staying online. You can also run
other trusted services on the registry host, as long as you give the registry
its own service user, configuration directory, and Unix socket.

### Prepare the hosts

1. Install the same reviewed Forge revision on the controller, devboxes, and
   registry. If you build from source, follow the [Quick Start](quickstart.md)
   build instructions and record the full commit and toolchain version. Confirm
   that `kenn-forge devbox --help` lists `worker`, `broker`, and `registry`.
2. Enroll all hosts in Tailscale. The **controller daemon's host**, which may
   differ from the browser's laptop, needs access to both the registry and the
   workers.
3. On each Linux devbox, install Git, tmux, GitHub CLI, and your agent tools.
   Create each developer's account with a private home directory and no sudo or
   privileged supplementary groups. Enroll dedicated SSH public keys for
   terminal clients, and keep a separate administrator login and recovery
   access. If you use Codex, install your distribution's `bubblewrap` package
   and follow its
   [Linux sandbox prerequisites](https://learn.chatgpt.com/docs/sandboxing). Run
   `codex sandbox -- /usr/bin/true` as the developer to check the sandbox before
   launching an agent. A successful login check does not exercise it.
4. On the registry host, install Caddy and point a stable hostname, such as
   `devboxes.example.org`, to its Tailscale address. Obtain a certificate
   trusted by the controller and set up automatic renewal. For a private
   listener, DNS validation avoids exposing an HTTP challenge endpoint. Verify
   certificate issuance before publishing the registry site.

Allow these connections in your tailnet policy:

| Source | Destination |
| --- | --- |
| Approved controller hosts | Registry HTTPS port 443 and devbox worker port range, for example TCP 9100–9199. |
| Approved terminal clients and administrators | Devbox SSH port 22. |
| Devboxes | GitHub and the agent providers over outbound HTTPS. |
| Registry | Local Tailscale identity service; outbound access needed by your certificate renewal setup. |

During normal operation, the registry does not need SSH access to devboxes. Your
deployment automation needs administrator access when it copies worker
identities into the registry. Keep certificate credentials, App keys, and worker
tokens out of source control and deployment logs.

### GitHub App and branch rules

Create this GitHub App once for your organization in GitHub's browser UI. It is
separate from any App your controller uses to sync GitHub data. The same
installation can serve multiple devboxes, and you do not create an App per host
or developer. The examples below use `example-org`, `project-a`, and `user-a`.
Replace them with your organization, pilot repository, and developer login.

#### Register and install the App

Before you start, sign in to GitHub as an organization owner or authorized App
manager, and prepare a disposable repository in that organization for the push
and branch-rule checks below.

1. Open your **organization's Settings → Developer settings → GitHub Apps →
   New GitHub App**. Do not create the App under your personal account.
2. Enter a globally unique name, such as `example-org-devboxes`. For
   **Homepage URL**, enter your organization's website or GitHub page. You do
   not need a running Forge server or a public callback endpoint to register the
   App.
3. Leave **Callback URL** and **Setup URL** blank. Leave **Request user
   authorization (OAuth) during installation** and **Enable Device Flow**
   unchecked, and uncheck **Active** under **Webhook**.
4. Set the permissions in this table, and leave all others at **No access**.

   | Scope | Permission | Access |
   | --- | --- | --- |
   | Repository | Contents | Read and write |
   | Repository | Workflows | Read and write |
   | Repository | Pull requests | Read and write |
   | Repository | Checks | Read-only |
   | Repository | Commit statuses | Read-only |
   | Repository | Metadata | Read-only (automatic) |
   | Organization | Members | Read-only |

   The Workflows permission lets writable accounts push changes to workflow
   files, and the broker requests it only when the installation has granted
   **Workflows: write**. If you are updating an existing App, add the permission
   and have an organization owner accept the updated permissions on the
   installation.

5. For **Where can this GitHub App be installed?**, select **Only on this
   account**, then click **Create GitHub App**.
6. On the App's **General** page, record the numeric **App ID**, not the Client
   ID. Under **Private keys**, click **Generate a private key**. Store the
   downloaded PEM file on your trusted operator workstation, outside Git, with
   mode `0600`. You do not need a client secret.
7. Open **Install App** and select your organization. Choose **All repositories**
   for organization-wide devbox access, or **Only select repositories** for a
   limited rollout, then click **Install**. Complete the branch-rule checks
   below before granting access to production repositories.

These follow GitHub's standard
[registration](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/registering-a-github-app),
[key generation](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/managing-private-keys-for-github-apps),
and [installation](https://docs.github.com/en/apps/using-github-apps/installing-your-own-github-app)
steps. Forge does not automate App registration or require a user OAuth flow for
devbox Git access.

#### Record the broker's IDs

Run these read-only lookups with an existing GitHub CLI login on your trusted
workstation. Do not sign in with a personal GitHub credential on the devbox.

```sh
gh api --paginate /orgs/example-org/installations \
  --jq '.installations[] | {app_slug, app_id, installation_id: .id, repository_selection}'
gh api /orgs/example-org --jq .id
gh api /users/user-a --jq '{id, login}'
```

Find the installation whose `app_slug` matches the App you created, and copy its
`app_id`, its `installation_id`, and the organization's `id` into the broker
TOML below. Use the user's `id` for `github_user_id`, and use that developer's
Linux account ID for `uid`. None of these IDs are secrets.

Have your deployment tooling install the PEM at `private_key_file`, readable
only by the broker service account and never by development accounts. Host
administrators remain trusted with this organization-scoped App authority. Keep
the PEM out of source control and logs. The App authenticates pushes, while each
developer's commit name and GitHub noreply email determine commit attribution.

Before issuing a short-lived installation token for one repository, the broker
checks the GitHub ID rostered for the calling Unix UID, that GitHub user's
current organization membership, the repository identity, and the repository
permission. Read-only collaborators get read-only Git access, and token caching
does not skip those admission checks. The App installation controls which
repositories are available to developers, and the broker has no separate
repository list for them. When you add a repository to the installation, you do
not need to change the broker configuration or restart the broker.
[Service accounts](#service-accounts) instead list their repositories.

#### Protect the default branch

**App permissions cannot express "push branches, but never merge or push main."**
Configure an enforced **Restrict updates** ruleset on default and protected
branches, and grant bypass only to the intended human maintainers, never to this
App. Keep required-review and checks rules in a separate ruleset so bypassing
the update restriction does not waive those requirements. Test an actual
rejected default-branch push and merge attempt before admitting production
repositories. Run your initial allow and deny exercises in a disposable
repository governed by the same rules.
[Contents write can authorize the merge endpoint](https://docs.github.com/en/rest/pulls/pulls#merge-a-pull-request),
and omitting Pull requests write does not prevent that. Preserve protection
against force pushes and deletion. Workflows write lets the App change CI
workflow files on any branch it can push. If those branches must not change
them, add a file path rule for `.github/workflows/**`.

The broker configuration uses TOML:

```toml
socket = "/run/forge-broker/broker.sock"
private_key_file = "/etc/forge-broker/app.pem"
app_id = 100
installation_id = 200
organization_id = 300
organization = "example-org"

[[accounts]]
uid = 1001
github_user_id = 1234
login = "user-a"
commit_name = "Developer A"
commit_email = "1234+user-a@users.noreply.github.com"
```

Run `kenn-forge devbox broker --config /etc/forge-broker/broker.toml` under a
separate service user. On Linux, the broker authenticates each connecting UID
with `SO_PEERCRED`. Any local account can connect to its socket, and the broker
rejects any unlisted UID regardless of request fields. It does not listen on the
network.

If you are upgrading a broker that uses `[[repositories]]`, remove those
sections when you update the binary. The new broker rejects the old settings,
and repository access now follows the App installation.

For systemd, create a system user named `forge-broker` with no login shell or
home directory. Install the configuration and PEM under `/etc/forge-broker`,
readable only by `forge-broker`, with mode `0700` on the directory and `0600` on
the files. Install the binary at `/usr/local/bin/kenn-forge`, owned by root, and
save this unit as `/etc/systemd/system/forge-broker.service`:

```ini
[Unit]
Description=Forge devbox GitHub credentials
Wants=network-online.target
After=network-online.target

[Service]
User=forge-broker
Group=forge-broker
RuntimeDirectory=forge-broker
RuntimeDirectoryMode=0755
UMask=0077
ExecStart=/usr/local/bin/kenn-forge devbox broker --config /etc/forge-broker/broker.toml
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
```

Run `sudo systemctl daemon-reload` and
`sudo systemctl enable --now forge-broker`. The runtime directory lets
developers reach the socket, and the broker checks admission on every request
using their kernel-supplied Unix UID.

#### Service accounts

A service account is a non-SSH Linux account for an unattended coordinator rather
than a developer. To configure one, set `service = true` and use the App's bot
user ID for `github_user_id`. You can look up that ID with
`gh api /users/example-app%5Bbot%5D --jq .id`.

List the repositories the account can use by GitHub repository ID, which you can
look up with `gh api /repos/example-org/project-a --jq .id`:

```toml
[[accounts]]
uid = 1002
github_user_id = 5555
login = "example-app[bot]"
service = true
write_repository_ids = [42]
read_repository_ids = [43]
```

For service accounts, the broker still verifies the installation, organization,
and repository identity, and skips the user, organization membership, and
repository permission checks that apply to developers. Instead, it admits only
the listed repositories and their contributor forks, using the upstream's grant:

- A repository in `write_repository_ids` gets the `git`, `push`, and `pr`
  profiles with the same permissions as a writable developer.
- A repository in `read_repository_ids` gets read-only `git` access, for
  example to fetch a private dependency. The broker refuses `push` and `pr`.
- The broker refuses every other repository, even when the App installation can
  reach it.

Forks serve `git` for the credential helper, Forge clone and fetch, and worker
admission under their own ID, and the broker refuses `push` and `pr`. GitHub
controls branch pushes.

Each service account must list at least one repository in either list, and the
broker refuses to start if both lists are empty. If an account only needs to
fetch repositories, omit `write_repository_ids`. Repository IDs survive renames,
and a reused name does not inherit another repository's access. The broker does
not restrict a service account from protected branches of its writable
repositories. The rulesets in
[Protect the default branch](#protect-the-default-branch) do.

### Per-account worker

Create a private data directory owned by the developer with mode `0700`, for
example `/home/user-a/.local/share/forge-worker`. Save the worker configuration
below as `/etc/forge-worker/user-a.toml`, owned by root and readable by that
account. Look up the actual Linux UID with `id -u user-a` and use it in both the
worker and broker configurations.

```toml
host = "127.0.0.1"
port = 9100
base_path = "/"
data_dir = "/home/user-a/.local/share/forge-worker"
allowed_hosts = ["host-a.example.ts.net:9100"]
trust_reverse_proxy = true

[api]
require_auth = true

[execution_worker]
enabled = true
uid = 1001
github_user_id = 1234
broker_socket = "/run/forge-broker/broker.sock"
commit_name = "Developer A"
commit_email = "1234+user-a@users.noreply.github.com"
worktree_dir = "/home/user-a/workspaces"

[tmux]
command = ["tmux"]
```

Setting `worktree_dir` places new GitHub.com worktrees at
`<worktree_dir>/<owner>/<repo>/<workspace>`, while Forge keeps its database,
credentials, and managed clones in `data_dir`. Changing `worktree_dir` does not
move existing workspaces or interrupt their agents. If you omit `worktree_dir`,
worktrees stay under `data_dir/worktrees`.

Start the broker before the workers, then run
`kenn-forge devbox worker --config /etc/forge-worker/user-a.toml` as the
developer account with its tool `PATH`, agent installation, and credentials. Set
`KillMode=process` so restarting the worker does not kill tmux sessions. If SSH
and worker processes should share one resource budget, apply CPU and memory
limits to the user's slice.

Save this unit in the developer's home directory at
`.config/systemd/user/forge-worker.service`. Update `PATH` to include the actual
agent and tool installation directories. A systemd service does not source the
developer's interactive shell configuration.

```ini
[Unit]
Description=Forge workspace worker

[Service]
Environment=HOME=/home/user-a
Environment=PATH=/home/user-a/.local/bin:/usr/local/bin:/usr/bin:/bin
Environment=KENN_FORGE_DEV_RESTART=1
UMask=0077
ExecStart=/usr/local/bin/kenn-forge devbox worker --config /etc/forge-worker/user-a.toml
Restart=on-failure
RestartSec=3
KillMode=process
TimeoutStopSec=30

[Install]
WantedBy=default.target
```

Have an administrator run `sudo loginctl enable-linger user-a`. Then, in an SSH
session as `user-a`, enable the service and check the worker:

```sh
systemctl --user daemon-reload
systemctl --user enable --now forge-worker
kenn-forge api --config /etc/forge-worker/user-a.toml GET /api/v1/worker
```

Verify the returned account identity before publishing the worker. On first
startup, Forge creates `auth_token` and `node_id` in the data directory. Both
identity files belong to the developer with mode `0600`. Preserve those files
and the rest of the data directory across upgrades. Your deployment must not
generate replacement identities. Back up this directory as application data,
including the workspace repositories. Reinstalling services does not recover
lost work.

Publish only that listener with
[Tailscale Serve](https://tailscale.com/docs/reference/tailscale-cli/serve):

```sh
tailscale serve --bg --http=9100 http://127.0.0.1:9100
```

Assign a distinct fixed port to each account, then add one tailnet policy grant
from approved developers and controllers to the worker port range. The bearer
token is the account boundary. Keep `api.tailscale_serve` disabled. Another
local account can send forged identity headers to a loopback listener. Worker
bearers are unscoped, and the restricted worker route set is the authorization
boundary. It excludes settings, provider mutations, fleet enrollment, and the
browser UI.

### Discovery registry

The registry serves a read-only roster with
`kenn-forge devbox registry --config /etc/forge-registry/registry.toml`. Point a
stable HTTPS hostname at the registry host's tailnet address. Terminate TLS with
a reverse proxy and forward traffic over a Unix socket whose parent directory is
accessible only to the registry user and proxy group. Do not replace this setup
with unrestricted loopback TCP and trusted identity headers.

Create a system user named `forge-registry` with no login shell or home
directory, then create `/etc/forge-registry` and `/etc/forge-registry/tokens`
owned by `forge-registry` with mode `0700`. Using your provisioning tool's
private file-transfer mechanism, copy each running worker's `auth_token` into
its registry token file, owned by `forge-registry` with mode `0600`. Copy each
worker's `node_id` into its assignment below, and leave the worker's original
files intact. Never paste tokens into a terminal or print them in automation
output.

On the registry host, run `tailscale whois --json CONTROLLER_TAILSCALE_IP` to
get `UserProfile.ID` for `tailscale_user_id` and, if you are explicitly pinning
a controller, `Node.StableID`. Get SSH host fingerprints from an independently
trusted source, such as the host console. Save the roster below as
`/etc/forge-registry/registry.toml`, owned by `forge-registry` with mode `0600`.

```toml
socket = "/run/forge-registry/registry.sock"
registry_id = "example-devboxes"
revision = "deployment-1"

[[developers]]
github_user_id = 1234
tailscale_user_id = 5678
controller_node_ids = []
enabled = true

[[assignments]]
host_id = "host-a"
name = "Compute A"
url = "http://host-a.example.ts.net:9100"
account = "user-a"
ssh_address = "user-a@host-a.example.ts.net"
ssh_host_fingerprint = "SHA256:REPLACE_WITH_VERIFIED_HOST_FINGERPRINT"
node_id = "REPLACE_WITH_WORKER_32_HEX_NODE_ID"
uid = 1001
github_user_id = 1234
role = "execution"
protocol = 1
enabled = true
maintenance = false
token_file = "/etc/forge-registry/tokens/host-a-user-a"
```

Each assignment's `github_user_id` names the developer who can discover and
connect to that account, and personal workers use that same GitHub identity. For
a service account, set `worker_github_user_id` to the App's bot ID. The
controller verifies that execution identity without changing the human owner.
To give several developers access to one service worker, add an assignment for
each developer with the same worker details and `worker_github_user_id`. Each
developer's `github_user_id`, `enabled` flag, and display name can differ, while
personal worker NodeIDs stay unique.

Before you add `worker_github_user_id` to service assignments, upgrade each
supervisor's controller to Forge v0.6.0 or later. Before you add duplicate
assignments, upgrade the registry to a version that supports shared service
workers. Older controllers still work with personal accounts, though they cannot
connect to service workers. Before rolling the registry back to a version
without shared-worker support, remove duplicate assignments. If the rollback
target also lacks service-worker support, remove `worker_github_user_id` as well.
Keep personal assignments and existing worker data intact.

Save `/etc/systemd/system/forge-registry.service`:

```ini
[Unit]
Description=Forge devbox discovery
Requires=tailscaled.service
After=tailscaled.service

[Service]
User=forge-registry
Group=caddy
RuntimeDirectory=forge-registry
RuntimeDirectoryMode=0750
UMask=0077
ExecStart=/usr/local/bin/kenn-forge devbox registry --config /etc/forge-registry/registry.toml
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
```

Run `sudo systemctl daemon-reload` and
`sudo systemctl enable --now forge-registry`, then check the local service:

```sh
sudo curl --fail --unix-socket /run/forge-registry/registry.sock http://localhost/healthz
```

The response should include `"status":"ok"`. This verifies process health, not
Tailscale admission. Both the service and the Caddy group can access the socket,
and the registry uses no shared database or job queue.

Add a site block to your existing Caddy configuration with your hostname,
Tailscale IP, and certificate paths. Caddy must be able to read the PEM pair,
and the proxy overwrites the peer headers with the actual TCP peer:

```caddyfile
https://devboxes.example.org {
    bind YOUR_REGISTRY_TAILSCALE_IP
    tls /etc/caddy/certs/devboxes.crt /etc/caddy/certs/devboxes.key
    reverse_proxy unix//run/forge-registry/registry.sock {
        header_up X-Devbox-Peer-IP {remote_host}
        header_up X-Devbox-Peer-Port {remote_port}
    }
}
```

Before reloading Caddy, validate the full configuration with
`sudo caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile`. Keep
the previous site and certificate until validation and reload succeed. Your
certificate renewal process must reload Caddy after publishing a renewed pair.
For details, see Caddy's
[Unix-socket proxy](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy)
and [certificate configuration](https://caddyserver.com/docs/caddyfile/directives/tls).

The registry calls `tailscale whois --json IP:PORT`. Verify that this command
succeeds as its service user before publishing the site. Ordinary untagged nodes
map by Tailscale user ID, while tagged or shared controllers require an explicit
stable node ID in `controller_node_ids`. Pinning a node trusts every process on
that node as the assigned developer. Do not pin a shared multi-user devbox as
one person's controller.

Discovery returns only the caller's assignments. The native controller fetches
the worker token and verifies the node ID, Unix UID, GitHub ID, role, and
protocol. The browser never receives worker tokens. Existing connections survive
registry outages, while new connections and token refresh require the registry.
To relocate the registry, keep `registry_id`, host IDs, assignments, and tokens
the same and update DNS behind the stable hostname. Restart the registry
whenever you change its prepared roster.

From the actual controller host, verify
`curl --fail https://devboxes.example.org/api/v1/devboxes`, then connect through
**Settings → Workspaces**. That discovery response contains assignments, not
tokens, and a successful local health check alone does not prove this network
path.

### Maintenance and recovery

| Situation | Action |
| --- | --- |
| Add a devbox or developer | Provision the account and worker, copy its persistent identity into the registry, restart the registry, then discover and connect from Forge. |
| Update Forge | Preserve account data and identities, replace the reviewed binary, restart services, then check identity and reattach to an existing workspace. |
| Registry unavailable | Existing saved connections keep using their workers. Restore the registry to add connections or refresh tokens. Agents keep running on their devboxes. |
| Worker unavailable | Check its user service and journal, disk space and account resource pressure. Restart it with the same data directory; do not delete the directory to repair startup. |
| Worker token changed | Restart the worker with the intended token, copy that token into the registry, restart the registry and use **Reconnect**. An unexpected node/account identity change requires explicit removal and re-enrollment. |
| GitHub or the broker unavailable | Inspect the broker journal and helper's error. Local work can continue, but GitHub operations need restored service. |
| Agent subscription exhausted | Stop affected agent sessions, install the replacement credentials in that account and start new sessions. GitHub App credentials are independent. |

To inspect logs, run `journalctl --user -u forge-worker` as the developer, and
run `sudo journalctl -u forge-broker -u forge-registry` on the hosts running
those services. Ordinary deployments should not reset worktrees, worker
identities, or agent credentials. When you offboard a personal account, also disable
account access, stop the worker, and remove its broker admission.

Supervisors of a shared service worker hold the same bearer token, and removing
a registry grant alone does not revoke a saved bearer. To remove one supervisor:

1. Remove or disable that supervisor's registry assignment.
2. After active fixes finish, stop the worker, replace its `auth_token`, and
   restart it with the same `node_id` and workspace data.
3. Copy the new token to the registry and restart the registry.
4. Refresh any other services that hold a copy of the token, and have the
   remaining supervisors select **Reconnect**.

### Commit identity and acceptance

Managed worktrees reset inherited credential helpers and set
`user.useConfigOnly`, name, and email. Forge validates identity before starting
an agent and before pushing. On a cherry-pick or rebase, Git preserves the
original author while updating the committer, and Forge reports that preserved
author separately. Commit metadata is an attribution convention, not
cryptographic proof of who typed a change. The App remains the push actor and
PR-edit actor.

Before you call a deployment ready, exercise the actual controller, registry,
worker, agent, and GitHub repository end to end. Create a workspace, attach to
it, commit, push, inspect both resolved GitHub identities, disconnect, restart,
reattach, and delete it. Test a second account to confirm that the wrong bearer
and forged Serve headers are denied, and that concurrent work with its own
credentials succeeds. A local fixture test cannot prove your deployment's
Tailscale grants, App installation, or rulesets.
