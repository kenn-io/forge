# Plan — Host validation middleware

Companion to `docs/superpowers/specs/2026-05-19-host-origin-middleware-design.md`.

Each task ends with one `git commit -m "..."` line per the
`writing-plans` template. Tasks are ordered by dependency.

Repo testing conventions apply to every new test:

- `github.com/stretchr/testify/require` for setup/preconditions and
  `github.com/stretchr/testify/assert` for expectations
  (`assert.New(t)` once a function has more than three checks).
- No `t.Fatal`, `t.Errorf`, `t.Error`, `t.Fail`, or manual
  `if got != want` checks.
- Table-driven cases.
- Use `t.TempDir()` and the established helpers (e.g. `dbtest.Open`).

## Task 1 — Add the canonicalization parser to internal/config

- Add a new file `internal/config/hostmatch.go` exporting:
  - `type HostKey struct { Host, Port string }`
  - `func ParseHostKey(s string) (HostKey, error)` — accepts the
    full input set: `mm.local`, `mm.local:8091`, `[::1]:8091`,
    `[::1]`, `127.0.0.1:8091`, `LOCALHOST:8091`. Algorithm:
    - Trim ASCII whitespace. Reject empty.
    - If input starts with `[` and contains a matching `]`, the
      part between the brackets is the host. After the closing
      bracket, an optional `:port` may follow (otherwise port is
      empty). Reject if there's any trailing garbage. The host
      between brackets must parse as an IPv6 literal via
      `net.ParseIP`. The canonical form keeps the brackets.
    - Otherwise, if input contains a `:`, treat as `host:port` and
      split on the LAST `:` so bare-host inputs without any `:`
      route to the no-port branch. Reject if the resulting host
      part contains any `:` (catches unbracketed IPv6 like `::1`
      or `::1:8091`). Reject ports outside 1-65535 (parsed via
      `strconv.Atoi`). Reject inputs that are port-only
      (`:8091`).
    - Otherwise, the input is host-only with no port.
    - Lowercase the host. Keep port as the digits-only string (or
      `""` for the no-port case).
  - `func (k HostKey) String() string` returns `Host` when Port is
    empty, otherwise `net.JoinHostPort(Host-without-brackets, Port)`
    or `Host + ":" + Port` for the bracketed IPv6 case (treat the
    bracketed `[::1]` as already a fully-qualified host literal,
    so `[::1]:8091` is just `Host + ":" + Port`). Used for slog.
  - `func (k HostKey) Equal(other HostKey) bool` — exact match on
    both fields (host already lowercased on construction).
  - `func (k HostKey) Valid() bool { return k.Host != "" && k.Port != "" }` — used by the server constructor's
    precedence rule (Task 4) to decide whether a caller-supplied
    or cfg-derived key counts as populated.
- Add `internal/config/hostmatch_test.go` with the spec's parser
  table. Cover the explicit `[::1]` (no port) row in the table.
  Use `assert.New(t)` and table-driven subtests.
- Verify: `nix run nixpkgs#go -- test ./internal/config/... -run TestParseHostKey -shuffle=on`

```bash
git add internal/config/hostmatch.go internal/config/hostmatch_test.go
git commit -m "feat(config): add ParseHostKey for shared host canonicalization"
```

## Task 2 — Wire allowed_hosts, trust_reverse_proxy, and BindHostKey through config

- Add two fields to `Config` in `internal/config/config.go`:
  - `AllowedHosts []string \`toml:"allowed_hosts"\``
  - `TrustReverseProxy bool \`toml:"trust_reverse_proxy"\``
- Add unexported caches on `Config`:
  - `parsedAllowedHosts []HostKey`
  - `parsedBindKey HostKey`
- In `Validate()`:
  - After the existing loopback validation, build the bind key via
    `ParseHostKey(net.JoinHostPort(c.Host, strconv.Itoa(c.Port)))`.
    Fail config load with the existing `config: invalid host`
    error wording on parse failure so we don't regress the message.
    Cache to `parsedBindKey`.
  - For each `AllowedHosts` entry, call `ParseHostKey`. On failure
    return `fmt.Errorf("config: invalid allowed_hosts entry %q: %w", entry, err)`. Cache the slice on `parsedAllowedHosts`.
  - `Validate()` MUST stay side-effect-light: no logging. The
    `trust_reverse_proxy && empty allowed_hosts` startup warning
    is emitted in `newServer` (Task 4), not here.
- Add accessors:
  - `func (c *Config) ParsedAllowedHosts() []HostKey { return append([]HostKey(nil), c.parsedAllowedHosts...) }` (defensive copy).
  - `func (c *Config) BindHostKey() HostKey { return c.parsedBindKey }`.
- Update `internal/config/config_test.go` with a small table for
  the new fields: valid + invalid entries; defaults; bracketed
  IPv6 entry; uppercase canonicalisation.
- Verify: `nix run nixpkgs#go -- test ./internal/config/... -shuffle=on`

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): accept allowed_hosts and trust_reverse_proxy"
```

## Task 3 — Add the Host check middleware (no constructor wiring yet)

- Add `internal/server/host_check.go` with:
  - Exported `type HostCheckOptions struct { Bind config.HostKey; Allowed []config.HostKey; TrustReverseProxy bool }`.
  - `func (o HostCheckOptions) Valid() bool` — true when
    `o.Bind.Host != "" && o.Bind.Port != ""`. The middleware
    requires both fields populated; a bind without a port is a
    programming error.
  - `func checkHost(w http.ResponseWriter, r *http.Request, opts HostCheckOptions) bool` — runs Steps 1-3 of the spec; returns false and writes the 403 body via `writeError` when invalid. The function panics with a `// programming error` panic if `!opts.Valid()`; production paths must always construct valid options.
  - `parseForwardedHost(value string) (config.HostKey, bool, error)`
    — RFC 7239 single-entry parser for `host=` (case-insensitive
    parameter name; quoted-value unwrap); comma-separated chains are
    rejected until middleman has a trusted-hop model. Unexported.
  - `parseXForwardedHost(value string) (config.HostKey, bool, error)`
    — single value, ASCII trim; comma-separated chains are rejected.
    Unexported.
  - Fixed `hostValidationError` const carrying the 403 body string from the spec.
  - `slog.Warn` on rejection with fields `reason`, `host`,
    `forwarded_host`, `remote_addr`.
- Loopback synonym set computed by `checkHost`: when
  `opts.Bind.Host` is one of `127.0.0.1`, `localhost`, `[::1]`,
  the accepted set for Step 2 includes all three (with the same
  port). Otherwise only the bind itself is auto-accepted in Step 2.
- Do NOT modify `Server.ServeHTTP` in this task — keep the change
  surface small. Wiring happens in Task 4.
- Tests for the unexported parser helpers live in
  `internal/server/host_check_test.go` (same package), so the
  helpers stay unexported.
- Verify: `nix run nixpkgs#go -- build ./internal/server/...`

```bash
git add internal/server/host_check.go
git commit -m "feat(server): add Host validation middleware (no wiring yet)"
```

## Task 4 — Wire the middleware into Server constructors and ServeHTTP

- In `internal/server/server.go`:
  - Add `hostOpts HostCheckOptions` to the `Server` struct.
  - Update `newServer` to derive `hostOpts` as follows:
    The rule for deriving `s.hostOpts` is single-pass and uses
    strict precedence (no field-by-field merging — avoids the
    "intentional zero vs omitted" ambiguity):
    1. **Caller override.** If `ServerOptions.HostCheck.Valid()`
       (Bind is fully populated), use the entire override
       as-is. Done.
    2. **Production path.** Else if `cfg != nil` and
       `cfg.BindHostKey().Valid()` (cfg was loaded via
       `config.Load` so `Validate()` cached the bind key), derive
       the entire option set from cfg —
       `HostCheckOptions{Bind: cfg.BindHostKey(), Allowed:
       cfg.ParsedAllowedHosts(), TrustReverseProxy:
       cfg.TrustReverseProxy}`. Done.
    3. **Test-friendly default.** Else if `cfg == nil` AND
       `ServerOptions.HostCheck` is zero, install the documented
       fallback:
       `HostCheckOptions{Bind: {127.0.0.1, 8091}, Allowed:
       [{example.com, ""}, {middleman.test, ""}],
       TrustReverseProxy: false}`. This exists so the dozens of
       pre-existing test helpers that construct servers with
       `cfg = nil` keep working without per-test churn. The
       default does NOT accept `attacker.example` or other
       rebinding-style hosts. Security tests in Task 5 and Task 6
       use explicit options (step 1).
    4. **Fail-fast.** Else (`cfg != nil` but partial — Host/Port
       not set, Validate never ran — and no override was
       provided), panic with a programming-error message:
       `"server: cannot construct without HostCheck options or a validated config"`. This forces partial-cfg test sites to pass an explicit `ServerOptions.HostCheck`.
- Emit `slog.Warn("cfg=nil server.New used without ServerOptions.HostCheck; using httptest-compatible Host defaults. Production callers must pass a validated config or explicit HostCheck options.")` exactly when step 3 fires (single intended log site).
- `Server.ServeHTTP` becomes (in order):
  1. `if !checkHost(w, r, s.hostOpts) { return }` — unconditional
  2. existing request-started log line
  3. existing CSRF gating
- Startup warning: in `newServer`, after `hostOpts` is set, if
  `s.hostOpts.TrustReverseProxy && len(s.hostOpts.Allowed) == 0`,
  emit `slog.Warn("trust_reverse_proxy is enabled but allowed_hosts is empty; only loopback Hosts will be accepted")`.
- Constructor call-site audit (`rg -n '\b(server\.)?(New|NewWithConfig)\b' cmd internal/server middleman.go --no-heading`):
  - `cmd/middleman/main.go:343` — `NewWithConfig(cfg, ...)`. cfg
    is loaded via `config.Load` which now caches the bind key;
    nothing else to change.
  - `cmd/middleman/main_test.go:310` — same, loaded cfg.
  - `cmd/e2e-server/main.go:770` — same.
  - `middleman.go:418` — top-level wrapper. Uses a non-nil `cfg`
    (audit confirmed): `cfg` is constructed by the caller and
    passed into the wrapper; it should be validated upstream.
    No change needed beyond Task 2 ensuring `Validate()` caches
    the bind key.
  - All the `New(database, syncer, nil, "/", nil, ServerOptions{})`
    call sites in `internal/server/api_test.go`,
    `internal/server/apitest/*`, `internal/server/e2etest/*`, and
    `internal/server/basepath_test.go::setupWithBasePath` — these
    rely on the cfg=nil test-friendly default. No per-call-site
    changes required.
  - `internal/server/api_test.go:2549,2579` —
    `cfg := &config.Config{BasePath: "/", Repos: ...}` and
    `NewWithConfig(... cfg ...)`. Partial cfg without Host/Port
    triggers step 4 (panic). Targeted fix: do NOT call
    `cfg.Validate()` (would require padding out other partial
    fields like `SyncInterval`). Instead pass an explicit
    `ServerOptions.HostCheck` so step 1 of the precedence rule
    provides the bind without touching the cfg literal:
    `ServerOptions{HostCheck: server.HostCheckOptions{Bind:
    config.HostKey{Host: "127.0.0.1", Port: "8091"}}}`. cfg
    stays partial for the test's original purpose.
  - `internal/server/workspacetest/fixtures_test.go:92` —
    `server.New(database, syncer, nil, basePath, cfg, ...)`. cfg
    is supplied by the test caller; audit confirms callers
    either pass `nil` (step 3 covers) or pass a `*config.Config`
    literal that omits Host/Port (step 4 panic). Same fix as
    above: add the explicit `ServerOptions.HostCheck` override
    in the helper so callers don't have to care about cfg
    completeness.
  - Both of the above are TWO targeted edits, much smaller than
    the 30+ cfg=nil churn that step 3 covers automatically.
  - One small change in `basepath_test.go::TestCSRF*`: the
    existing CSRF tests issue `httptest.NewRequest`, which
    defaults the request `Host` to `example.com`. The
    cfg=nil test-friendly default explicitly allows `example.com`
    so these continue to pass unchanged.
- Add a small constructor-level test
  (`internal/server/host_check_default_test.go`) verifying that
  `New(..., cfg=nil, ServerOptions{})` yields a server whose
  `Server.ServeHTTP` accepts `Host: 127.0.0.1:8091`,
  `Host: example.com`, and `Host: middleman.test`, and rejects
  `Host: attacker.example`. This pins the test-friendly default
  so future contributors don't widen it accidentally.
- Verify (broader, since Task 4 touches the request chain):
  `nix run nixpkgs#go -- build ./... && nix run nixpkgs#go -- test ./internal/server/... -shuffle=on`

```bash
git add internal/server/server.go internal/server/host_check_default_test.go \
        internal/server/api_test.go \
        internal/server/workspacetest/fixtures_test.go \
        cmd/middleman/main.go
git commit -m "feat(server): wire Host validation into the request chain"
```

## Task 5 — Wire-level middleware tests

- Add `internal/server/host_check_test.go` exercising every row of
  the spec's wire-level table. A helper `setupHostCheckServer(t,
  HostCheckOptions)` builds a `Server` via `New(..., nil, "/",
  nil, ServerOptions{HostCheck: opts})` so each table row controls
  bind, allowed_hosts, and trust_reverse_proxy precisely.
- Tests use `require.NoError` / `assert.Equal` / `assert.New(t)`.
- Each row issues `srv.ServeHTTP(rr, req)` with `req.Host` /
  `req.Header.Set("X-Forwarded-Host", …)` / `req.Header.Set("Forwarded", …)` as the row dictates, then asserts:
  - `rr.Code`
  - On the dedicated 403-body row: response body parses as JSON
    `{"error": "..."}` whose value contains the substrings
    `allowed_hosts` and `trust_reverse_proxy`.
- A second table-driven `TestParseForwardedHost` exercises the
  unexported parser helpers directly for zero-length-header and
  malformed-but-present cases that don't round-trip cleanly via
  `httptest.NewRequest`.
- Verify: `nix run nixpkgs#go -- test ./internal/server/... -run "TestHostCheck|TestParseForwardedHost|TestCSRF|TestBasePath" -shuffle=on`

```bash
git add internal/server/host_check_test.go
git commit -m "test(server): cover Host validation middleware wire behavior"
```

## Task 6 — Full-stack apitest coverage

- Add `internal/server/apitest/host_check_test.go` with two cases:
  1. `Host: attacker.example:8091` on `GET /api/v1/pulls` → 403
     with JSON body shape `{"error":"..."}` whose value contains
     `allowed_hosts` and `trust_reverse_proxy`. Asserted BEFORE
     any handler runs.
  2. `Host: 127.0.0.1:8091` on `GET /api/v1/pulls` → 200 with the
     seeded PR list (using the existing `seedPR` helper).
- The apitest round-tripper in
  `internal/server/apitest/fixtures_test.go` builds the test
  `Request` via `httptest.NewRequest(req.Method, req.URL.String(), body)`
  so `serverReq.Host` comes from `req.URL.Host`. The test client
  base URL is `http://middleman.test` (so `req.URL.Host` is
  `middleman.test`). That hostname is in Task 4's cfg=nil
  test-friendly default allowlist, so existing apitest cases
  continue to work unchanged.
- For Task 6, build a dedicated server with an EXPLICIT
  `ServerOptions.HostCheck` so the production contract is tested
  rather than the test-friendly fallback:
  `HostCheckOptions{Bind: {127.0.0.1, 8091}, Allowed: nil}`.
  This server only accepts loopback synonyms at port 8091. Use
  the existing round-tripper but build the test request URL with
  `http://127.0.0.1:8091` for the success case and
  `http://attacker.example:8091` for the rejection case so
  `req.URL.Host` (and therefore `serverReq.Host`) carries the
  per-row value through. Mechanism: ONE — `req.URL.Host`.
- Use `require` and `assert.New(t)` per the convention.
- Verify: `make test-short`

```bash
git add internal/server/apitest/host_check_test.go internal/server/apitest/fixtures_test.go
git commit -m "test(apitest): exercise Host validation through the full stack"
```

## Task 7 — README documentation

- Add two rows to the configuration table in `README.md` under
  `## Configuration`:
  - `allowed_hosts` | `[]` | Extra Host headers to accept beyond the
    bind address.
  - `trust_reverse_proxy` | `false` | Honor X-Forwarded-Host and
    Forwarded RFC 7239 host= under reverse-proxy deployments.
- Add a short paragraph below the table explaining the
  DNS-rebinding rationale and how to configure for a reverse-proxy
  deployment.
- Stage explicit files. Lint findings from earlier tasks fold back
  into those tasks' commits during execution, not here.
- Verify: `make lint && make vet && make test`

```bash
git add README.md
git commit -m "docs(readme): document allowed_hosts and trust_reverse_proxy"
```

## Verification checklist (final, before declaring done)

- `make lint` clean
- `make vet` clean
- `make test` clean (`-shuffle=on` applied by Make)
- `nix run nixpkgs#go -- test ./internal/config/... ./internal/server/... -shuffle=on`
  passes
- Acceptance behavior verified by tests:
  - direct 127.0.0.1:8091 succeeds (Task 5 + Task 6 case 2)
  - `Host: attacker.example` to 127.0.0.1:8091 → 403 (Task 5 + Task 6 case 1)
  - `Host: localhost:8091` and `127.0.0.1:8091` canonicalised
    equivalent against loopback bind (Task 5)
  - `allowed_hosts = ["middleman.local:8091"]` allows that host
    (Task 5)
  - `trust_reverse_proxy` with forwarded headers succeeds (Task 5)
