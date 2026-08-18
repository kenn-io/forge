# Server Runtime

Use this document for daemon startup, discovery, request-origin validation,
and the root event stream.

## Startup Contracts

- Bare `kenn-forge` is help-only, `serve` is foreground, and background
  lifecycle management is under `daemon start|status|stop|restart`
  (`cmd/kenn-forge/cli.go::newRootCommand`).
- `daemon start` is idempotent: reuse requires verified identity for the same
  resolved `data_dir`; incompatible versions require `daemon restart`
  (`internal/daemonruntime/lifecycle.go::NewManager`).
- Lifecycle startup mints the API token under the authoritative data-directory
  lock; atomic serialized publication makes concurrent paths retain one
  credential (`internal/runtimelock/token.go::EnsureAuthToken`).
- Lifecycle commands retain caller-spelled paths for initial default migration,
  then freeze canonical identity for locks, reloads, runtime records, and
  comparisons (`cmd/kenn-forge/daemon_lifecycle.go::daemonLifecycle.lockMutationConfig`).
- Default-home upgrades copy the legacy config and its referenced credential
  files before marking completion and relocating the database; explicit config
  paths and `KENN_FORGE_HOME` never relocate config
  (`internal/config/legacy_migration.go::migrateLegacyConfig`).

## Startup Lock

- Config identity must converge across relative, symlinked, and filesystem-equivalent
  name aliases before hashing or comparison
  (`internal/daemonruntime/runtime.go::CanonicalConfigPath`).
- Data-directory identity must converge across symlinked and filesystem-equivalent
  name aliases before hashing, comparison, or runtime publication
  (`internal/config/data_dir.go::CanonicalDataDir`).
- Linux without procfs retains the already symlink-resolved spelling instead of
  making all config-backed commands fail
  (`internal/pathidentity/canonical_linux.go::CanonicalExisting`).
- Background lifecycle mutations lock canonical config identity before sorted
  resolved `data_dir` identities; release but never unlink the stable
  cross-process paths (`cmd/kenn-forge/daemon_lifecycle.go::daemonLifecycle.prepareConfigMutation`).
- Mutations reload config after locking and retry if `data_dir` changed;
  detached children reject a config that differs from the locked identity
  (`cmd/kenn-forge/daemon_lifecycle.go::daemonLifecycle.prepareConfigMutation`, `cmd/kenn-forge/start_background.go::validateBackgroundLaunchConfig`).
- Start, stop, and restart use canonical config identity to lock both prior and
  current `data_dir` before reusing, replacing, or stopping a moved daemon
  (`cmd/kenn-forge/daemon_lifecycle.go::daemonLifecycle.prepareConfigMutation`).
- Lifecycle mutations proof-authenticate config-attributed candidates individually;
  multiple records require one authoritative lock-metadata match before shadows
  are removed (`cmd/kenn-forge/daemon_lifecycle.go::daemonLifecycle.runtimeForConfig`).
- An unauthenticated record is removable only while its authoritative lock is
  free or its former `data_dir` no longer exists
  (`cmd/kenn-forge/daemon_lifecycle.go::daemonLifecycle.readCleanupRuntimeStatus`).
- Config-identified records take precedence over legacy records; a live legacy
  record remains attributable only while its `data_dir` matches current config
  (`internal/daemonruntime/runtime.go::ConfigRuntimes`).
- Authenticate moved legacy candidates before rejection; discard an unauthenticated
  stale record only when its authoritative data-directory lock is free
  (`cmd/kenn-forge/daemon_lifecycle.go::daemonLifecycle.runtimeForConfig`).
- An authenticated pre-config-identity daemon matches authoritative status only
  when both discovery and status omit `config_path`; current-directory attribution
  above remains the boundary (`cmd/kenn-forge/daemon_lifecycle.go::runtimeStatusMatches`).
- Stop and restart prove the exact candidate record before signaling; moved
  candidates must also match authoritative config path and PID, while version
  differences remain stoppable
  (`internal/daemonruntime/lifecycle.go::FindVerifiedRecord`).
- Stop prefers authenticated config-identified runtime state under lifecycle locks;
  malformed TOML is consulted only when no attributable daemon exists
  (`cmd/kenn-forge/daemon_lifecycle.go::daemonLifecycle.loadMutationConfigOrRuntime`).
- Daemon stop waits through the sum of every bounded shutdown phase plus process
  exit margin (`internal/shutdownbudget/budget.go::Total`).
- Unix sends one SIGTERM before requiring manual recovery; force-kill escalation
  cannot rule out PID reuse
  (`cmd/kenn-forge/daemon_lifecycle.go::daemonLifecycle.stopLocked`).
- `kenn-forge.lock` is a stable lock target, not a disposable liveness sentinel.
  Never delete it: unlinking a held file lets another daemon lock a different
  inode and use the same data directory concurrently
  (`internal/runtimelock/lock.go::Acquire`).
- Release the OS lock while leaving the file in place. A file's existence says
  nothing about daemon liveness; only lock acquisition does
  (`internal/runtimelock/lock.go::Handle.Release`).

## Discovery And Readiness

- Every ready server publishes the standard `daemon.<pid>.json` record in
  config home, even when `config.toml` changes `data_dir`; the record is a
  discovery surface and does not replace the authoritative data-directory
  lock/status (`internal/daemonruntime/runtime.go::Publish`).
- Publish a runtime record only after the startup handler is installed and the
  HTTP server enters its listener accept loop; lifecycle discovery still proves
  the published loopback identity exactly before reuse (`cmd/kenn-forge/main.go::serveReadyListener.Accept`).
- Early identity proof establishes the startup owner, not application readiness;
  background lifecycle success requires `/healthz` and then re-proves the exact
  runtime record (`cmd/kenn-forge/start_background.go::waitForBackgroundReadiness`).
- Status uses authenticated config identity before TOML, including for moved runtimes;
  invalid config cannot strand an attributable daemon, while proof-unavailable state
  may report only the configured `data_dir` lock
  (`cmd/kenn-forge/daemon_lifecycle.go::daemonLifecycle.Status`).
- Record metadata is string-valued `host`, `port`, `read_only=false`,
  `require_auth`, `data_dir`, canonical `config_path`, and canonical
  `base_path`; `auth_token_path` is present only when auth is enabled, and
  `mcp_listen_addr` only when MCP is enabled. One typed owner builds and
  validates it;
  discovery still requires a live PID and a token-derived proof bound to the
  record's service, version, PID, network, and address
  (`internal/daemonruntime/runtime.go::Compatible`).
- The ready handler exposes standard identity at `GET /api/ping`; its private
  proof path binds the same identity to the published record and only accepts
  direct loopback requests
  (`internal/server/daemon_access.go::daemonRequestPolicy.admit`).

## Transport Trust Boundary

- Loopback TCP is the required cross-platform transport; Unix sockets and
  named pipes are not lifecycle requirements. Background startup rejects
  non-loopback listeners before launching
  (`cmd/kenn-forge/start_background.go::validateBackgroundConfig`).
- Only the startup-bound bearer with exact loopback authority/peer and no
  forwarding headers bypasses proxy Host interpretation; cookies never qualify,
  and the bearer remains available when general API auth is off
  (`internal/server/daemon_access.go::daemonRequestPolicy.admit`).
- Discovery sends only a random challenge until the endpoint proves the daemon
  token and full runtime identity; the proof route requires the exact direct
  loopback authority without forwarding headers
  (`internal/daemonruntime/lifecycle.go::discovery.probe`).

## Host And Origin Boundary

- Host validation is the DNS-rebinding boundary and runs before API auth, CSRF,
  and route handling. Do not move it behind middleware that trusts request
  credentials first (`internal/server/server.go::Server.ServeHTTP`).
- Trusted forwarded-host support adds validation of the canonical forwarded
  authority; it never replaces validation of the raw backend `Host`
  (`internal/server/host_check.go::checkHost`).

## Event Replay

- SSE event IDs are process-scoped replay cursors, not durable sequence
  numbers. Reconnects may replay only IDs retained by the current process's
  ring (`internal/server/event_hub.go::EventHub.ReplaySnapshotSince`).
- A cursor older than the ring or ahead of the current process head emits
  `reconnect.stale`; the client must discard incremental assumptions and perform
  an authoritative refetch (`internal/server/server.go::Server.handleSSE`).
- The frontend checkpoint advances only after an event's Effect consequences succeed;
  overlapping owners must keep it monotonic, and buffer pressure reconnects from the
  last accepted ID (`frontend/src/lib/stores/provider-events-workflow.ts::providerEventsProgram`).

## Long-Lived Transport Inventory

- Long-lived HTTP and WebSocket contracts derive from the Huma registrations;
  catch-all proxies declare finite streaming variants on their operation, and
  tracing consumes the same inventory (`internal/server/transport_inventory.go::NewTransportInventory`).
- An annotated proxy stream is served only when the request explicitly accepts
  its declared media type (`internal/server/httpapi/transport.go::ValidateTransportAccept`).
