# Daemon lifecycle

## Startup contracts

- `middleman` and `middleman serve` are raw foreground commands; their
  authoritative `data_dir` lock keeps duplicate startup as an error.
- `middleman start --background` is idempotent: reuse requires verified
  identity for the same resolved `data_dir`, and concurrent starts serialize
  token creation and launch (`cmd/middleman/start_background.go::Ensure`).

## Discovery and readiness

- Every ready server publishes the standard `daemon.<pid>.json` record in
  config home, even when `config.toml` changes `data_dir`; the record is a
  discovery surface and does not replace the authoritative data-directory
  lock/status (`cmd/middleman/main.go::writeDaemonRuntimeRecord`).
- Record metadata is string-valued `host`, `port`, `read_only=false`,
  `require_auth`, and `data_dir`; `auth_token_path` is present only when auth is
  enabled. Discovery still requires a live PID and authenticated ping.
- `GET /api/ping` is registered only on the ready application handler and
  returns the standard service, version, and PID identity
  (`internal/server/daemon_ping.go::registerDaemonPing`).

## Transport trust boundary

- Loopback TCP is the required cross-platform transport; Unix sockets and
  named pipes are not lifecycle requirements. Background startup rejects
  non-loopback listeners before launching; proxy-trusting configurations also
  require API authentication.
- A direct request bypasses proxy Host interpretation only with the valid API
  bearer, exact loopback listener authority, loopback peer, and no
  `Forwarded` or `X-Forwarded-*` headers. Cookies never qualify, and requests
  with forwarding metadata stay on the proxy path
  (`internal/server/api_auth.go::isDirectDaemonBearerRequest`).
