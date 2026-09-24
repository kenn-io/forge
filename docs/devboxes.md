# Devboxes

Kenn Forge can run agent workspaces in your account on a shared Linux machine.
Your usual Forge remains the controller: you choose work, supervise agents,
review diffs and push branches there. The devbox owns the worktrees, tools,
agent processes and tmux sessions. Closing the browser does not stop them.

This is an initial operator-managed deployment for trusted teams on Tailscale.
It is not a container sandbox. Each developer gets a separate non-root Linux
account. Administrators can read account data and installed agent credentials;
personal GitHub credentials stay on the controller.

## Daily workflow

1. Open **Settings → Workspaces** on your usual Forge hub or
   standalone Forge. **Workspace machines** lists this Forge machine and every
   devbox assigned to you, each once, with its current state: **Not
   connected**, **Online**, **Offline**, or **Maintenance**. The registry that
   supplied the list is shown beneath it. If no registry is configured yet,
   enter the address from your operator and select **Find devboxes**; you never
   enter ports or tokens. Select **Connect** beside a machine; Forge checks its
   account identity and updates that row. **Refresh** checks for new
   assignments without removing saved connections.
2. Select the radio button beside a connected machine to run new workspaces
   there. Existing workspaces remain where they were created. You can choose
   another connected machine in the new-workspace dialog. An unavailable
   default does not silently move work to your local machine; the settings row
   shows **Offline** with a **Reconnect** action instead.
3. Create a workspace from a repository, pull request or issue. Remote pull
   request and issue actions open the workspace in **Workspaces**. The first
   clone of each repository in your account can take longer. No second local
   checkout is needed.
4. Start an agent, give it the task and inspect its output and diff. Files and
   tests stay on the devbox. The agent uses the tools and subscription installed
   in your account. Manual editing is optional; the normal workflow is to ask
   the agent to make the change.
5. Push the branch. Commits use your configured name and GitHub noreply email;
   the GitHub App authenticates the push. Forge checks GitHub's resolved author
   and committer IDs after pushing and displays a warning if attribution cannot
   be verified. A verification failure does not retry the push.
6. Review and merge through your usual trusted Forge or GitHub session. Delete
   the remote workspace explicitly when finished. Automatic cleanup after a
   merge is not part of this initial deployment.

Reconnect after sleep to see progress. There are no new worker-generated
notifications while the controller is disconnected. Existing terminals and
Git work survive an expired source-context lease; starting another agent for a
pull request or issue needs fresh context from the controller. If a worker
credential changes, use **Reconnect** in settings. Removing a connection leaves
its remote work intact.

Preview-port discovery and forwarding are not included. For a browser preview,
read the port from agent output and use your terminal client's explicit port
forwarding. Do not expose application listeners publicly to obtain a preview.

## Repository access and commit identity

Each developer uses a separate Linux account on the devbox. The operator links
that account's numeric Linux user ID to the developer's GitHub user ID and
configures their commit name and email. Forge uses that link to check repository
access; Git uses the name and email to identify new commits.

### How the credential broker works

The credential broker is a small service on each devbox that supplies temporary
GitHub credentials to Git. It runs under its own service account, which holds
the GitHub App's private key. Development accounts receive short-lived tokens;
the deployment keeps the App key readable only by the broker service account.
Your personal GitHub credentials stay on the controller.

Repository access follows these steps:

1. **The operator enables the repository.** They install the organization's
   GitHub App on selected repositories and add the allowed repositories and
   developer accounts to the broker configuration. Connecting a devbox in Forge
   does not itself grant GitHub access.
2. **Git asks for a credential.** When Forge clones a repository, or Git needs
   to fetch or push from a managed worktree, it asks the broker for access to
   that specific repository. An agent or SSH shell uses the same Git helper.
3. **The broker identifies the caller and checks access.** The request travels
   over a local Unix socket. Linux supplies the caller's user ID, which the
   broker looks up in its configured account list. The broker checks the linked
   GitHub user's current organization membership and repository permissions,
   as well as the configured App installation and repository identity.
4. **Git receives a temporary token.** The broker requests a GitHub App
   installation token restricted to that one repository. A developer with read
   access can clone and fetch; write access also permits pushes, subject to
   GitHub's branch rules. The helper passes the token to Git without saving it
   as a stored credential.
5. **Later requests repeat the access checks.** The broker can reuse a token
   cached in memory, but still checks the caller's access before returning it.
   It replaces tokens near expiry. Already issued tokens remain usable until
   they expire or are revoked.

This initial devbox integration supports repositories on `github.com`. It does
not require a personal access token or `gh auth login` in the developer's
devbox account. See [GitHub App and branch rules](#github-app-and-branch-rules)
for the operator setup.

### How commits are linked to you

Forge sets `user.name` and `user.email` in each managed worktree from your
configured developer identity. Use the GitHub-provided noreply address shown
in your GitHub email settings. GitHub uses the email in a commit to
[associate it with your account](https://docs.github.com/en/account-and-profile/how-tos/email-preferences/setting-your-commit-email-address).
New commits made by an agent in your account therefore use your configured
identity, just as commits made from your SSH shell do.

The identities have different jobs:

| Identity | What it means |
| --- | --- |
| GitHub App | Authenticates the push and any pull request actions made through the devbox wrapper. |
| Commit author | The person credited with the original change, recorded in the commit. |
| Committer | The person who created this version of the commit, also recorded in the commit. |

For a new commit, author and committer normally both identify you. A cherry-pick
or rebase can preserve someone else's author identity while recording you as
the committer. The App can push either kind of commit without becoming its
author.

Forge validates the worktree's configured identity before starting an agent or
pushing through Forge. After a push, the controller asks GitHub which accounts
it resolved for the branch's latest commit and compares their numeric user IDs
with your enrolled GitHub ID. Forge reports a match, a preserved original
author, a mismatch, or an unverified result. If the email is wrong or GitHub
cannot be reached, a successful push is not repeated. Commit attribution does
not provide a cryptographic signature or prove who typed the change.

## Using an SSH terminal client

An SSH terminal client connects to the same Unix account with the
operator-approved development key and agent forwarding disabled. With the
worker configured to use that account's default tmux server, clients can
list and attach to its existing sessions. Configure `window-size latest` in
system tmux settings so two attached clients do not constrain each other to
the smaller terminal. Forge still owns its worktrees and workspace lifecycle.

For repository-only work started from the remote terminal, the existing API
CLI can create a workspace without a second Forge daemon:

```sh
kenn-forge api --config /etc/forge-worker/user-a.toml \
  POST /api/v1/worker/workspaces --data '{"repository":{"provider":"github","platform_host":"github.com","owner":"example-org","name":"project-a"},"branch":"work/task-a"}'
```

The response includes the workspace ID and setup state. Use the same CLI with
`GET /api/v1/workspaces/ID` to wait for `ready`, then start a configured agent:

```sh
kenn-forge api --config /etc/forge-worker/user-a.toml \
  POST /api/v1/workspaces/ID/runtime/sessions --data '{"target_key":"codex"}'
```

Create pull request and issue workspaces through the controller. The worker
cannot obtain or validate that source context itself. Git in managed worktrees
uses the same broker helper from Forge, an agent or an SSH shell. Do not run
`gh auth login` on the devbox. Supported pull request commands use:

```sh
kenn-forge devbox github --socket /run/forge-broker/broker.sock \
  --repository example-org/project-a -- pr create
```

The wrapper supports `pr create`, `edit`, `comment`, `view`, `list` and `checks`.
Its token goes only to that child process. Git and the wrapper explain broker
failures, such as a repository not being admitted or missing write permission.

## Operator setup

Forge supplies generic worker, registry and credential-broker commands. Your
provisioning repository owns users, SSH admission, packages, service units,
roster assignments, agent credentials and network policy. The examples here
use ordinary Linux accounts, systemd and Caddy; no private deployment tooling
is required. Keep your chosen versions and configuration in your own automation.

### What runs where

| Host | Processes and state |
| --- | --- |
| Your existing controller | Full Forge with its browser UI, provider access and saved devbox connections. |
| Each devbox | One credential broker and one worker per developer account. Worktrees, tools, agent credentials and tmux sessions live here. |
| Registry host | One registry process, a roster and copies of worker connection tokens, behind an HTTPS proxy. It needs no worktrees, agents or GitHub App key. |

The registry handles discovery and connection setup. Workspace requests and
terminal traffic go directly from the controller to the worker. An existing
connection does not depend on the registry staying online. A registry host can
also run other trusted services; give the registry its own service user,
configuration directory and Unix socket.

### Prepare the hosts

1. Install the same reviewed Forge revision on the controller, devboxes and
   registry. Use the [Quick Start](quickstart.md) build instructions when building
   from source, and record the full commit and toolchain version. Confirm that
   `kenn-forge devbox --help` lists `worker`, `broker` and `registry`.
2. Enroll all hosts in Tailscale. The **controller daemon's host**, which may
   differ from the browser's laptop, needs access to the registry and workers.
3. On each Linux devbox, install Git, tmux, GitHub CLI and your agent tools.
   Create each developer's account with a private home and no sudo or privileged
   supplementary groups. Enroll dedicated SSH public keys for terminal clients;
   retain a separate administrator login and recovery access.
   For Codex, install the distribution's `bubblewrap` package and follow its
   [Linux sandbox prerequisites](https://learn.chatgpt.com/docs/sandboxing).
   Run `codex sandbox -- /usr/bin/true` as the developer to check the sandbox
   before launching an agent; a successful login check does not exercise it.
4. On the registry host, install Caddy and prepare a stable hostname, such as
   `devboxes.example.org`, pointing to its Tailscale address. Obtain a certificate
   trusted by the controller and arrange automatic renewal. For a private
   listener, DNS validation avoids exposing an HTTP challenge endpoint. Verify
   certificate issuance before publishing the registry site.

Use your existing tailnet policy with these connections:

| Source | Destination |
| --- | --- |
| Approved controller hosts | Registry HTTPS port 443 and devbox worker port range, for example TCP 9100–9199. |
| Approved terminal clients and administrators | Devbox SSH port 22. |
| Devboxes | GitHub and the agent providers over outbound HTTPS. |
| Registry | Local Tailscale identity service; outbound access needed by your certificate renewal setup. |

The registry does not need SSH access to devboxes during normal operation.
Deployment automation needs administrator access when it copies worker
identities into the registry. Keep certificate credentials, App keys and worker
tokens outside source control and deployment logs.

### GitHub App and branch rules

Create this App once for your organization using GitHub's browser UI. It is
separate from any App used by your controller to sync GitHub data. The same
installation can serve multiple devboxes; you do not create an App per host
or developer. The examples below use `example-org`, `project-a` and `user-a`;
replace them with your organization, pilot repository and developer login.

#### Register and install the App

Sign in to GitHub as an organization owner or authorized App manager. Start
with a disposable repository in that organization for the push and branch-rule
checks below.

1. Open your **organization's Settings → Developer settings → GitHub Apps →
   New GitHub App**. Do not create it under your personal account.
2. Choose a globally unique name, such as `example-org-devboxes`. For
   **Homepage URL**, use your organization's website or GitHub page. A running
   Forge server or public callback endpoint is not needed to register the App.
3. Leave **Callback URL** and **Setup URL** blank. Leave **Request user
   authorization (OAuth) during installation** and **Enable Device Flow**
   unchecked. Under **Webhook**, uncheck **Active**.
4. Set the permissions in this table. Leave all others at **No access**.

   | Scope | Permission | Access |
   | --- | --- | --- |
   | Repository | Contents | Read and write |
   | Repository | Pull requests | Read and write |
   | Repository | Checks | Read-only |
   | Repository | Commit statuses | Read-only |
   | Repository | Metadata | Read-only (automatic) |
   | Organization | Members | Read-only |

5. For **Where can this GitHub App be installed?**, choose **Only on this
   account**, then click **Create GitHub App**.
6. On its **General** page, record the numeric **App ID**, not the Client ID.
   Under **Private keys**, click **Generate a private key**. Keep the downloaded
   PEM on your trusted operator workstation, outside Git, with mode `0600`.
   No client secret is needed.
7. Open **Install App**, choose your organization, then **Only select
   repositories**. Select the pilot repository and click **Install**. Add
   production repositories only after the branch-rule checks pass.

These are GitHub's standard [registration](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/registering-a-github-app),
[key generation](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/managing-private-keys-for-github-apps)
and [installation](https://docs.github.com/en/apps/using-github-apps/installing-your-own-github-app)
steps. Forge does not automate App registration or require a user OAuth flow
for devbox Git access.

#### Record the broker's IDs

Use an existing GitHub CLI login on your trusted workstation for these read-only
lookups. Do not sign in with a personal GitHub credential on the devbox.

```sh
gh api --paginate /orgs/example-org/installations \
  --jq '.installations[] | {app_slug, app_id, installation_id: .id, repository_selection}'
gh api /orgs/example-org --jq .id
gh api /repos/example-org/project-a --jq '{id, name, full_name}'
gh api /users/user-a --jq '{id, login}'
```

Choose the installation whose `app_slug` matches the App you just created and
whose `repository_selection` is `selected`. Use its `app_id` and
`installation_id`, the organization's `id`, and the repository's `id` and `name`
in the broker TOML below. Use the user's `id` for `github_user_id`; `uid` is
that developer's Linux account ID. These IDs are not secrets.

Your deployment tooling installs the PEM at `private_key_file`, readable only
by the broker service account, never by development accounts. Host administrators
remain trusted with this organization-scoped App authority. Keep the PEM out of
source control and logs. The App authenticates pushes; each developer's commit
name and GitHub noreply email determine commit attribution.

The broker verifies each Unix UID's rostered GitHub ID, current organization
membership, repository identity and repository permission before issuing a
short-lived installation token for one repository. Read-only collaborators get
read-only Git access. Token caching does not skip those admission checks.

#### Protect the default branch

**App permissions cannot express “push branches, but never merge or push main.”**
Use an enforced **Restrict updates** ruleset on default/protected branches,
with bypass assigned only to the intended human maintainers and never this App.
Keep the required-review and checks rules in a separate ruleset so bypassing the
update restriction does not waive those requirements.
Test an actual rejected default-branch push and merge attempt before admitting
production repositories. [Contents write can authorize the merge endpoint](https://docs.github.com/en/rest/pulls/pulls#merge-a-pull-request);
omitting Pull requests write does not solve this. Preserve protection against
force pushes and deletion. Do the initial allow/deny exercises in a disposable
repository governed by the same rules.

The broker configuration is TOML:

```toml
socket = "/run/forge-broker/broker.sock"
private_key_file = "/etc/forge-broker/app.pem"
app_id = 100
installation_id = 200
organization_id = 300
organization = "example-org"

[[repositories]]
id = 400
name = "project-a"

[[accounts]]
uid = 1001
github_user_id = 1234
login = "user-a"
commit_name = "Developer A"
commit_email = "1234+user-a@users.noreply.github.com"
```

Start `kenn-forge devbox broker --config /etc/forge-broker/broker.toml` as a
separate service user. On Linux, the broker authenticates the connecting UID
with `SO_PEERCRED`. Its socket permits local connections; an unlisted UID is
rejected regardless of request fields. It does not listen on the network.

For systemd, create a system user named `forge-broker` with no login shell or
home. Install the configuration and PEM under `/etc/forge-broker`, readable
only by that user (directory `0700`, files `0600`). Install the binary at
`/usr/local/bin/kenn-forge`, owned by root. Save this unit as
`/etc/systemd/system/forge-broker.service`:

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
`sudo systemctl enable --now forge-broker`. The runtime directory allows
developers to reach the socket; admission is checked using their kernel-supplied
Unix UID on every request.

### Per-account worker

Create a private data directory owned by the developer, for example
`/home/user-a/.local/share/forge-worker` with mode `0700`. Save this worker
configuration as `/etc/forge-worker/user-a.toml`, owned by root and readable by
the account. Obtain the actual Linux UID with `id -u user-a` and use it in
both worker and broker configuration.

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

`worktree_dir` places new GitHub.com worktrees at `<worktree_dir>/<owner>/<repo>/<workspace>`.
Forge keeps its database, credentials and managed clones in `data_dir`. Changing
this setting does not move existing workspaces or interrupt their agents. When
omitted, worktrees remain under `data_dir/worktrees`.

Run `kenn-forge devbox worker --config /etc/forge-worker/user-a.toml` as that
account. Provide its tool PATH, agent installation and credentials. Use
`KillMode=process` so restarting the worker does not kill tmux sessions. Apply
CPU and memory limits to the user's slice if SSH and worker processes should
share one budget. Run the broker before the workers.

Save this unit in the developer's home at
`.config/systemd/user/forge-worker.service`. Adapt `PATH` to include the actual
agent and tool installation directories; a systemd service does not source the
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

An administrator runs `sudo loginctl enable-linger user-a`. Then, in an SSH
session as `user-a`, run:

```sh
systemctl --user daemon-reload
systemctl --user enable --now forge-worker
kenn-forge api --config /etc/forge-worker/user-a.toml GET /api/v1/worker
```

Check the returned account identity before publication. On first startup Forge
creates `auth_token` and `node_id` in the data directory. Keep those files and
the rest of the data directory across upgrades; deployment must not generate
replacement identities. Both identity files belong to the developer and use
mode `0600`. Back up this directory as application data, including the workspace
repositories; reinstalling services does not recover lost work.

Publish just that listener with [Tailscale Serve](https://tailscale.com/docs/reference/tailscale-cli/serve):

```sh
tailscale serve --bg --http=9100 http://127.0.0.1:9100
```

Use a distinct fixed port for each account, then one tailnet policy grant from
approved developers/controllers to the worker port range. The account boundary
is the bearer token. Keep `api.tailscale_serve` disabled: another local account
can send forged identity headers to a loopback listener. Worker bearers are
unscoped; the restricted worker route set is the authorization boundary. It
excludes settings, provider mutations, fleet enrollment and the browser UI.

### Discovery registry

The registry is a read-only roster served by
`kenn-forge devbox registry --config /etc/forge-registry/registry.toml`.
Use a stable HTTPS hostname resolving to the registry host's tailnet address.
Terminate TLS with a reverse proxy and forward over a Unix socket whose parent
is accessible only to the registry user and proxy group. Do not replace this
with unrestricted loopback TCP and trusted identity headers.

Create a system user named `forge-registry` with no login shell or home.
Create `/etc/forge-registry` and its `tokens` subdirectory owned by that user
with mode `0700`. Through your provisioning tool's private file-transfer
mechanism, copy each running worker's `auth_token` into the corresponding
registry token file, owned by `forge-registry` with mode `0600`. Read the
worker's `node_id` into its assignment below. Preserve the worker's originals.
Do not paste tokens into a terminal or print them in automation output.

Use `tailscale whois --json CONTROLLER_TAILSCALE_IP` on the registry host to
obtain `UserProfile.ID` for `tailscale_user_id` and, when explicitly pinning a
controller, `Node.StableID`. Obtain SSH host fingerprints from an independently
trusted source, such as the host console. Save the roster below as
`/etc/forge-registry/registry.toml`, owned by `forge-registry`, mode `0600`.

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
`sudo systemctl enable --now forge-registry`. Check the local service:

```sh
sudo curl --fail --unix-socket /run/forge-registry/registry.sock http://localhost/healthz
```

The response should contain `"status":"ok"`. This checks process health,
not Tailscale admission. The socket is accessible to the service and Caddy
group, and the registry uses no shared database or job queue.

Add a site to your existing Caddy configuration, replacing the hostname,
Tailscale IP and certificate paths. The PEM pair must be readable by Caddy.
The proxy overwrites the peer headers with the actual TCP peer:

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

Validate the complete configuration with
`sudo caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile` before
reloading Caddy. Retain the previous site and certificate until validation and
reload succeed. Your certificate renewal process must reload Caddy after
publishing a renewed pair. See Caddy's
[Unix-socket proxy](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy)
and [certificate configuration](https://caddyserver.com/docs/caddyfile/directives/tls).

The service calls `tailscale whois --json IP:PORT`. Verify that it succeeds as
its service user before publication. Ordinary untagged nodes map by Tailscale
user ID. Tagged or shared controllers require an explicit stable node ID in
`controller_node_ids`. This trusts every process on that node as the assigned
developer; do not pin a shared multi-user devbox as one person's controller.

Discovery lists only the caller's assignments. The native controller obtains
the worker token and verifies node ID, Unix UID, GitHub ID, role and protocol.
The browser never receives worker tokens. Connections survive registry outages;
new connections and token refresh require the registry. Relocation preserves
`registry_id`, host IDs, assignments and tokens and changes DNS behind the
stable hostname. Restart the registry after changing its prepared roster.

From the actual controller host, check
`curl --fail https://devboxes.example.org/api/v1/devboxes`, then connect through
**Settings → Workspaces**. That discovery response contains assignments, not
tokens. A successful local health check alone does not prove this network path.

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

Use `journalctl --user -u forge-worker` as the developer and
`sudo journalctl -u forge-broker -u forge-registry` on the hosts that run those
services. Ordinary deployment should not reset worktrees, worker identities or
agent credentials. For offboarding, removing a registry assignment alone does
not revoke an already saved bearer: also disable account access and stop the
worker, and remove the broker admission for that account.

### Commit identity and acceptance

Managed worktrees reset inherited credential helpers and set `user.useConfigOnly`,
name and email. Forge validates identity before agent start and push. Git
preserves another person's author on cherry-pick and rebase while changing the
committer; Forge reports that preserved author separately. Commit metadata is
an attribution convention, not a cryptographic proof of who typed a change.
The App remains the push actor and PR-edit actor.

Before calling a deployment ready, exercise the actual controller, registry,
worker, agent and GitHub repository: create, attach, commit, push, inspect both
resolved GitHub identities, disconnect, restart, reattach and delete. Test a
second account's denied access with the wrong bearer and forged Serve headers,
plus allowed concurrent work using its own credentials. A local fixture test
cannot prove the deployment's Tailscale grants, App installation or rulesets.
