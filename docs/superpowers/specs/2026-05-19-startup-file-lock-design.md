# Startup File Lock Design

## Goal

Make it obvious when a second middleman process is launched against the same
`data_dir`. Today the failure mode is opaque: the SQLite WAL handles its own
file lock so DB writes stay safe, but the second HTTP listener fails with
`bind: address in use`. Users see a port-collision error and don't realize
another middleman is already running. The fix is an explicit cross-platform
file lock taken at startup that fails fast with a banner identifying the
holder's PID, listen address, lock file, and `data_dir`, plus a small
`middleman status` subcommand that uses the same lock-probe to report
liveness even when the daemon is unreachable over HTTP.

## Scope

This is Phase 1 of a hypothetical multi-phase effort. Phase 1 covers:

- A startup-time OS file lock under `data_dir/middleman.lock` taken before
  the HTTP listener binds.
- A sibling metadata file `data_dir/middleman.run.json` with PID, port, host,
  resolved listen address, started_at, version, and commit.
- A clear multi-line stderr banner when lock acquisition fails, plus a terse
  structured slog record for log aggregation.
- A new `middleman status [--config <path>]` subcommand that probes the lock
  and reads the metadata file when the lock is busy.
- A "previous run terminated uncleanly" slog warning when a stale
  `.run.json` is found at startup; the stale file is removed under the held
  lock before listener setup.
- Cross-platform support: macOS, Linux, AND Windows. Implemented with
  `gofrs/flock` (already an indirect dependency).
- Tests for the lock-acquire-and-fail path, the stale-metadata path, the
  status command in both lock states, and the metadata format.

Explicitly out of scope (these would be later phases):

- Idle shutdown / auto-stop.
- Auto-start of a daemon when a CLI invocation needs it.
- Daemon supervision (systemd unit, launchd plist, Windows service).
- HTTP `/api/status` route or similar server-side status surface.
- Rebinding to a different `data_dir` mid-flight, hot config reload, or any
  multi-instance coordination.

## Background

`gofrs/flock` v0.13.0 is already in the module graph (pulled in indirectly
through testcontainers). It exposes `flock.New(path)` with `TryLock`,
`Lock`, and `Unlock`. The implementation uses `fcntl` on POSIX and
`LockFileEx` on Windows. Both grant exclusive access to one byte at offset
zero (length 1); both release on process exit even if the holder crashes.

Two cross-platform constraints shape the design:

- On Windows, `LockFileEx` with `LOCKFILE_EXCLUSIVE_LOCK` is **mandatory**:
  another process can't read the locked byte through `ReadFile` while the
  lock is held. That rules out a single-file design where metadata lives
  in the same file as the lock byte and is read by `middleman status`
  without acquiring the lock.
- Removing the lock file on shutdown introduces a multi-process race
  (process A unlocks, process B opens-and-locks, process A then unlinks
  the path B is holding — process C now creates a new file and locks
  it, and B and C both think they're the unique daemon). The lock file
  must therefore be treated as a permanent sentinel; liveness is always
  a function of lock acquisition, never of file presence.

The user-facing spec said "lock + pid files are removed on graceful
shutdown." This design overrides that wording for the lock file based on
the race above. Only the metadata file is removed; the lock file persists
and is reused on subsequent starts. The OS releases the lock itself on
process exit (graceful or not).

## File Layout

Two files at the root of `data_dir`, beside the existing `middleman.db`:

- `<data_dir>/middleman.lock` — zero-length. Holds the OS lock. Created
  once with mode `0o600`, never removed. Existence implies "middleman has
  run here at least once"; nothing more.
- `<data_dir>/middleman.run.json` — JSON metadata about the running
  daemon. Written under the held lock immediately after `net.Listen`
  returns the bound listener, so the recorded port matches the actual
  bound port even if the configured value and the resolved value
  ever diverge. Removed on graceful shutdown. Stale instances from
  crashes are unlinked at the next successful start.

The `data_dir/` subtree already contains `middleman.db`, the SQLite WAL
sidecars, `clones/`, and `worktrees/`. Two extra files at the root are
the least intrusive layout and the easiest to find when troubleshooting.

## Metadata File Format

`middleman.run.json` is a JSON object with the following keys:

```json
{
  "pid": 12345,
  "host": "127.0.0.1",
  "port": 8091,
  "listen_addr": "127.0.0.1:8091",
  "started_at": "2026-05-19T10:30:00Z",
  "version": "1.2.3",
  "commit": "abcd1234"
}
```

- `pid` (int): the daemon's process ID at startup.
- `host` (string): the **bound** host as reported by `ln.Addr().
  (*net.TCPAddr).IP.String()` — exactly what the kernel returned, not
  the configured `cfg.Host`. For a config of `0.0.0.0:0`, this might
  be `0.0.0.0` (depending on the platform); for `127.0.0.1:0`, it's
  `127.0.0.1`.
- `port` (int): the **bound** port reported by `ln.Addr()`. Today's
  config validator requires `port >= 1`, so the bound port equals
  the configured port in practice. Recording the bound port (rather
  than `cfg.Port`) is robust to a future relaxation of that
  validator and to any bind-time resolution differences. The
  redundant host + port shape is preserved beside `listen_addr`
  because it is easier to query programmatically.
- `listen_addr` (string): the bound `host:port` as returned by
  `ln.Addr().String()` (which already brackets IPv6 literals via
  `net.JoinHostPort` internally).
- `started_at` (string): UTC RFC3339, per the project's
  "UTC-everywhere-except-presentation" convention.
- `version`, `commit` (strings): the same values used by `middleman
  version`.

Decoding is tolerant of unknown fields (default `encoding/json`
behavior). Adding new fields later does not break older readers.

Writes are atomic: write to `<data_dir>/.middleman.run.json.tmp` (same
directory, dot-prefixed so it doesn't collide), `fsync`, then
`os.Rename` over the final path. This pattern is already used in
`internal/ptyowner/paths.go:writeState`. On modern Go (the project
targets 1.26), `os.Rename` over an existing file works on Windows via
`MoveFileEx` with `MOVEFILE_REPLACE_EXISTING`.

## Startup Sequence

The `run(configPath string) error` function in `cmd/middleman/main.go`
gains a lock acquisition step and reorders the listener bind to happen
synchronously instead of inside the existing `go func() {
srv.ListenAndServe(addr) }()`. The new ordering:

- `config.EnsureDefault`, `config.Load` (unchanged).
- `os.MkdirAll(cfg.DataDir, 0o700)` (unchanged).
- **NEW**: `handle, err := runtimelock.Acquire(cfg.DataDir)`. Acquire
  opens the lock file, calls `TryLock`, removes any stale
  `middleman.run.json` under the held lock (logging a `slog.Warn`
  for the stale-removal case), and returns the handle.
  - If `err != nil` and `errors.As` matches `*CollisionError`: read
    the metadata file from the error, print the collision banner to
    stderr (see "Collision Banner" below), return a sentinel error
    so main exits with status 1. The `slog.Error` fatal line stays
    terse (`"another middleman is already running"`).
  - If `err != nil` otherwise: return the wrapped error; main exits
    via the existing `slog.Error("fatal", "err", err)` path with no
    banner.
- **NEW**: `defer handle.Release()`. Release removes the metadata
  file (best-effort) and unlocks. The lock-file path stays on disk.
- Open DB, build provider tokens, resolve repos, build syncer — same
  work as today.
- **NEW**: `ln, err := net.Listen("tcp", cfg.ListenAddr())`. On error,
  return the wrapped listen error (still distinct from the lock
  collision — a non-port-collision listen failure has nothing to do
  with the lock).
- **NEW**: `handle.WriteMetadata(meta)` with values built from
  `ln.Addr().(*net.TCPAddr)` (IP, Port), `ln.Addr().String()` for the
  string form, `os.Getpid()`, `time.Now().UTC().Format(time.RFC3339)`,
  and the existing `version` / `commit` package globals.
- **NEW**: wire `syncer.SetOnStatusChange`, `syncer.SetOnSyncCompleted`,
  `syncer.Start(ctx)`, set the SSE hub's initial status — same code as
  today, just runs after the listener is bound and metadata is written.
- **NEW**: start the server with `srv.Serve(ln)` in a goroutine
  instead of `srv.ListenAndServe(addr)`. `Server` already exposes
  `Serve(ln net.Listener)` for this exact use case.

The signal-context wait and the existing `defer srv.Shutdown(...)` /
`defer syncer.Stop()` chain are unchanged. Because `defer
handle.Release()` is registered before those teardown defers, it runs
LAST (LIFO), so the lock stays held through full server + syncer
drain. Inside Release, `os.Remove(middleman.run.json)` runs before
`flock.Unlock()`, so the metadata file is gone before the lock is
released; readers between those two ticks see "metadata unavailable".

The lock-held-without-metadata window is the synchronous work between
stale-metadata removal (inside Acquire) and the WriteMetadata call —
DB open, provider setup, and listener bind. During this window,
`middleman status` correctly reports "daemon running but metadata is
missing or corrupt" rather than stale data, because Acquire already
removed any prior `.run.json`.

## `middleman status` Subcommand

Routed from `runCLI` in `cmd/middleman/main.go` alongside the existing
`version`, `config`, and `pty-owner` cases. Flags:

```
middleman status [--config <path>] [--json]
```

- `--config <path>`: same `config.DefaultConfigPath()` default as the
  main server invocation.
- `--json`: render the output as a single JSON object instead of the
  human-readable lines (for scripting). Default human output uses the
  same key alignment as the collision banner.

Behavior:

- Load the config to resolve `data_dir`.
- Call `status, err := runtimelock.Read(cfg.DataDir)`. Read constructs
  a `flock.New`, attempts `TryLock`, and releases immediately if it
  acquires. The returned `Status` captures three cases: lock-acquired
  (no running daemon), lock-busy-with-parsed-metadata, and
  lock-busy-with-metadata-error.
- On Read error (e.g., parent dir missing, EACCES on the lock file
  path): exit 1 with the wrapped error.
- On `status.Running == false`: print `"no running daemon"`. The lock
  file is left on disk. Exit 0.
- On `status.Running == true` with parsed metadata: print PID, host,
  port, listen_addr, started_at, version, commit. Exit 0.
- On `status.Running == true` with a metadata error: print `"running
  (metadata unavailable: <reason>)"` and the data_dir + lock path.
  Exit 0.

The TryLock-then-release pattern inside `Read` is safe: the kernel
reserves the lock only for the calling fd; releasing it immediately
doesn't disturb any other process that wasn't already in line.
`flock.New` opens the path with `O_CREATE | O_RDONLY` (POSIX) or the
Windows equivalent, so `status` will create the empty lock file the
first time it runs on a `data_dir` that has never hosted a daemon —
that's fine because the file is otherwise empty and persists by
design. The `--config` default ensures the parent directory exists;
if it does not, the lock-file open fails with ENOENT and `status`
exits 1 with a wrapped error.

`--json` output schema:

```json
{
  "running": true,
  "data_dir": "/home/u/.config/middleman",
  "lock_file": "/home/u/.config/middleman/middleman.lock",
  "metadata": {
    "pid": 12345,
    "host": "127.0.0.1",
    "port": 8091,
    "listen_addr": "127.0.0.1:8091",
    "started_at": "2026-05-19T10:30:00Z",
    "version": "1.2.3",
    "commit": "abcd1234"
  }
}
```

When `running` is `false`, `metadata` is `null`. When metadata is
missing or corrupt while running, `metadata` is `null` and a sibling
`"metadata_error": "<reason>"` key carries the cause.

## Collision Banner

Printed to **stderr** before `slog.Error` fires, so it stays on top of
the structured log even in non-interactive runs. The banner is fixed
text; only the field values vary.

Full-metadata form:

```
error: another middleman instance is already running
  data_dir:     /home/u/.config/middleman
  lock file:    /home/u/.config/middleman/middleman.lock
  running pid:  12345
  listening on: 127.0.0.1:8091
  started at:   2026-05-19T10:30:00Z
  version:      1.2.3

  Run `middleman status` to inspect it.
```

When the failing process passed a non-default `--config`, the last
line renders as ``Run `middleman status --config <path>`.``.

When `middleman.run.json` is missing, unreadable, or fails to decode,
the metadata-derived lines collapse to a single line:

```
error: another middleman instance is already running
  data_dir:     /home/u/.config/middleman
  lock file:    /home/u/.config/middleman/middleman.lock
  metadata:     unavailable (daemon may be early in startup, or metadata is missing/corrupt)

  Run `middleman status` to inspect it.
```

The slog record that follows is terse:

```
slog.Error("fatal", "err", "another middleman is already running on /home/u/.config/middleman")
```

The full per-field info stays in the banner so the log line stays
glanceable.

## Packaging

A new internal package `internal/runtimelock/` carries the lock
acquisition, metadata read/write, and status rendering logic so
`cmd/middleman/main.go` stays focused on wiring. Public surface:

- `runtimelock.Acquire(dataDir string) (*Handle, error)`: takes the
  lock, removes stale metadata, returns a handle. On collision returns
  a typed `*CollisionError` carrying the read metadata (or a sentinel
  when unavailable) plus the data_dir and lock-file path.
- `(*Handle).WriteMetadata(meta Metadata) error`: atomic temp+rename
  write of the metadata file.
- `(*Handle).Release() error`: removes metadata, unlocks. Idempotent;
  safe to call from a defer. Internally logs `slog.Warn` for any
  best-effort failure (metadata remove, unlock) so the returned error
  carries the same information for callers that check it, and a
  deferred call still surfaces failures via the log.
- `runtimelock.Read(dataDir string) (Status, error)`: the
  `middleman status` reader. Probes the lock, reads metadata when
  busy, returns a `Status` struct.
- `runtimelock.FormatCollisionBanner(err *CollisionError, configPath
  string, defaultConfigPath string, w io.Writer)`: renders the banner.
  Separate from `Acquire` so tests can format synthetic collisions.
- `runtimelock.FormatStatus(status Status, w io.Writer, asJSON bool)`:
  renders status output for the subcommand.

The `Metadata` struct mirrors the JSON object exactly; field tags
match the JSON keys.

## Error Handling

- All errors from `runtimelock.Acquire` other than collision (e.g.,
  EACCES on the lock file, an unwritable lock-file directory, an
  ENOSPC at the kernel locking layer) bubble up unwrapped through
  the existing `run() -> main` return path. They produce the
  existing `slog.Error("fatal", ...)` output with no banner; these
  are configuration or filesystem problems unrelated to the
  multi-instance case. (The caller is responsible for `MkdirAll` on
  `data_dir`; `Acquire` does not create the directory.)
- `WriteMetadata` returns its error to the caller; main's call site
  logs `slog.Warn("write runtime metadata", "err", err)` and
  continues. The lock is still held; the daemon is still safe; only
  the metadata file is missing. Status will report "metadata
  unavailable" in that window. This is preferable to aborting
  startup over a cosmetic write failure.
- `Release` logs its own best-effort failures internally as
  `slog.Warn` (see Packaging). Whether called directly or via
  `defer`, the warning still appears.
- Stale-metadata `os.Remove` failure during startup logs `slog.Warn`
  and continues; the next successful metadata write replaces the
  stale file regardless.

## Testing

End-to-end tests run against the real CLI binary (or its `runCLI`
entry point with a synthetic stdout/stderr), not unit-level fakes,
per the project's testing standards. Tests live in
`internal/runtimelock/` for the package-level behavior plus
`cmd/middleman/` (or an `e2etest` subpackage) for the full startup
path.

Cases:

1. **Acquire on empty data_dir succeeds.** Lock file is created;
   metadata file is absent until the caller writes it.
2. **Second Acquire fails with CollisionError.** Holds the first
   handle, calls Acquire again, declares `var cerr *CollisionError`
   and asserts `errors.As(err, &cerr)`. Verifies the data_dir +
   lock-file paths on the error.
3. **CollisionError surfaces metadata when present.** First handle
   writes metadata; second Acquire's CollisionError carries the
   parsed Metadata.
4. **CollisionError surfaces "unavailable" when metadata absent.**
   First handle has not yet written metadata; second Acquire's
   CollisionError exposes a typed "unavailable" reason.
5. **Stale metadata is removed on successful Acquire.** Pre-populate
   `middleman.run.json` without holding the lock; Acquire succeeds
   and the file is gone.
6. **Release removes metadata, leaves lock file.** Sanity check that
   the lock file persists across restarts.
7. **Atomic write integrity.** After a successful WriteMetadata,
   simulate a crash mid-write by creating a sibling
   `.middleman.run.json.tmp` file with garbage contents (no rename),
   then call WriteMetadata again. Assert the final
   `middleman.run.json` parses to the latest payload and that the
   garbage temp file from the simulated crash is gone (the real
   writer cleans up after itself or overwrites the temp path).
8. **`Read` reports running with metadata.** Acquire + WriteMetadata
   in one goroutine; in a sibling goroutine call `Read` and verify
   the parsed metadata.
9. **`Read` reports running without metadata.** Acquire without
   WriteMetadata; `Read` reports `running=true, metadata_error=<>`.
10. **`Read` reports not running.** No live daemon; `Read` returns
    `running=false` and leaves the lock file on disk.
11. **Banner formatting (golden tests).** Full-metadata form and
    metadata-unavailable form. Both `--config` cases.
12. **Status formatting (golden tests).** Human and JSON. All three
    states (running with metadata, running without metadata, not
    running).
13. **`middleman status` E2E.** Spawn the binary in two flavors
    (subprocess writing real files; in-process `runCLI` exercising
    the same paths). Cover the three states.
14. **End-to-end collision E2E.** Two `middleman` subprocesses
    against the same `data_dir` via `t.TempDir()`. The test
    pre-resolves a free TCP port with `net.Listen("tcp",
    "127.0.0.1:0")`, closes the listener, and writes that port into
    the config so both subprocesses target the same address. The
    first subprocess starts successfully; the second's exit code is
    1; the second's stderr matches the banner shape.

The Windows path uses the same `gofrs/flock` API. We do not run
Windows tests in CI today, but the `_windows` build-tag is not
needed: the entire package is platform-neutral and relies on
`gofrs/flock` for the platform-specific syscall. A Windows-build
smoke test (`GOOS=windows go build ./...`) is sufficient to catch
build breaks; runtime verification is left to ad-hoc local testing
or a future CI job.

## Open Questions

None. The four design decisions taken in the brainstorming phase
(status subcommand shape, file layout, metadata format/write
semantics, diagnostic presentation) settle the surface; the rest is
straightforward implementation.

## Future Work

These are explicitly out of scope for Phase 1 but are noted so they
don't get tangled into the implementation review:

- Idle shutdown: a daemon that exits after N minutes of no client
  activity. Would interact with the lock here but doesn't change it.
- Auto-start: a CLI invocation that notices "no running daemon"
  via `middleman status` and forks one. Same lock surface, plus a
  detach/double-fork story on POSIX and a `CREATE_NO_WINDOW` story
  on Windows.
- Server-side `/api/status` route: lets the SPA show the same data
  for completeness, but requires the daemon to be reachable, which
  defeats the troubleshooting case.
- Cross-instance coordination (e.g., a worker daemon and a UI
  daemon sharing a data_dir): would require a different lock model
  (shared/exclusive split, or per-subsystem locks).
- Distinct exit codes per error class (collision = 4, etc.) for
  wrapper scripts. Cheap to add later; defer until there is a stated
  consumer.
