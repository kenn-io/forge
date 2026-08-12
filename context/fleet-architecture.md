# Fleet Architecture

Use this document for fleet settings, snapshot aggregation, host routing,
peer transports, or remote workspace and session operations.

## Ownership And Topology

- A fleet is a one-hop view over independent kenn-forge daemons, not a provider
  or a replicated database. Each host remains authoritative for its repositories,
  workspaces, runtimes, and mutations.
- The hub enriches its local raw snapshot with raw snapshots fetched directly
  from configured peers; peers are never asked to fan out, which prevents
  federation loops (`internal/server/fleetapi/fleet_hub.go::Handler.buildFleetSnapshot`).
- Fleet consumes detached Workspace-owned summaries and runtime snapshots, not
  Workspace managers or root mutable config
  (`internal/server/workspaceapi/fleet_snapshot.go::FleetSnapshot`,
  `internal/server/fleetapi/handler.go::ConfigSnapshot`).

## Snapshot And Routing Contracts

- A failed peer degrades only that peer to unreachable; it must not fail the
  local or other peer results (`internal/server/fleetapi/fleet_hub.go::Handler.fetchPeerRaw`).
- Advertise mutations only when the hub can route the owning host. Hosts seen
  in a nested or stale snapshot without a configured route remain read-only
  (`internal/server/fleetapi/fleet_hub.go::hubRoutabilityPolicy.ForHost`).
- The reserved `self` alias and the current resolved self key address the local
  handler. Configured HTTP and SSH peer keys route only while federation is
  enabled (`internal/server/fleetapi/fleet_proxy.go::Handler.resolveFleetHostTarget`).
- Proxy operations preserve the owning host's status, problem envelope, and
  non-hop-by-hop response headers instead of translating remote domain failures
  at the hub
  (`internal/server/fleetapi/fleet_proxy.go::Handler.serveRemoteFleetRESTProxy`).

## Transport Trust Boundary

- HTTP peer requests strip browser credentials and browser/proxy provenance;
  the hub bearer or cookie authenticates the hub and must never leak to a peer.
  HTTP peers therefore require a separately trusted network boundary
  (`internal/server/fleetapi/fleet_proxy.go::isPeerProxyCredentialHeader`).
- SSH peers use the installed remote kenn-forge relay through the shared
  generation-bound OpenSSH connection. When multiplexing is unavailable,
  explicit masterless SSH remains the transport; kenn-forge does not own a
  second socket lifecycle (`internal/server/fleetapi/fleet_ssh.go::sshFleetTransport.relay`).
- WebSocket attach tracing ends after bounded connection setup and before the
  long-lived bridge (`internal/server/fleetapi/fleet_proxy.go::startFleetAttachSpan`).
- SSH terminal viewers have independent proxy PTYs; mirror the winning size across
  every attachment so stale viewer geometry cannot constrain remote tmux, while
  retaining each viewer's fallback size (`internal/server/fleetapi/fleet_proxy.go::fleetSSHResizeGroup.applySizeLocked`).

## Configuration And Lifecycle

- Settings validate and persist the complete fleet membership before publishing
  the detached live snapshot (`internal/server/settings_routes.go::Server.updateFleetSettings`).
- HTTP membership and ordinary fleet settings can reload live. SSH membership
  and startup-bound session-monitor policy report `restart_required` until the
  running transport matches persisted config
  (`internal/server/settings_routes.go::Server.fleetSettingsRestartRequiredLocked`).
- Fleet workers start after Workspace and shut down before Workspace. Detailed
  shutdown and shared SSH lifecycle rules live in
  [`workspace-runtime-lifecycle.md`](./workspace-runtime-lifecycle.md).
