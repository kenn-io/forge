# API Auth Invariants

Scope: the `[api].require_auth` gate — `internal/server/api_auth.go`
(`authGate`), `internal/runtimelock` (token + nonce stores),
`cmd/middleman/auth_cli.go`, and the frontend auth store/overlay.

## Credentials

- The long-lived daemon token (`<data_dir>/auth_token`, 0600) is valid
  only as an `Authorization: Bearer` header or a `POST /auth/login`
  JSON body. It must never appear in a URL: request URIs land in proxy
  and access logs. The `?auth_token=` bootstrap route accepts only
  single-use nonces and rejects the raw token.
- Login links (`middleman auth url`) carry a nonce minted into
  `<data_dir>/auth_nonces` (`runtimelock.MintAuthNonce`), single-use,
  10-minute TTL. The consume claim is an `os.Remove` — filenames are
  SHA-256 digests, so the store never contains a usable credential.
  The filesystem is the only CLI-to-daemon channel; do not replace it
  with an RPC, or minting breaks while the daemon is starting or down.
- The session cookie's value is the token itself, `HttpOnly`, and
  scoped to the configured base path (not `/`) so sibling apps on the
  same origin never receive it. Any cookie the server sets or expires
  must use `authGate.cookiePath()`; a mismatched deletion path leaves
  the browser holding both cookies.

## Gate placement

- `authGate` is shared by the full `Server` and the startup handler;
  the auth contract must be identical in both phases because
  `middleman auth url` hands out links as soon as runtime metadata
  exists, which is before the full server swaps in.
- Gated prefixes are `/api/` and `/ws/` (terminal WebSockets). Health
  probes (`/healthz`, `/livez`) stay exempt so supervisors can poll
  before reading the token file.
- Rotation (`middleman auth rotate`) holds the runtime lock so a
  daemon cannot start mid-rotation and cache the stale token; a live
  daemon keeps honoring its cached token until restart.

## Frontend

- The login overlay appears only for middleman's own challenges: the
  auth store flips on 401 plus `WWW-Authenticate: Bearer
  realm="middleman"`, never on bare upstream 401s relayed from kata,
  msgvault, or fleet routes.
- Wrappers on the shared API fetch pipeline must stay
  timing-transparent (see `context/error-handling.md`).
