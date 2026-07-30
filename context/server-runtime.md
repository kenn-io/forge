# Server Runtime

Use this document for daemon startup, discovery, request-origin validation,
and the root event stream.

## Startup Contracts

- `kenn-forge` and `kenn-forge serve` are raw foreground commands; their
  authoritative `data_dir` lock keeps duplicate startup as an error.
- `kenn-forge start --background` is idempotent: reuse requires verified
  identity for the same resolved `data_dir`; starts serialize per data
  directory through the shared daemon manager without blocking unrelated
  instances (`internal/daemonruntime/lifecycle.go::NewManager`).
- Lifecycle startup mints the API token under the authoritative data-directory
  lock; atomic serialized publication makes concurrent paths retain one
  credential (`internal/runtimelock/token.go::EnsureAuthToken`).
- Config loading establishes the canonical `data_dir` identity used by startup
  and reload comparisons (`internal/config/config.go::load`).

## Startup Lock

- `middleman.lock` is a stable lock target, not a disposable liveness sentinel.
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
- Record metadata is string-valued `host`, `port`, `read_only=false`,
  `require_auth`, `data_dir`, and canonical `base_path`; `auth_token_path` is
  present only when auth is enabled. One typed owner builds and validates it;
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
  non-loopback listeners before launching.
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
