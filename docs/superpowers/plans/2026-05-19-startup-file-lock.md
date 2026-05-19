# Startup File Lock Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make double-launch of `middleman` against the same `data_dir` fail fast with a banner naming the holding PID + listen address, and add `middleman status` to inspect liveness without HTTP.

**Architecture:** A new `internal/runtimelock` package wraps `gofrs/flock` to take an OS-level lock at `<data_dir>/middleman.lock` before the HTTP listener binds. The handle writes a JSON metadata sidecar (`middleman.run.json`) under the held lock once the bound port is known, and removes it on graceful release. The `cmd/middleman` entrypoint reorders startup to bind the listener synchronously (via `srv.Serve(ln)`), defers `handle.Release()` so it runs LAST in the LIFO defer chain, and a new `status` subcommand reuses the same lock-probe to report liveness.

**Tech Stack:** Go 1.26, `github.com/gofrs/flock` v0.13.0 (already an indirect dependency), `log/slog`, `encoding/json`, `net.Listen`, `testify`, `nix run nixpkgs#go`.

---

## Background for the executing engineer

Read these before starting:

- The spec: `docs/superpowers/specs/2026-05-19-startup-file-lock-design.md`. The spec is the contract for shape, error handling, banner format, and test coverage. Where this plan and the spec disagree, the spec wins.
- `cmd/middleman/main.go`. The lock wiring lives in `run(configPath string) error` (currently lines ~281 to ~424) and the `runCLI` dispatcher (around lines ~175 to ~202). Re-read these regions before Task 9.
- `internal/server/server.go` exposes `Serve(ln net.Listener) error` (around line ~776) — that's the entrypoint we switch to.
- `internal/ptyowner/paths.go:writeState` is the atomic-temp+rename pattern the project already uses. Mirror it in `internal/runtimelock/metadata.go`.
- `internal/db/db_test.go` and `internal/ptyowner/owner_test.go` show the project's testify conventions: `require := require.New(t)` for setup, `assert := Assert.New(t)` only when there are >3 follow-up assertions in a function.

## Tooling reminders

- Go binary is not on `PATH`. Every Go invocation goes through `nix run nixpkgs#go --`. Example: `nix run nixpkgs#go -- test ./internal/runtimelock -shuffle=on`.
- `golangci-lint` is similarly behind nix: `nix shell 'nixpkgs#golangci-lint' --command golangci-lint run ./internal/runtimelock/... ./cmd/middleman/...`.
- Always pass `-shuffle=on` when invoking `go test` directly.
- Never `-count=1` and never `-v`.
- No emojis in code, output, or commit messages.
- Use conventional commit messages whose subject describes the user-visible outcome.

## File Structure

These are the files this plan creates or modifies. Each task touches only the files listed under it.

**New files (under `internal/runtimelock/`):**

- `internal/runtimelock/metadata.go` — `Metadata` struct (JSON tags), atomic write helper, sibling temp-file constants.
- `internal/runtimelock/errors.go` — `CollisionError` type, `MetadataUnavailableReason` constants.
- `internal/runtimelock/lock.go` — `Handle` struct, `Acquire`, `(*Handle).WriteMetadata`, `(*Handle).Release`. Owns the `flock.Flock` internally.
- `internal/runtimelock/read.go` — `Status` struct, `Read(dataDir string) (Status, error)`.
- `internal/runtimelock/format.go` — `FormatCollisionBanner`, `FormatStatus` (human + JSON forms).
- `internal/runtimelock/paths.go` — `LockPath`, `MetadataPath`, `metadataTmpPath` helpers so callers and tests refer to the same constants.

**New test files:**

- `internal/runtimelock/lock_test.go` — Acquire / Release / WriteMetadata / collision / stale-metadata cases.
- `internal/runtimelock/read_test.go` — `Read` against the three states.
- `internal/runtimelock/format_test.go` — banner + status golden strings.
- `cmd/middleman/lock_e2e_test.go` — E2E: two subprocesses, collision banner, `middleman status` cases.

**Files to modify:**

- `cmd/middleman/main.go` — add `status` subcommand, replace `ListenAndServe` with `Serve(ln)` after `Acquire`/`WriteMetadata`. Touch range described in Task 9.
- `go.mod` and `go.sum` — promote `github.com/gofrs/flock` from indirect to direct require.

## Dependency note: `gofrs/flock`

`gofrs/flock` v0.13.0 is already present (indirect via testcontainers — see `go.mod:96`). The first task makes it a direct dependency. Do NOT touch the version: stay on v0.13.0 unless `go mod tidy` insists otherwise.

The flock API surface this plan relies on:

```go
fl := flock.New(path)                // returns *flock.Flock; does not open the file yet
ok, err := fl.TryLock()              // opens file (O_CREATE|O_RDWR mode 0o600) and tries exclusive lock
err  := fl.Unlock()                  // releases and closes
```

`TryLock` returns `(ok=false, err=nil)` on lock-busy, and `(ok=true, err=nil)` on success.

---

## Task 1: Promote gofrs/flock to a direct dependency

**Files:**
- Modify: `go.mod`
- Modify: `go.sum` (regenerated by `go mod tidy`)

**Steps:**

- [ ] **Step 1: Verify current state**

Run: `nix run nixpkgs#go -- list -m github.com/gofrs/flock`
Expected: `github.com/gofrs/flock v0.13.0`

Run: `grep -n 'gofrs/flock' go.mod`
Expected: shows `github.com/gofrs/flock v0.13.0 // indirect`

- [ ] **Step 2: Add a direct import**

Create a temporary scratch file to force the direct-dependency promotion. Add the file at `internal/runtimelock/doc.go`:

```go
// Package runtimelock guards the middleman daemon against double-launch
// against the same data_dir. Acquire takes an OS-level file lock under
// data_dir before the HTTP listener binds; WriteMetadata records PID,
// listen address, and version under the held lock; Release removes the
// metadata file and unlocks. Status reads liveness via a try-and-release
// probe of the same lock.
package runtimelock

import _ "github.com/gofrs/flock"
```

- [ ] **Step 3: Run go mod tidy**

Run: `nix run nixpkgs#go -- mod tidy`
Expected: exits 0; `go.mod` now lists `github.com/gofrs/flock v0.13.0` without the `// indirect` comment in the require block; `go.sum` may shrink slightly.

- [ ] **Step 4: Verify the import resolves**

Run: `nix run nixpkgs#go -- build ./internal/runtimelock`
Expected: exits 0 with no output.

- [ ] **Step 5: Replace the blank import with a real one**

Edit `internal/runtimelock/doc.go` to remove the blank import (we will add real usage in Task 4):

```go
// Package runtimelock guards the middleman daemon against double-launch
// against the same data_dir. Acquire takes an OS-level file lock under
// data_dir before the HTTP listener binds; WriteMetadata records PID,
// listen address, and version under the held lock; Release removes the
// metadata file and unlocks. Status reads liveness via a try-and-release
// probe of the same lock.
package runtimelock
```

Note: `go mod tidy` is now used to "lock in" the direct require — the require remains direct because Task 4 will add a real import. If you re-run `go mod tidy` between Task 1 and Task 4 the require may go back to `// indirect`; that's fine, Task 4 will promote it again.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/runtimelock/doc.go
git commit -m "$(cat <<'EOF'
build: promote gofrs/flock to a direct require for the runtime lock

Adds an empty internal/runtimelock package as a placeholder so the
upcoming startup-file-lock implementation has a home. The flock module
was already pulled in transitively by testcontainers; promoting it to a
direct require makes the dependency visible in go.mod and prevents the
indirect entry from disappearing if a future testcontainers bump drops
that transitive edge.
EOF
)"
```

---

## Task 2: Define the Metadata struct and path helpers

**Files:**
- Create: `internal/runtimelock/paths.go`
- Create: `internal/runtimelock/metadata.go`
- Create: `internal/runtimelock/metadata_test.go`

**Steps:**

- [ ] **Step 1: Write the failing test for path helpers**

Create `internal/runtimelock/metadata_test.go`:

```go
package runtimelock

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPathsAreUnderDataDir(t *testing.T) {
	dir := t.TempDir()

	require.Equal(t, filepath.Join(dir, "middleman.lock"), LockPath(dir))
	require.Equal(t, filepath.Join(dir, "middleman.run.json"), MetadataPath(dir))
	require.Equal(t, filepath.Join(dir, ".middleman.run.json.tmp"), metadataTmpPath(dir))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `nix run nixpkgs#go -- test ./internal/runtimelock -shuffle=on -run TestPathsAreUnderDataDir`
Expected: FAIL with "undefined: LockPath" (and friends).

- [ ] **Step 3: Implement the path helpers**

Create `internal/runtimelock/paths.go`:

```go
package runtimelock

import "path/filepath"

// File names at the root of data_dir.
const (
	lockFileName      = "middleman.lock"
	metadataFileName  = "middleman.run.json"
	metadataTmpFile   = ".middleman.run.json.tmp"
)

// LockPath returns the absolute path of the lock file under dataDir.
// The file is created on first Acquire and persists across restarts;
// existence implies nothing about liveness.
func LockPath(dataDir string) string {
	return filepath.Join(dataDir, lockFileName)
}

// MetadataPath returns the absolute path of the runtime metadata file
// under dataDir. The file exists only while a daemon is running.
func MetadataPath(dataDir string) string {
	return filepath.Join(dataDir, metadataFileName)
}

func metadataTmpPath(dataDir string) string {
	return filepath.Join(dataDir, metadataTmpFile)
}
```

- [ ] **Step 4: Run path test to verify it passes**

Run: `nix run nixpkgs#go -- test ./internal/runtimelock -shuffle=on -run TestPathsAreUnderDataDir`
Expected: PASS.

- [ ] **Step 5: Write the failing test for metadata round-trip**

Append to `internal/runtimelock/metadata_test.go`:

```go
func TestMetadataAtomicWriteRoundTrip(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()

	meta := Metadata{
		PID:        4242,
		Host:       "127.0.0.1",
		Port:       8091,
		ListenAddr: "127.0.0.1:8091",
		StartedAt:  "2026-05-19T10:30:00Z",
		Version:    "1.2.3",
		Commit:     "abcd1234",
	}

	require.NoError(writeMetadata(dir, meta))

	got, err := readMetadata(dir)
	require.NoError(err)
	require.Equal(meta, got)
}

func TestMetadataWriteOverwritesStaleTempFile(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()

	// Simulate a previous run that crashed mid-rename: a leftover
	// temp file with garbage that we want overwritten.
	require.NoError(os.WriteFile(metadataTmpPath(dir), []byte("garbage"), 0o600))

	meta := Metadata{PID: 1, ListenAddr: "127.0.0.1:1"}
	require.NoError(writeMetadata(dir, meta))

	got, err := readMetadata(dir)
	require.NoError(err)
	require.Equal(meta, got)

	// The temp file from the simulated crash is gone.
	_, err = os.Stat(metadataTmpPath(dir))
	require.True(os.IsNotExist(err), "temp file should be cleaned up, got %v", err)
}

func TestReadMetadataMissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := readMetadata(dir)
	require.ErrorIs(t, err, errMetadataMissing)
}

func TestReadMetadataCorrupt(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	require.NoError(os.WriteFile(MetadataPath(dir), []byte("not json"), 0o600))

	_, err := readMetadata(dir)
	require.Error(err)
	require.NotErrorIs(err, errMetadataMissing)
}
```

Also add the new imports at the top of the file:

```go
import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)
```

- [ ] **Step 6: Run metadata tests to verify they fail**

Run: `nix run nixpkgs#go -- test ./internal/runtimelock -shuffle=on`
Expected: FAIL (undefined Metadata, writeMetadata, readMetadata, errMetadataMissing).

- [ ] **Step 7: Implement Metadata and atomic helpers**

Create `internal/runtimelock/metadata.go`:

```go
package runtimelock

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// Metadata is the on-disk shape of middleman.run.json. JSON tags are
// the wire format; do not rename keys without a migration story.
//
// Decoders accept unknown keys so future fields don't break older
// readers; the default encoding/json behavior already does this.
type Metadata struct {
	PID        int    `json:"pid"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	ListenAddr string `json:"listen_addr"`
	StartedAt  string `json:"started_at"`
	Version    string `json:"version"`
	Commit     string `json:"commit"`
}

// errMetadataMissing is the typed reason returned by readMetadata
// when the metadata file is absent. Distinguished from a decode
// failure so callers can render "metadata unavailable: missing" vs
// "metadata unavailable: corrupt".
var errMetadataMissing = errors.New("runtime metadata is missing")

// writeMetadata writes meta atomically to MetadataPath(dataDir).
//
// Pattern (mirrors internal/ptyowner/paths.go:writeState):
//  1. Marshal meta to JSON.
//  2. Open <dataDir>/.middleman.run.json.tmp with O_CREATE|O_WRONLY|O_TRUNC mode 0o600.
//     Truncating, rather than O_EXCL, ensures a leftover temp file
//     from a previous crash is overwritten rather than blocking us.
//  3. Write, fsync, close.
//  4. os.Rename onto MetadataPath. On Go 1.26 + Windows this maps to
//     MoveFileEx with MOVEFILE_REPLACE_EXISTING.
//
// Any failure removes the temp file before returning so we never leak.
func writeMetadata(dataDir string, meta Metadata) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal runtime metadata: %w", err)
	}

	tmpPath := metadataTmpPath(dataDir)
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open runtime metadata temp file: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write runtime metadata temp file: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("sync runtime metadata temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close runtime metadata temp file: %w", err)
	}
	if err := os.Rename(tmpPath, MetadataPath(dataDir)); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename runtime metadata: %w", err)
	}
	return nil
}

// readMetadata reads and decodes the metadata file under dataDir.
// Returns errMetadataMissing when the file does not exist, and a
// wrapped JSON error when present-but-undecodable.
func readMetadata(dataDir string) (Metadata, error) {
	data, err := os.ReadFile(MetadataPath(dataDir))
	if err != nil {
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) && errors.Is(pathErr.Err, fs.ErrNotExist) {
			return Metadata{}, errMetadataMissing
		}
		return Metadata{}, fmt.Errorf("read runtime metadata: %w", err)
	}
	var meta Metadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return Metadata{}, fmt.Errorf("decode runtime metadata: %w", err)
	}
	return meta, nil
}

// removeMetadata removes the metadata file. Missing-file is not an
// error; the caller treats it as "already clean".
func removeMetadata(dataDir string) error {
	if err := os.Remove(MetadataPath(dataDir)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove runtime metadata: %w", err)
	}
	return nil
}
```

- [ ] **Step 8: Run all package tests to verify pass**

Run: `nix run nixpkgs#go -- test ./internal/runtimelock -shuffle=on`
Expected: PASS — all four metadata tests plus the path test.

- [ ] **Step 9: Commit**

```bash
git add internal/runtimelock/paths.go internal/runtimelock/metadata.go internal/runtimelock/metadata_test.go internal/runtimelock/doc.go
git commit -m "$(cat <<'EOF'
feat(runtimelock): define metadata struct and atomic write helpers

The startup file-lock design records the running daemon's PID, bound
listen address, and version in a JSON sidecar so middleman status (and
the collision banner) can identify the holder. The write goes through a
sibling .middleman.run.json.tmp file and an os.Rename so partial writes
never appear at the canonical path. Truncating the temp file rather
than using O_EXCL recovers cleanly from a crash that left a stale temp
file behind.
EOF
)"
```

---

## Task 3: Define CollisionError and metadata reasons

**Files:**
- Create: `internal/runtimelock/errors.go`
- Create: `internal/runtimelock/errors_test.go`

**Steps:**

- [ ] **Step 1: Write the failing test**

Create `internal/runtimelock/errors_test.go`:

```go
package runtimelock

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCollisionErrorImplementsError(t *testing.T) {
	require := require.New(t)

	meta := Metadata{PID: 99, ListenAddr: "127.0.0.1:9999"}
	cerr := &CollisionError{
		DataDir:  "/tmp/dd",
		LockPath: "/tmp/dd/middleman.lock",
		Metadata: &meta,
	}

	require.Contains(cerr.Error(), "/tmp/dd")
	require.Contains(cerr.Error(), "already running")

	// errors.As pattern (this is the exact idiom Acquire callers use).
	var asTarget *CollisionError
	require.True(errors.As(error(cerr), &asTarget))
	require.Equal(cerr, asTarget)

	// A plain non-collision error must not match.
	asTarget = nil
	require.False(errors.As(errors.New("unrelated"), &asTarget))
	require.Nil(asTarget)
}

func TestCollisionErrorMetadataUnavailable(t *testing.T) {
	require := require.New(t)

	cerr := &CollisionError{
		DataDir:           "/tmp/dd",
		LockPath:          "/tmp/dd/middleman.lock",
		Metadata:          nil,
		MetadataUnavailable: ReasonMetadataMissing,
	}

	require.Nil(cerr.Metadata)
	require.Equal(ReasonMetadataMissing, cerr.MetadataUnavailable)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `nix run nixpkgs#go -- test ./internal/runtimelock -shuffle=on -run TestCollisionError`
Expected: FAIL (undefined: CollisionError, ReasonMetadataMissing).

- [ ] **Step 3: Implement the errors file**

Create `internal/runtimelock/errors.go`:

```go
package runtimelock

import "fmt"

// MetadataUnavailableReason explains why the runtime metadata file
// could not be read when reporting a CollisionError or Status. The
// banner and `middleman status` use it for the metadata-unavailable
// branch.
type MetadataUnavailableReason string

const (
	// ReasonMetadataMissing indicates the metadata file does not
	// exist. Most commonly seen when the daemon is mid-startup
	// between Acquire and WriteMetadata.
	ReasonMetadataMissing MetadataUnavailableReason = "missing"

	// ReasonMetadataCorrupt indicates the metadata file exists but
	// could not be decoded as JSON.
	ReasonMetadataCorrupt MetadataUnavailableReason = "corrupt"
)

// String returns a human-readable form for inclusion in error
// messages and banners.
func (r MetadataUnavailableReason) String() string {
	switch r {
	case ReasonMetadataMissing:
		return "missing (daemon may be early in startup)"
	case ReasonMetadataCorrupt:
		return "corrupt (file present but could not be parsed)"
	default:
		return string(r)
	}
}

// CollisionError is returned by Acquire when another process already
// holds the lock. It carries enough context for the caller to render
// a banner without re-reading any files.
//
// Metadata is nil when the running daemon has not yet written its
// metadata file (or when the file is corrupt). In that case
// MetadataUnavailable explains why.
type CollisionError struct {
	DataDir             string
	LockPath            string
	Metadata            *Metadata
	MetadataUnavailable MetadataUnavailableReason
}

func (e *CollisionError) Error() string {
	return fmt.Sprintf("another middleman is already running on %s", e.DataDir)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `nix run nixpkgs#go -- test ./internal/runtimelock -shuffle=on`
Expected: PASS for all tests so far.

- [ ] **Step 5: Commit**

```bash
git add internal/runtimelock/errors.go internal/runtimelock/errors_test.go
git commit -m "$(cat <<'EOF'
feat(runtimelock): add CollisionError and metadata-unavailable reasons

CollisionError lets Acquire callers detect a multi-instance collision
with errors.As without text matching, and carries the parsed metadata
(or a typed reason when the metadata is missing or corrupt) so the
banner renderer does not need to re-read files. The reason enum keeps
the missing vs corrupt distinction explicit for users debugging a
half-started daemon.
EOF
)"
```

---

## Task 4: Implement Acquire, Release, and WriteMetadata

**Files:**
- Create: `internal/runtimelock/lock.go`
- Create: `internal/runtimelock/lock_test.go`

**Steps:**

- [ ] **Step 1: Write the failing test for Acquire on empty data_dir**

Create `internal/runtimelock/lock_test.go`:

```go
package runtimelock

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAcquireSucceedsOnEmptyDataDir(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()

	h, err := Acquire(dir)
	require.NoError(err)
	require.NotNil(h)
	t.Cleanup(func() { _ = h.Release() })

	// Lock file is created.
	_, err = os.Stat(LockPath(dir))
	require.NoError(err)

	// Metadata file is NOT created until WriteMetadata is called.
	_, err = os.Stat(MetadataPath(dir))
	require.True(os.IsNotExist(err))
}

func TestAcquireSecondCallReturnsCollision(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()

	h1, err := Acquire(dir)
	require.NoError(err)
	t.Cleanup(func() { _ = h1.Release() })

	_, err = Acquire(dir)
	require.Error(err)

	var cerr *CollisionError
	require.True(errors.As(err, &cerr))
	require.Equal(dir, cerr.DataDir)
	require.Equal(LockPath(dir), cerr.LockPath)
}

func TestAcquireCollisionWithMetadata(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()

	h1, err := Acquire(dir)
	require.NoError(err)
	t.Cleanup(func() { _ = h1.Release() })

	meta := Metadata{PID: 1234, ListenAddr: "127.0.0.1:8091"}
	require.NoError(h1.WriteMetadata(meta))

	_, err = Acquire(dir)
	var cerr *CollisionError
	require.True(errors.As(err, &cerr))
	require.NotNil(cerr.Metadata)
	require.Equal(meta, *cerr.Metadata)
}

func TestAcquireCollisionWithoutMetadata(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()

	h1, err := Acquire(dir)
	require.NoError(err)
	t.Cleanup(func() { _ = h1.Release() })

	_, err = Acquire(dir)
	var cerr *CollisionError
	require.True(errors.As(err, &cerr))
	require.Nil(cerr.Metadata)
	require.Equal(ReasonMetadataMissing, cerr.MetadataUnavailable)
}

func TestAcquireRemovesStaleMetadata(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()

	// Simulate a previous unclean shutdown: metadata file exists but
	// no holder of the lock.
	stale := Metadata{PID: 9999, ListenAddr: "127.0.0.1:9999"}
	require.NoError(writeMetadata(dir, stale))

	h, err := Acquire(dir)
	require.NoError(err)
	t.Cleanup(func() { _ = h.Release() })

	_, err = os.Stat(MetadataPath(dir))
	require.True(os.IsNotExist(err), "stale metadata should be removed under the held lock")
}

func TestReleaseRemovesMetadataKeepsLockFile(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()

	h, err := Acquire(dir)
	require.NoError(err)

	require.NoError(h.WriteMetadata(Metadata{PID: 1, ListenAddr: "127.0.0.1:1"}))
	require.NoError(h.Release())

	// Metadata file is gone.
	_, err = os.Stat(MetadataPath(dir))
	require.True(os.IsNotExist(err))

	// Lock file persists.
	_, err = os.Stat(LockPath(dir))
	require.NoError(err)

	// A fresh Acquire on the same dir now succeeds.
	h2, err := Acquire(dir)
	require.NoError(err)
	t.Cleanup(func() { _ = h2.Release() })
}

func TestReleaseIsIdempotent(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()

	h, err := Acquire(dir)
	require.NoError(err)

	require.NoError(h.Release())
	require.NoError(h.Release(), "second Release call must not error")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `nix run nixpkgs#go -- test ./internal/runtimelock -shuffle=on -run TestAcquire`
Expected: FAIL (undefined: Acquire, Handle.Release, Handle.WriteMetadata).

- [ ] **Step 3: Implement lock.go**

Create `internal/runtimelock/lock.go`:

```go
package runtimelock

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/gofrs/flock"
)

// Handle wraps a held runtime lock. It is returned by Acquire and
// surrendered by Release. The zero value is not usable.
//
// Handle is goroutine-safe for Release (a sync.Once protects the
// teardown path) but WriteMetadata is intended to be called once
// during startup before any other goroutine has a reference; it does
// not lock against itself.
type Handle struct {
	dataDir string
	lock    *flock.Flock

	releaseOnce sync.Once
	releaseErr  error
}

// Acquire takes the runtime lock under dataDir. The caller must have
// already ensured dataDir exists (Acquire does not create it).
//
// On a collision with another running daemon, Acquire returns a
// *CollisionError describing the holder; use errors.As to detect it.
// Stale metadata left behind by an unclean shutdown is removed under
// the held lock before Acquire returns; a slog.Warn announces the
// cleanup so operators see it in the logs.
func Acquire(dataDir string) (*Handle, error) {
	lock := flock.New(LockPath(dataDir))

	ok, err := lock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("acquire runtime lock: %w", err)
	}
	if !ok {
		cerr := &CollisionError{
			DataDir:  dataDir,
			LockPath: LockPath(dataDir),
		}
		meta, readErr := readMetadata(dataDir)
		switch {
		case readErr == nil:
			cerr.Metadata = &meta
		case errors.Is(readErr, errMetadataMissing):
			cerr.MetadataUnavailable = ReasonMetadataMissing
		default:
			cerr.MetadataUnavailable = ReasonMetadataCorrupt
		}
		return nil, cerr
	}

	// Clean up stale metadata from a previous unclean shutdown
	// under the held lock so a partially-started daemon does not
	// see another's PID.
	if _, statErr := os.Stat(MetadataPath(dataDir)); statErr == nil {
		slog.Warn("previous middleman run terminated uncleanly; removing stale metadata",
			"data_dir", dataDir,
			"metadata_path", MetadataPath(dataDir),
		)
		if err := removeMetadata(dataDir); err != nil {
			slog.Warn("remove stale runtime metadata", "err", err)
		}
	}

	return &Handle{dataDir: dataDir, lock: lock}, nil
}

// WriteMetadata persists meta to the metadata file under the held
// lock. Call this once the listener has bound so the recorded port
// matches the kernel-assigned value.
func (h *Handle) WriteMetadata(meta Metadata) error {
	return writeMetadata(h.dataDir, meta)
}

// Release removes the metadata file (best effort) and unlocks the
// runtime lock. The lock file itself is left on disk by design
// (removing it would race against a concurrent Acquire on the same
// path). Release is safe to call from a defer and is idempotent.
//
// Any best-effort failure is logged via slog.Warn so a deferred caller
// that ignores the return value still surfaces the problem.
func (h *Handle) Release() error {
	h.releaseOnce.Do(func() {
		if err := removeMetadata(h.dataDir); err != nil {
			slog.Warn("remove runtime metadata on release",
				"data_dir", h.dataDir, "err", err)
			h.releaseErr = err
		}
		if err := h.lock.Unlock(); err != nil {
			slog.Warn("unlock runtime lock",
				"data_dir", h.dataDir, "err", err)
			if h.releaseErr == nil {
				h.releaseErr = err
			}
		}
	})
	return h.releaseErr
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `nix run nixpkgs#go -- test ./internal/runtimelock -shuffle=on`
Expected: PASS for all lock and metadata tests so far.

- [ ] **Step 5: Commit**

```bash
git add internal/runtimelock/lock.go internal/runtimelock/lock_test.go
git commit -m "$(cat <<'EOF'
feat(runtimelock): add Acquire/Release/WriteMetadata for the startup lock

Acquire takes a gofrs/flock-backed exclusive lock under data_dir before
the HTTP listener binds and removes any stale metadata under the held
lock so a half-started daemon never sees a previous run's PID. Release
removes the metadata file before unlocking so readers between those
two ticks see metadata-unavailable rather than stale data; it is
idempotent so a deferred call is safe even if the caller also calls
Release explicitly.
EOF
)"
```

---

## Task 5: Implement Read (the status probe)

**Files:**
- Create: `internal/runtimelock/read.go`
- Create: `internal/runtimelock/read_test.go`

**Steps:**

- [ ] **Step 1: Write the failing test**

Create `internal/runtimelock/read_test.go`:

```go
package runtimelock

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadOnEmptyDataDirReportsNotRunning(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()

	st, err := Read(dir)
	require.NoError(err)
	require.False(st.Running)
	require.Equal(dir, st.DataDir)
	require.Equal(LockPath(dir), st.LockPath)
	require.Nil(st.Metadata)
	require.Empty(st.MetadataUnavailable)
}

func TestReadWhileLockHeldWithMetadata(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()

	h, err := Acquire(dir)
	require.NoError(err)
	t.Cleanup(func() { _ = h.Release() })

	meta := Metadata{
		PID:        4242,
		Host:       "127.0.0.1",
		Port:       8091,
		ListenAddr: "127.0.0.1:8091",
		StartedAt:  "2026-05-19T10:30:00Z",
		Version:    "1.2.3",
		Commit:     "abcd1234",
	}
	require.NoError(h.WriteMetadata(meta))

	st, err := Read(dir)
	require.NoError(err)
	require.True(st.Running)
	require.NotNil(st.Metadata)
	require.Equal(meta, *st.Metadata)
	require.Empty(st.MetadataUnavailable)
}

func TestReadWhileLockHeldWithoutMetadata(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()

	h, err := Acquire(dir)
	require.NoError(err)
	t.Cleanup(func() { _ = h.Release() })

	st, err := Read(dir)
	require.NoError(err)
	require.True(st.Running)
	require.Nil(st.Metadata)
	require.Equal(ReasonMetadataMissing, st.MetadataUnavailable)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `nix run nixpkgs#go -- test ./internal/runtimelock -shuffle=on -run TestRead`
Expected: FAIL (undefined: Read, Status fields).

- [ ] **Step 3: Implement Read**

Create `internal/runtimelock/read.go`:

```go
package runtimelock

import (
	"errors"
	"fmt"

	"github.com/gofrs/flock"
)

// Status describes the lock state of a data_dir at a moment in time.
// It is the return shape of Read.
type Status struct {
	// DataDir is the data_dir argument passed to Read.
	DataDir string

	// LockPath is the absolute path of the lock file under DataDir.
	LockPath string

	// Running is true when another process holds the runtime lock
	// at the moment of the probe.
	Running bool

	// Metadata is the decoded run metadata when Running is true and
	// the metadata file is present and parseable. Nil otherwise.
	Metadata *Metadata

	// MetadataUnavailable is non-empty when Running is true but the
	// metadata file could not be read. ReasonMetadataMissing is the
	// expected value during the startup window between Acquire and
	// WriteMetadata.
	MetadataUnavailable MetadataUnavailableReason
}

// Read probes the runtime lock under dataDir without holding it.
// The implementation TryLocks the file, releases immediately if it
// acquires, and reports either "not running" or "running with
// metadata X / unavailable for reason Y" otherwise.
//
// Errors are returned only for operational failures (e.g., parent
// directory missing, permission denied opening the lock file path).
// A busy lock is reported via Status.Running and is not an error.
func Read(dataDir string) (Status, error) {
	st := Status{
		DataDir:  dataDir,
		LockPath: LockPath(dataDir),
	}

	lock := flock.New(LockPath(dataDir))
	ok, err := lock.TryLock()
	if err != nil {
		return Status{}, fmt.Errorf("probe runtime lock: %w", err)
	}
	if ok {
		// We acquired it ourselves; release and report not-running.
		if unlockErr := lock.Unlock(); unlockErr != nil {
			return Status{}, fmt.Errorf("release runtime lock after probe: %w", unlockErr)
		}
		return st, nil
	}

	st.Running = true
	meta, readErr := readMetadata(dataDir)
	switch {
	case readErr == nil:
		st.Metadata = &meta
	case errors.Is(readErr, errMetadataMissing):
		st.MetadataUnavailable = ReasonMetadataMissing
	default:
		st.MetadataUnavailable = ReasonMetadataCorrupt
	}
	return st, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `nix run nixpkgs#go -- test ./internal/runtimelock -shuffle=on`
Expected: PASS for all tests.

- [ ] **Step 5: Commit**

```bash
git add internal/runtimelock/read.go internal/runtimelock/read_test.go
git commit -m "$(cat <<'EOF'
feat(runtimelock): add Read for non-blocking liveness probes

Read powers the upcoming middleman status subcommand. It tries the
lock, releases immediately if it acquires (proving no other holder is
in line), and otherwise reports the metadata under the busy lock or a
typed unavailable reason during the Acquire-to-WriteMetadata startup
window. Operational filesystem errors propagate; a busy lock does not.
EOF
)"
```

---

## Task 6: Implement FormatCollisionBanner

**Files:**
- Create: `internal/runtimelock/format.go`
- Create: `internal/runtimelock/format_test.go`

**Steps:**

- [ ] **Step 1: Write the failing test (golden strings)**

Create `internal/runtimelock/format_test.go`:

```go
package runtimelock

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFormatCollisionBannerWithMetadata(t *testing.T) {
	require := require.New(t)

	cerr := &CollisionError{
		DataDir:  "/home/u/.config/middleman",
		LockPath: "/home/u/.config/middleman/middleman.lock",
		Metadata: &Metadata{
			PID:        12345,
			Host:       "127.0.0.1",
			Port:       8091,
			ListenAddr: "127.0.0.1:8091",
			StartedAt:  "2026-05-19T10:30:00Z",
			Version:    "1.2.3",
			Commit:     "abcd1234",
		},
	}

	var buf bytes.Buffer
	FormatCollisionBanner(&buf, cerr, "" /* configPath */, "/home/u/.config/middleman/config.toml" /* defaultConfigPath */)

	want := `error: another middleman instance is already running
  data_dir:     /home/u/.config/middleman
  lock file:    /home/u/.config/middleman/middleman.lock
  running pid:  12345
  listening on: 127.0.0.1:8091
  started at:   2026-05-19T10:30:00Z
  version:      1.2.3

  Run ` + "`middleman status`" + ` to inspect it.
`
	require.Equal(want, buf.String())
}

func TestFormatCollisionBannerWithNonDefaultConfig(t *testing.T) {
	require := require.New(t)

	cerr := &CollisionError{
		DataDir:  "/home/u/.config/middleman",
		LockPath: "/home/u/.config/middleman/middleman.lock",
		Metadata: &Metadata{
			PID:        12345,
			ListenAddr: "127.0.0.1:8091",
			StartedAt:  "2026-05-19T10:30:00Z",
			Version:    "1.2.3",
		},
	}

	var buf bytes.Buffer
	FormatCollisionBanner(&buf, cerr, "/etc/middleman/alt.toml", "/home/u/.config/middleman/config.toml")

	require.Contains(buf.String(), "Run `middleman status --config /etc/middleman/alt.toml` to inspect it.")
}

func TestFormatCollisionBannerMetadataUnavailable(t *testing.T) {
	require := require.New(t)

	cerr := &CollisionError{
		DataDir:             "/home/u/.config/middleman",
		LockPath:            "/home/u/.config/middleman/middleman.lock",
		MetadataUnavailable: ReasonMetadataMissing,
	}

	var buf bytes.Buffer
	FormatCollisionBanner(&buf, cerr, "", "/home/u/.config/middleman/config.toml")

	want := `error: another middleman instance is already running
  data_dir:     /home/u/.config/middleman
  lock file:    /home/u/.config/middleman/middleman.lock
  metadata:     unavailable (daemon may be early in startup, or metadata is missing/corrupt)

  Run ` + "`middleman status`" + ` to inspect it.
`
	require.Equal(want, buf.String())
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `nix run nixpkgs#go -- test ./internal/runtimelock -shuffle=on -run TestFormatCollisionBanner`
Expected: FAIL (undefined: FormatCollisionBanner).

- [ ] **Step 3: Implement FormatCollisionBanner**

Create `internal/runtimelock/format.go`:

```go
package runtimelock

import (
	"fmt"
	"io"
)

// FormatCollisionBanner writes a multi-line human-readable banner to
// w describing the collision. configPath and defaultConfigPath are
// used to render the "Run `middleman status [--config ...]`" hint;
// when configPath is empty or equals defaultConfigPath, the flag is
// omitted.
//
// When cerr.Metadata is nil, the per-field lines collapse to a
// single "metadata: unavailable (...)" line.
func FormatCollisionBanner(w io.Writer, cerr *CollisionError, configPath, defaultConfigPath string) {
	fmt.Fprintln(w, "error: another middleman instance is already running")
	fmt.Fprintf(w, "  data_dir:     %s\n", cerr.DataDir)
	fmt.Fprintf(w, "  lock file:    %s\n", cerr.LockPath)

	if cerr.Metadata != nil {
		m := cerr.Metadata
		fmt.Fprintf(w, "  running pid:  %d\n", m.PID)
		fmt.Fprintf(w, "  listening on: %s\n", m.ListenAddr)
		fmt.Fprintf(w, "  started at:   %s\n", m.StartedAt)
		if m.Version != "" {
			fmt.Fprintf(w, "  version:      %s\n", m.Version)
		}
	} else {
		fmt.Fprintln(w, "  metadata:     unavailable (daemon may be early in startup, or metadata is missing/corrupt)")
	}

	fmt.Fprintln(w)
	if configPath != "" && configPath != defaultConfigPath {
		fmt.Fprintf(w, "  Run `middleman status --config %s` to inspect it.\n", configPath)
	} else {
		fmt.Fprintln(w, "  Run `middleman status` to inspect it.")
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `nix run nixpkgs#go -- test ./internal/runtimelock -shuffle=on -run TestFormatCollisionBanner`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/runtimelock/format.go internal/runtimelock/format_test.go
git commit -m "$(cat <<'EOF'
feat(runtimelock): render the collision banner that names the holder

When a second middleman launches against the same data_dir, the banner
gives operators the holding PID, listen address, start time, and
version on stderr (above the slog line that follows). When the metadata
file is missing (early startup) or corrupt, the per-field lines
collapse to a single "metadata: unavailable" line so the banner stays
readable. The --config flag appears in the inspect hint only when the
failing process passed a non-default config path.
EOF
)"
```

---

## Task 7: Implement FormatStatus (human + JSON)

**Files:**
- Modify: `internal/runtimelock/format.go`
- Modify: `internal/runtimelock/format_test.go`

**Steps:**

- [ ] **Step 1: Write the failing test**

Append to `internal/runtimelock/format_test.go`:

```go
func TestFormatStatusHumanRunningWithMetadata(t *testing.T) {
	require := require.New(t)

	st := Status{
		DataDir:  "/home/u/.config/middleman",
		LockPath: "/home/u/.config/middleman/middleman.lock",
		Running:  true,
		Metadata: &Metadata{
			PID:        12345,
			Host:       "127.0.0.1",
			Port:       8091,
			ListenAddr: "127.0.0.1:8091",
			StartedAt:  "2026-05-19T10:30:00Z",
			Version:    "1.2.3",
			Commit:     "abcd1234",
		},
	}

	var buf bytes.Buffer
	require.NoError(FormatStatus(&buf, st, false))

	want := `running
  data_dir:     /home/u/.config/middleman
  lock file:    /home/u/.config/middleman/middleman.lock
  pid:          12345
  host:         127.0.0.1
  port:         8091
  listen_addr:  127.0.0.1:8091
  started_at:   2026-05-19T10:30:00Z
  version:      1.2.3
  commit:       abcd1234
`
	require.Equal(want, buf.String())
}

func TestFormatStatusHumanRunningMetadataUnavailable(t *testing.T) {
	require := require.New(t)

	st := Status{
		DataDir:             "/home/u/.config/middleman",
		LockPath:            "/home/u/.config/middleman/middleman.lock",
		Running:             true,
		MetadataUnavailable: ReasonMetadataMissing,
	}

	var buf bytes.Buffer
	require.NoError(FormatStatus(&buf, st, false))

	want := `running (metadata unavailable: missing (daemon may be early in startup))
  data_dir:     /home/u/.config/middleman
  lock file:    /home/u/.config/middleman/middleman.lock
`
	require.Equal(want, buf.String())
}

func TestFormatStatusHumanNotRunning(t *testing.T) {
	require := require.New(t)

	st := Status{
		DataDir:  "/home/u/.config/middleman",
		LockPath: "/home/u/.config/middleman/middleman.lock",
	}

	var buf bytes.Buffer
	require.NoError(FormatStatus(&buf, st, false))

	want := `no running daemon
  data_dir:     /home/u/.config/middleman
  lock file:    /home/u/.config/middleman/middleman.lock
`
	require.Equal(want, buf.String())
}

func TestFormatStatusJSONRunning(t *testing.T) {
	require := require.New(t)

	st := Status{
		DataDir:  "/dd",
		LockPath: "/dd/middleman.lock",
		Running:  true,
		Metadata: &Metadata{
			PID:        4242,
			Host:       "127.0.0.1",
			Port:       8091,
			ListenAddr: "127.0.0.1:8091",
			StartedAt:  "2026-05-19T10:30:00Z",
			Version:    "v1",
			Commit:     "c1",
		},
	}

	var buf bytes.Buffer
	require.NoError(FormatStatus(&buf, st, true))

	want := `{
  "running": true,
  "data_dir": "/dd",
  "lock_file": "/dd/middleman.lock",
  "metadata": {
    "pid": 4242,
    "host": "127.0.0.1",
    "port": 8091,
    "listen_addr": "127.0.0.1:8091",
    "started_at": "2026-05-19T10:30:00Z",
    "version": "v1",
    "commit": "c1"
  }
}
`
	require.Equal(want, buf.String())
}

func TestFormatStatusJSONNotRunning(t *testing.T) {
	require := require.New(t)

	st := Status{
		DataDir:  "/dd",
		LockPath: "/dd/middleman.lock",
	}

	var buf bytes.Buffer
	require.NoError(FormatStatus(&buf, st, true))

	want := `{
  "running": false,
  "data_dir": "/dd",
  "lock_file": "/dd/middleman.lock",
  "metadata": null
}
`
	require.Equal(want, buf.String())
}

func TestFormatStatusJSONMetadataUnavailable(t *testing.T) {
	require := require.New(t)

	st := Status{
		DataDir:             "/dd",
		LockPath:            "/dd/middleman.lock",
		Running:             true,
		MetadataUnavailable: ReasonMetadataCorrupt,
	}

	var buf bytes.Buffer
	require.NoError(FormatStatus(&buf, st, true))

	want := `{
  "running": true,
  "data_dir": "/dd",
  "lock_file": "/dd/middleman.lock",
  "metadata": null,
  "metadata_error": "corrupt"
}
`
	require.Equal(want, buf.String())
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `nix run nixpkgs#go -- test ./internal/runtimelock -shuffle=on -run TestFormatStatus`
Expected: FAIL (undefined: FormatStatus).

- [ ] **Step 3: Implement FormatStatus**

Append to `internal/runtimelock/format.go`:

```go

// statusJSON is the wire shape of FormatStatus(..., asJSON=true).
// Defined explicitly so the JSON keys do not depend on a field-order
// accident in Status.
type statusJSON struct {
	Running       bool      `json:"running"`
	DataDir       string    `json:"data_dir"`
	LockFile      string    `json:"lock_file"`
	Metadata      *Metadata `json:"metadata"`
	MetadataError string    `json:"metadata_error,omitempty"`
}

// FormatStatus renders st to w. When asJSON is true, a single
// indented JSON object is written followed by a trailing newline.
// Otherwise a human-readable multi-line summary is written using the
// same key alignment as the collision banner.
func FormatStatus(w io.Writer, st Status, asJSON bool) error {
	if asJSON {
		payload := statusJSON{
			Running:       st.Running,
			DataDir:       st.DataDir,
			LockFile:      st.LockPath,
			Metadata:      st.Metadata,
			MetadataError: string(st.MetadataUnavailable),
		}
		data, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return fmt.Errorf("encode status json: %w", err)
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
		_, err = fmt.Fprintln(w)
		return err
	}

	switch {
	case !st.Running:
		fmt.Fprintln(w, "no running daemon")
	case st.Metadata != nil:
		fmt.Fprintln(w, "running")
	default:
		fmt.Fprintf(w, "running (metadata unavailable: %s)\n", st.MetadataUnavailable)
	}

	fmt.Fprintf(w, "  data_dir:     %s\n", st.DataDir)
	fmt.Fprintf(w, "  lock file:    %s\n", st.LockPath)

	if st.Metadata != nil {
		m := st.Metadata
		fmt.Fprintf(w, "  pid:          %d\n", m.PID)
		fmt.Fprintf(w, "  host:         %s\n", m.Host)
		fmt.Fprintf(w, "  port:         %d\n", m.Port)
		fmt.Fprintf(w, "  listen_addr:  %s\n", m.ListenAddr)
		fmt.Fprintf(w, "  started_at:   %s\n", m.StartedAt)
		fmt.Fprintf(w, "  version:      %s\n", m.Version)
		fmt.Fprintf(w, "  commit:       %s\n", m.Commit)
	}

	return nil
}
```

Update the imports at the top of `internal/runtimelock/format.go` to include `encoding/json`:

```go
import (
	"encoding/json"
	"fmt"
	"io"
)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `nix run nixpkgs#go -- test ./internal/runtimelock -shuffle=on`
Expected: PASS for all tests.

- [ ] **Step 5: Commit**

```bash
git add internal/runtimelock/format.go internal/runtimelock/format_test.go
git commit -m "$(cat <<'EOF'
feat(runtimelock): render middleman status output in human and JSON forms

FormatStatus drives the upcoming status subcommand. The human form
mirrors the collision banner's key alignment so the two outputs are
easy to compare side by side. The JSON form is a fixed object shape
(not Status's field order) so scripts can parse it without depending
on Go reflection details; metadata is null when the daemon is not
running or when the metadata file could not be read.
EOF
)"
```

---

## Task 8: Wire Acquire/Release/WriteMetadata into main.go

**Files:**
- Modify: `cmd/middleman/main.go`

**Steps:**

- [ ] **Step 1: Read the current run() function**

Open `cmd/middleman/main.go` and re-read the entire `run(configPath string) error` function (currently lines ~281 to ~424). You will:

- Add a `runtimelock.Acquire` call after the `MkdirAll(cfg.DataDir)` step.
- Replace `srv.ListenAndServe(addr)` with `net.Listen` + `srv.Serve(ln)` so the listener bind is observable before background work starts.
- Insert `handle.WriteMetadata(meta)` between the listener bind and the syncer.Start call.
- Add `runtime` import for `goos` is NOT needed; only `net` and `runtimelock` are new imports.

- [ ] **Step 2: Add the new imports**

In the imports block of `cmd/middleman/main.go`, add `"net"` (alphabetically sorted with stdlib) and `"github.com/wesm/middleman/internal/runtimelock"` (with the other internal packages). After editing the imports section becomes:

```go
import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/wesm/middleman/internal/config"
	"github.com/wesm/middleman/internal/db"
	"github.com/wesm/middleman/internal/gitclone"
	ghclient "github.com/wesm/middleman/internal/github"
	"github.com/wesm/middleman/internal/platform"
	"github.com/wesm/middleman/internal/ptyowner"
	"github.com/wesm/middleman/internal/runtimelock"
	"github.com/wesm/middleman/internal/server"
	"github.com/wesm/middleman/internal/stacks"
	"github.com/wesm/middleman/internal/web"
)
```

- [ ] **Step 3: Insert Acquire after MkdirAll**

Find the block in `run()`:

```go
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf(
			"create data directory %s: %w", cfg.DataDir, err,
		)
	}

	database, err := db.Open(cfg.DBPath())
```

Insert between them:

```go
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf(
			"create data directory %s: %w", cfg.DataDir, err,
		)
	}

	lockHandle, err := runtimelock.Acquire(cfg.DataDir)
	if err != nil {
		var cerr *runtimelock.CollisionError
		if errors.As(err, &cerr) {
			runtimelock.FormatCollisionBanner(
				os.Stderr, cerr, configPath, config.DefaultConfigPath(),
			)
			return fmt.Errorf(
				"another middleman is already running on %s",
				cfg.DataDir,
			)
		}
		return fmt.Errorf("acquire runtime lock: %w", err)
	}
	defer func() {
		if err := lockHandle.Release(); err != nil {
			slog.Warn("release runtime lock", "err", err)
		}
	}()

	database, err := db.Open(cfg.DBPath())
```

The defer is registered BEFORE the existing `defer database.Close()`. Defers run in LIFO order, so Release will fire AFTER `srv.Shutdown` and `syncer.Stop`. That is intentional: keep the lock held until the daemon has fully drained.

- [ ] **Step 4: Replace ListenAndServe with Listen + Serve + WriteMetadata**

Find the existing tail of `run()`:

```go
	addr := cfg.ListenAddr()
	slog.Info(fmt.Sprintf("starting server at http://%s", addr))

	errCh := make(chan error, 1)
	go func() {
		if listenErr := srv.ListenAndServe(addr); !errors.Is(listenErr, http.ErrServerClosed) {
			errCh <- listenErr
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutting down")
		return nil
	case err := <-errCh:
		return fmt.Errorf("server: %w", err)
	}
}
```

Replace with:

```go
	addr := cfg.ListenAddr()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}

	if err := writeRuntimeMetadata(lockHandle, ln); err != nil {
		slog.Warn("write runtime metadata", "err", err)
	}

	slog.Info(fmt.Sprintf("starting server at http://%s", ln.Addr().String()))

	errCh := make(chan error, 1)
	go func() {
		if serveErr := srv.Serve(ln); !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutting down")
		return nil
	case err := <-errCh:
		return fmt.Errorf("server: %w", err)
	}
}

// writeRuntimeMetadata snapshots the bound listener and process state
// into the runtime metadata file. The recorded port comes from
// ln.Addr() (not cfg.Port) so it matches the kernel-assigned value if
// they ever diverge.
func writeRuntimeMetadata(h *runtimelock.Handle, ln net.Listener) error {
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return fmt.Errorf("listener returned non-TCP address %T", ln.Addr())
	}
	return h.WriteMetadata(runtimelock.Metadata{
		PID:        os.Getpid(),
		Host:       tcpAddr.IP.String(),
		Port:       tcpAddr.Port,
		ListenAddr: ln.Addr().String(),
		StartedAt:  time.Now().UTC().Format(time.RFC3339),
		Version:    version,
		Commit:     commit,
	})
}
```

Note: leave the existing `syncer.SetOnStatusChange`, `srv.Hub().Broadcast`, and `defer srv.Shutdown` blocks where they are. The signal-context wait pattern (`signal.NotifyContext` + `select { case <-ctx.Done() }`) is unchanged.

- [ ] **Step 5: Build to verify the wiring compiles**

Run: `nix run nixpkgs#go -- build ./cmd/middleman`
Expected: exits 0 with no output.

Run: `nix run nixpkgs#go -- vet ./cmd/middleman ./internal/runtimelock`
Expected: exits 0 with no output.

- [ ] **Step 6: Sanity-run the binary against a temp data_dir (no providers configured)**

Run:

```bash
TMPDIR=$(mktemp -d)
nix run nixpkgs#go -- run ./cmd/middleman --config /dev/null &
PID=$!
sleep 1
kill $PID
wait $PID 2>/dev/null
ls -la $HOME/.config/middleman 2>/dev/null | grep -E 'middleman\.(lock|run\.json)' || true
```

This is exploratory — exact paths depend on whether `--config /dev/null` is acceptable. If it fails, this step is a smoke check only; the real verification is the E2E test in Task 10. Skip if the binary requires a real config.

- [ ] **Step 7: Commit**

```bash
git add cmd/middleman/main.go
git commit -m "$(cat <<'EOF'
feat(server): take a startup file lock so double-launch fails fast

Wires runtimelock.Acquire into the run() bootstrap before the HTTP
listener binds and switches from srv.ListenAndServe to net.Listen +
srv.Serve so the bound port is captured synchronously and recorded in
middleman.run.json under the held lock. Defer ordering keeps the lock
held until after both the HTTP server and the sync engine have drained
so concurrent middleman status probes report the daemon as live for the
full shutdown window. On collision, the user sees a stderr banner with
the holding PID and listen address instead of an opaque "bind: address
in use" error.
EOF
)"
```

---

## Task 9: Add the `middleman status` subcommand

**Files:**
- Modify: `cmd/middleman/main.go`

**Steps:**

- [ ] **Step 1: Add the dispatch case**

In `runCLI`, find the switch over `args[0]`:

```go
		switch args[0] {
		case "version":
			_, err := fmt.Fprintf(
				stdout,
				"middleman %s (%s) built %s\n",
				version, commit, buildDate,
			)
			return err
		case "config":
			return runConfigCLI(args[1:], stdout)
		case "pty-owner":
			return runPtyOwnerCLI(args[1:])
		}
```

Add a `status` case:

```go
		switch args[0] {
		case "version":
			_, err := fmt.Fprintf(
				stdout,
				"middleman %s (%s) built %s\n",
				version, commit, buildDate,
			)
			return err
		case "config":
			return runConfigCLI(args[1:], stdout)
		case "pty-owner":
			return runPtyOwnerCLI(args[1:])
		case "status":
			return runStatusCLI(args[1:], stdout)
		}
```

- [ ] **Step 2: Implement runStatusCLI**

Add a new function below `runConfigRead` in `cmd/middleman/main.go`:

```go
func runStatusCLI(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("middleman status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String(
		"config", config.DefaultConfigPath(),
		"path to config file",
	)
	asJSON := fs.Bool("json", false, "render output as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if err := config.EnsureDefault(*configPath); err != nil {
		return fmt.Errorf("ensure config: %w", err)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf(
			"create data directory %s: %w", cfg.DataDir, err,
		)
	}

	st, err := runtimelock.Read(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("read runtime status: %w", err)
	}

	return runtimelock.FormatStatus(stdout, st, *asJSON)
}
```

- [ ] **Step 3: Build and vet**

Run: `nix run nixpkgs#go -- build ./cmd/middleman`
Expected: exits 0.

Run: `nix run nixpkgs#go -- vet ./cmd/middleman`
Expected: exits 0.

- [ ] **Step 4: Commit**

```bash
git add cmd/middleman/main.go
git commit -m "$(cat <<'EOF'
feat(cli): add middleman status for liveness without HTTP

Reuses the same runtime-lock probe the startup path uses so operators
can tell whether a daemon is holding the data_dir even when the HTTP
endpoint is unreachable (port closed, mid-startup, blocked by a
firewall, etc). --json emits a fixed object shape so wrapper scripts
can parse it.
EOF
)"
```

---

## Task 10: End-to-end test — two subprocesses, status states

**Files:**
- Create: `cmd/middleman/lock_e2e_test.go`

**Steps:**

- [ ] **Step 1: Write the failing test**

Create `cmd/middleman/lock_e2e_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/middleman/internal/runtimelock"
)

// buildMiddleman compiles the middleman binary into a temp dir and
// returns the absolute path. The binary is built only once per test
// invocation via t.Cleanup, but each test sets a unique data_dir.
func buildMiddleman(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "middleman")
	cmd := exec.Command("go", "build", "-o", binPath, ".")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Run(), "go build ./cmd/middleman")
	return binPath
}

// reserveFreePort opens a listener on 127.0.0.1:0, closes it, and
// returns the port the kernel assigned. The window between Close and
// the test's own Listen is wide enough on practice to be flaky in
// theory, but is the same idiom used elsewhere in the repo for
// "pick me a free port".
func reserveFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

// writeMinimalConfig writes a config that binds to the chosen port
// with no provider repos. The dataDir is set so it does not collide
// with the developer's real ~/.config/middleman.
func writeMinimalConfig(t *testing.T, configPath, dataDir string, port int) {
	t.Helper()
	body := "host = \"127.0.0.1\"\n" +
		"port = " + itoa(port) + "\n" +
		"data_dir = \"" + dataDir + "\"\n" +
		"sync_interval = \"5m\"\n" +
		"github_token_env = \"MIDDLEMAN_GITHUB_TOKEN_UNSET\"\n" +
		"[activity]\nview_mode = \"threaded\"\ntime_range = \"7d\"\n" +
		"[terminal]\nrenderer = \"xterm\"\n"
	require.NoError(t, os.WriteFile(configPath, []byte(body), 0o600))
}

func itoa(n int) string {
	// Avoid pulling in fmt for this hot inner path; the file already
	// imports strings indirectly via testify, so use a local helper.
	return strings.TrimSpace(strings.ReplaceAll(strings.Repeat(" ", 0)+intToStr(n), " ", ""))
}

func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func TestStartupLockCollisionAndStatus(t *testing.T) {
	require := require.New(t)

	bin := buildMiddleman(t)
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	require.NoError(os.MkdirAll(dataDir, 0o700))
	cfgPath := filepath.Join(root, "config.toml")

	port := reserveFreePort(t)
	writeMinimalConfig(t, cfgPath, dataDir, port)

	// First subprocess: should start and hold the lock.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first := exec.CommandContext(ctx, bin, "--config", cfgPath)
	first.Stdout = os.Stderr
	first.Stderr = os.Stderr
	first.Env = append(os.Environ(),
		"MIDDLEMAN_LOG_LEVEL=warn",
		"MIDDLEMAN_GITHUB_TOKEN_UNSET=", // make sure no token is needed
	)
	require.NoError(first.Start())
	t.Cleanup(func() {
		cancel()
		_ = first.Wait()
	})

	// Wait until the metadata file appears (means Acquire +
	// WriteMetadata both completed).
	waitForFile(t, runtimelock.MetadataPath(dataDir), 5*time.Second)

	// Second subprocess against the same data_dir + port. Should exit 1
	// with the banner on stderr.
	second := exec.Command(bin, "--config", cfgPath)
	var stderr bytes.Buffer
	second.Stderr = &stderr
	err := second.Run()
	require.Error(err)
	var exitErr *exec.ExitError
	require.True(errors.As(err, &exitErr))
	require.Equal(1, exitErr.ExitCode())
	require.Contains(stderr.String(), "another middleman instance is already running")
	require.Contains(stderr.String(), dataDir)
	require.Contains(stderr.String(), "running pid:")

	// `middleman status` against the same config: reports running
	// with metadata.
	statusCmd := exec.Command(bin, "status", "--config", cfgPath)
	var statusOut bytes.Buffer
	statusCmd.Stdout = &statusOut
	statusCmd.Stderr = os.Stderr
	require.NoError(statusCmd.Run())
	require.Contains(statusOut.String(), "running")
	require.Contains(statusOut.String(), dataDir)
	require.Contains(statusOut.String(), "pid:")

	// `middleman status --json`: same data, JSON shape.
	jsonCmd := exec.Command(bin, "status", "--json", "--config", cfgPath)
	var jsonOut bytes.Buffer
	jsonCmd.Stdout = &jsonOut
	jsonCmd.Stderr = os.Stderr
	require.NoError(jsonCmd.Run())
	require.Contains(jsonOut.String(), "\"running\": true")
	require.Contains(jsonOut.String(), "\"data_dir\": \""+dataDir+"\"")

	// Shut down the first process; lock is released by the kernel
	// once the process exits.
	cancel()
	_ = first.Wait()

	// Wait for the metadata file to disappear (clean Release path).
	waitForNoFile(t, runtimelock.MetadataPath(dataDir), 5*time.Second)

	// `middleman status` now reports not-running.
	statusCmd2 := exec.Command(bin, "status", "--config", cfgPath)
	var statusOut2 bytes.Buffer
	statusCmd2.Stdout = &statusOut2
	statusCmd2.Stderr = os.Stderr
	require.NoError(statusCmd2.Run())
	require.Contains(statusOut2.String(), "no running daemon")
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.FailNowf(t, "file did not appear", "path=%s timeout=%s", path, timeout)
}

func waitForNoFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err != nil && os.IsNotExist(err) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.FailNowf(t, "file did not disappear", "path=%s timeout=%s", path, timeout)
}
```

The `itoa`/`intToStr` helpers are deliberately stdlib-free so the test does not pull in extra packages. If `fmt` is already imported elsewhere in the test file (it is not, by design), use `fmt.Sprintf` instead.

- [ ] **Step 2: Run the test to verify it works (it should pass given Tasks 4-9 are done)**

Run: `nix run nixpkgs#go -- test ./cmd/middleman -shuffle=on -run TestStartupLockCollisionAndStatus -timeout 60s`
Expected: PASS.

Note: this test invokes `go build` as a subprocess. The harness must have `go` on the path INSIDE the test process, but the developer who is invoking tests runs `nix run nixpkgs#go -- test ...` which puts `go` on the inner PATH. If the test fails to build the binary, that's the cause: re-invoke via `nix run nixpkgs#go --` so the test's `exec.Command("go", ...)` resolves.

- [ ] **Step 3: Commit**

```bash
git add cmd/middleman/lock_e2e_test.go
git commit -m "$(cat <<'EOF'
test(cmd): cover startup-lock collision and status against the real binary

End-to-end test that launches two middleman subprocesses against the
same data_dir and the same TCP port, asserts that the second exits 1
with the banner naming the holder, and exercises middleman status (both
human and JSON forms) against the held lock and again after the daemon
exits. Pre-resolving a free port via net.Listen and closing it before
the subprocesses start keeps the second collision deterministic.
EOF
)"
```

---

## Task 11: Final integration sweep

**Files:**
- None (verification only).

**Steps:**

- [ ] **Step 1: Re-run the full runtimelock test suite shuffled**

Run: `nix run nixpkgs#go -- test ./internal/runtimelock -shuffle=on -count=10`
Expected: PASS. The `-count=10` is the one explicit exception to the "no -count unless N > 1" rule — flake-hunt the lock package because it's a new file-system-bound dependency.

- [ ] **Step 2: Run the full cmd test suite**

Run: `nix run nixpkgs#go -- test ./cmd/middleman -shuffle=on -timeout 120s`
Expected: PASS.

- [ ] **Step 3: Vet everything new**

Run: `nix run nixpkgs#go -- vet ./internal/runtimelock ./cmd/middleman`
Expected: exits 0.

- [ ] **Step 4: Lint everything new**

Run: `nix shell 'nixpkgs#golangci-lint' --command golangci-lint run ./internal/runtimelock/... ./cmd/middleman/...`
Expected: exits 0 with no findings.

If the linter reports `unused-parameter`, `nilerr`, or `errcheck` on the best-effort `_ = h.lock.Unlock()` style writes, add an `//nolint:errcheck // best-effort release; logged via slog.Warn` comment with the same wording in every spot. Do not add file-wide `//nolint` lines.

- [ ] **Step 5: Cross-compile sanity check for Windows**

Run: `GOOS=windows nix run nixpkgs#go -- build ./internal/runtimelock ./cmd/middleman`
Expected: exits 0. The flock library is platform-portable; this catches accidental POSIX-only imports.

- [ ] **Step 6: Run the full project test suite (the gate before declaring done)**

Run: `nix run nixpkgs#go -- test ./... -shuffle=on -timeout 300s`
Expected: PASS. Any failures unrelated to the lock work belong to other branches and must NOT be touched on this branch.

- [ ] **Step 7: Final commit if there were lint or vet fixes**

If Steps 4 or 5 produced any code edits, commit them now:

```bash
git add -p   # selectively stage the lint fixes
git commit -m "$(cat <<'EOF'
chore(runtimelock): address linter findings on the startup-lock surface

Cleans up the per-call //nolint:errcheck comments on the best-effort
metadata-remove and unlock paths so the linter does not flag deferred
error returns that are intentionally surfaced via slog.Warn.
EOF
)"
```

If there were no fixes, skip this step.

---

## Self-Review

Spec coverage check (every section of `docs/superpowers/specs/2026-05-19-startup-file-lock-design.md` should map to a task):

- Goal + Scope + Background → covered indirectly; nothing to implement.
- File Layout (`middleman.lock`, `middleman.run.json`) → Task 2 (paths + metadata), Task 4 (Acquire creates lock file).
- Metadata File Format (PID, host, port, listen_addr, started_at, version, commit) → Task 2 (`Metadata` struct).
- Atomic write (`.tmp` + rename + tolerance of leftover temp) → Task 2.
- Startup Sequence (Acquire → MkdirAll → defer Release → DB open → Listen → WriteMetadata → Serve) → Task 8.
- `middleman status` Subcommand (TryLock-then-release, three states, `--config`, `--json`) → Task 5 (Read), Task 7 (FormatStatus), Task 9 (CLI wiring).
- Collision Banner (full-metadata + metadata-unavailable forms, `--config` variants) → Task 6 (FormatCollisionBanner).
- Packaging (`runtimelock.Acquire/WriteMetadata/Release/Read/FormatCollisionBanner/FormatStatus`) → Task 1 (package), Tasks 4-7.
- Error Handling (Acquire non-collision errors bubble up, WriteMetadata logs slog.Warn and continues, Release logs slog.Warn) → Tasks 4 + 8.
- Testing cases 1-14 → Tasks 2-10 (case 1 = Task 4 step 1; case 2 = Task 4; cases 3-4 = Task 4; case 5 = Task 4; case 6 = Task 4; case 7 = Task 2; cases 8-10 = Task 5; cases 11-12 = Tasks 6-7; case 13 = Task 10; case 14 = Task 10).
- Future Work → explicitly out of scope; no tasks.

Placeholder scan: searched the plan for "TBD", "TODO", "implement later", "Similar to Task", "Add appropriate ...", "Write tests for the above" — none present.

Type consistency: `Metadata`, `Handle`, `CollisionError`, `Status`, `MetadataUnavailableReason`, `LockPath`, `MetadataPath`, `Acquire`, `Release`, `WriteMetadata`, `Read`, `FormatCollisionBanner`, `FormatStatus` are used with the same signatures from definition (Tasks 2-7) through wiring (Tasks 8-9) through tests (Tasks 4-10).

---

## Out of scope (do not touch)

- Idle shutdown.
- Auto-start.
- Daemon supervision (systemd, launchd, Windows services).
- `/api/status` HTTP route.
- Distinct exit codes per error class.
- Anything in adjacent worktrees.
