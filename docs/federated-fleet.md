# Federated Forge

A Kenn Forge fleet gives several development machines one shared view without
moving their local work to a central server.

- One **hub** owns provider synchronization, provider mutations, and
  the fleet-wide view.
- Each **spoke** owns its local repositories, workspaces, processes, Git
  traffic, and tmux sessions.
- Every machine keeps its own directly accessible Forge UI.
- Forge-to-Forge requests and terminal WebSockets use authenticated HTTPS.

Every machine runs the same `kenn-forge` binary. The hub does not use
SSH to start a spoke or attach to its tmux server. Start and supervise each
daemon on its own machine.

This guide uses the following example topology. Each name is a private HTTPS
origin reachable from every fleet machine:

```text
https://forge-hub.example.test  hub and local execution
https://build-a.example.test            spoke and local execution
https://build-b.example.test            spoke and local execution
```

Tailscale Serve is a first-class way to provide those origins, but it is not a
fleet requirement. A private LAN, UniFi network, VPN, or custom reverse proxy
works through the same federation protocol.

## Understand the trust boundary

Use federation only inside one operator-controlled private network. Keep every
Forge listener behind that private ingress.

Private network access is necessary, but it is not sufficient. Forge also
requires HTTPS, local browser authentication, and separate credentials created
during enrollment. An activated hub and its spokes form one
administrative trust domain. Someone who controls an active peer should be
treated as controlling fleet workspace and terminal operations.

Pending spokes have narrower access. They can resolve only the provider facts
needed for preparation, hand state to the hub, and use enrollment
routes. They cannot browse general provider data or perform provider writes.
Those preparation routes are limited by operation, not by repository. Creating
and transferring the one-time token approves that machine to become a full
fleet member, so give a token only to a machine you trust with the fleet's
provider data.

## Before you start

On every machine:

1. Install the same `kenn-forge` build on every machine.
2. Log in to the Git host used for local clone, fetch, and push operations. For
   GitHub, `gh auth login` is the normal route.
3. Choose one stable private HTTPS origin for that machine.
4. Confirm every machine can resolve and reach every origin.

Do not copy a Forge data directory between machines. Each directory receives a
stable random node ID on first start.

A federation origin contains only a scheme and authority, such as
`https://build-a.example.test`. Forge rejects cleartext HTTP origins, URL
paths, query strings, fragments, and embedded credentials. Explicit ports are
allowed; default `:443` is normalized away.

Federation also requires Forge to use the root base path (`base_path = "/"`).
Non-root UI prefixes are not part of the federation protocol.

## Set up the hub

Run setup as the operating-system user that will run Forge. The normal
Tailscale path discovers the machine's certificate name and current Tailscale
login, publishes Forge with Tailscale Serve, installs a per-user service, and
checks the protected HTTPS API:

```sh
kenn-forge fleet setup hub --tailscale
```

On macOS, setup also finds the command-line client inside the standard
Tailscale app when `tailscale` is not on `PATH`.

Review the displayed plan and confirm it. For unattended setup, inspect the
same plan with `--dry-run`, then repeat with `--yes`.

If your LAN, UniFi network, VPN, or reverse proxy already provides private
HTTPS, give Forge its canonical origin instead:

```sh
kenn-forge fleet setup hub \
  --origin https://forge-hub.example.test
```

The `--origin` path never calls Tailscale. It configures and supervises the
same loopback Forge service, while your ingress owns DNS and TLS. It retains
Forge's normal bearer and browser-cookie authentication.

Setup requires exactly one of `--tailscale` and `--origin`. It does not expose
the Forge listener directly, weaken API authentication, or make the service
depend on a particular network product.

The hub also needs the provider credentials and repository
configuration used for synchronization.

## Set up each spoke

Run the matching setup command on each spoke:

```sh
# Tailscale Serve
kenn-forge fleet setup spoke --tailscale

# Operator-managed private HTTPS
kenn-forge fleet setup spoke --origin https://build-a.example.test
```

Spoke setup deliberately leaves the daemon in standalone mode. Do not set
`fleet.role = "spoke"` by hand. Enrollment preparation persists that role only
after provider writes drain and state handoff completes.

On Linux, setup installs a systemd user service and enables lingering so Forge
survives logout. On macOS, it installs a LaunchAgent under the selected user.
Both service definitions execute the installed binary directly and preserve
the user's credential environment.

Tailscale identity mode treats local processes on the Forge host as trusted,
because Tailscale Serve forwards identity headers over loopback. Use it on a
single-user or otherwise trusted machine. On a multi-user host, use an external
origin and Forge's bearer/cookie authentication instead.

## Keep credentials separate

Four credential types serve different purposes:

| Credential              | Stored on              | Purpose                                                          |
| ----------------------- | ---------------------- | ---------------------------------------------------------------- |
| Browser/API session     | Each daemon            | Authorizes a person using that daemon's UI or local API          |
| Federation credential   | Hub and enrolled spoke | Authorizes one exact Forge-to-Forge direction and route set      |
| Provider API credential | Hub                    | Synchronizes provider data and performs provider mutations       |
| Git credential          | Each execution machine | Clones, fetches, pulls, and pushes directly against the Git host |

Do not copy browser cookies or API tokens between machines. Enrollment creates
a different machine credential in each direction. Forge stores inbound
credentials as digests and keeps credential files private to the daemon
account.

A spoke needs its own Git credential even though it does not synchronize
provider data. Preparation verifies the exact Git credential route before it
stores a workspace launch specification.

The hub may have more than one credential source. User-attributed
provider mutations use a user credential. Forge-managed Git follows normal
credential priority and can use an App credential for repositories covered by
that App. Verify the route that matters to your deployment instead of assuming
all hub Git traffic uses one credential.

The hub strips browser authorization, cookies, origin, and forwarding
headers before it calls a spoke. It adds only the federation credential enrolled
for that exact HTTPS origin and does not follow redirects.

## Enroll one spoke at a time

Finish and verify one spoke before enrolling another. This keeps state handoff
and rollback bounded to one machine.

### 1. Create a one-time token

On the hub:

```sh
umask 077
kenn-forge fleet enrollment-token \
  --base-url https://forge-hub.example.test \
  --name "Forge hub" \
  --ttl 10m > ./forge-enrollment-token
```

The token is printed once, expires, and can create one enrollment. Creating and
transferring it approves that spoke to finish preparation and activate. Treat it
as a membership credential.

Transfer the file through an approved secret-sharing channel. Do not put the
token in a command-line argument or config file.

### 2. Join from the spoke

On `build-a`:

```sh
kenn-forge fleet join https://forge-hub.example.test \
  --base-url https://build-a.example.test \
  --name "Build spoke A" \
  --token-file ./forge-enrollment-token
rm ./forge-enrollment-token
```

For automation, pass the token on standard input instead of creating a spoke-side
file:

```sh
secret-command | kenn-forge fleet join \
  https://forge-hub.example.test \
  --base-url https://build-a.example.test \
  --name "Build spoke A"
```

Interactive terminals use a hidden token prompt when neither input method is
provided.

Joining records a pending enrollment. It does not change the spoke role or
restart the daemon.

### 3. Prepare the spoke

Run on the spoke:

```sh
kenn-forge fleet prepare-spoke
```

Preparation stops new provider writes, waits for admitted writes and deferred
merges, drains notification acknowledgements, refreshes workspace launch
information, hands provider state to the hub, and seals local provider
writes. If it reports concrete remaining work, resolve that work and run the
command again.

The token's original deadline no longer applies after preparation starts.

### 4. Restart and verify activation

When preparation reports completion, restart Forge through the spoke's service
manager. Activation happens during startup. Verify the spoke is active in the
hub's Fleet settings and in the spoke's direct UI.

Do not enroll the next spoke yet. Complete the checks in [Verify the
fleet](#verify-the-fleet) for this spoke first.

## Abort or revoke an enrollment

Before activation, restore standalone operation from the spoke:

```sh
kenn-forge fleet abort-preparation
```

This revokes the pending enrollment and reopens the durable provider-write
gate. On a standalone process, provider writes resume immediately. If the
daemon already restarted in spoke mode, the command reports that another
restart is required.

If the hub is unavailable, `--force` performs local recovery and
prints the enrollment ID that must be revoked later. The spoke retains only a
narrow revocation credential so that later hub cleanup can still complete.

To remove a pending or active enrollment, run on the hub:

```sh
kenn-forge fleet revoke ENROLLMENT_ID
```

The first attempt requires the spoke to be reachable so it can invalidate its
local side of the relationship. Once the spoke acknowledges revocation, retries
can finish hub cleanup without contacting it again. Revoke before removing a
spoke from configuration. After activation, restoring a former standalone
spoke requires its complete pre-enrollment unit: config, database, federation
store, credential store, and binary. Do not restore only one file from that set.

## Verify the fleet

Configuration checks are not enough. Exercise the protected paths and the
behavior an operator depends on.

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
- Confirm each spoke UI shows fresh local state for its own workspace and does
  not list workspaces owned by the hub or another spoke.
- Confirm the hub shows each workspace under its owning spoke.
- Start a tmux-backed session and attach through the spoke's direct UI.
- Attach to the same session through the hub's WebSocket proxy.
- Remove the disposable spoke workspace from the hub and confirm the
  spoke performs the deletion.

A spoke UI keeps the fleet host directory in the Forge selector, but its
workspace surface is local-only. Use the selector to open another spoke, or use
the hub for the fleet-wide workspace view and remote mutations.

### Provider and Git ownership

- Observe provider synchronization on the hub.
- Confirm spokes run no provider synchronization worker.
- Start one safe provider mutation from a spoke UI and observe it complete
  through the hub.
- Clone or fetch a private repository on every spoke and confirm that machine's
  Git credential route is used.
- Confirm provider API request budget is not independently consumed by every
  spoke.

### Failure behavior

- Stop one spoke. Healthy machines and their workspaces should remain visible;
  only the stopped spoke should become unavailable.
- Stop the hub. Each spoke should retain local workspace authority,
  keep provider data already loaded in that browser tab visible behind a
  stale-data banner, disable Sync and pull-request Refresh, and mark aggregate
  data incomplete.
- Reload a spoke UI while the hub remains stopped. The new page has no
  persistent provider cache, so provider pages cannot repopulate; local
  workspaces must remain usable.
- Restart the hub. Spokes should reconnect, replay events, reconcile
  provider state, and restore the fleet view without a browser reload.
- Restart each spoke. Its direct UI, local workspaces, enrollment, and remote
  terminal attachment should recover.

## Use the fleet

Use the Forge selector in the top bar to open any fleet member directly. It is
an ordinary link, so browser modifiers open another tab or window. Each origin
keeps its own browser state, filters, searches, terminal connections, and event
cursors; changing one Forge tab does not retarget another.

Open the hub UI for the aggregate workspace view and fleet-wide
operations. Supported actions call the owning spoke's HTTPS API:

- create, inspect, refresh, or remove a workspace;
- launch or stop a runtime session;
- inspect local Git state;
- open a terminal through the WebSocket bridge.

The hub never creates a local proxy process for a remote terminal.
Input, output, resize messages, and close events pass between the two WebSocket
connections.

Open a spoke UI when you want that machine's local execution context while still
seeing the aggregate fleet and global provider data. If the hub is
down, the spoke remains useful for local workspace operations but cannot provide
current hub-owned provider data.

## Understand Sync, Refresh, and hub outages

Provider controls keep the same owner regardless of which Forge UI you opened:

| Action in a spoke UI      | What Forge does                                                                                                                                                                      |
| ------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Top-bar **Sync**         | Asks the hub to sync its configured repositories. The spoke does not run a second provider sync.                                                                              |
| **Sync current repo**    | Asks the hub to sync only the repository selected by the route or repository filter. Use this when one repository needs attention without spending a full-fleet sync budget. |
| Pull-request **Refresh** | Asks the hub to fetch that pull request, then returns the refreshed detail to the spoke.                                                                                      |
| Start a shell or agent   | Starts the session on the spoke from the existing local workspace. It does not contact the hub.                                                                               |
| Provider mutation        | Sends the mutation to the hub, which applies it with the hub's provider credential.                                                                                  |

Opening a pull request normally reads the hub's stored detail first. If
the repository is tracked but that pull request is missing from the stored
projection, Forge performs one targeted pull-request sync automatically. This
repairs a cold or partially completed repository sync without starting another
full sync. If the repository itself is not configured, Forge directs you to
**Settings → Repositories**; add it there, then use **Sync current repo** or
reopen the pull request.

When a spoke loses its hub connection, it keeps provider data already
loaded in that browser tab visible and labels it as stale. Sync and pull-request
Refresh are disabled, provider changes are unavailable, and local workspace and
terminal operations continue. New shell and agent sessions in an existing
workspace also remain local; generated agent context uses the last validated
workspace facts. A fresh browser load has no durable provider cache, so it
cannot reconstruct provider pages until the hub returns.

Reconnection is automatic. The spoke replays hub events and reconciles
the visible provider projection before removing the stale-data banner. Do not
reload merely to recover a connection.

Installing a provider API credential on a spoke does not create automatic
failover. In spoke role, Forge uses local credentials for Git clone, fetch, pull,
and push, but it does not construct an independent provider syncer. Automatic
multi-writer provider failover is not currently supported.

## Advanced: publish with your own reverse proxy

The setup command always binds Forge to loopback and validates the public
authority with `trust_reverse_proxy = false`. Your proxy must preserve the
incoming `Host` header and forward to the configured loopback port. It must not
rewrite `Host` to the upstream address.

For example:

```caddyfile
https://build-a.example.test {
    bind 192.0.2.20
    reverse_proxy 127.0.0.1:8091
}
```

Use your real private address and a certificate trusted by every fleet
machine. Verify the HTTPS URL with the normal operating-system trust store. Do
not use `curl -k`; it hides certificate and hostname mistakes that also break
federation.

`--origin` does not manage this proxy, its certificate, DNS, or browser login.
That separation is intentional: the federation contract is canonical HTTPS,
not Tailscale or Caddy.

## Hub replacement is a separate migration

Do not replace the hub by changing DNS or copying one credential. An
enrollment is bound to the hub's stable node ID and canonical origin.
The hub also stores provider state alongside its own local execution
state.

A safe replacement must transfer hub-owned provider state without
reassigning local workspaces, enroll every spoke against the new hub,
revoke both old credential directions, and support rollback during the
transition. Forge does not currently provide that complete workflow.

## Replace an older fleet

Older key-based and shell-relay entries are not accepted. Before starting the
new binary:

1. Remove the old fleet entries from every config file.
2. Enable API authentication and select the hub.
3. Start each daemon with a reachable HTTPS origin.
4. Enroll every spoke again with a new one-time token.

Do not copy old tokens, node IDs, relay sockets, or daemon data directories.
There is no automatic translation because the older entries did not establish
the identity and credential pair required by federation.

## Troubleshooting

### A spoke is unreachable

From the hub machine, verify the spoke's HTTPS origin and certificate.
Confirm the URL exactly matches the enrolled origin. A changed hostname, port,
or certificate route requires deliberate re-enrollment rather than a redirect.

If the daemon is down, restart it through its local service manager. The
hub does not log in to start it.

### A request is forbidden

Check that the enrollment is active and has not been revoked. A valid
credential can still be forbidden when its direction does not grant the
requested operation. Re-enroll instead of widening or copying a token by hand.

### A terminal does not open

Confirm the spoke is reachable and the local session still exists. The
hub attaches to the spoke's terminal WebSocket; it does not attach to
the spoke's tmux server directly.

### A config no longer loads

Remove old fleet keys and relay entries, then use the enrollment workflow.
Forge fails closed instead of guessing how an old host label maps to a stable
spoke identity.
