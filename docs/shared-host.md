# Shared Linux host

A shared Linux machine needs one Forge instance for each OS user. This page
shows a rootless Podman container for each user, managed by an
administrator-owned systemd service running as that user's OS account. Kenn
Forge runs inside each container with a user-owned
TLS gateway. The examples use synthetic Alice and Bob accounts.

The repository publishes no production container image. The operator supplies
an image, pins it by digest, and confirms that it contains the tools needed by
the selected workspace setup.

## What this protects

Each instance has its own configuration, data directory, provider credentials,
gateway certificate, container process namespace, and container network
namespace. The owning OS user owns those paths and the rootless container.

Separate ports alone do not isolate users. A local user can connect to another
user's host-loopback port. If Alice's service stops, another user could also
bind that port unless the outer proxy checks the expected gateway certificate.
The proxy must authenticate the user, choose one fixed gateway for that user,
and verify the gateway identity before it forwards a request.

Root and sudo can read files and inspect process memory. Rootless containers
and file modes do not protect a process from those administrators. This page
also does not make virtualization part of the boundary.

## Plan one instance per user

Give every OS user a separate private root, public hostname, host-loopback
port, and gateway certificate. Use a different hostname for every instance
because Forge cookies are scoped to their host.

| OS user | Private root | Public origin | Host gateway port | Gateway certificate |
| --- | --- | --- | --- | --- |
| Alice | `/srv/forge/alice` | `https://alice.forge.example.test` | `127.0.0.1:18091` | `alice-gateway.pem, SHA256:11:AA:20:BB:31:CC` |
| Bob | `/srv/forge/bob` | `https://bob.forge.example.test` | `127.0.0.1:18092` | `bob-gateway.pem, SHA256:44:DD:53:EE:62:FF` |

Use one operator-supplied image digest for both containers when possible. The
digest in this example is synthetic:

```
registry.example.test/forge@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
```

The image must contain one process supervisor that starts Forge and the TLS
gateway in the same single container. The supervisor must restart or stop both
processes together.

## Prepare the data directory

Create the roots as an administrator, then let each OS user write only inside
that user's root. Repeat the commands for Bob with the Bob paths and owner.

```sh
sudo install -d -o alice -g alice -m 700 /srv/forge/alice
sudo install -d -o alice -g alice -m 700 \
  /srv/forge/alice/forge \
  /srv/forge/alice/certs \
  /srv/forge/alice/containers \
  /srv/forge/alice/home
sudo install -d -o alice -g alice -m 700 \
  /srv/forge/alice/forge/credentials \
  /srv/forge/alice/containers/graphroot
```
Configure rootless Podman to use the persistent graph root under
/srv/forge/alice/containers. Let systemd create an Alice-owned runtime
directory under /run for the run root and XDG_RUNTIME_DIR on every boot. These
paths stay outside /home and /run/user, which lets the service use
ProtectHome=true.

Place a storage.conf in /srv/forge/alice/containers with these roots:

```
[storage]
runroot = "/run/forge-alice/containers/runroot"
graphroot = "/srv/forge/alice/containers/graphroot"

```
Verify that Alice has subordinate UID and GID ranges in /etc/subuid and /etc/subgid. Rootless Podman uses those ranges for userns=keep-id.

Use a restrictive umask whenever you create files:

```sh
umask 077
```

Keep directories at mode `0700`, private regular files at mode `0600`, and
ownership consistent with the OS user running the service. Apply `0600` to
configuration, provider token, gateway key, gateway certificate, and
`auth_token` files. Do not use `chmod -R 600`; directories need their search
bit and executable files need their execute bit.

The container sees the host paths through the mounts in the next section. Put
this configuration in /srv/forge/alice/forge/config.toml:

```toml
host = "127.0.0.1"
port = 8091
data_dir = "/var/lib/forge"
allowed_hosts = ["alice.forge.example.test"]
trust_reverse_proxy = false

[api]
require_auth = true

[api.tailscale_serve]
enabled = false
```

Forge listens on `127.0.0.1:8091` inside the container. The gateway forwards
to that address. `allowed_hosts` contains Alice's public hostname, and the
proxy preserves that raw `Host` header. Keep `trust_reverse_proxy = false`
because this recipe does not ask Forge to trust forwarded-host headers.

Bob's configuration uses the same settings with Bob's own paths and public
hostname:

```toml
host = "127.0.0.1"
port = 8091
data_dir = "/var/lib/forge"
allowed_hosts = ["bob.forge.example.test"]
trust_reverse_proxy = false

[api]
require_auth = true

[api.tailscale_serve]
enabled = false
```

Keep provider credentials inside the same private root. For example:

```toml
[[repos]]
platform = "forgejo"
platform_host = "code.example.test"
owner = "alice"
name = "sample-project"
token_file = "/var/lib/forge/credentials/provider-token"
```

The `token_file` credential belongs to the provider repository. Forge reads
that file on each request, so the owning user can replace it atomically when a
provider token changes. See the [configuration reference](configuration.md#credentials)
for the other credential sources. Token minting and rotation automation belong to separate GitHub App credential setup.

## Run the container

The container has a private PID namespace and a private per-user network
namespace. The gateway listens on its container TLS port, `8443`, and the
publish rule exposes only the host loopback port. It reaches Forge through the
container loopback.

This is an illustrative rootless command. Adapt the volume labels, user ID
mapping, network mode, and supervisor arguments to the image you selected:

```sh
podman run \
  --name forge-alice \
  --userns=keep-id \
  --pid=private \
  --network=slirp4netns \
  --publish 127.0.0.1:18091:8443 \
  --replace \
  --env KENN_FORGE_HOME=/var/lib/forge \
  --volume /srv/forge/alice/forge:/var/lib/forge:rw \
  --volume /srv/forge/alice/certs:/etc/forge-gateway:ro \
  registry.example.test/forge@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
```

The image entrypoint must be the one supervisor described above. It starts
Forge with the configuration shown earlier and starts the gateway with
`/etc/forge-gateway/alice-gateway.pem` and its private key. The gateway must
forward only to `127.0.0.1:8091`.

Do not use host networking, host PID sharing, or a separate bridge container
that cannot reach Forge's container loopback. The host publish is a local
transport for the outer proxy. It is not a user identity check.

Manage the container with an administrator-owned systemd service running as
Alice. The following layout shows
the required hardening settings. Keep the unit with the operator's service
configuration; this repository does not add a Quadlet or a service file.
This system-level unit keeps ProtectHome=true without the implicit PrivateUsers
namespace of a user-manager unit, so rootless Podman can use Alice's
subordinate ID mapping.

```ini
# /etc/systemd/system/forge-alice.service
[Unit]
Description=Alice Forge container
After=network-online.target
Wants=network-online.target

[Service]
User=alice
Group=alice
Environment=HOME=/srv/forge/alice/home
Type=simple
RuntimeDirectory=forge-alice
RuntimeDirectoryMode=0700
Environment=XDG_RUNTIME_DIR=/run/forge-alice
Environment=CONTAINERS_STORAGE_CONF=/srv/forge/alice/containers/storage.conf
UMask=0077
ProtectHome=true
PrivateTmp=yes
KillMode=control-group
Restart=on-failure
ExecStart=/usr/bin/podman run --replace --name forge-alice --userns=keep-id --pid=private --network=slirp4netns --publish 127.0.0.1:18091:8443 --env KENN_FORGE_HOME=/var/lib/forge --volume /srv/forge/alice/forge:/var/lib/forge:rw --volume /srv/forge/alice/certs:/etc/forge-gateway:ro registry.example.test/forge@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
ExecStop=/usr/bin/podman stop --time 10 forge-alice

[Install]
WantedBy=default.target
```

ProtectHome=true hides the normal home tree and /run/user from the service.
RuntimeDirectory creates and clears /run/forge-alice for each boot, so the
Podman run root does not retain stale boot state while the graph root and Forge
data remain persistent. Before starting it, confirm that the rootless Podman
graph root, run root, runtime directory, image configuration, private
certificate path, and data path under /srv/forge/alice are reachable under
this sandbox. Do not start the service until this check succeeds.

Run the checks as Alice from the same OS account that owns the service:

```sh
sudo systemd-run --uid=alice --wait --pipe \
  -p RuntimeDirectory=forge-alice \
  -p RuntimeDirectoryMode=0700 \
  -p ProtectHome=true \
  -p PrivateTmp=yes \
  env HOME=/srv/forge/alice/home \
  XDG_RUNTIME_DIR=/run/forge-alice \
  CONTAINERS_STORAGE_CONF=/srv/forge/alice/containers/storage.conf \
  sh -c 'test -d "$XDG_RUNTIME_DIR" && test -w "$XDG_RUNTIME_DIR" && exec /usr/bin/podman info'
namei -l /srv/forge/alice/forge /srv/forge/alice/certs/alice-gateway.pem
```

Confirm that the reported storage and runtime paths are user-owned and
available to the service. Then load and enable the unit:

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now forge-alice.service
```

Repeat the layout for Bob with his own `data_dir`, gateway certificate,
container name, and host port.

## Publish through an authenticating proxy

The outer proxy is the user identity boundary. It authenticates the incoming
user, selects the matching host-loopback port, connects to the gateway over
TLS, and verifies the gateway's expected certificate before it forwards any
browser cookie, bearer token, or `auth_token` query.

| Public host | Authenticated user | Upstream | Expected gateway certificate |
| --- | --- | --- | --- |
| `alice.forge.example.test` | Alice | `https://127.0.0.1:18091` | `alice-gateway.pem, SHA256:11:AA:20:BB:31:CC` |
| `bob.forge.example.test` | Bob | `https://127.0.0.1:18092` | `bob-gateway.pem, SHA256:44:DD:53:EE:62:FF` |

Configure the proxy so that:

- Alice can reach only Alice's fixed upstream, and Bob can reach only Bob's.
- The proxy preserves the browser's `Host` header, including the public
  hostname and no unexpected port.
- The proxy terminates public HTTPS and verifies the gateway certificate on
  every upstream connection. A stopped Alice service followed by an
  untrusted process binding `18091` must fail this check.
- The proxy refuses an unauthenticated user before routing and refuses a
  request whose authenticated user does not match the hostname.
- The proxy does not forward browser cookies, `Authorization` headers, or
  `auth_token` bootstrap queries until the upstream certificate is verified.

Forge still enforces its own bearer or browser-cookie check because a local
user can reach a host-loopback port directly. `api.require_auth = true` keeps
the protected API and terminal WebSocket routes behind the per-instance
daemon token. Health probes and static assets remain separate public probes;
do not treat them as proof of API authorization. The existing [external proxy
contract](federated-fleet.md#advanced-publish-with-your-own-reverse-proxy)
covers the preserved `Host` rule and HTTPS origin requirements.

## Sign in

After Alice's service is ready, Alice reads that instance's own
`data_dir/auth_token`. The file is on the host at
`/srv/forge/alice/forge/auth_token` in this example. Alice opens the correct
HTTPS origin once with the token in the query:

```text
https://alice.forge.example.test/?auth_token=<contents-of-alice-data_dir-auth-token>
```

Forge accepts the token, sets the `forge_auth` browser cookie, and redirects
to the same URL without the `auth_token` query. The browser then uses that
cookie for the UI, API, and terminal WebSocket requests. API clients can send
the same daemon token as an `Authorization: Bearer` value.

This `auth_token` is the bearer for Alice's Forge instance. It differs from
the provider `token_file` in the configuration example. The first protects
Forge's API and WebSocket routes; the second lets Forge authenticate to a
provider. Never put the provider token in the browser bootstrap URL.

Bob repeats the procedure with Bob's own file and
`https://bob.forge.example.test/`. Do not reuse a hostname or browser profile
when testing the two instances.

## Verify isolation

Run this checklist on the target Linux host after the proxy and both services
are configured. Each command is an operator check; its expected result is
listed so a failed check stops the rollout.

| Check | Command or action | Expected result |
| --- | --- | --- |
| Modes and ownership | `stat -c '%A %U:%G %n' /srv/forge/alice /srv/forge/alice/forge /srv/forge/alice/forge/auth_token /srv/forge/alice/certs/alice-gateway.pem` | Alice owns the paths, directories are `0700`, and private files are `0600`. |
| Cross-user file reads | As Bob, run `sudo -u bob -- cat /srv/forge/alice/forge/auth_token` and try Alice's gateway key. | DAC denies both reads. Root and sudo retain administrator access. |
| Protected API and WebSocket | From an authenticated proxy client, request Alice's health endpoint, a protected `/api/` route, and a terminal `/ws/` upgrade without a bearer or cookie. | Health stays available; the protected API and WebSocket receive the authentication boundary response. |
| Own browser and API | Alice signs in through `https://alice.forge.example.test/` and calls a protected API route with the resulting session. Repeat for Bob on Bob's origin. | Each owner reaches only that owner's state, API, and terminal session. |
| Proxy routing | Authenticate as Bob and request Alice's hostname. Inspect the proxy route and upstream logs. | The proxy denies the request or never connects it to Alice's gateway. |
| Stopped-instance substitution | Stop Alice's service, bind `127.0.0.1:18091` with a gateway using a certificate other than Alice's pinned certificate, then request Alice's origin. | The proxy rejects the upstream before forwarding Alice's credentials. |
| Restart and token replacement | Restart Alice's service, confirm the data directory and gateway certificate remain Alice's, then atomically replace a provider `token_file`. | Alice's instance keeps its state and certificate, and later provider requests read the replacement file. |
| Host reboot recovery | Reboot the host, then inspect Alice's systemd unit, runtime directory, graph root, and Forge data. | systemd recreates the Alice-owned `/run/forge-alice` directory, the service starts, the run root is fresh, and the graph root and Forge data persist. |
| Image workspace tools | From Alice's disposable workspace, check the selected image's Git, tmux, and configured agent commands. | The service environment exposes the tools the operator expects. |

The repository checks in this change prove static documentation publication. They do not prove Linux Podman, systemd services, proxy identity, certificate verification, browser login, DAC, or workspace tools. Run those checks on the target Linux host and record their real output separately.

## Limits and later work

Root and sudo can inspect process memory even when the containers are rootless.
Use virtualization when the threat model requires a boundary from the host
administrator. Virtualization is outside this deployment guide.

This recipe does not add a production image, a repository Quadlet, or proxy
configuration. It also does not add GitHub App credential minting or Tailscale
per-user ACLs. Configure those separately. Keep api.tailscale_serve.enabled = false here
because Tailscale Serve's local identity mode trusts the local process that
reaches Forge.
