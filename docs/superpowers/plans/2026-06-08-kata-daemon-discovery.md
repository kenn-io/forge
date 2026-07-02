# Kata Daemon Discovery Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the first middleman-native Kata integration slice: a pure Go `internal/kata` package that reads Kata's daemon catalog and runtime records without storing daemon definitions in middleman config.

**Adaptation rule:** Start each implementation step by porting the existing source tests and implementation for the same behavior. Edits should be mechanical adaptations for package boundaries, exported names, middleman test conventions, route/security mechanics, and forbidden legacy names. Do not re-author behavior from scratch when source code already exists.

**Architecture:** Create `internal/kata` as a focused domain package with no dependency on `internal/server` or middleman DB code. The package exposes catalog/runtime discovery and validation primitives that later server routes can compose into daemon roster, health, and proxy behavior.

**Tech Stack:** Go 1.26, `github.com/BurntSushi/toml`, stdlib filesystem/process/network APIs, testify assertions, `go test -shuffle=on`.

---

## File Structure

- Create `internal/kata/types.go`: shared daemon and runtime record types.
- Create `internal/kata/paths.go`: `KATA_HOME`, `KATA_DB`, catalog path, runtime directory hash helpers.
- Create `internal/kata/catalog.go`: read `$KATA_HOME/config.toml`, parse `active_daemon` and `[[daemon]]`, map to middleman-owned `Daemon` values.
- Create `internal/kata/runtime.go`: read runtime daemon records, normalize addresses, filter dead processes, resolve local daemon URLs.
- Create `internal/kata/validation.go`: target validation and redaction helpers for static and local daemon URLs.
- Create `internal/kata/catalog_test.go`, `runtime_test.go`, and `validation_test.go`: migrated behavior tests adapted to middleman's testify conventions.

## Task 1: Catalog Types And Tests

**Files:**
- Create: `internal/kata/types.go`
- Create: `internal/kata/paths.go`
- Create: `internal/kata/catalog.go`
- Test: `internal/kata/catalog_test.go`

- [ ] **Step 1: Write failing catalog tests**

Create `internal/kata/catalog_test.go` with tests for:

- mapping `$KATA_HOME/config.toml` `active_daemon` and `[[daemon]]` entries;
- absent catalog returning an empty result;
- duplicate daemon names;
- missing daemon name;
- entries with neither `local` nor `url`;
- entries with both `local` and `url`;
- entries with both `token` and `token_env`;
- `active_daemon` that does not exist.

Preserve source-covered mapping semantics, including unresolved `token_env` and `allow_insecure`.

Use `require := require.New(t)` and `assert := Assert.New(t)` from testify. Use `t.TempDir()` and `t.Setenv("KATA_HOME", home)`.

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/kata -run TestLoadCatalog -shuffle=on
```

Expected: FAIL because `internal/kata` functions/types do not exist yet.

- [ ] **Step 3: Implement catalog parsing**

Add:

- `type Daemon struct { ID, URL, Token, TokenEnv string; Default, Local, AllowInsecure bool }`
- `type Catalog struct { Daemons []Daemon; Source string }`
- `func Home() (string, error)`
- `func CatalogPath() (string, error)`
- `func LoadCatalog() (Catalog, error)`

`LoadCatalog` reads `$KATA_HOME/config.toml` (default `~/.kata/config.toml`), returns an empty `Catalog` when the file is absent, maps `[[daemon]] name` to `Daemon.ID`, preserves `token_env` without resolving it, and marks `Default` when `active_daemon` matches.

- [ ] **Step 4: Run tests to verify they pass**

Run:

```bash
go test ./internal/kata -run TestLoadCatalog -shuffle=on
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/kata docs/superpowers/plans/2026-06-08-kata-daemon-discovery.md
git commit -m "feat: read kata daemon catalog"
```

## Task 2: Runtime Discovery

**Files:**
- Modify: `internal/kata/types.go`
- Modify: `internal/kata/paths.go`
- Create: `internal/kata/runtime.go`
- Test: `internal/kata/runtime_test.go`

- [ ] **Step 1: Write failing runtime tests**

Create tests for:

- runtime record with live process and bare `host:port` address returns `http://host:port`;
- bare `localhost:port` becomes `http://localhost:port`;
- no runtime directory returns no discovered daemon;
- dead process is skipped;
- HTTP records are preferred over Unix sockets for general discovery;
- unparseable JSON is skipped;
- local daemon discovery accepts loopback HTTP and Unix sockets;
- local daemon discovery rejects non-loopback HTTP;
- local daemon discovery prefers a local Unix socket over a non-loopback HTTP record;
- runtime address normalization for bare TCP, IPv6 TCP, `http://`, `https://`, `unix://`, and invalid addresses.
- local-address classification for `localhost.`, IPv6 loopback, private LAN addresses, `0.0.0.0`, hostnames, and unsupported schemes.

Use a helper that writes `daemon.<pid>.json` into the runtime directory computed from the same `KATA_HOME` and `KATA_DB` values the package uses.

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/kata -run 'TestDiscover|TestRuntimeAddressURL' -shuffle=on
```

Expected: FAIL because runtime discovery functions do not exist.

- [ ] **Step 3: Implement runtime discovery**

Add:

- `type RuntimeRecord struct { PID int; Address string; DBPath string; Version string; StartedAt time.Time }`
- `func RuntimeDir() (string, error)`
- `func RuntimeAddressURL(addr string) string`
- `func Discover() *Discovered`
- `func DiscoverLocalDaemonURL() string`
- `func AliveRuntimeRecords() []RuntimeRecord`
- unexported `processAlive(pid int) bool`
- unexported `isLocalDaemonAddress(addr string) bool`

`RuntimeDir` uses `$KATA_HOME`, `$KATA_DB`, default `<home>/kata.db`, absolute DB path, and the first 12 lower-hex chars of `sha256(absDBPath)`.

- [ ] **Step 4: Run tests to verify they pass**

Run:

```bash
go test ./internal/kata -run 'TestDiscover|TestRuntimeAddressURL' -shuffle=on
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/kata
git commit -m "feat: discover kata runtime daemons"
```

## Task 3: Target Validation And Redaction

**Files:**
- Create: `internal/kata/validation.go`
- Test: `internal/kata/validation_test.go`

- [ ] **Step 1: Write failing validation tests**

Create tests for:

- `https://` static daemon target passes;
- `unix://` static daemon target passes;
- loopback/private `http://` static daemon target passes;
- `http://localhost.`, CGNAT `100.64/10`, IPv6 ULA, link-local, and unspecified static daemon targets pass;
- public `http://` static daemon target fails unless `AllowInsecure` is true;
- invalid URL and unsupported scheme fail;
- local daemon target accepts only `unix://`, loopback `http://`, loopback `https://`, and `localhost`;
- local daemon target rejects non-loopback HTTP even when private/public validation would otherwise allow it;
- redaction strips credentials/query/fragment and HTTP path from HTTP(S) URLs, while preserving Unix socket paths.
- `ResolveDaemon` resolves token env values, rejects unset or empty token env values, and preserves inline tokens.

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/kata -run 'TestValidate|TestRedact' -shuffle=on
```

Expected: FAIL because validation helpers do not exist.

- [ ] **Step 3: Implement validation helpers**

Add:

- `func ValidateTarget(Daemon) error`
- `func ValidateLocalTarget(Daemon) error`
- `func ResolveDaemon(Daemon) (Daemon, error)`
- `func RedactURL(raw string) string`

`ResolveDaemon` resolves `TokenEnv`, validates target rules, and leaves dynamic `Local` daemon entries URL-less until request time.

- [ ] **Step 4: Run tests to verify they pass**

Run:

```bash
go test ./internal/kata -run 'TestValidate|TestRedact' -shuffle=on
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/kata
git commit -m "feat: validate kata daemon targets"
```

## Task 4: Package Verification And Documentation

**Files:**
- Modify: `CLAUDE.md`
- Modify: `docs/superpowers/specs/2026-06-08-kata-docs-msgvault-modes-design.md`

- [ ] **Step 1: Update planned markers**

Remove the "(planned on this branch)" marker for `internal/kata/` in `CLAUDE.md`, leaving Docs and Msgvault marked planned.

- [ ] **Step 2: Run full package tests**

Run:

```bash
go test ./internal/kata -shuffle=on
```

Expected: PASS.

- [ ] **Step 3: Run repository doc hygiene checks**

Run:

```bash
git diff --check
```

Expected: no whitespace errors. Also inspect the new Kata package and docs for
legacy source-project names or header namespaces before committing.

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md docs/superpowers/specs/2026-06-08-kata-docs-msgvault-modes-design.md internal/kata
git commit -m "docs: mark kata package integration landed"
```
