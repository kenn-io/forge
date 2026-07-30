# Daemon lifecycle

## Startup contracts

- `middleman` and `middleman serve` are raw foreground commands; their
  authoritative `data_dir` lock keeps duplicate startup as an error.
- `middleman start --background` is idempotent: reuse requires verified
  identity for the same resolved `data_dir`; starts serialize per data
  directory through the shared daemon manager without blocking unrelated
  instances (`cmd/middleman/start_background.go::newBackgroundManager`).
- Config loading establishes the canonical `data_dir` identity used by startup
  and reload comparisons (`internal/config/config.go::load`).

## Discovery and readiness

- Every ready server publishes the standard `daemon.<pid>.json` record in
  config home, even when `config.toml` changes `data_dir`; the record is a
  discovery surface and does not replace the authoritative data-directory
  lock/status (`internal/daemonruntime/runtime.go::Publish`).
- Record metadata is string-valued `host`, `port`, `read_only=false`,
  `require_auth`, and `data_dir`; `auth_token_path` is present only when auth is
  enabled. Its producer and compatibility checks share one typed owner;
  discovery still requires a live PID and a token-derived proof bound to the
  record's service, version, PID, network, and address
  (`internal/daemonruntime/runtime.go::Compatible`).
- The ready handler exposes standard identity at `GET /api/ping`; its private
  proof path binds the same identity to the published record and only accepts
  direct loopback requests (`internal/server/daemon_ping.go::registerDaemonPing`).

## Transport trust boundary

- Loopback TCP is the required cross-platform transport; Unix sockets and
  named pipes are not lifecycle requirements. Background startup rejects
  non-loopback listeners before launching.
- Only the startup-bound bearer with exact loopback authority/peer and no
  forwarding headers bypasses proxy Host interpretation; cookies never qualify,
  and the bearer remains available when general API auth is off (`internal/server/api_auth.go::isDirectDaemonBearerRequest`).
- Discovery sends only a random challenge until the endpoint proves the daemon
  token and full runtime identity; the proof route requires the exact direct
  loopback authority without forwarding headers (`cmd/middleman/start_background.go::backgroundDiscovery.probe`).
