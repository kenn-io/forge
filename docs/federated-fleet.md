# Federated Forge

With a Kenn Forge fleet, you can manage several development machines from one
shared interface while leaving every repository, worktree, and agent session on
the machine where it runs.

## Why use a fleet

Use a fleet when no single machine fits every task. You might handle routine
work on a laptop, run large builds on a workstation, and reach for a machine
with a specific operating system or GPU when a task needs one.

- Open Forge on any machine to see workspaces from every machine, each labeled
  with the node that owns it.
- Create a workspace on any writable machine from any Forge UI, and pick which
  machine new workspaces use by default.
- Sync pull requests, issues, and activity once instead of spending provider
  API budget on every machine.
- Keep each repository, worktree, process, and tmux session on the machine that
  runs it.

If one machine already handles all your work, keep Forge standalone. A fleet is
worth setting up when it matters which machine runs each task.

## The mental model

One **hub** owns provider sync, provider changes, and the fleet-wide view, while
each **spoke** owns its local repositories, workspaces, Git commands, processes,
and tmux sessions. The hub can also own local workspaces, so it can run work as
well as coordinate the fleet.

<figure class="fleet-diagram">
  <svg viewBox="0 0 960 560" role="img" aria-labelledby="fleet-topology-title fleet-topology-description">
    <title id="fleet-topology-title">Forge hub-and-spoke topology</title>
    <desc id="fleet-topology-description">A browser can open the hub or either spoke. The hub talks to the Git provider for synchronized provider data and sends workspace or terminal operations to the spoke that owns the work. Each spoke keeps its own repositories, workspaces, and agents.</desc>
    <defs>
      <marker id="fleet-arrow" markerWidth="10" markerHeight="10" refX="8" refY="3" orient="auto" markerUnits="strokeWidth">
        <path d="M0,0 L0,6 L9,3 z" class="fleet-diagram__arrowhead" />
      </marker>
      <marker id="fleet-arrow-start" markerWidth="10" markerHeight="10" refX="1" refY="3" orient="auto-start-reverse" markerUnits="strokeWidth">
        <path d="M9,0 L9,6 L0,3 z" class="fleet-diagram__arrowhead" />
      </marker>
    </defs>
    <rect class="fleet-diagram__node" x="55" y="45" width="230" height="90" rx="14" />
    <text class="fleet-diagram__title" x="170" y="82" text-anchor="middle">Your browser</text>
    <text class="fleet-diagram__body" x="170" y="111" text-anchor="middle">Open any Forge directly</text>

    <rect class="fleet-diagram__node" x="675" y="45" width="230" height="90" rx="14" />
    <text class="fleet-diagram__title" x="790" y="82" text-anchor="middle">Git provider</text>
    <text class="fleet-diagram__body" x="790" y="111" text-anchor="middle">Pull requests, issues, Git</text>

    <rect class="fleet-diagram__node fleet-diagram__node--hub" x="330" y="210" width="300" height="130" rx="14" />
    <text class="fleet-diagram__eyebrow" x="480" y="245" text-anchor="middle">HUB</text>
    <text class="fleet-diagram__title" x="480" y="276" text-anchor="middle">Shared provider data + fleet view</text>
    <text class="fleet-diagram__body" x="480" y="307" text-anchor="middle">Also owns its local workspaces</text>

    <rect class="fleet-diagram__node" x="55" y="425" width="330" height="100" rx="14" />
    <text class="fleet-diagram__eyebrow" x="220" y="458" text-anchor="middle">SPOKE A</text>
    <text class="fleet-diagram__title" x="220" y="489" text-anchor="middle">Local repos, workspaces, agents</text>

    <rect class="fleet-diagram__node" x="575" y="425" width="330" height="100" rx="14" />
    <text class="fleet-diagram__eyebrow" x="740" y="458" text-anchor="middle">SPOKE B</text>
    <text class="fleet-diagram__title" x="740" y="489" text-anchor="middle">Local repos, workspaces, agents</text>

    <path class="fleet-diagram__line fleet-diagram__line--secondary" d="M282 115 C340 145 380 165 415 210" marker-end="url(#fleet-arrow)" />
    <path class="fleet-diagram__line fleet-diagram__line--secondary" d="M145 135 C110 245 120 340 185 425" marker-end="url(#fleet-arrow)" />
    <path class="fleet-diagram__line fleet-diagram__line--secondary" d="M215 135 C420 165 845 140 830 425" marker-end="url(#fleet-arrow)" />
    <text class="fleet-diagram__label" x="177" y="220">private HTTPS</text>

    <path class="fleet-diagram__line" d="M630 250 C690 225 745 185 775 136" marker-start="url(#fleet-arrow-start)" marker-end="url(#fleet-arrow)" />
    <text class="fleet-diagram__label" x="700" y="214">provider API</text>

    <path class="fleet-diagram__line" d="M405 340 C360 375 315 397 270 425" marker-start="url(#fleet-arrow-start)" marker-end="url(#fleet-arrow)" />
    <path class="fleet-diagram__line" d="M555 340 C600 375 645 397 690 425" marker-start="url(#fleet-arrow-start)" marker-end="url(#fleet-arrow)" />
    <text class="fleet-diagram__label" x="480" y="389" text-anchor="middle">authenticated Forge API</text>
  </svg>
  <figcaption>The hub coordinates the fleet. It does not become the filesystem or process owner for a spoke.</figcaption>
</figure>

Every machine runs the same `kenn-forge` binary and keeps its own UI that you
can open directly. Forge-to-Forge requests and terminal WebSockets use
authenticated HTTPS. The hub does not use SSH to start a spoke or attach to its
tmux server. Start and supervise each daemon on its own machine.

### Fleet rules at a glance

| Question | Hub | Spoke |
| --- | --- | --- |
| Which workspaces appear? | Workspaces from the full fleet, each with an execution-node badge | The same |
| Where can **New workspace** run? | Any writable fleet member selected in **Run on** | The same; the spoke sends the request through the hub |
| Where do pull-request, issue, and quick-action workspaces run? | On this Forge's [default machine](#choose-where-new-workspaces-run) | The same |
| Who runs Git and agent processes? | The machine that owns the workspace | The machine that owns the workspace |
| Who syncs provider data and applies provider changes? | The hub | The spoke asks the hub |

Each Forge has its own default machine. If you do not set one, new workspaces
run on whichever Forge UI you opened.

This guide uses the following example topology, where each name is a private
HTTPS origin reachable from every fleet machine:

```text
https://forge-hub.example.test  hub and local execution
https://build-a.example.test            spoke and local execution
https://build-b.example.test            spoke and local execution
```

Tailscale Serve is a first-class way to provide these origins, though you do
not need it for a fleet. A private LAN, UniFi network, VPN, or custom reverse
proxy works with the same federation protocol.

## Understand the trust boundary

Run federation only inside one operator-controlled private network, and keep
every Forge listener behind that private ingress.

Private network access is necessary, but not enough on its own. Forge also requires
HTTPS, local browser authentication, and separate credentials created during
enrollment. An activated hub and its spokes share one administrative trust
domain. Treat anyone who controls an active peer as having control over fleet
workspace and terminal operations.

An active spoke can create, inspect, remove, and attach to workspaces on every
other member. It sends those requests to the hub, which forwards them using its
own credential for the owning member. Spokes never hold credentials for each
other.

Pending spokes have narrower access. They can resolve only the provider facts
needed for preparation, hand state to the hub, and use enrollment routes, and
they cannot browse general provider data or perform provider writes. Those
preparation routes are limited by operation, not by repository. Creating and
transferring the one-time token approves that machine to become a full fleet
member, so give a token only to a machine you trust with the fleet's provider
data.

## Before you start

Complete these steps on every machine:

1. Install the same `kenn-forge` build.
2. Install Git, tmux, your agent CLIs, and the language tools your local
   workspaces use, and authenticate the agents as the user that runs Forge.
3. Log in to the Git host you use for local clone, fetch, and push operations.
   For GitHub, `gh auth login` is the normal route.
4. Pick one stable private HTTPS origin for the machine.
5. Confirm every spoke can reach the hub. The hub reaches a spoke through a
   connection the spoke opens, so the hub does not need its own route to the
   spoke. See [Spokes the hub cannot reach](#spokes-the-hub-cannot-reach).

`fleet setup` configures Forge and its service. It does not provision the
operating system, install development tools, clone your repositories, or
authenticate agents. SSH access helps with provisioning and upgrades, while
federation itself uses HTTPS and does not require reciprocal SSH keys.

The examples below use the default config location. If your service uses a
custom config, pass that same `--config /path/to/config.toml` to **every** fleet
command, including setup, token creation, join, preparation, and revocation.
Those commands contact the local daemon selected by that config, not by the hub
URL.

Do not copy a Forge data directory between machines. Forge generates a stable
random node ID for each directory on first start.

A federation origin includes only a scheme and authority, such as
`https://build-a.example.test`. Forge rejects cleartext HTTP origins, URL
paths, query strings, fragments, and embedded credentials. You can include an
explicit port, and Forge strips the default `:443`.

Federation also requires Forge to use the root base path (`base_path = "/"`).
Non-root UI prefixes are not part of the federation protocol.

## Setup at a glance

Set up the hub and spoke services first, then enroll one spoke at a time. A
spoke stays standalone until preparation safely moves provider ownership to the hub
and a service restart activates the enrollment.

<figure class="fleet-diagram fleet-diagram--flow">
  <svg viewBox="0 0 960 260" role="img" aria-labelledby="fleet-enrollment-title fleet-enrollment-description">
    <title id="fleet-enrollment-title">Four steps to enroll one Forge spoke</title>
    <desc id="fleet-enrollment-description">Set up a hub and standalone spoke, create a one-time token on the hub and join from the spoke, prepare the spoke so provider state moves to the hub, then restart and verify the active spoke.</desc>
    <defs>
      <marker id="fleet-flow-arrow" markerWidth="10" markerHeight="10" refX="8" refY="3" orient="auto" markerUnits="strokeWidth">
        <path d="M0,0 L0,6 L9,3 z" class="fleet-diagram__arrowhead" />
      </marker>
    </defs>

    <circle class="fleet-diagram__step" cx="75" cy="42" r="23" />
    <text class="fleet-diagram__step-number" x="75" y="49" text-anchor="middle">1</text>
    <rect class="fleet-diagram__node" x="35" y="78" width="200" height="125" rx="14" />
    <text class="fleet-diagram__title" x="135" y="115" text-anchor="middle">Set up services</text>
    <text class="fleet-diagram__body" x="135" y="146" text-anchor="middle">Hub + standalone spoke</text>
    <text class="fleet-diagram__command" x="135" y="176" text-anchor="middle">fleet setup</text>

    <circle class="fleet-diagram__step" cx="310" cy="42" r="23" />
    <text class="fleet-diagram__step-number" x="310" y="49" text-anchor="middle">2</text>
    <rect class="fleet-diagram__node" x="270" y="78" width="200" height="125" rx="14" />
    <text class="fleet-diagram__title" x="370" y="115" text-anchor="middle">Join</text>
    <text class="fleet-diagram__body" x="370" y="146" text-anchor="middle">One-time hub token</text>
    <text class="fleet-diagram__command" x="370" y="176" text-anchor="middle">fleet join</text>

    <circle class="fleet-diagram__step" cx="545" cy="42" r="23" />
    <text class="fleet-diagram__step-number" x="545" y="49" text-anchor="middle">3</text>
    <rect class="fleet-diagram__node" x="505" y="78" width="200" height="125" rx="14" />
    <text class="fleet-diagram__title" x="605" y="115" text-anchor="middle">Prepare</text>
    <text class="fleet-diagram__body" x="605" y="146" text-anchor="middle">Drain + hand off state</text>
    <text class="fleet-diagram__command" x="605" y="176" text-anchor="middle">fleet prepare-spoke</text>

    <circle class="fleet-diagram__step" cx="780" cy="42" r="23" />
    <text class="fleet-diagram__step-number" x="780" y="49" text-anchor="middle">4</text>
    <rect class="fleet-diagram__node fleet-diagram__node--hub" x="740" y="78" width="185" height="125" rx="14" />
    <text class="fleet-diagram__title" x="832" y="115" text-anchor="middle">Activate</text>
    <text class="fleet-diagram__body" x="832" y="146" text-anchor="middle">Restart + verify</text>
    <text class="fleet-diagram__command" x="832" y="176" text-anchor="middle">active spoke</text>

    <path class="fleet-diagram__line" d="M235 141 H265" marker-end="url(#fleet-flow-arrow)" />
    <path class="fleet-diagram__line" d="M470 141 H500" marker-end="url(#fleet-flow-arrow)" />
    <path class="fleet-diagram__line" d="M705 141 H735" marker-end="url(#fleet-flow-arrow)" />
  </svg>
  <figcaption>Complete all four steps for one spoke and verify it before enrolling the next.</figcaption>
</figure>

## Set up the hub

Run setup as the operating-system user that runs Forge. With Tailscale, setup
discovers the machine's certificate name and current Tailscale login, publishes
Forge with Tailscale Serve, installs a per-user service, and checks the
protected HTTPS API.

To use Tailscale, join the machine to your tailnet and enable
[HTTPS certificates](https://tailscale.com/docs/features/tailscale-serve)
first. On Linux, an administrator can let the Forge user manage Tailscale
without running Forge as root:

```sh
sudo tailscale set --operator="$USER"
```

That grants the current user control of the local Tailscale daemon. See the
[Tailscale CLI reference](https://tailscale.com/docs/reference/tailscale-cli#set).
Next, run Forge setup as that user:

```sh
kenn-forge fleet setup hub --tailscale
```

On macOS, setup also finds the CLI inside the standard Tailscale app when
`tailscale` is not on `PATH`.

Review the plan Forge displays and confirm it. For unattended setup, inspect
the plan with `--dry-run`, then run the same command with `--yes`.

With `--tailscale`, anyone whose Tailscale login is on the allowlist can open
the UI without a token. Setup adds this device's login to the allowlist and
keeps any logins you allowed before. If your phone or another machine signs in
to Tailscale with a different login, for example when a device is shared from
another tailnet, add it:

```sh
kenn-forge fleet setup hub --tailscale \
  --tailscale-login you@example.com \
  --tailscale-login you@work.example
```

Passing `--tailscale-login` replaces the device's own login in the new
allowlist, so list every login you use. To remove a login later, delete it from
`[api.tailscale_serve] allowed_users` in your config and restart Forge.

Tailscale sends no login for [tagged devices](https://tailscale.com/docs/features/tags)
such as CI runners and servers, so the allowlist cannot admit them. To let them
in, grant an [app capability](https://tailscale.com/docs/features/access-control/grants/grants-app-capabilities)
in your tailnet policy and name it in Forge. Replace the example tags with
yours and pick a capability name under a domain you control:

```json
"grants": [
  {
    "src": ["tag:ci"],
    "dst": ["tag:forge"],
    "ip": ["tcp:443"],
    "app": {"example.com/cap/forge": [{}]}
  }
]
```

```toml
[api.tailscale_serve]
enabled = true
allowed_users = ["you@example.com"]
app_capability = "example.com/cap/forge"
```

Allowed logins keep working alongside the capability, and `allowed_users` may
be empty. Serve forwards a capability only when its mapping names it, which
requires Tailscale 1.92 or later. Publish Forge with the capability using your
own HTTPS and Forge ports, then restart Forge:

```sh
tailscale serve --bg --https=443 --accept-app-caps=example.com/cap/forge http://127.0.0.1:8091
```

If you run setup again, it keeps `app_capability` and passes it to Serve when
creating a new mapping. It leaves an existing mapping unchanged.

Every user or device granted the capability gets full access to Forge, including
agents and terminals on this machine, the same as an allowed login. Anyone who
can edit the tailnet policy therefore controls who gets that access.

Serve publishes on HTTPS port 443 by default. To use another port, such as
`https://build-a.example.ts.net:8091`, pass `--tailscale-https-port 8091`.
Running setup again preserves the port of the existing origin.

When MCP is enabled, the same logins and capability holders can reach it at the
Serve origin without a token, for example
`https://build-a.example.ts.net/mcp`. With no `[mcp]` setting, MCP is enabled
by default on a loopback Forge with no separate MCP port, which is how setup
configures Forge. Set `[mcp] enabled = false` to turn it off.

If your LAN, UniFi network, VPN, or reverse proxy already provides private
HTTPS, pass Forge its canonical origin instead:

```sh
kenn-forge fleet setup hub \
  --origin https://forge-hub.example.test
```

The `--origin` path never calls Tailscale. It configures and supervises the
same loopback Forge service while your ingress handles DNS and TLS, and it
keeps Forge's normal bearer and browser-cookie authentication.

Setup requires either `--tailscale` or `--origin`, not both. It never exposes
the Forge listener directly, weakens API authentication, or ties the service to
a particular network product.

The hub also needs its own provider credentials. Open **Settings → Repositories**
on the hub and add the repositories you want it to sync. Cloning a repository
on disk does not add it to this list. On a fresh hub, wait for the initial sync
to populate provider data before moving existing spokes.

## Set up each spoke

Run the matching setup command on each spoke:

```sh
# Tailscale Serve
kenn-forge fleet setup spoke --tailscale

# Operator-managed private HTTPS
kenn-forge fleet setup spoke --origin https://build-a.example.test
```

Spoke setup leaves the daemon in standalone mode by design. Do not set
`fleet.role = "spoke"` by hand for a spoke you enroll with `fleet join`.
Enrollment preparation writes that role only after provider writes drain and
state handoff finishes. If you
[declare spoke membership in config](#declare-membership-in-config), you set the
role in config instead, and Forge runs the same preparation on startup.

On Linux, setup installs a systemd user service and enables lingering so Forge
survives logout. On macOS, it installs a LaunchAgent under the selected user.
Both service definitions run the installed binary directly and preserve the
setup user's `HOME` and `PATH`, without starting an interactive shell or
copying arbitrary exported credentials. Configure any additional credential
environment through your service manager.

Run setup with a stable `PATH` that includes your agent CLIs and tool-manager
shims. A minimal SSH command environment can omit tools that work in your
interactive terminal. After the service restarts, verify your setup by
launching an agent from a Forge workspace.

Tailscale identity mode trusts local processes on the Forge host because
Tailscale Serve forwards identity headers over loopback. Use it on a
single-user or otherwise trusted machine. On a multi-user host, use an external origin
and Forge's bearer and cookie authentication instead.

### Adopt an existing service

If Forge already runs under a service you maintain, inspect
`fleet setup ... --dry-run` first. Setup refuses to overwrite a service
definition it does not own. If you have a federation-ready daemon with working
private HTTPS, keep its service and continue at
[Enroll one spoke at a time](#enroll-one-spoke-at-a-time). Re-enrollment does
not require reinstalling the service or moving its data.

If you want setup to own the service instead, back up the current definition
and configuration, stop it, and explicitly move the old definition aside before running
setup. Keep its data directory and installed binary path consistent with the
displayed plan.

## Spokes the hub cannot reach

Every active spoke keeps one tunnel open to its hub over the hub's HTTPS
origin, next to its provider event stream. While the tunnel is connected, the
hub sends all of its requests to that spoke through it: workspace snapshots,
remote workspace operations, terminals, browser login, and revocation. A spoke
that can reach the hub, even if the hub cannot reach it, therefore appears in
the hub's workspace view like any other spoke.

When the tunnel is down, the hub dials the spoke's enrolled origin directly.
If the hub can never reach that origin, add `outbound_disabled = true` to the
spoke's existing `[[fleet.members]]` entry in the **hub's** config, matching
the entry by its `node_id`. The change reloads without a restart.

The hub reaches an outbound-disabled spoke only through its tunnel. While the
tunnel is down, the hub skips snapshot requests for it, leaves it out of the
workspace host selector, and refuses remote operations targeting it without
waiting for a direct request to time out. The spoke stays listed in
**Settings → Fleet federation** and does not produce an unreachable-host
warning in the workspace view.

If you revoke an outbound-disabled spoke while its tunnel is down, the hub does
not call the spoke. The spoke observes the revocation the next time it checks
its activation with the hub.

## Keep credentials separate

Forge uses four separate credential types:

| Credential              | Stored on              | Purpose                                                          |
| ----------------------- | ---------------------- | ---------------------------------------------------------------- |
| Browser/API session     | Each daemon            | Authorizes a person using that daemon's UI or local API          |
| Federation credential   | Hub and enrolled spoke | Authorizes one exact Forge-to-Forge direction and route set      |
| Provider API credential | Hub                    | Synchronizes provider data and performs provider mutations       |
| Git credential          | Each execution machine | Clones, fetches, pulls, and pushes directly against the Git host |

Do not copy browser cookies or API tokens between machines. Enrollment creates
a separate machine credential in each direction, and Forge stores inbound
credentials as digests while keeping credential files private to the daemon
account.

A spoke needs its own Git credential even though it does not sync provider
data. Preparation checks the exact Git credential route before storing a
workspace launch specification.

The hub may have more than one credential source. User-attributed provider
mutations use a user credential, while Forge-managed Git follows normal
credential priority and can use an App credential for repositories covered by
that App. Verify the route that matters to your deployment instead of assuming
all hub Git traffic uses one credential.

Before calling a spoke, the hub strips browser authorization, cookies, origin,
and forwarding headers. It attaches only the federation credential enrolled for
that exact HTTPS origin and does not follow redirects.

## Enroll one spoke at a time

Finish and verify one spoke before enrolling the next. That keeps state handoff
and rollback scoped to a single machine.

### 1. Create a one-time token

On the hub, run:

```sh
umask 077
kenn-forge fleet enrollment-token \
  --base-url https://forge-hub.example.test \
  --name "Forge hub" \
  --ttl 10m > ./forge-enrollment-token
```

Forge prints the token once. It expires and can create one enrollment. Creating
and transferring it approves that spoke to finish preparation and activate.
Treat it as a membership credential.

Transfer the file through an approved secret-sharing channel. Never put the
token in a command-line argument or config file.

### 2. Join from the spoke

On `build-a`, run:

```sh
kenn-forge fleet join https://forge-hub.example.test \
  --base-url https://build-a.example.test \
  --name "Build spoke A" \
  --token-file ./forge-enrollment-token
rm ./forge-enrollment-token
```

For automation, pass the token on standard input instead of writing a file on
the spoke:

```sh
secret-command | kenn-forge fleet join \
  https://forge-hub.example.test \
  --base-url https://build-a.example.test \
  --name "Build spoke A"
```

If you provide neither a file nor standard input in an interactive terminal,
Forge prompts for the token with hidden input.

Joining records a pending enrollment without changing the spoke role or
restarting the daemon. Save the printed enrollment ID alongside the host and
origin in your private operations inventory. Revocation uses that ID. Remove
any remaining copies of the token after a successful join.

### 3. Prepare the spoke

On the spoke, run:

```sh
kenn-forge fleet prepare-spoke
```

Preparation stops new provider writes, waits for admitted writes and deferred
merges, drains notification acknowledgements, refreshes workspace launch
information, hands review drafts to the hub, and seals local provider writes.
If it reports concrete remaining work, resolve that work and run the command again.

This handoff does not copy the entire provider database or move repositories,
worktrees, or agent sessions. The hub must already track the repositories your
existing workspaces need, and the spoke must be able to use their Git
credentials.

After preparation starts, the token's original deadline no longer applies.

### 4. Restart and verify activation

When preparation reports completion, restart Forge through the spoke's service
manager. Activation happens during startup. Check that the spoke shows as
active in the hub's **Settings → Fleet federation** and in the spoke's direct
UI.

For services installed by `fleet setup`, run the command for your platform as
the service user:

```sh
# Linux
systemctl --user restart kenn-forge.service
systemctl --user status kenn-forge.service --no-pager

# macOS, in the user's GUI login session
launchctl kickstart -k "gui/$(id -u)/io.kenn.forge"
```

For an existing custom service, use its actual unit or LaunchAgent label. A
running process alone does not prove enrollment activated.

Complete the checks in [Verify the fleet](#verify-the-fleet) for this spoke
before enrolling the next one.

## Declare membership in config

Instead of running the four enrollment steps, you can declare the hub and its
spokes in each daemon's config. This works well for fleets deployed with a
configuration management tool. A declared spoke joins, prepares, and activates
on its first start, with no CLI step and no second restart.

Give each spoke its own enrollment secret of at least 32 characters with no
whitespace, such as the output of `openssl rand -hex 32`. Place the same secret
in a file on both the hub and that spoke, readable only by the Forge service
user. Secret managers that publish files as symlinks work. Never put the secret
itself in `config.toml`.

On the hub, list each spoke by name and origin:

```toml
[fleet]
enabled = true
base_url = "https://forge-hub.example.test"

[[fleet.declared_spokes]]
name = "build-a"
base_url = "https://build-a.example.test"
enrollment_secret_file = "/run/secrets/forge-fleet-build-a"
```

On the spoke, set the role and name the hub:

```toml
[fleet]
enabled = true
role = "spoke"
base_url = "https://build-a.example.test"

[fleet.hub]
base_url = "https://forge-hub.example.test"
enrollment_secret_file = "/run/secrets/forge-fleet-build-a"
```

Declared membership also works with a default machine. On a declared spoke, you
can set `workspaces.default_execution_target` to another member's origin, which
configuration management knows before any machine starts. See
[Choose where new workspaces run](#choose-where-new-workspaces-run).

Forge writes what it learns back to `config.toml`: the hub's node ID in
`fleet.hub.node_id` on the spoke, and each active spoke in `fleet.members` on
the hub. The Forge service user must be able to write the file. If your
configuration tool rewrites `config.toml` without those entries, Forge restores
them on the next start or config reload.

Deploy the hub first, then start the spoke. On startup, the spoke:

1. Joins the hub with the secret. The hub admits the join only from the origin
   it declares for that secret.
2. Runs the same preparation as `fleet prepare-spoke`, draining provider writes
   and handing review drafts to the hub.
3. Activates, like a restart after manual preparation.

Until the spoke activates for the first time, Forge exits with an error if it
cannot read its secret file, write its config, or reach the hub, and its
service manager restarts it until all three succeed. After activation, the
spoke behaves like any enrolled spoke: restarts keep the same enrollment, and a
hub outage pauses only provider work. If the hub rejects the join or
preparation finds work it cannot finish, the spoke starts with local execution
only and logs the reason along with the next step. Fix the cause and restart
the spoke.

If the hub stays unreachable for more than 24 hours after the spoke joins and
before preparation starts, the hub drops the pending enrollment. Write a new
secret on both machines, and the spoke joins again on its next start.

Because a spoke cannot send notification read marks to the provider, a
standalone daemon with unsent read marks cannot finish preparation after you
declare it a spoke. Convert that daemon with `fleet join` and
`fleet prepare-spoke` while it still runs standalone, then add the declared
config.

Each secret starts one enrollment, like a one-time token, and a spoke that
restarts with a joined enrollment does not use its secret again. Rotate the
secret on both machines when either of these happens:

- You reinstall a spoke with a new data directory. The hub revokes the
  enrollment of the machine previously at that origin and enrolls the new one.
- You see a join rejected because the secret was already consumed. This happens
  when the hub accepted a join but the spoke never received the reply.

Removing a spoke from `fleet.declared_spokes` revokes its enrollment when the
hub reloads its config or restarts, and the hub logs
`revoke fleet spoke removed from fleet.declared_spokes`. If the hub can reach
the spoke, it notifies the spoke. If the spoke is offline, it learns
of the removal the next time it renews its activation lease, within 6 hours of
coming back, or the next time it starts.

Once the spoke learns of the removal, it starts with local execution only, its
local-only reason says the hub revoked its enrollment, and
`kenn-forge fleet leave` accepts that spoke. It
keeps starting with local execution only, even after you delete its secret
file, until you either follow
[Return a spoke to standalone operation](#return-a-spoke-to-standalone-operation)
or write a new secret on both machines and declare it again. If the hub refuses
a new secret, the spoke keeps its revoked enrollment and `fleet leave` still
accepts it.

Declared and CLI-enrolled spokes can share a hub. If a spoke enrolled with
`fleet join` sits at an origin you later declare, the hub adopts it as declared
membership, so you can move an existing fleet to declared config without
re-enrolling. Removing declarations never revokes spokes you enrolled with the
CLI and did not declare.

## Abort or revoke an enrollment

To restore standalone operation from the spoke before activation, run:

```sh
kenn-forge fleet abort-preparation
```

This command revokes the pending enrollment and reopens the durable
provider-write gate. On a standalone process, provider writes resume right
away. If the daemon already restarted in spoke mode, the command tells you that
another restart is required.

If the hub is unavailable, pass `--force` to recover locally and print the
enrollment ID you need to revoke later. The spoke keeps only a narrow
revocation credential so hub cleanup can still finish later.

To remove a pending or active enrollment, run on the hub:

```sh
kenn-forge fleet revoke ENROLLMENT_ID
```

The spoke must be reachable on the first attempt so it can invalidate its local
side of the relationship. Once the spoke acknowledges revocation, retries can
finish hub cleanup without contacting it again. Revoke before removing a spoke
from configuration. After activation, restoring a former standalone spoke
requires its complete pre-enrollment set: config, database, federation store,
credential store, and binary. Do not restore only one file from that set.

## Verify the fleet

Configuration checks are not enough on their own. Exercise the protected paths
and day-to-day behavior before relying on the fleet.

### Transport and identity

- Open every direct HTTPS UI and establish its own authenticated browser
  session.
- Confirm every HTTPS certificate validates normally.
- Confirm the HTTPS ingress is reachable only through the intended private
  network.
- Confirm every daemon reports a unique node ID.
- Restart each daemon and confirm its node ID and enrollment persist.

### Fleet view and local ownership

- Confirm every UI shows the same fleet host set and provider data.
- Create a disposable workspace on each execution host.
- Confirm every UI lists each workspace under its owning machine.
- Start a tmux-backed session and attach through the spoke's direct UI.
- Attach to the same session through the hub's WebSocket proxy and through
  another spoke's UI.
- From one spoke's UI, create a workspace on another spoke, then remove it and
  confirm the owning spoke performs the deletion.

### Provider and Git ownership

- Watch provider sync run on the hub.
- Confirm spokes run no provider sync worker.
- Trigger one safe provider mutation from a spoke UI and confirm it completes
  through the hub.
- Clone or fetch a private repository on every spoke and confirm Forge uses
  that machine's Git credential route.
- Confirm every spoke does not independently spend provider API request budget.

### Failure behavior

- Stop one spoke. Healthy machines and their workspaces stay visible, and only
  the stopped spoke becomes unavailable.
- Stop the hub. Each spoke keeps authority over its local workspaces, loses
  access to other members' workspaces, keeps provider data already loaded in
  that browser tab visible behind a stale-data banner, disables **Sync** and
  pull-request **Refresh detail**, and marks aggregate data incomplete.
- Reload a spoke UI while the hub is still stopped. The new page has no
  persistent provider cache, so provider pages cannot repopulate, while local
  workspaces stay usable.
- Restart the hub. Spokes reconnect, replay events, reconcile provider state,
  and restore the fleet view without a browser reload.
- Restart each spoke. Its direct UI, local workspaces, enrollment, and remote
  terminal attachment recover.

## Use the fleet

Use the Forge selector in the top bar to open any fleet member directly.
Choosing another fleet member signs you in to that Forge automatically and
opens the same page there. A spoke signs you in to another spoke through the
hub, which asks that spoke for a one-time login link. If the hub is
unavailable to the spoke, or runs a Forge version without this, the other
spoke opens as an ordinary link and uses its own sign-in, such as Tailscale
Serve. Browser modifiers open the plain link in another tab or window. Each
origin keeps its own browser state, filters, searches, terminal connections,
and event cursors, and changing one Forge tab does not retarget another.

Every member's UI shows the fleet-wide workspace view, with an execution-node
badge on each workspace row. Supported actions call the owning machine's HTTPS
API, directly from the hub and through the hub from a spoke:

- create a repository, pull-request, or issue workspace on any writable member;
- inspect, refresh, or remove a workspace on its owning machine;
- launch or stop a runtime session, including quick actions;
- inspect local Git state;
- open a terminal through the WebSocket bridge.

In **New workspace**, the **Run on** selector starts on this Forge's default
machine and keeps unavailable machines visible along with the reason each one
is unavailable.

No Forge creates a local proxy process for a remote terminal. Input, output,
resize messages, and close events pass between WebSocket connections: one hop
from the hub, two from a spoke.

If the hub goes down, a spoke still works for its own workspaces, though it
cannot reach other members' workspaces or show current hub-owned provider data.

### Choose where new workspaces run

To set the fleet member where new workspaces from a Forge UI run by default,
set `workspaces.default_execution_target` in that Forge's `config.toml` to the
member's HTTPS origin or node ID:

```toml
[workspaces]
default_execution_target = "https://build-b.example.test"
```

For example, set this on the `build-a` spoke to work from its UI while every
new workspace runs on `build-b`. The hub keeps syncing the provider and
applying provider changes as before. The setting also accepts `devbox:<id>` for
a [devbox](devboxes.md) on a hub or standalone Forge. Leave it empty, or name
this Forge itself, to create workspaces locally, so configuration management
can write the same value on every machine.

**Settings → Workspaces** lists the fleet members as well and saves the same
key. This default applies to the pull-request and issue workspace buttons,
quick actions, onboarding, and the **Run on** selector's starting choice.

When the default machine is offline, Forge does not fall back to the local
Forge. It disables the workspace button, names the unavailable machine and why
it is unavailable, and offers a **Create on** button for each machine that can
take the workspace now. Running a quick action on selected items reports the
unavailable machine for each item and creates nothing.

A spoke reaches other members only through an active hub. When upgrading,
upgrade the hub first, then restart each spoke. The hub grants the spoke this
access when the spoke next activates.

## Understand Sync, Refresh, and hub outages

Provider controls keep the same owner regardless of which Forge UI you open:

| Action in a spoke UI             | What Forge does                                                                                                                                                               |
| -------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Top-bar **Sync**                 | Asks the hub to sync its configured repositories. The spoke does not run a second provider sync.                                                                              |
| **Sync current repo**            | Asks the hub to sync only the repository selected by the route or repository filter. Use this when one repository needs attention without spending a full-fleet sync budget.  |
| Pull-request **Refresh detail**  | Asks the hub to fetch that pull request, then returns the refreshed detail to the spoke.                                                                                      |
| Start a shell or agent           | Starts the session on the spoke from the existing local workspace. It does not contact the hub.                                                                               |
| Provider mutation                | Sends the mutation to the hub, which applies it with the hub's provider credential.                                                                                           |

Opening a pull request normally reads the hub's stored detail first. If the
repository is tracked but that pull request is missing from the stored
projection, Forge automatically runs one targeted pull-request sync, repairing
a cold or partially completed repository sync without starting another full
sync. If the repository itself is not configured, Forge points you to
**Settings → Repositories**. Add it there, then use **Sync current repo** or
reopen the pull request.

When a spoke loses its connection to the hub, it keeps provider data already
loaded in that browser tab visible and labels it as stale. Forge disables
**Sync** and pull-request **Refresh detail**, makes provider changes
unavailable, and keeps local workspace and terminal operations working. New
shell and agent sessions in an existing workspace stay local as well, and
generated agent context uses the last validated workspace facts. A fresh
browser load has no durable provider cache, so it cannot rebuild provider pages
until the hub returns.

Reconnection happens automatically. Before clearing the stale-data banner, the
spoke replays hub events and reconciles the visible provider projection. Do not
reload the page only to recover a connection.

Installing a provider API credential on a spoke does not set up automatic
failover. In spoke role, Forge uses local credentials for Git clone, fetch,
pull, and push, and does not construct an independent provider syncer.
Automatic multi-writer provider failover is not currently supported.

## Advanced: publish with your own reverse proxy

The setup command always binds Forge to loopback and validates the public
authority with `trust_reverse_proxy = false`. Configure your proxy to preserve
the incoming `Host` header and forward traffic to the configured loopback port,
without rewriting `Host` to the upstream address.

For example:

```caddyfile
https://build-a.example.test {
    bind 192.0.2.20
    reverse_proxy 127.0.0.1:8091
}
```

Use your real private address and a certificate trusted by every fleet machine.
Verify the HTTPS URL with the normal operating-system trust store. Do not use
`curl -k`. It hides certificate and hostname mistakes that also break
federation.

`--origin` does not manage this proxy, its certificate, DNS, or browser login.
That separation is intentional because the federation contract is canonical
HTTPS, not Tailscale or Caddy.

## Replace the hub

Do not replace a hub by changing DNS or copying one credential. Every
enrollment is bound to the hub's stable node ID and canonical origin, and the
hub also stores provider state alongside its own local execution state.

Pick the recovery path that matches your goal before changing enrollment:

| Goal | Approach |
| --- | --- |
| Restore the same hub after an OS reinstall | Restore its complete matching backup, including identity, configuration, database, and credentials. Restore its original HTTPS origin and verify existing spokes. |
| Start with a fresh hub and retain local workspaces on other machines | Create a new hub identity, configure its repositories, then revoke and re-enroll each old member using the procedure below. |
| Move the complete provider archive to a different hub while leaving the old hub's execution state behind | Forge does not provide an automated selective migration for this. Retain the old backup; do not transplant its database or individual credentials into a new hub. |

### Move to a fresh hub

This procedure rebuilds provider data through sync. It keeps each retained
machine's local identity and workspaces, though it does not reproduce the old
hub's full archive. Preparation transfers review drafts when each machine
joins.

The old hub and spokes must stay reachable for revocation. If you lost the old
hub in a reinstall, restore it from its matching backup first. A replacement at
the same hostname with an empty data directory is still a different hub.

1. **Record and back up the old fleet.** Record each machine's node ID, origin,
   enrollment ID, config and data paths, service definition, binary version,
   and local workspace list. Pause fleet changes and stop the Forge services
   while you take a consistent backup of their databases and state, including
   any SQLite WAL files, enrollment and credential stores, configuration,
   binaries, and service definitions. Keep these as matching rollback sets
   restricted to the operator, and back up local repositories and worktrees
   separately if they live outside those paths. Restart the old fleet and
   verify it before continuing.
2. **Prepare the new hub.** Give the new hub a fresh data directory and its own
   private HTTPS origin. Follow [Set up the hub](#set-up-the-hub), authenticate
   its provider and Git access, and configure its repository list. Test actual
   repository and pull-request reads, plus any local agent tools the hub runs.
3. **Move one spoke.** On the old hub, run
   `kenn-forge fleet revoke ENROLLMENT_ID` with that spoke's old enrollment ID.
   Then create a new token on the new hub and follow
   [join, prepare, restart, and verify](#enroll-one-spoke-at-a-time) on the
   spoke, using the new hub origin while keeping the spoke's existing origin,
   config, and data directory. Expect provider access to pause between
   revocation and activation. Before moving the next spoke, confirm the spoke's
   node ID and local workspace list are unchanged and that the new hub can
   reach its workspaces and terminals.
4. **Move the old hub last, if you want it to become a spoke.** Revoke all of
   its old members first. A hub with members cannot join another hub. Keep
   its existing service, origin, and data directory, then run the same join,
   prepare, and restart sequence against the new hub. Its local workspaces stay
   on that machine. Do not set its role by hand or run spoke setup only to
   change the role. If you are retiring the old hub instead, stop its service
   after the cutover.
5. **Verify and record the final fleet.** Check every direct HTTPS UI and the
   new hub's aggregate workspace view, and run a local agent and a terminal
   through the hub on each execution machine. Record the new enrollment IDs,
   service restart commands, and verification results. Keep the pre-cutover
   backups until the replacement passes your recovery checks.

### Roll back a cutover

Rolling back restores a coordinated fleet state, not only the old hub's
database. Stop the new hub and affected Forge services first, preserve current
state and any work created since the cutover, and then restore the old hub and
affected members from their matching pre-cutover sets along with their service
definitions and original ingress bindings. Start the old hub, then start its
spokes, and verify their original identities, enrollment, and workspace access.
Keep the replacement hub stopped while the restored fleet is authoritative.

Database rollback can discard changes made after the backup. Do not overwrite
new worktree contents when restoring Forge state. Revoking a new enrollment
alone does not restore the old enrollment or provider ownership.

## Return a spoke to standalone operation

Keep the spoke running and connected until revocation finishes. If an
outbound-disabled spoke has its tunnel down, the hub revokes it without a
callback. On the hub, run:

```sh
kenn-forge fleet revoke ENROLLMENT_ID --config /path/to/hub/config.toml
```

Stop the spoke's Forge service while leaving its worktrees and tmux sessions in
place. Keep a matching backup of its current data directory, configuration,
binary, and service definition, as described in
[Move to a fresh hub](#move-to-a-fresh-hub). Then, on the stopped spoke, run:

```sh
kenn-forge fleet leave --config /path/to/spoke/config.toml
```

This command disables federation and removes the old hub's credentials while
keeping the current node identity, local database, workspaces, and session
records. You can repeat it after an interruption. It does not restore an old
database or copy the hub's provider archive, review drafts, or credentials.

Configure this machine's own provider access and repository list before
starting Forge, sync its repositories, and then check its local workspaces and
terminals. Provider state created on the hub stays there. For pending
enrollments, use `fleet abort-preparation` instead of `fleet leave`.

After running `abort-preparation --force`, finish pending revocation on the hub
before disabling federation. `leave` must not remove the credential needed for
that cleanup, and an already-disabled standalone instance stays unchanged.

## Upgrade federation protocol 3 to 4

A protocol-3 fleet needs a one-time data migration before protocol-4 binaries
can use its enrollment. Keep a matching rollback backup of every host's
database, enrollment and credential stores, configuration, and binary.

1. Stop the Forge service on every fleet member, leaving its worktrees and tmux
   sessions in place.
2. On each host, run the new binary against that host's existing configuration:

   ```sh
   /path/to/new/kenn-forge fleet migrate-protocol --config ~/.kenn/forge/config.toml
   ```

3. Install the new binary, start the hub, and then start its spokes. Verify the
   fleet workspace list and terminal access on each member.

The migration keeps node identities, enrollment, credentials, preparation
seals, local workspaces, and provider data, though it does not allow
protocol-3 and protocol-4 peers to talk to each other. Repeating the command
after an interruption resumes the migration, and already migrated or unenrolled
hosts need no changes. To roll back, stop the affected services and restore
every host's matching pre-migration state and binary together.

## Replace an older fleet

Forge does not accept older key-based or shell-relay entries. Before starting
the new binary:

1. Remove the old fleet entries from every config file.
2. Enable API authentication and pick the hub.
3. Start each daemon with a reachable HTTPS origin.
4. Enroll every spoke again with a new one-time token.

Do not copy old tokens, node IDs, relay sockets, or daemon data directories.
There is no automatic translation because the older entries did not establish
the identity and credential pair federation requires.

## Troubleshooting

### Setup fails its HTTPS check

Read the setup error and any rollback errors before retrying. When a later step
fails, setup tries to restore its previous config, service, and managed
publication. Check what is actually running instead of assuming a failed setup
left a usable service behind.

For Tailscale, confirm the device is logged in, the Forge user can manage
Serve, and HTTPS certificates are enabled. First-time certificate issuance can
outlast Forge's readiness check. Inspect Tailscale's certificate and Serve
status, fix the reported problem, and rerun the setup plan. For external
ingress, check the proxy route, preserved `Host` header, and certificate trust.

### Agents work in SSH but not in Forge

Check the service's `PATH` and the service user's agent authentication. The
generated service does not load shell startup files. For a service owned by
setup, rerun the setup command with the stable tool paths you want. For a
custom service, update its environment through your service manager. Restart
Forge and launch an agent from a workspace to verify.

### A fresh hub is missing repositories or pull requests

Check **Settings → Repositories** on the new hub first. Enrollment does not
import the old hub's repository list. Next, inspect sync status: a large first
sync can exhaust the available request budget before all repositories finish.
Let the budget recover and use **Sync current repo** for the repository you
need instead of repeatedly starting a full sync. Verify a real repository or
pull-request read separately from fleet reachability.

### A spoke is unreachable

A running spoke that can reach its hub keeps a tunnel open, and the hub uses
it. Start by confirming that the spoke daemon is running and can reach the hub.

Without a tunnel, the hub dials the spoke directly. From the hub machine, check
the spoke's HTTPS origin and certificate, and confirm the URL exactly matches the
enrolled origin. A changed hostname, port, or certificate route requires deliberate
re-enrollment rather than a redirect.

If the daemon is down, restart it through its local service manager. The hub
does not log in to start it.

### A request is forbidden

Read the full degraded-host message in the hub. Forge preserves the peer's
authorization reason instead of collapsing it to an HTTP status.

If the message says fleet authorization is inactive, check the spoke service's
startup log and confirm it can resolve and reach the hub. An active spoke
revalidates its sealed enrollment with the hub at startup and every six hours.
If the hub is temporarily unavailable, the spoke can use its last confirmed
activation lease for up to 24 hours. Once that lease expires, the spoke rejects
hub requests until it reaches the hub and renews the lease.

The lease handshake is versioned separately from fleet snapshots. During a
rolling upgrade, upgrade the hub first, then each spoke. A lease-unaware spoke
stays isolated from fleet reads and writes until you upgrade and restart it.
Its existing enrollment carries over, so you do not need to re-enroll it. If
you upgraded a spoke before its hub, upgrade the hub and restart that spoke so
it can complete the lease handshake.

If the peer rejects the hub identity, check that the enrollment is active and
has not been revoked. Re-enroll instead of widening or copying a token by hand.
A valid credential can still be forbidden when its direction does not grant the
requested operation.

### A terminal does not open

Confirm the spoke is reachable and the local session still exists. The hub
attaches to the spoke's terminal WebSocket, not directly to its tmux server.

### A config no longer loads

Remove old fleet keys and relay entries, then use the enrollment workflow or
[declare membership in config](#declare-membership-in-config). Forge fails
closed instead of guessing how an old host label maps to a stable spoke
identity.
