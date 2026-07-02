# Host validation middleware — design

(Originally framed as "Host/Origin middleware". `Origin` is not used
by this middleware; cross-origin rejection stays with the existing
`checkCSRF` middleware via `Sec-Fetch-Site`. The Host gate alone
closes the DNS-rebinding bypass.)

## Motivation

middleman's HTTP server validates that its configured bind is loopback
(`internal/config/config.go:786`) and gates mutating API calls on
`Sec-Fetch-Site` plus `Content-Type: application/json`
(`internal/server/server.go:719,740`). Neither check inspects the
request's `Host` header against the configured bind, so the TCP
listener accepts requests with arbitrary `Host`.

That gap enables DNS rebinding: a page on `attacker.example` makes
its first DNS lookup resolve normally, then a TTL-zero re-lookup
points the same hostname at `127.0.0.1`. The browser sees a single
origin (`attacker.example`) so it sends every subsequent request
with `Sec-Fetch-Site: same-origin`, bypassing the CSRF middleware,
while the TCP connection lands on middleman's loopback listener.

middleman is local-first and stores sensitive per-user data (tokens
via env vars, SQLite of PR/MR state, workspace sessions). A
DNS-rebinding bypass on a mutating endpoint can drive the workspace
runtime or rewrite config. Closing the gap requires a Host check
keyed off the configured bind, with explicit opt-outs for the two
legitimate cases that need a different `Host`: extra hostnames the
operator registers (`allowed_hosts`) and reverse-proxy deployments
that put a different hostname in front (`trust_reverse_proxy`).

## Vocabulary

The middleware sees up to two distinct hostnames per request. We use
these names consistently throughout the spec:

- **Backend Host** (a.k.a. raw / proxy-facing Host): the value of
  the request's `Host` header as it lands on middleman's listener.
  For a reverse-proxy deployment this is what the proxy sends to
  middleman, which may be the loopback address or a rewritten
  internal hostname.
- **Public Host** (a.k.a. client-facing / forwarded Host): the value
  the browser typed; surfaces only when `trust_reverse_proxy = true`
  via `X-Forwarded-Host` or `Forwarded` `host=`.

Both must independently pass the allowlist gate. `allowed_hosts`
holds the union: every non-loopback hostname expected at either
position must be listed. Loopback synonyms (127.0.0.1 / localhost /
[::1]) at the bind port are auto-accepted and do not need to be
listed.

## Goals

- Reject any HTTP request whose Backend Host does not canonical-match
  the configured bind, a loopback synonym for the bind port, or an
  entry in `allowed_hosts`.
- When `trust_reverse_proxy = true`, additionally reject any request
  whose Public Host (from `X-Forwarded-Host` or `Forwarded`) does
  not canonical-match the same set.
- Keep the existing reverse-proxy / `base_path` deployment story
  working: one or two `allowed_hosts` entries plus the
  `trust_reverse_proxy` flag suffice.
- Return a 403 with a JSON body that names the two config knobs
  (`allowed_hosts`, `trust_reverse_proxy`) so a clobbered request
  is debuggable from curl output alone.
- Cover every code path with wire-level tests against the real
  middleware chain, plus focused config-validation tests for the
  canonicalization parser.

## Non-goals

- Token binding to client IP / TLS fingerprint.
- Per-route Host allowlists. The middleware decision is global.
- Wildcard / glob / regex matching in `allowed_hosts`. Exact-string
  after canonicalization in v1. If patterns are needed later, a
  separate `allowed_host_patterns` field can be added without
  breaking v1 semantics.
- Changing the existing CSRF middleware (`checkCSRF`). The new
  middleware runs alongside it.
- Inspecting the `Origin` header (covered by `Sec-Fetch-Site`
  already).

## Threat model

| Attacker | Vector | Pre-fix outcome | Post-fix outcome |
|---|---|---|---|
| Malicious web page | DNS rebinding to 127.0.0.1 | `Sec-Fetch-Site: same-origin` because browser sees one origin; passes CSRF; reaches handler | Backend Host is `attacker.example:8091` (browser sends what it typed). Step 2 rejects 403 before CSRF runs. |
| Direct curl from another LAN host hitting loopback | N/A | Already rejected by bind | Already rejected by bind |
| Browser on the same host typing `http://127.0.0.1:8091` | Normal use | Allowed | Allowed (Backend Host `127.0.0.1:8091` matches bind) |
| Browser typing `http://localhost:8091` | Normal use | Allowed | Allowed (`localhost` is a loopback synonym for the bind port) |
| Reverse-proxy front, public hostname `mm.example.com` | Proxied use | Already works (no Host check) | Operator sets `trust_reverse_proxy = true` and adds the Public Host (and the proxy-set Backend Host, if it isn't loopback) to `allowed_hosts`. |
| DNS-rebound page with spoofed `X-Forwarded-Host: mm.example.com` | Hits loopback directly with attacker Host on Host header | N/A pre-fix | Backend Host fails Step 2 regardless of `X-Forwarded-Host` content. Forwarded header is never read. |

## Validation flow

The middleware runs FIRST on every request (before `checkCSRF`,
before the `basePath` stripper, before any handler). Validation is
identical for safe and unsafe methods — DNS rebinding can drive
both `GET` (read-out) and `POST` (write) attacks, and the cost of
validating GETs is negligible.

Canonicalization: parse a host header value into a `hostKey` of
`(lowercase_host, port_string_or_empty)`. IPv6 literals are wrapped
in `[]`. No default-port assumption: an empty port stays empty.

For each request:

1. **Parse Backend Host.** Split into `hostKey`. Empty / malformed →
   403.
2. **Validate Backend Host** against the accepted set:
   - The bind's canonical `hostKey` (e.g. `("127.0.0.1", "8091")`).
   - Loopback synonyms at the bind port: `("localhost", "8091")`,
     `("127.0.0.1", "8091")`, `("[::1]", "8091")`. These are added
     unconditionally because the existing config validation forces
     loopback binds; the synonyms collapse to a fixed three-element
     set parameterised only by the bind port.
   - Each `allowed_hosts` entry, in canonical form. Entry semantics
     are LITERAL:
     - An entry like `mm.example.com:8443` matches only a Backend
       Host of `mm.example.com:8443`.
     - An entry like `mm.example.com` (no port) matches only a
       Backend Host of `mm.example.com` with no explicit port.
     - We do NOT collapse "default ports" (80/443). middleman binds
       on a non-standard port by default and operators see HTTP
       fronted by a proxy with the proxy-public port; explicit
       entries prevent surprises.

   If no match, 403.
3. **(Optional, `trust_reverse_proxy` only.)** When the flag is
   false, the request is accepted at Step 2. When true:
   - For each present forwarded-host header, require exactly one
     value. Reject comma-separated chains because middleman has no
     trusted-hop model and cannot know which proxy hop to trust.
     - `X-Forwarded-Host`: the single value is the host value. Trim
       ASCII whitespace.
     - `Forwarded`: the single entry must contain a `host=`
       parameter (case-insensitive). Unquote `host="..."` if quoted.
       If the entry lacks `host=`, treat the header as malformed.
   - An empty or malformed selected entry from EITHER header (when
     that header is present) is a 403; we do not silently fall
     back to the other header if one is present but bad. A header
     that is absent altogether (no `X-Forwarded-Host` line, no
     `Forwarded` line) is just absent.
   - If both headers are present and their parsed canonical
     `hostKey` disagree, 403.
   - If both are absent under `trust_reverse_proxy = true`, 403
     (the proxy did not supply a forwarded host; the deployment is
     mis-configured).
   - Validate the selected Public Host against the same accepted
     set used in Step 2. Mismatch → 403.

The Step 2 gate is the security-critical step: a DNS-rebound request
to 127.0.0.1 cannot smuggle a spoofed `X-Forwarded-Host` past Step 2
because the attacker controls only the Backend Host (and that
hostname is, by attack construction, not loopback and not in
`allowed_hosts` unless the operator explicitly added it).

## Config

Two new optional fields on `config.Config`:

```toml
# Extra Host headers to accept beyond the bind address.
# Exact-string match after canonicalization (lowercase host,
# preserve port, IPv6 in brackets). Loopback synonyms
# (127.0.0.1 / localhost / [::1]) are auto-accepted at the bind
# port and do not need to be listed.
allowed_hosts = ["mm.local:8091", "mm.example.com"]

# When true, honor X-Forwarded-Host and Forwarded RFC 7239 host=
# under reverse-proxy deployments. The Backend Host must still
# pass the allowed_hosts gate before any forwarded header is read.
trust_reverse_proxy = true
```

Defaults: `allowed_hosts = []`, `trust_reverse_proxy = false`.

Validation:

- Each `allowed_hosts` entry must parse with the same canonicalization
  as runtime `Host` parsing. Invalid entries fail config load with
  `config: invalid allowed_hosts entry %q`.
- Entries are stored canonicalized (lowercase host, port preserved,
  IPv6 bracketed). Comparison is exact-string against the canonical
  `hostKey`.
- IPv6 entries must be bracketed (`[::1]:8091`). Validation rejects
  unbracketed IPv6 literals to match existing repo conventions
  (`internal/config/config.go:427`).
- `trust_reverse_proxy = true` with empty `allowed_hosts` is allowed
  (the validator does not gate on it) but emits a startup
  `slog.Warn` because the deployment likely needs at least one
  allowlist entry for the Public Host.

## 403 body

Single JSON shape using the existing `writeError` helper:

```json
{
  "error": "host validation failed: the requested hostname is not allowed. Add expected Backend and Public hostnames to allowed_hosts in middleman's config.toml. If a reverse proxy sets forwarded-host headers, also enable trust_reverse_proxy."
}
```

The rejected hostname is NOT echoed into the body (avoids reflecting
attacker-controlled input and avoids log injection via crafted Host
values). It is recorded server-side via `slog.Warn` with fields
`reason`, `host`, `forwarded_host`, `remote_addr` for operator
debugging.

## Implementation surface

Module layout. `internal/server` already imports `internal/config`,
so the canonicalization parser lives in `internal/config` and is
called by both config validation and server middleware. The server
package never re-implements the parser; it borrows the exported
helper. This satisfies the "same canonicalization" rule without
creating an import cycle.

| File | Change |
|---|---|
| `internal/config/hostmatch.go` | New file. Exports `ParseHostKey(string) (HostKey, error)` plus the `HostKey{Host, Port string}` type. Pure stdlib. |
| `internal/config/hostmatch_test.go` | New file. Table-driven tests for the parser: good entries, bad entries, IPv6 bracketing, uppercase normalisation, empty/missing port. |
| `internal/config/config.go` | Add `AllowedHosts []string`, `TrustReverseProxy bool` to `Config`. Validate via `ParseHostKey`. Canonicalize once at load and store the parsed list on the config struct (e.g., `parsedAllowedHosts []HostKey`, populated in `Validate` and accessed via a getter so the TOML-visible field stays a `[]string`). |
| `internal/server/host_check.go` | New file. Exports `checkHost(w, r, bindKey, allowed []config.HostKey, trustProxy) bool`. Uses `config.ParseHostKey` for runtime Host parsing. Pure stdlib + slog + the existing `writeError` helper. |
| `internal/server/server.go` | Wire `checkHost` into `ServeHTTP` BEFORE `checkCSRF`. Plumb bind host:port plus `allowed_hosts`/`trust_reverse_proxy` through `Server` struct (default constructor reads them from `cfg`; the test constructor accepts an explicit option so callers without a full `config.Config` can still wire the middleware). |
| `internal/server/host_check_test.go` | New wire-level test file. Table-driven cases per the test plan below. |
| `internal/server/apitest/host_check_test.go` | New apitest case per the test plan below. |
| `cmd/middleman/main.go` | Confirm `server.NewWithConfig` is given the bind host:port; no other change. |
| `README.md` | Document the two new fields under Configuration. |

## Test plan

### Parser-level (`internal/config/hostmatch_test.go`)

| name | input | expect |
|---|---|---|
| empty allowed_hosts | `[]` | no error |
| valid entry with port | `["mm.local:8091"]` | canonicalised to `("mm.local", "8091")` |
| valid bare entry (no port) | `["mm.local"]` | canonicalised to `("mm.local", "")` |
| uppercase canonicalisation | `["MM.Local:8091"]` | canonicalised to `("mm.local", "8091")` |
| IPv6 entry with port | `["[::1]:8091"]` | canonicalised to `("[::1]", "8091")` |
| IPv6 entry no port | `["[::1]"]` | canonicalised to `("[::1]", "")` |
| unbracketed IPv6 | `["::1:8091"]` | error: `config: invalid allowed_hosts entry %q` |
| empty entry | `[""]` | error |
| port-only entry | `[":8091"]` | error |
| port out of range | `["mm.local:99999"]` | error |
| trust_reverse_proxy default | (unset) | false |
| allowed_hosts default | (unset) | `nil` |

### Wire-level (`internal/server/host_check_test.go`)

All tests build a `Server` via the same helper used by
`basepath_test.go` and drive requests through `Server.ServeHTTP`,
which is the established full-stack shape for the existing CSRF
tests. They thread the new fields through a constructor option so
the assertions don't depend on a stub `config.Config`. Each case is
a table row in a single Go test function (or two: one for raw-Host
behavior, one for the forwarded-header dance).

Bind for every case below: `127.0.0.1:8091`.

| name | allowed_hosts | trust_reverse_proxy | Host | X-Forwarded-Host | Forwarded | expect |
|---|---|---|---|---|---|---|
| direct loopback IP | [] | false | `127.0.0.1:8091` | - | - | 200 |
| direct localhost | [] | false | `localhost:8091` | - | - | 200 |
| direct IPv6 loopback | [] | false | `[::1]:8091` | - | - | 200 |
| uppercase host accepted | [] | false | `LOCALHOST:8091` | - | - | 200 |
| wrong port | [] | false | `127.0.0.1:9999` | - | - | 403 |
| attacker host (DNS rebinding) | [] | false | `attacker.example:8091` | - | - | 403 |
| empty Host | [] | false | (none) | - | - | 403 |
| malformed Host | [] | false | `][` | - | - | 403 |
| port-only Host | [] | false | `:8091` | - | - | 403 |
| allowed_hosts hit, exact port | `["mm.local:8091"]` | false | `mm.local:8091` | - | - | 200 |
| allowed_hosts miss, wrong port | `["mm.local:8091"]` | false | `mm.local:9999` | - | - | 403 |
| allowed_hosts bare entry hits bare Host | `["mm.local"]` | false | `mm.local` | - | - | 200 |
| allowed_hosts bare entry rejects ported Host | `["mm.local"]` | false | `mm.local:8091` | - | - | 403 |
| IPv6 allowed_hosts hit | `["[::1]:8443"]` | false | `[::1]:8443` | - | - | 200 |
| allowed_hosts attacker miss | `["mm.local:8091"]` | false | `attacker.example:8091` | - | - | 403 |
| trust_reverse_proxy off, X-Forwarded-Host ignored | [] | false | `attacker.example:8091` | `127.0.0.1:8091` | - | 403 |
| trust on, raw Host loopback, XFH in allowlist | `["mm.example.com"]` | true | `127.0.0.1:8091` | `mm.example.com` | - | 200 |
| trust on, raw Host loopback, Forwarded in allowlist | `["mm.example.com"]` | true | `127.0.0.1:8091` | - | `for=10.0.0.1;host=mm.example.com` | 200 |
| trust on, Forwarded quoted host | `["mm.example.com"]` | true | `127.0.0.1:8091` | - | `host="mm.example.com"` | 200 |
| trust on, multi-value XFH rejected | `["mm.example.com"]` | true | `127.0.0.1:8091` | `mm.example.com, other.example.com` | - | 403 |
| trust on, multi-value XFH rejected even when later entry is allowed | `["other.example.com"]` | true | `127.0.0.1:8091` | `mm.example.com, other.example.com` | - | 403 |
| trust on, both headers agree | `["mm.example.com"]` | true | `127.0.0.1:8091` | `mm.example.com` | `host=mm.example.com` | 200 |
| trust on, headers disagree | `["mm.example.com", "other.example.com"]` | true | `127.0.0.1:8091` | `mm.example.com` | `host=other.example.com` | 403 |
| trust on, neither forwarded header | `["mm.example.com"]` | true | `127.0.0.1:8091` | - | - | 403 |
| trust on, forwarded host NOT in allowlist | [] | true | `127.0.0.1:8091` | `attacker.example` | - | 403 |
| trust on, raw Host fails (DNS rebinding even with proxy on) | `["mm.example.com"]` | true | `attacker.example:8091` | `mm.example.com` | - | 403 |
| trust on, empty XFH | [] | true | `127.0.0.1:8091` | `` (empty) | - | 403 |
| trust on, malformed Forwarded | [] | true | `127.0.0.1:8091` | - | `wat` | 403 |
| trust on, Forwarded first entry lacks host= | [] | true | `127.0.0.1:8091` | - | `for=10.0.0.1, host=mm.example.com` | 403 |
| trust on, present-but-malformed Forwarded with valid XFH | `["mm.example.com"]` | true | `127.0.0.1:8091` | `mm.example.com` | `wat` | 403 |
| trust on, present-but-empty XFH with valid Forwarded | `["mm.example.com"]` | true | `127.0.0.1:8091` | `` (empty) | `host=mm.example.com` | 403 |
| trust on, forwarded port mismatch | `["mm.example.com:8443"]` | true | `127.0.0.1:8091` | `mm.example.com:9999` | - | 403 |

403 body shape: one dedicated test reads the JSON body on any 403
case and asserts the body contains both `allowed_hosts` and
`trust_reverse_proxy` (string substring check) and matches the
`writeError` shape `{"error":"..."}`.

Health endpoints (`/healthz`, `/livez`) are checked through the same
middleware. The brief does not call out exempting them, and a
loopback `GET /healthz` from the same host already passes Step 2
naturally; an external prober hitting the proxy would have the
Public Host (handled by `trust_reverse_proxy + allowed_hosts`). If
concrete deployments need exemption, that's a follow-up.

### Full-stack apitest (`internal/server/apitest/host_check_test.go`)

Two cases through the real `setupTestServer` helper (SQLite-backed,
full Huma router). The apitest helper builds a server with a known
bind port (e.g. `127.0.0.1:8091` — the default the test server
constructor passes through). Tests issue requests with explicit
`Host: 127.0.0.1:8091` so port semantics match the locked rule
(no implicit default-port collapsing):

1. `Host: attacker.example:8091` on a `GET /api/v1/pulls` request
   returns 403 with the `{"error":"..."}` JSON body before any
   handler runs.
2. `Host: 127.0.0.1:8091` on the same `GET /api/v1/pulls` request
   reaches the handler and returns `200` with the seeded PR list.

These cover the e2e contract per project rules (full HTTP API +
SQLite) without duplicating the parser exhaustiveness already
covered in `host_check_test.go`.

## Open questions / follow-ups

- IPv6 dual-stack bind (`::`) is not currently allowed by config
  validation (loopback only), so the canonicalization does not have
  to handle wildcard binds.
- Future: per-route `allowed_hosts_for_path` if a specific endpoint
  needs different rules. None today.
- Future: surface a clearer settings-UI editor for `allowed_hosts`.
- Future: optional `denied_hosts` for explicit block-list. Not
  needed in v1 given the default-deny posture.
